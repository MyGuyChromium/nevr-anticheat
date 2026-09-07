package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

type eventDigest struct {
	DetectorID      string  `json:"detector_id"`
	DetectorVersion string  `json:"detector_version"`
	PlayerID        string  `json:"player_id"`
	PlayerName      string  `json:"player_name,omitempty"`
	FrameIndex      int     `json:"frame_index"`
	Severity        float64 `json:"severity"`
	Confidence      float64 `json:"confidence"`
	ObservedValue   string  `json:"observed_value"`
	ExpectedRange   string  `json:"expected_range"`
	Explanation     string  `json:"explanation"`
}

type eventChange struct {
	Status string       `json:"status"`
	Before *eventDigest `json:"before,omitempty"`
	After  *eventDigest `json:"after,omitempty"`
}

func eventKey(e model.DetectionEvent) string {
	return fmt.Sprintf("%s\x00%s\x00%d", e.DetectorID, e.PlayerID, e.FrameIndex)
}

func digestEvent(e model.DetectionEvent, mc *model.MatchContext) eventDigest {
	return eventDigest{
		DetectorID: e.DetectorID, DetectorVersion: e.DetectorVersion, PlayerID: e.PlayerID,
		PlayerName: nameOf(mc, e.PlayerID), FrameIndex: e.FrameIndex, Severity: e.Severity,
		Confidence: e.Confidence, ObservedValue: e.ObservedValue, ExpectedRange: e.ExpectedRange,
		Explanation: explainEvent(e),
	}
}

func compareEvents(before, after []model.DetectionEvent, mc *model.MatchContext) []eventChange {
	old, cur := make(map[string]model.DetectionEvent), make(map[string]model.DetectionEvent)
	for _, e := range before {
		old[eventKey(e)] = e
	}
	for _, e := range after {
		cur[eventKey(e)] = e
	}
	keys := make([]string, 0, len(old)+len(cur))
	seen := make(map[string]bool)
	for k := range old {
		seen[k] = true
		keys = append(keys, k)
	}
	for k := range cur {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]eventChange, 0, len(keys))
	for _, k := range keys {
		b, hasB := old[k]
		a, hasA := cur[k]
		change := eventChange{}
		switch {
		case !hasB:
			change.Status = "added"
			d := digestEvent(a, mc)
			change.After = &d
		case !hasA:
			change.Status = "removed"
			d := digestEvent(b, mc)
			change.Before = &d
		default:
			bd, ad := digestEvent(b, mc), digestEvent(a, mc)
			change.Before, change.After = &bd, &ad
			if b.DetectorVersion == a.DetectorVersion && b.ObservedValue == a.ObservedValue &&
				math.Abs(b.Severity-a.Severity) < 0.0001 && math.Abs(b.Confidence-a.Confidence) < 0.0001 {
				change.Status = "unchanged"
			} else {
				change.Status = "changed"
			}
		}
		out = append(out, change)
	}
	return out
}

func summarizeChanges(changes []eventChange) map[string]int {
	out := map[string]int{"added": 0, "removed": 0, "changed": 0, "unchanged": 0}
	for _, c := range changes {
		out[c.Status]++
	}
	return out
}

func (s *server) handleAnalysisComparison(w http.ResponseWriter, r *http.Request) {
	ctx, matchID := r.Context(), r.PathValue("id")
	snap, err := s.engine.Store().GetLatestAnalysisSnapshot(ctx, matchID)
	if errors.Is(err, sqlite.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "match_id": matchID, "message": "Analyze this replay again to create its first before/after comparison."})
		return
	}
	if err != nil {
		writeError(w, 500, "loading previous analysis: %v", err)
		return
	}
	current, err := s.engine.Store().GetMatchEvents(ctx, matchID)
	if err != nil {
		writeError(w, 500, "loading current analysis: %v", err)
		return
	}
	mc, _ := s.engine.Store().GetMatchContext(ctx, matchID)
	changes := compareEvents(snap.Events, current, mc)
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true, "match_id": matchID, "before_at": fmtTime(snap.CreatedAt),
		"before_events": len(snap.Events), "after_events": len(current),
		"summary": summarizeChanges(changes), "changes": changes,
	})
}

