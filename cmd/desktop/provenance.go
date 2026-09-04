package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// applyActiveProfile applies a previously selected detector profile before the
// runtime starts. Profiles deliberately contain detector settings only: game
// physics and score boundaries stay controlled by the reviewed TOML config.
func applyActiveProfile(engine *replay.Engine) {
	profile, ok, err := engine.Store().GetActiveConfigProfile(context.Background())
	if err != nil {
		engine.Logger().Warn("could not read active detector profile", "error", err)
		return
	}
	if !ok {
		return
	}
	var detectors map[string]config.DetectorConfig
	if err := json.Unmarshal([]byte(profile.DetectorsJSON), &detectors); err != nil {
		engine.Logger().Warn("could not decode active detector profile", "profile", profile.Name, "error", err)
		return
	}
	for id, detector := range detectors {
		// The desktop remains evidence-only regardless of imported profile data.
		detector.Mode = "shadow"
		detector.AutoEnforce = false
		detector.EnforcementWeight = 0
		detectors[id] = detector
	}
	candidate := cloneConfig(engine.Config())
	candidate.Detectors = detectors
	if err := config.Validate(candidate); err != nil {
		engine.Logger().Warn("active detector profile is invalid; using configured defaults", "profile", profile.Name, "error", err)
		return
	}
	engine.Config().Detectors = detectors
}

func (s *server) activeProfileName(ctx context.Context) string {
	profile, ok, err := s.engine.Store().GetActiveConfigProfile(ctx)
	if err != nil || !ok {
		return "default"
	}
	return profile.Name
}

func recordAnalysisResults(ctx context.Context, engine *replay.Engine, results []*replay.AnalyzeResult, source string, wall time.Duration) {
	if len(results) == 0 {
		return
	}
	profileName := "default"
	if profile, ok, err := engine.Store().GetActiveConfigProfile(ctx); err == nil && ok {
		profileName = profile.Name
	}
	data, _ := json.Marshal(struct {
		Detectors any `json:"detectors"`
		Physics   any `json:"physics"`
		Levels    any `json:"levels"`
	}{engine.Config().EffectiveTable(), engine.Physics(), engine.Levels()})
	fingerprint := shortHash(data)
	perMatchWall := wall.Milliseconds() / int64(len(results))
	for _, result := range results {
		if result == nil || result.Result == nil || result.MatchCtx == nil || result.PersistError() != nil {
			continue
		}
		quality := result.Result.TelemetryQuality
		_, err := engine.Store().StoreAnalysisRun(ctx, sqlite.AnalysisRun{
			MatchID: result.MatchCtx.MatchID, Source: strings.TrimSpace(source), AppVersion: appVersion,
			BuildCommit: buildCommit, ConfigFingerprint: fingerprint, ProfileName: profileName,
			TelemetryQuality: quality.Score, QualityGrade: quality.Grade, QualityGated: quality.Gated,
			WallMilliseconds: perMatchWall, PipelineMilliseconds: result.Result.Duration.Milliseconds(),
			FramesProcessed: result.Frames, EventsProduced: len(result.Result.DetectionEvents),
		})
		if err != nil {
			engine.Logger().Warn("could not record analysis provenance", "match_id", result.MatchCtx.MatchID, "error", err)
		}
	}
}

func shortHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12])
}
