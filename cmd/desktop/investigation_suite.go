package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

type investigationPoint struct {
	Frame          int                      `json:"frame"`
	Time           float64                  `json:"time"`
	PlayerID       string                   `json:"player_id"`
	PlayerName     string                   `json:"player_name,omitempty"`
	PoseSpeed      float64                  `json:"pose_speed"`
	GameSpeed      float64                  `json:"game_speed"`
	DiscSpeed      float64                  `json:"disc_speed"`
	LeftHandSpeed  float64                  `json:"left_hand_speed"`
	RightHandSpeed float64                  `json:"right_hand_speed"`
	PingMS         float64                  `json:"ping_ms"`
	Context        model.LegalMotionContext `json:"legal_context"`
}

type investigationIncident struct {
	ID          string   `json:"id"`
	PlayerID    string   `json:"player_id"`
	PlayerName  string   `json:"player_name,omitempty"`
	StartFrame  int      `json:"start_frame"`
	EndFrame    int      `json:"end_frame"`
	StartTime   float64  `json:"start_time"`
	EndTime     float64  `json:"end_time"`
	Detectors   []string `json:"detectors"`
	EventIDs    []string `json:"event_ids"`
	Agreement   int      `json:"agreement"`
	Confidence  float64  `json:"confidence"`
	MaxSeverity float64  `json:"max_severity"`
}

type throwEnvelope struct {
	replay.ThrowEvent
	Cap         float64 `json:"cap"`
	Uncertainty float64 `json:"uncertainty"`
	// Retained for old consumers, always false: this heuristic is not a
	// calibrated measurement uncertainty and cannot establish a certain breach.
	CertainBreach    bool   `json:"certain_breach"`
	SampleAboveGuide bool   `json:"sample_above_guide"`
	MarginKind       string `json:"margin_kind"`
	NearBoundary     bool   `json:"near_boundary"`
}