type regressionItem struct {
	MatchID     string       `json:"match_id"`
	PlayerID    string       `json:"player_id"`
	PlayerName  string       `json:"player_name,omitempty"`
	DetectorID  string       `json:"detector_id"`
	FrameIndex  int          `json:"frame_index"`
	Expectation string       `json:"expectation"`
	Passed      bool         `json:"passed"`
	Current     *eventDigest `json:"current,omitempty"`
	Comment     string       `json:"comment,omitempty"`
	ReviewedAt  string       `json:"reviewed_at"`
}

func findReviewedEvent(review sqlite.EventReview, events []model.DetectionEvent) *model.DetectionEvent {
	var best *model.DetectionEvent
	bestDistance := 11
	for i := range events {
		e := &events[i]
		if e.PlayerID != review.PlayerID || e.DetectorID != review.DetectorID {
			continue
		}
		d := e.FrameIndex - review.FrameIndex
		if d < 0 {
			d = -d
		}
		if d <= 10 && d < bestDistance {
			best, bestDistance = e, d
		}
	}
	return best
}

func (s *server) handleRegressionLab(w http.ResponseWriter, r *http.Request) {
	ctx, store := r.Context(), s.engine.Store()
	reviews, err := store.ListEventReviews(ctx, time.Time{})
	if err != nil {
		writeError(w, 500, "loading regression labels: %v", err)
		return
	}
	byMatch := make(map[string][]model.DetectionEvent)
	contexts := make(map[string]*model.MatchContext)
	items := make([]regressionItem, 0, len(reviews))
	passed, failed, excluded := 0, 0, 0
	for _, review := range reviews {
		if review.Verdict == "uncertain" {
			excluded++
			continue
		}
		if _, ok := byMatch[review.MatchID]; !ok {
			byMatch[review.MatchID], _ = store.GetMatchEvents(ctx, review.MatchID)
			contexts[review.MatchID], _ = store.GetMatchContext(ctx, review.MatchID)
		}
		current := findReviewedEvent(review, byMatch[review.MatchID])
		expectation := "signal remains present"
		ok := current != nil
		if review.Verdict == "no" {
			expectation, ok = "legal play stays clear", current == nil
		}
		item := regressionItem{MatchID: review.MatchID, PlayerID: review.PlayerID,
			PlayerName: nameOf(contexts[review.MatchID], review.PlayerID), DetectorID: review.DetectorID,
			FrameIndex: review.FrameIndex, Expectation: expectation, Passed: ok,
			Comment: review.Comment, ReviewedAt: fmtTime(review.ReviewedAt)}
		if current != nil {
			d := digestEvent(*current, contexts[review.MatchID])
			item.Current = &d
		}
		if ok {
			passed++
		} else {
			failed++
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": len(items), "passed": passed, "failed": failed,
		"excluded_unsure": excluded, "items": items,
		"notice": "Regression expectations come from your Correct and False positive detector labels. No player is punished by this test."})
}

type thresholdPreviewRequest struct {
	MatchID   string  `json:"match_id"`
	Detector  string  `json:"detector_id"`
	Parameter string  `json:"parameter"`
	Value     float64 `json:"value"`
	Enabled   *bool   `json:"enabled,omitempty"`
}

func cloneConfig(in *config.Config) *config.Config {
	out := *in
	out.Shadow.ShadowDetectors = append([]string(nil), in.Shadow.ShadowDetectors...)
	out.Warnings = append([]string(nil), in.Warnings...)
	out.Detectors = make(map[string]config.DetectorConfig, len(in.Detectors))
	for id, dc := range in.Detectors {
		copyDC := dc
		copyDC.Params = make(map[string]any, len(dc.Params))
		for k, v := range dc.Params {
			copyDC.Params[k] = v
		}
		out.Detectors[id] = copyDC
	}
	return &out
}

