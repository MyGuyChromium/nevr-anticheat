package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// investigationTelemetryKind names the cached document and its schema.
const investigationTelemetryKind = "investigation-telemetry/v1"

// investigationTelemetry is everything the investigation document derives
// from the normalized frames of a match: the quality report, the downsampled
// speed timeline, the legal-context tallies and each player's mean ping.
// Building it loads and JSON-parses every frame and replays the feature
// extractor over them (15-30 s on a full-length match), and none of it changes
// until the match is re-analyzed or the config changes, so it is computed once
// and kept in match_view_cache. Notes, reviews, incidents and analysis runs are
// never cached: they are read fresh on every request.
type investigationTelemetry struct {
	Quality  pipeline.TelemetryQualityReport `json:"quality"`
	Timeline []investigationPoint            `json:"timeline"`
	Contexts map[string]int                  `json:"legal_context_counts"`
	MeanPing map[string]float64              `json:"mean_ping_ms"`
}

// investigationTelemetryKey identifies the state the document depends on: the
// frames (replaced only by an analysis, which always records a new run; their
// count guards a run that could not be recorded) and the config the feature
// extractor and quality gate read.
func (s *server) investigationTelemetryKey(ctx context.Context, matchID string) (string, error) {
	_, frames, err := s.engine.Store().GetMatchStorageCounts(ctx, matchID)
	if err != nil {
		return "", err
	}
	var latestRun int64
	if runs, runErr := s.engine.Store().ListAnalysisRuns(ctx, matchID, 1); runErr == nil && len(runs) > 0 {
		latestRun = runs[0].RunID
	}
	return fmt.Sprintf("run=%d frames=%d config=%s", latestRun, frames, s.configFingerprint()), nil
}

func (s *server) investigationTelemetry(ctx context.Context, matchID string, mc *model.MatchContext) (*investigationTelemetry, error) {
	store := s.engine.Store()
	key, err := s.investigationTelemetryKey(ctx, matchID)
	if err != nil {
		return nil, err
	}
	if cachedKey, raw, getErr := store.GetMatchViewCache(ctx, matchID, investigationTelemetryKind); getErr == nil && cachedKey == key {
		var cached investigationTelemetry
		if json.Unmarshal(raw, &cached) == nil {
			cached.normalize()
			return &cached, nil
		}
	} else if getErr != nil && !errors.Is(getErr, sqlite.ErrNotFound) {
		return nil, getErr
	}
	value, err := s.runDetached(ctx, investigationTelemetryKind+"\x00"+matchID+"\x00"+key, func(workCtx context.Context) (any, error) {
		built, buildErr := s.buildInvestigationTelemetry(workCtx, matchID, mc)
		if buildErr != nil {
			return nil, buildErr
		}
		// A document that cannot be encoded (a non-finite speed) or stored is
		// still served; it is simply rebuilt next time.
		if doc, marshalErr := json.Marshal(built); marshalErr != nil {
			s.engine.Logger().Warn("investigation telemetry not cached", "match_id", matchID, "error", marshalErr)
		} else if storeErr := store.StoreMatchViewCache(workCtx, matchID, investigationTelemetryKind, key, doc); storeErr != nil {
			s.engine.Logger().Warn("investigation telemetry not cached", "match_id", matchID, "error", storeErr)
		}
		return built, nil
	})
	if err != nil {
		return nil, err
	}
	built, _ := value.(*investigationTelemetry)
	if built == nil {
		return nil, errors.New("investigation telemetry was not built")
	}
	return built, nil
}

// normalize restores the empty (non-nil) collections a freshly built document has.
func (t *investigationTelemetry) normalize() {
	if t.Timeline == nil {
		t.Timeline = []investigationPoint{}
	}
	if t.Contexts == nil {
		t.Contexts = map[string]int{}
	}
	if t.MeanPing == nil {
		t.MeanPing = map[string]float64{}
	}
}

func (s *server) buildInvestigationTelemetry(ctx context.Context, matchID string, mc *model.MatchContext) (*investigationTelemetry, error) {
	frames, err := s.engine.Store().GetMatchFrames(ctx, matchID)
	if err != nil {
		return nil, err
	}
	cfg := s.engine.Config()
	quality := pipeline.AssessTelemetryQuality(frames, cfg)

	sort.SliceStable(frames, func(i, j int) bool {
		if frames[i].FrameIndex != frames[j].FrameIndex {
			return frames[i].FrameIndex < frames[j].FrameIndex
		}
		return frames[i].PlayerID < frames[j].PlayerID
	})
	extractor := pipeline.NewFeatureExtractor(cfg.Pipeline.HistoryWindow)
	extractor.SetMaxFrameDt(cfg.Pipeline.MaxFrameDt)
	extractor.SetHighPingThreshold(cfg.Pipeline.HighPingThresholdMs)
	states := make(map[string]*model.PlayerState)
	playerTarget := maxInt(20, 600/maxInt(1, quality.Players))
	playerSeen := make(map[string]int)
	out := &investigationTelemetry{Quality: quality, Timeline: make([]investigationPoint, 0, minInt(len(frames), 600)),
		Contexts: map[string]int{}, MeanPing: map[string]float64{}}
	pingSum, pingCount := map[string]float64{}, map[string]int{}
	for i := range frames {
		frame := &frames[i]
		if frame.EstimatedPingMs > 0 {
			pingSum[frame.PlayerID] += frame.EstimatedPingMs
			pingCount[frame.PlayerID]++
		}
		state := states[frame.PlayerID]
		if state == nil {
			state = &model.PlayerState{PlayerID: frame.PlayerID, Team: mc.TeamAssignments[frame.PlayerID]}
			states[frame.PlayerID] = state
		}
		extractor.UpdatePlayerState(state, frame, mc)
		out.Contexts[state.LegalContext.PrimaryExplanation]++
		playerSeen[frame.PlayerID]++
		playerStep := maxInt(1, int(math.Ceil(float64(quality.PerPlayerRows[frame.PlayerID])/float64(playerTarget))))
		if (playerSeen[frame.PlayerID]-1)%playerStep != 0 && playerSeen[frame.PlayerID] != quality.PerPlayerRows[frame.PlayerID] {
			continue
		}
		// Unknown measurements remain null in the presentation contract. A
		// measured stationary velocity is distinct from an absent source.
		var gameSpeed, discSpeed *float64
		if frame.ReportedVelocity != nil {
			speed := frame.ReportedVelocity.Magnitude()
			gameSpeed = &speed
		}
		if frame.Disc != nil {
			speed := frame.Disc.Speed
			discSpeed = &speed
		}
		out.Timeline = append(out.Timeline, investigationPoint{Frame: frame.FrameIndex, Time: frame.Timestamp,
			PlayerID: frame.PlayerID, PlayerName: nameOf(mc, frame.PlayerID), PoseSpeed: state.Speed,
			GameSpeed: gameSpeed, DiscSpeed: discSpeed, LeftHandSpeed: state.LeftHandSpeed,
			RightHandSpeed: state.RightHandSpeed, PingMS: frame.EstimatedPingMs, Context: state.LegalContext})
	}
	for playerID, count := range pingCount {
		out.MeanPing[playerID] = pingSum[playerID] / float64(count)
	}
	return out, nil
}