func buildIncidents(events []model.DetectionEvent, mc *model.MatchContext) []investigationIncident {
	copyEvents := append([]model.DetectionEvent(nil), events...)
	sort.Slice(copyEvents, func(i, j int) bool {
		if copyEvents[i].PlayerID != copyEvents[j].PlayerID {
			return copyEvents[i].PlayerID < copyEvents[j].PlayerID
		}
		return copyEvents[i].FrameRangeStart < copyEvents[j].FrameRangeStart
	})
	var out []investigationIncident
	for _, event := range copyEvents {
		start, end := event.FrameRangeStart, event.FrameRangeEnd
		if start == 0 && end == 0 {
			start, end = event.FrameIndex, event.FrameIndex
		}
		var incident *investigationIncident
		if len(out) > 0 {
			candidate := &out[len(out)-1]
			if candidate.PlayerID == event.PlayerID && start <= candidate.EndFrame+20 {
				incident = candidate
			}
		}
		if incident == nil {
			out = append(out, investigationIncident{ID: fmt.Sprintf("INC-%03d", len(out)+1), PlayerID: event.PlayerID,
				PlayerName: nameOf(mc, event.PlayerID), StartFrame: start, EndFrame: end,
				StartTime: event.Timestamp, EndTime: event.Timestamp})
			incident = &out[len(out)-1]
		}
		incident.EndFrame = maxInt(incident.EndFrame, end)
		incident.EndTime = math.Max(incident.EndTime, event.Timestamp)
		incident.EventIDs = append(incident.EventIDs, event.EventID)
		incident.MaxSeverity = math.Max(incident.MaxSeverity, event.Severity)
		incident.Confidence += event.Confidence
		if !containsString(incident.Detectors, event.DetectorID) {
			incident.Detectors = append(incident.Detectors, event.DetectorID)
		}
	}
	for i := range out {
		out[i].Agreement = len(out[i].Detectors)
		out[i].Confidence /= float64(len(out[i].EventIDs))
		sort.Strings(out[i].Detectors)
	}
	return out
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (s *server) investigationDocument(ctx context.Context, matchID string) (map[string]any, error) {
	store := s.engine.Store()
	mc, err := store.GetMatchContext(ctx, matchID)
	if err != nil {
		return nil, err
	}
	frames, err := store.GetMatchFrames(ctx, matchID)
	if err != nil {
		return nil, err
	}
	events, err := store.GetMatchEvents(ctx, matchID)
	if err != nil {
		return nil, err
	}
	reviews, _ := store.GetEventReviewsByMatch(ctx, matchID)
	notes, _ := store.ListInvestigationNotes(ctx, matchID)
	runs, _ := store.ListAnalysisRuns(ctx, matchID, 20)
	quality := pipeline.AssessTelemetryQuality(frames, s.engine.Config())

	sort.SliceStable(frames, func(i, j int) bool {
		if frames[i].FrameIndex != frames[j].FrameIndex {
			return frames[i].FrameIndex < frames[j].FrameIndex
		}
		return frames[i].PlayerID < frames[j].PlayerID
	})
	extractor := pipeline.NewFeatureExtractor(s.engine.Config().Pipeline.HistoryWindow)
	extractor.SetMaxFrameDt(s.engine.Config().Pipeline.MaxFrameDt)
	extractor.SetHighPingThreshold(s.engine.Config().Pipeline.HighPingThresholdMs)
	states := make(map[string]*model.PlayerState)
	playerTarget := maxInt(20, 600/maxInt(1, quality.Players))
	playerSeen := make(map[string]int)
	points := make([]investigationPoint, 0, minInt(len(frames), 600))
	contexts := map[string]int{}
	for i := range frames {
		frame := &frames[i]
		state := states[frame.PlayerID]
		if state == nil {
			state = &model.PlayerState{PlayerID: frame.PlayerID, Team: mc.TeamAssignments[frame.PlayerID]}
			states[frame.PlayerID] = state
		}
		extractor.UpdatePlayerState(state, frame, mc)
		contexts[state.LegalContext.PrimaryExplanation]++
		playerSeen[frame.PlayerID]++
		playerStep := maxInt(1, int(math.Ceil(float64(quality.PerPlayerRows[frame.PlayerID])/float64(playerTarget))))
		if (playerSeen[frame.PlayerID]-1)%playerStep != 0 && playerSeen[frame.PlayerID] != quality.PerPlayerRows[frame.PlayerID] {
			continue
		}
		gameSpeed, discSpeed := 0.0, 0.0
		if frame.ReportedVelocity != nil {
			gameSpeed = frame.ReportedVelocity.Magnitude()
		}
		if frame.Disc != nil {
			discSpeed = frame.Disc.Speed
		}
		points = append(points, investigationPoint{Frame: frame.FrameIndex, Time: frame.Timestamp,
			PlayerID: frame.PlayerID, PlayerName: nameOf(mc, frame.PlayerID), PoseSpeed: state.Speed,
			GameSpeed: gameSpeed, DiscSpeed: discSpeed, LeftHandSpeed: state.LeftHandSpeed,
			RightHandSpeed: state.RightHandSpeed, PingMS: frame.EstimatedPingMs, Context: state.LegalContext})
	}

	view, err := s.storedMatchView(ctx, matchID)
	if err != nil {
		return nil, err
	}
	var throws []throwEnvelope
	if view.Summary != nil {
		pingByPlayer := map[string][]float64{}
		for _, frame := range frames {
			if frame.EstimatedPingMs > 0 {
				pingByPlayer[frame.PlayerID] = append(pingByPlayer[frame.PlayerID], frame.EstimatedPingMs)
			}
		}
		for _, throw := range view.Summary.Throws {
			uncertainty := .12 + (100-quality.Score)*.006
			if values := pingByPlayer[throw.PlayerID]; len(values) > 0 {
				var sum float64
				for _, value := range values {
					sum += value
				}
				uncertainty += (sum / float64(len(values))) * .0015
			}
			cap := mc.Physics.DiscSpeedCap
			throws = append(throws, throwEnvelope{ThrowEvent: throw, Cap: cap, Uncertainty: uncertainty,
				SampleAboveGuide: throw.Speed-uncertainty > cap, MarginKind: "heuristic_unvalidated", NearBoundary: math.Abs(throw.Speed-cap) <= uncertainty})
		}
	}
	reviewed := 0
	for _, event := range events {
		if _, ok := reviews[event.EventID]; ok {
			reviewed++
		}
	}
	return map[string]any{
		"version": 1, "match": view, "quality": quality, "timeline": points,
		"incidents": buildIncidents(events, mc), "throws": throws, "legal_context_counts": contexts,
		"notes": notes, "analysis_runs": runs,
		"review_progress": map[string]any{"total": len(events), "reviewed": reviewed, "remaining": len(events) - reviewed,
			"percent": percent(reviewed, len(events))},
		"playlist": buildPlaylist(events, view.Summary, mc),
		"limits": []string{
			"Replays do not identify the physical object contacted by a hand or head; slap, push, head contact, and tracking-loss context remain conservative possibilities.",
			"Uncertainty bands describe measurement tolerance, not permission to exceed the configured game cap.",
			"Detector agreement raises review priority but is not proof because detectors can share telemetry inputs.",
		},
	}, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func percent(n, total int) float64 {
	if total == 0 {
		return 100
	}
	return 100 * float64(n) / float64(total)
}

func buildPlaylist(events []model.DetectionEvent, summary *replay.MatchSummary, mc *model.MatchContext) []map[string]any {
	var out []map[string]any
	for _, event := range events {
		out = append(out, map[string]any{"kind": "detector", "id": event.EventID, "frame": event.FrameIndex,
			"time": event.Timestamp, "player_id": event.PlayerID, "player_name": nameOf(mc, event.PlayerID), "label": event.DetectorID})
	}
	if summary != nil {
		for i, throw := range summary.Throws {
			out = append(out, map[string]any{"kind": "throw", "id": fmt.Sprintf("throw-%d", i), "frame": throw.FrameIndex,
				"time": throw.Time, "player_id": throw.PlayerID, "player_name": throw.Player, "label": fmt.Sprintf("Throw %.2f m/s", throw.Speed)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["time"].(float64) < out[j]["time"].(float64) })
	return out
}

func (s *server) handleInvestigation(w http.ResponseWriter, r *http.Request) {
	doc, err := s.investigationDocument(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "loading investigation: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *server) handleListNotes(w http.ResponseWriter, r *http.Request) {
	notes, err := s.engine.Store().ListInvestigationNotes(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "loading notes: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"notes": notes})
}

func (s *server) handleStoreNote(w http.ResponseWriter, r *http.Request) {
	var note sqlite.InvestigationNote
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&note); err != nil {
		writeError(w, 400, "invalid note: %v", err)
		return
	}
	note.MatchID = r.PathValue("id")
	if note.FrameIndex == 0 && note.Kind == "note" {
		note.FrameIndex = -1
	}
	note, err := s.engine.Store().StoreInvestigationNote(r.Context(), note)
	if err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, note)
}

func (s *server) handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	ok, err := s.engine.Store().DeleteInvestigationNote(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "deleting note: %v", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": ok})
}

func (s *server) handlePlayerComparison(w http.ResponseWriter, r *http.Request) {
	ids := []string{strings.TrimSpace(r.URL.Query().Get("a")), strings.TrimSpace(r.URL.Query().Get("b"))}
	if ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		writeError(w, 400, "two different player ids are required")
		return
	}
	rows := make([]map[string]any, 0, 2)
	for _, id := range ids {
		history, err := s.playerHistory(r.Context(), id)
		if err != nil {
			writeError(w, 500, "loading player %s: %v", id, err)
			return
		}
		var events, throws int
		var maxThrow float64
		for _, match := range history.Matches {
			events += match.Events + match.ShadowEvents
			throws += match.Throws
			maxThrow = math.Max(maxThrow, match.MaxThrow)
		}
		rows = append(rows, map[string]any{"player_id": id, "name": history.PlayerName, "matches": len(history.Matches),
			"events": events, "throws": throws, "max_throw_speed": maxThrow, "history": history.Matches})
	}
	writeJSON(w, 200, map[string]any{"players": rows, "notice": "Comparison is descriptive and never normalizes one player's result into another player's verdict."})
}

// playerHistoryData is an internal version of the existing endpoint response.
type playerHistoryData struct {
	PlayerID   string               `json:"player_id"`
	PlayerName string               `json:"player_name"`
	Matches    []playerMatchHistory `json:"matches"`
}

func (s *server) playerHistory(ctx context.Context, playerID string) (playerHistoryData, error) {
	matches, err := s.engine.Store().ListMatches(ctx, 500)
	if err != nil {
		return playerHistoryData{}, err
	}
	out := playerHistoryData{PlayerID: playerID, Matches: []playerMatchHistory{}}
	for _, stored := range matches {
		mc := stored.Context
		present := containsString(mc.PlayerIDs, playerID)
		if !present {
			continue
		}
		if out.PlayerName == "" {
			out.PlayerName = nameOf(mc, playerID)
		}
		events, _ := s.engine.Store().GetMatchEvents(ctx, mc.MatchID)
		row := playerMatchHistory{MatchID: mc.MatchID, StartTime: fmtTime(mc.StartTime), Team: mc.TeamAssignments[playerID]}
		for _, event := range events {
			if event.PlayerID == playerID {
				if event.IsShadow {
					row.ShadowEvents++
				} else {
					row.Events++
				}
			}
		}
		if raw, err := s.engine.Store().GetMatchSummaryJSON(ctx, mc.MatchID); err == nil {
			var summary replay.MatchSummary
			if json.Unmarshal(raw, &summary) == nil {
				for _, throw := range summary.Throws {
					if throw.PlayerID == playerID {
						row.Throws++
						row.MeanThrow += throw.Speed
						row.MaxThrow = math.Max(row.MaxThrow, throw.Speed)
					}
				}
				if row.Throws > 0 {
					row.MeanThrow /= float64(row.Throws)
				}
			}
		}
		out.Matches = append(out.Matches, row)
	}
	return out, nil
}

func (s *server) handleValidationDashboard(w http.ResponseWriter, r *http.Request) {
	dashboard, err := s.buildCalibrationDashboard(r.Context(), nil)
	if err != nil {
		s.rollBackAllPromotions(r.Context())
		writeError(w, 500, "building calibration dashboard: %v", err)
		return
	}
	runs, _ := s.engine.Store().ListAnalysisRuns(r.Context(), "", 500)
	dashboard.Drift = computeDrift(runs)
	dashboard.AutoRolledBack = s.reconcilePromotions(r.Context(), dashboard)
	writeJSON(w, 200, dashboard)
}

func computeDrift(runs []sqlite.AnalysisRun) map[string]any {
	if len(runs) < 4 {
		return map[string]any{"available": false, "message": "At least four recorded analyses are needed."}
	}
	mid := len(runs) / 2
	average := func(values []sqlite.AnalysisRun) (quality, events, millis float64) {
		for _, run := range values {
			quality += run.TelemetryQuality
			events += float64(run.EventsProduced)
			millis += float64(run.PipelineMilliseconds)
		}
		n := float64(len(values))
		return quality / n, events / n, millis / n
	}
	newQ, newE, newM := average(runs[:mid])
	oldQ, oldE, oldM := average(runs[mid:])
	return map[string]any{"available": true, "recent": map[string]float64{"quality": newQ, "events": newE, "pipeline_ms": newM},
		"baseline": map[string]float64{"quality": oldQ, "events": oldE, "pipeline_ms": oldM},
		"change":   map[string]float64{"quality": newQ - oldQ, "events": newE - oldE, "pipeline_ms": newM - oldM}}
}

func (s *server) handleExperimentMatrix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MatchID          string    `json:"match_id"`
		DetectorID       string    `json:"detector_id"`
		Parameter        string    `json:"parameter"`
		Scope            string    `json:"scope"`
		Enabled          *bool     `json:"enabled,omitempty"`
		Values           []float64 `json:"values"`
		LegacyMatchID    string    `json:"MatchID"`
		LegacyDetectorID string    `json:"DetectorID"`
		LegacyParameter  string    `json:"Parameter"`
		LegacyValues     []float64 `json:"Values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, 400, "invalid matrix: %v", err)
		return
	}
	if req.MatchID == "" {
		req.MatchID = req.LegacyMatchID
	}
	if req.DetectorID == "" {
		req.DetectorID = req.LegacyDetectorID
	}
	if req.Parameter == "" {
		req.Parameter = req.LegacyParameter
	}
	if len(req.Values) == 0 {
		req.Values = req.LegacyValues
	}
	req.DetectorID = strings.ToUpper(strings.TrimSpace(req.DetectorID))
	req.Parameter = strings.TrimSpace(req.Parameter)
	if len(req.Values) == 0 || len(req.Values) > 12 {
		writeError(w, 400, "provide 1-12 candidate values")
		return
	}
	req.Scope = strings.ToLower(strings.TrimSpace(req.Scope))
	if req.Scope == "" {
		req.Scope = "match"
	}
	if req.Scope != "match" && req.Scope != "training" && req.Scope != "validation" {
		writeError(w, 400, "scope must be match, training, or validation")
		return
	}
	type experimentMatch struct{ id string }
	var selected []experimentMatch
	splits, err := s.experimentSplits(r.Context())
	if err != nil {
		writeError(w, 500, "verifying experiment isolation: %v", err)
		return
	}
	if req.Scope == "match" {
		if strings.TrimSpace(req.MatchID) == "" {
			writeError(w, 400, "match_id is required for match scope")
			return
		}
		if _, err := s.engine.Store().GetMatchContext(r.Context(), req.MatchID); err != nil {
			writeError(w, 404, "loading match: %v", err)
			return
		}
		if err := checkExperimentMatch(splits, req.MatchID); err != nil {
			writeError(w, http.StatusConflict, "%v", err)
			return
		}
		selected = append(selected, experimentMatch{id: req.MatchID})
	} else {
		matches, err := s.engine.Store().ListMatches(r.Context(), 100000)
		if err != nil {
			writeError(w, 500, "loading training matches: %v", err)
			return
		}
		for _, match := range matches {
			if match.Context != nil && splits[match.Context.MatchID] == req.Scope {
				selected = append(selected, experimentMatch{id: match.Context.MatchID})
			}
		}
		if len(selected) == 0 {
			writeError(w, 422, "the player-grouped %s split has no matches", req.Scope)
			return
		}
	}
	var rows []map[string]any
	for _, value := range req.Values {
		candidate := cloneConfig(s.engine.Config())
		applied, unit, err := setSandboxParam(candidate, thresholdPreviewRequest{MatchID: req.MatchID, Detector: req.DetectorID, Parameter: req.Parameter, Value: value, Enabled: req.Enabled})
		if err != nil {
			writeError(w, 400, "value %v: %v", value, err)
			return
		}
		started := time.Now()
		candidateEvents := make(map[string][]model.DetectionEvent, len(selected))
		count := 0
		for _, match := range selected {
			mc, err := s.engine.Store().GetMatchContext(r.Context(), match.id)
			if err != nil {
				writeError(w, 500, "loading match %s: %v", match.id, err)
				return
			}
			frames, err := s.engine.Store().GetMatchFrames(r.Context(), match.id)
			if err != nil {
				writeError(w, 500, "loading frames for %s: %v", match.id, err)
				return
			}
			result, err := replay.NewEngine(candidate, s.engine.Store()).NewPipeline().ProcessMatch(r.Context(), mc, frames)
			if err != nil {
				writeError(w, 500, "running matrix on %s: %v", match.id, err)
				return
			}
			candidateEvents[match.id] = result.DetectionEvents
			for _, event := range result.DetectionEvents {
				if event.DetectorID == req.DetectorID {
					count++
				}
			}
		}
		row := map[string]any{"value": applied, "unit": unit, "events": count, "matches": len(selected), "pipeline_ms": time.Since(started).Milliseconds()}
		if req.Scope == "training" || req.Scope == "validation" {
			dashboard, err := s.buildCalibrationDashboard(r.Context(), candidateEvents)
			if err != nil {
				writeError(w, 500, "measuring candidate: %v", err)
				return
			}
			for _, metric := range dashboard.Detectors {
				if metric.DetectorID == req.DetectorID {
					if req.Scope == "training" {
						row["metrics"] = metric.Training
					} else {
						row["metrics"] = metric.Validation
					}
					break
				}
			}
		}
		rows = append(rows, row)
	}
	writeJSON(w, 200, map[string]any{"match_id": req.MatchID, "scope": req.Scope, "detector_id": req.DetectorID, "parameter": req.Parameter, "rows": rows, "persisted": false, "holdout_used": false,
		"notice": "Training and validation sweeps never evaluate the locked holdout. Candidate values are not written to the database or active configuration."})
}

func syntheticFrames(kind string) []model.PlayerTelemetryFrame {
	velocity := model.Vec3{0, 0, 1}
	frames := make([]model.PlayerTelemetryFrame, 45)
	for i := range frames {
		z := 5 + float64(i)/15
		frame := model.PlayerTelemetryFrame{PlayerID: "SYNTHETIC", Team: "blue", FrameIndex: i,
			Timestamp: float64(i) / 15, DeltaTime: 1.0 / 15, Position: model.Vec3{0, 1.7, z},
			Rotation: model.QuatIdentity(), LeftHandPosition: model.Vec3{-0.35, 1.45, z},
			RightHandPosition: model.Vec3{0.35, 1.45, z}, LeftHandRotation: model.QuatIdentity(),
			RightHandRotation: model.QuatIdentity(), ReportedVelocity: &velocity, GamePhase: "playing"}
		if kind == "playspace-step" {
			still := model.Vec3{}
			frame.ReportedVelocity = &still
		}
		if kind == "block-slap" && i == 20 {
			burst := model.Vec3{0, 0, 3}
			frame.ReportedVelocity = &burst
			frame.RightHandPosition[2] += 1
		}
		if kind == "tracking-drop" && i >= 20 && i <= 22 {
			frame.LeftHandPosition = model.Vec3{}
			frame.LeftHandRotation = model.Quat{}
		}
		if kind == "timestamp-reversal" && i >= 20 {
			frame.Timestamp = float64(38-i) / 15
		}
		if kind == "disc-cap-breach" {
			held := i <= 20
			discVelocity := model.Vec3{}
			if !held {
				discVelocity = model.Vec3{0, 0, 20.2}
			}
			frame.HasPossession = held
			frame.Disc = &model.DiscState{Position: model.Vec3{0.35, 1.45, z + .1}, Velocity: discVelocity,
				Speed: discVelocity.Magnitude(), PossessorID: func() string {
					if held {
						return "SYNTHETIC"
					}
					return ""
				}(), IsHeld: held}
		}
		frames[i] = frame
	}
	return frames
}

func (s *server) handleSyntheticProbes(w http.ResponseWriter, r *http.Request) {
	specs := []struct{ id, purpose, expected string }{
		{"clean-glide", "ordinary game-authored motion", "no movement anomaly"},
		{"playspace-step", "short coherent head-and-hands physical step", "legal-context suppression candidate"},
		{"block-slap", "brief hand burst plus player acceleration", "possible slap/push context"},
		{"tracking-drop", "missing hand pose segment", "confidence reduction"},
		{"timestamp-reversal", "non-monotonic source clock", "telemetry hard gate"},
		{"disc-cap-breach", "release above configured 18.9 m/s cap", "throw signal and review clip"},
	}
	var probes []map[string]any
	for _, spec := range specs {
		frames := syntheticFrames(spec.id)
		mc := &model.MatchContext{MatchID: "SYNTHETIC-" + spec.id, PlayerIDs: []string{"SYNTHETIC"},
			PlayerNames: map[string]string{"SYNTHETIC": "Synthetic probe"}, TeamAssignments: map[string]string{"SYNTHETIC": "blue"},
			GameMode: "Echo_Arena", TickRate: 15, Physics: s.engine.Physics(), Source: "generated-test"}
		result, err := s.engine.NewPipeline().ProcessMatch(r.Context(), mc, frames)
		detectors := []string{}
		quality := pipeline.AssessTelemetryQuality(frames, s.engine.Config())
		if err == nil {
			for _, event := range result.DetectionEvents {
				if !containsString(detectors, event.DetectorID) {
					detectors = append(detectors, event.DetectorID)
				}
			}
		}
		sort.Strings(detectors)
		probes = append(probes, map[string]any{"id": spec.id, "purpose": spec.purpose, "expected": spec.expected,
			"generated_frames": len(frames), "quality": quality, "detectors": detectors, "error": errorText(err)})
	}
	writeJSON(w, 200, map[string]any{"version": 1, "probes": probes,
		"notice": "These deterministic, generated telemetry cases are adversarial regression inputs; they are not labeled examples of real players."})
}

func (s *server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.engine.Store().ListConfigProfiles(r.Context())
	if err != nil {
		writeError(w, 500, "loading profiles: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"profiles": profiles, "active": s.activeProfileName(r.Context()), "current_fingerprint": s.configFingerprint()})
}

func shadowSafeDetectors(detectors map[string]config.DetectorConfig) map[string]config.DetectorConfig {
	out := make(map[string]config.DetectorConfig, len(detectors))
	for id, dc := range detectors {
		dc.Mode = "shadow"
		dc.AutoEnforce = false
		dc.EnforcementWeight = 0
		out[id] = dc
	}
	return out
}

func (s *server) handleStoreProfile(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Description string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeError(w, 400, "invalid profile: %v", err)
		return
	}
	doc, _ := json.Marshal(shadowSafeDetectors(s.engine.Config().Detectors))
	profile, err := s.engine.Store().StoreConfigProfile(r.Context(), sqlite.ConfigProfile{Name: body.Name, Description: body.Description, DetectorsJSON: string(doc)})
	if err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, profile)
}

func (s *server) handleActivateProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.Store().ActivateConfigProfile(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, 404, "activating profile: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "restart_required": true})
}
func (s *server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	ok, err := s.engine.Store().DeleteConfigProfile(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, 500, "deleting profile: %v", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": ok})
}

func (s *server) handleFilters(w http.ResponseWriter, r *http.Request) {
	filters, err := s.engine.Store().ListSavedFilters(r.Context())
	if err != nil {
		writeError(w, 500, "loading filters: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"filters": filters})
}
func (s *server) handleStoreFilter(w http.ResponseWriter, r *http.Request) {
	var f sqlite.SavedFilter
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&f); err != nil {
		writeError(w, 400, "invalid filter: %v", err)
		return
	}
	out, err := s.engine.Store().StoreSavedFilter(r.Context(), f)
	if err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *server) handleDeleteFilter(w http.ResponseWriter, r *http.Request) {
	ok, err := s.engine.Store().DeleteSavedFilter(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, 500, "deleting filter: %v", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": ok})
}

type evidenceLibrary struct {
	Version       int                             `json:"version"`
	ExportedAt    time.Time                       `json:"exported_at"`
	AppVersion    string                          `json:"app_version"`
	Labels        []sqlite.MatchLabel             `json:"match_labels"`
	Reviews       []sqlite.EventReview            `json:"event_reviews"`
	Opportunities []sqlite.CalibrationOpportunity `json:"calibration_opportunities,omitempty"`
	Notes         []sqlite.InvestigationNote      `json:"notes"`
	Filters       []sqlite.SavedFilter            `json:"saved_filters"`
}

func (s *server) handleExportLibrary(w http.ResponseWriter, r *http.Request) {
	store := s.engine.Store()
	labelsMap, err := store.GetMatchLabels(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "exporting match labels: %v", err)
		return
	}
	labels := make([]sqlite.MatchLabel, 0, len(labelsMap))
	for _, label := range labelsMap {
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i].MatchID < labels[j].MatchID })
	reviews, err := store.ListEventReviews(r.Context(), time.Time{})
	if err != nil {
		writeError(w, 500, "exporting event reviews: %v", err)
		return
	}
	opportunities, err := store.ListCalibrationOpportunities(r.Context(), "", "")
	if err != nil {
		writeError(w, 500, "exporting ground-truth opportunities: %v", err)
		return
	}
	notes, err := store.ListInvestigationNotes(r.Context(), "")
	if err != nil {
		writeError(w, 500, "exporting investigation notes: %v", err)
		return
	}
	filters, err := store.ListSavedFilters(r.Context())
	if err != nil {
		writeError(w, 500, "exporting saved filters: %v", err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="nevr-evidence-library.json"`)
	writeJSON(w, 200, evidenceLibrary{Version: 2, ExportedAt: time.Now().UTC(), AppVersion: appVersion, Labels: labels, Reviews: reviews, Opportunities: opportunities, Notes: notes, Filters: filters})
}

func (s *server) handleImportLibrary(w http.ResponseWriter, r *http.Request) {
	var lib evidenceLibrary
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&lib); err != nil {
		writeError(w, 400, "invalid library: %v", err)
		return
	}
	if lib.Version != 1 && lib.Version != 2 {
		writeError(w, 400, "unsupported library version %d", lib.Version)
		return
	}
	counts := map[string]int{"labels": 0, "reviews": 0, "opportunities": 0, "notes": 0, "filters": 0}
	rejected := map[string]int{"labels": 0, "reviews": 0, "opportunities": 0, "notes": 0, "filters": 0}
	var firstError string
	record := func(kind string, err error) {
		if err == nil {
			counts[kind]++
			return
		}
		rejected[kind]++
		if firstError == "" {
			firstError = err.Error()
		}
	}
	for _, x := range lib.Labels {
		record("labels", s.engine.Store().ImportMatchLabel(r.Context(), x))
	}
	for _, x := range lib.Reviews {
		record("reviews", s.engine.Store().ImportEventReview(r.Context(), x))
	}
	for _, x := range lib.Opportunities {
		record("opportunities", s.engine.Store().ImportCalibrationOpportunity(r.Context(), x))
	}
	for _, x := range lib.Notes {
		_, err := s.engine.Store().StoreInvestigationNote(r.Context(), x)
		record("notes", err)
	}
	for _, x := range lib.Filters {
		_, err := s.engine.Store().StoreSavedFilter(r.Context(), x)
		record("filters", err)
	}
	s.reconcileCalibrationChange(r.Context())
	totalRejected := 0
	for _, count := range rejected {
		totalRejected += count
	}
	writeJSON(w, 200, map[string]any{"ok": totalRejected == 0, "imported": counts, "rejected": rejected, "first_error": firstError})
}