func setSandboxParam(cfg *config.Config, req thresholdPreviewRequest) (any, string, error) {
	spec, ok := config.DetectorSpecFor(req.Detector)
	if !ok {
		return nil, "", fmt.Errorf("unknown detector %q", req.Detector)
	}
	var param *config.ParamSpec
	for i := range spec.Params {
		if spec.Params[i].Key == req.Parameter {
			param = &spec.Params[i]
			break
		}
	}
	if param == nil {
		return nil, "", fmt.Errorf("unknown parameter %q for %s", req.Parameter, req.Detector)
	}
	if math.IsNaN(req.Value) || math.IsInf(req.Value, 0) {
		return nil, "", errors.New("value must be finite")
	}
	var value any = req.Value
	if param.Type == config.ParamInt {
		if math.Trunc(req.Value) != req.Value {
			return nil, "", errors.New("this parameter requires a whole number")
		}
		value = int(req.Value)
	} else if param.Type != config.ParamFloat {
		return nil, "", errors.New("only numeric parameters can be previewed")
	}
	dc := cfg.GetDetectorConfig(req.Detector)
	if dc.Params == nil {
		dc.Params = make(map[string]any)
	}
	dc.Params[req.Parameter] = value
	if req.Enabled != nil {
		dc.Enabled = *req.Enabled
	}
	cfg.Detectors[req.Detector] = dc
	if err := config.Validate(cfg); err != nil {
		return nil, "", err
	}
	return value, param.Unit, nil
}

func (s *server) handleThresholdSpecs(w http.ResponseWriter, _ *http.Request) {
	items := make([]map[string]any, 0)
	for _, spec := range config.DetectorSpecs() {
		dc := s.engine.Config().GetDetectorConfig(spec.ID)
		params := make([]map[string]any, 0)
		for _, p := range spec.Params {
			if p.Type != config.ParamFloat && p.Type != config.ParamInt {
				continue
			}
			value := p.Default
			if v, ok := dc.Params[p.Key]; ok {
				value = v
			}
			params = append(params, map[string]any{"key": p.Key, "type": p.Type, "value": value, "unit": p.Unit, "doc": p.Doc})
		}
		items = append(items, map[string]any{"id": spec.ID, "name": spec.Name, "enabled": dc.Enabled, "params": params})
	}
	writeJSON(w, http.StatusOK, map[string]any{"detectors": items})
}

func (s *server) handleThresholdPreview(w http.ResponseWriter, r *http.Request) {
	var req thresholdPreviewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, 400, "invalid preview request: %v", err)
		return
	}
	if strings.TrimSpace(req.MatchID) == "" {
		writeError(w, 400, "match_id is required")
		return
	}
	splits, isolationErr := s.experimentSplits(r.Context())
	if isolationErr != nil {
		writeError(w, 500, "verifying preview isolation: %v", isolationErr)
		return
	}
	if err := checkExperimentMatch(splits, req.MatchID); err != nil {
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
	candidate := cloneConfig(s.engine.Config())
	value, unit, err := setSandboxParam(candidate, req)
	if err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	store, ctx := s.engine.Store(), r.Context()
	mc, err := store.GetMatchContext(ctx, req.MatchID)
	if err != nil {
		writeError(w, 404, "loading match: %v", err)
		return
	}
	frames, err := store.GetMatchFrames(ctx, req.MatchID)
	if err != nil {
		writeError(w, 500, "loading match frames: %v", err)
		return
	}
	result, err := replay.NewEngine(candidate, store).NewPipeline().ProcessMatch(ctx, mc, frames)
	if err != nil {
		writeError(w, 500, "running preview: %v", err)
		return
	}
	current, err := store.GetMatchEvents(ctx, req.MatchID)
	if err != nil {
		writeError(w, 500, "loading current events: %v", err)
		return
	}
	filter := func(events []model.DetectionEvent) []model.DetectionEvent {
		out := make([]model.DetectionEvent, 0)
		for _, e := range events {
			if e.DetectorID == req.Detector {
				out = append(out, e)
			}
		}
		return out
	}
	changes := compareEvents(filter(current), filter(result.DetectionEvents), mc)
	writeJSON(w, http.StatusOK, map[string]any{"match_id": req.MatchID, "detector_id": req.Detector,
		"parameter": req.Parameter, "value": value, "unit": unit, "persisted": false,
		"current_events": len(filter(current)), "candidate_events": len(filter(result.DetectionEvents)),
		"summary": summarizeChanges(changes), "changes": changes,
		"notice": "Preview only. The database and active configuration were not changed."})
}

type playerMatchHistory struct {
	MatchID       string  `json:"match_id"`
	StartTime     string  `json:"start_time"`
	Team          string  `json:"team"`
	Events        int     `json:"events"`
	ShadowEvents  int     `json:"shadow_events"`
	Throws        int     `json:"throws"`
	MeanThrow     float64 `json:"mean_throw_speed"`
	MaxThrow      float64 `json:"max_throw_speed"`
	ReviewedValid int     `json:"reviewed_valid"`
	ReviewedFalse int     `json:"reviewed_false"`
}

func (s *server) handlePlayerHistory(w http.ResponseWriter, r *http.Request) {
	ctx, store, pid := r.Context(), s.engine.Store(), strings.TrimSpace(r.PathValue("id"))
	if pid == "" {
		writeError(w, 400, "player id is required")
		return
	}
	matchIDs, err := store.GetPlayerMatchIDs(ctx, pid, 200)
	if err != nil {
		writeError(w, 500, "loading player matches: %v", err)
		return
	}
	reviews, _ := store.ListEventReviews(ctx, time.Time{})
	reviewByMatch := make(map[string][]sqlite.EventReview)
	for _, rev := range reviews {
		if rev.PlayerID == pid {
			reviewByMatch[rev.MatchID] = append(reviewByMatch[rev.MatchID], rev)
		}
	}
	detectors := make(map[string]int)
	items := make([]playerMatchHistory, 0, len(matchIDs))
	name := ""
	totalThrows, totalEvents, totalShadow := 0, 0, 0
	weightedThrowSpeed, maxThrow := 0.0, 0.0
	for _, mid := range matchIDs {
		mc, err := store.GetMatchContext(ctx, mid)
		if err != nil {
			continue
		}
		if name == "" {
			name = nameOf(mc, pid)
		}
		events, _ := store.GetMatchPlayerEvents(ctx, mid, pid)
		item := playerMatchHistory{MatchID: mid, StartTime: fmtTime(mc.StartTime), Team: mc.TeamAssignments[pid]}
		for _, e := range events {
			detectors[e.DetectorID]++
			item.Events++
			if e.IsShadow {
				item.ShadowEvents++
			}
		}
		for _, rev := range reviewByMatch[mid] {
			if rev.Verdict == "yes" {
				item.ReviewedValid++
			} else if rev.Verdict == "no" {
				item.ReviewedFalse++
			}
		}
		if doc, err := store.GetMatchSummaryJSON(ctx, mid); err == nil {
			var sum replay.MatchSummary
			if json.Unmarshal(doc, &sum) == nil {
				for _, p := range sum.Players {
					if p.PlayerID == pid {
						item.Throws, item.MeanThrow, item.MaxThrow = p.Throws.Count, p.Throws.MeanSpeed, p.Throws.MaxSpeed
					}
				}
			}
		}
		totalThrows += item.Throws
		weightedThrowSpeed += item.MeanThrow * float64(item.Throws)
		if item.MaxThrow > maxThrow {
			maxThrow = item.MaxThrow
		}
		totalEvents += item.Events
		totalShadow += item.ShadowEvents
		items = append(items, item)
	}
	meanThrow := 0.0
	if totalThrows > 0 {
		meanThrow = weightedThrowSpeed / float64(totalThrows)
	}
	writeJSON(w, http.StatusOK, map[string]any{"player_id": pid, "name": name, "matches": items,
		"match_count": len(items), "events": totalEvents, "shadow_events": totalShadow,
		"throws": totalThrows, "mean_throw_speed": meanThrow, "max_throw_speed": maxThrow,
		"detectors": detectors})
}