func (s *server) handleSetupDiagnostics(w http.ResponseWriter, r *http.Request) {
	db := s.databasePath()
	base := filepath.Dir(db)
	probe, err := os.CreateTemp(base, ".nevr-write-test-")
	writable := err == nil
	if err == nil {
		probe.Close()
		os.Remove(probe.Name())
	}
	sparkPath, sparkErr := findSparkReplayViewer()
	sparkOK := sparkErr == nil
	unsafe := []string{}
	for id, dc := range s.engine.Config().Detectors {
		if dc.Mode != "shadow" {
			unsafe = append(unsafe, id)
		}
	}
	sort.Strings(unsafe)
	stored, _ := s.engine.Store().GetStoredMatchCount(r.Context())
	labels, _ := s.engine.Store().MatchLabelCounts(r.Context())
	writeJSON(w, 200, map[string]any{"ready": writable && len(unsafe) == 0, "data_folder": base, "database": db, "writable": writable, "write_error": errorText(err), "spark_installed": sparkOK, "spark_path": sparkPath, "unsafe_detectors": unsafe, "stored_matches": stored, "labels": labels, "steps": []string{"Confirm the evidence folder is writable.", "Install or open Spark Replay Viewer for exact-frame review.", "Add known-clean and confirmed-cheat replays, then label observations and missed opportunities.", "Export the portable evidence library before moving to another PC."}})
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type backupInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Modified string `json:"modified"`
}