func explainEvent(e model.DetectionEvent) string {
	shadow := " This is observation-only and adds zero score."
	if !e.IsShadow {
		shadow = " This contributed to the review score but is not a cheating verdict."
	}
	switch v := e.Evidence.(type) {
	case model.ThrowEvidence:
		over := v.ReleaseSpeed - v.EffectiveCap
		if v.ArtifactSuspected {
			return fmt.Sprintf("The replay sampled the disc at %.2f m/s—over twice the %.2f m/s cap, so this is treated as a likely telemetry artifact.%s", v.ReleaseSpeed, v.EffectiveCap, shadow)
		}
		return fmt.Sprintf("The disc was sampled at %.2f m/s, %.2f m/s above the %.2f m/s release cap. Player motion aligned with the throw contributed %.2f m/s.%s", v.ReleaseSpeed, math.Max(0, over), v.EffectiveCap, v.AlignedMovementSpeed, shadow)
	case model.ReleaseAngleEvidence:
		return fmt.Sprintf("The %s-hand motion and disc release differed by %.1f°. Head-contact candidates are excluded before this detector runs.%s", v.ThrowingHand, v.ReleaseAngle, shadow)
	case model.DiscAccelerationEvidence:
		return fmt.Sprintf("Disc speed changed by %.2f m/s between the last held sample (%.2f) and release (%.2f).%s", v.SpeedDelta, v.PreReleaseSpeed, v.ReleaseSpeed, shadow)
	case model.SignatureRepeatEvidence:
		return fmt.Sprintf("%d throws produced generalized variance %.3g across %d informative motion dimensions. Skilled regrab styles can naturally look repeatable.%s", v.ThrowCount, v.GeneralizedVariance, v.InformativeDimensions, shadow)
	case model.PrecisionEvidence:
		return fmt.Sprintf("Across %d goal-directed throws, mean goal-line deviation was %.2f m with %.2f m standard deviation.%s", v.GoalDirectedThrows, v.MeanDeviation, v.StddevDeviation, shadow)
	case model.TrajectoryEvidence:
		return fmt.Sprintf("The free disc accumulated %.1f° of heading change across %d violation frames while travelling %.2f m.%s", v.CumulativeAngleChange, v.ViolationFrameCount, v.DistanceTraveled, shadow)
	case model.SpeedDistanceEvidence:
		return fmt.Sprintf("The free disc accelerated %d times after release; the largest sampled increase was %.2f m/s.%s", v.SpeedIncreaseCount, v.MaxSpeedIncrease, shadow)
	case model.HandSpeedEvidence:
		return fmt.Sprintf("The %s controller moved %.2f m/s relative to player translation for %d tracked frames; the comparison limit was %.2f m/s.%s", v.Hand, v.Speed, v.ConsecutiveFrames, v.PhysicalLimit, shadow)
	case model.WristRotationEvidence:
		return fmt.Sprintf("The %s wrist reached %.1f rad/s across %d frames versus a %.1f rad/s physical limit.%s", v.Hand, v.AngularVelocity, v.ConsecutiveFrames, v.PhysicalLimit, shadow)
	case model.ZeroJitterEvidence:
		return fmt.Sprintf("The %s controller's position variance was %.3g across %d active frames; perfectly still telemetry can be synthetic or a tracking state.%s", v.Hand, v.PositionVariance, v.ActiveFrames, shadow)
	case model.ZeroWobbleEvidence:
		return fmt.Sprintf("The %s controller's rotation varied by only %.3g (%.3f° standard deviation) across %d frames.%s", v.Hand, v.RotationVariance, v.StdDevDegrees, v.WindowFrames, shadow)
	case model.MovementEvidence:
		return fmt.Sprintf("%s The movement metrics crossed this detector's configured window; inspect the physics panel for lean, slap, collision, stacking, and tracking context.%s", strings.TrimSpace(v.DetectorSpecific), shadow)
	case model.StateEvidence:
		if e.DetectorID == "STATE_008" {
			return "The free disc deviated from a stable constant-velocity comparison before a confirmed catch. This is a receiver-associated trajectory observation; the cause and actor are unverified. Legal contact, prediction and source corrections remain possible. It cannot prove automated grip input and never adds suspicion score."
		}
		return fmt.Sprintf("%s The interaction state crossed the configured limit.%s", strings.TrimSpace(v.DetectorSpecific), shadow)
	case model.PatternEvidence:
		return fmt.Sprintf("%s A repeated-match pattern crossed the configured limit; this is statistical evidence, not proof.%s", strings.TrimSpace(v.DetectorSpecific), shadow)
	default:
		return fmt.Sprintf("Observed %s; expected %s.%s", e.ObservedValue, e.ExpectedRange, shadow)
	}
}