func (s *server) backupDirectory() string {
	return filepath.Join(filepath.Dir(s.engine.Store().Path()), "backups")
}
func (s *server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	dir := s.backupDirectory()
	entries, _ := os.ReadDir(dir)
	var out []backupInfo
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".db" {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			out = append(out, backupInfo{Name: entry.Name(), Path: filepath.Join(dir, entry.Name()), Bytes: info.Size(), Modified: fmtTime(info.ModTime())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	writeJSON(w, 200, map[string]any{"backups": out})
}

type restoreRequest struct {
	Source      string `json:"source"`
	Target      string `json:"target"`
	RequestedAt string `json:"requested_at"`
}

func restoreRequestPath(db string) string { return db + ".restore-request.json" }
func (s *server) handleScheduleRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, 400, "invalid restore request: %v", err)
		return
	}
	name := filepath.Base(body.Name)
	if name != body.Name || strings.ToLower(filepath.Ext(name)) != ".db" {
		writeError(w, 400, "invalid backup name")
		return
	}
	source := filepath.Join(s.backupDirectory(), name)
	if err := sqlite.VerifyDatabase(r.Context(), source); err != nil {
		writeError(w, 422, "backup verification failed: %v", err)
		return
	}
	safety := filepath.Join(s.backupDirectory(), "pre-restore-"+time.Now().UTC().Format("20060102-150405")+".db")
	if err := s.engine.Store().Backup(r.Context(), safety); err != nil {
		writeError(w, 500, "creating safety backup: %v", err)
		return
	}
	request := restoreRequest{Source: source, Target: s.engine.Store().Path(), RequestedAt: fmtTime(time.Now())}
	doc, _ := json.MarshalIndent(request, "", "  ")
	if err := os.WriteFile(restoreRequestPath(s.engine.Store().Path()), doc, 0600); err != nil {
		writeError(w, 500, "scheduling restore: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "restart_required": true, "safety_backup": safety})
}

func (s *server) handleCaseReport(w http.ResponseWriter, r *http.Request) {
	doc, err := s.investigationDocument(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "loading report: %v", err)
		return
	}
	match := doc["match"].(matchView)
	quality := doc["quality"].(pipeline.TelemetryQualityReport)
	incidents := doc["incidents"].([]investigationIncident)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="nevr-case-%s.html"`, safeClipName(match.MatchID)))
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>NEVR case %s</title><style>body{font:14px system-ui;max-width:960px;margin:40px auto;padding:0 24px;color:#17202b}table{width:100%%;border-collapse:collapse}th,td{text-align:left;border-bottom:1px solid #ccd5df;padding:8px}code{font-family:monospace}</style><h1>NEVR investigation report</h1><p><b>Match:</b> <code>%s</code><br><b>Generated:</b> %s<br><b>Configuration:</b> <code>%s</code></p><h2>Telemetry</h2><p>Quality %.1f/100 (%s); gated: %t.</p><h2>Assessment</h2><p>Review needed includes shadow-only observations that add zero score. No signals means no detector finding, not verified fair play. Scoring level is separate from this assessment and is not a cheating verdict.</p><table><tr><th>Player</th><th>Assessment</th><th>Score</th><th>Scoring level</th><th>Signals</th></tr>", html.EscapeString(match.MatchID), html.EscapeString(match.MatchID), html.EscapeString(fmtTime(time.Now())), html.EscapeString(s.configFingerprint()), quality.Score, html.EscapeString(quality.Grade), quality.Gated)
	for _, p := range match.Players {
		assessment := "No signals"
		if p.Assessment.Status == model.ReviewStatusReviewNeeded {
			assessment = "Review needed"
		} else if p.Assessment.Status == model.ReviewStatusInsufficientData {
			assessment = "Insufficient data"
		}
		fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%.1f</td><td>%s</td><td>%d total (%d scored + %d shadow)</td></tr>", html.EscapeString(p.Name), assessment, p.Score, html.EscapeString(p.Level), p.Assessment.SignalCount, p.Assessment.ScoredSignals, p.Assessment.ShadowSignals)
	}
	fmt.Fprint(w, "</table><h2>Grouped incidents</h2><table><tr><th>ID</th><th>Player</th><th>Frames</th><th>Agreement</th><th>Detectors</th></tr>")
	for _, x := range incidents {
		fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%d–%d</td><td>%d</td><td>%s</td></tr>", x.ID, html.EscapeString(x.PlayerName), x.StartFrame, x.EndFrame, x.Agreement, html.EscapeString(strings.Join(x.Detectors, ", ")))
	}
	fmt.Fprint(w, "</table><p><small>Evidence-only output. Human review is required; detector agreement is not proof.</small></p>")
}
