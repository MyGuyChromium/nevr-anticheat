// Package testutil provides test helpers for the anticheat system.
package testutil

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/catalog"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
)

// HarnessResult holds the output of a harness run for assertion.
type HarnessResult struct {
	T               *testing.T
	Result          *pipeline.MatchResult
	Events          []model.DetectionEvent
	PlayerScores    map[string]model.SuspicionScore
	DetectorFirings map[string]int // detectorID -> count
	CategoryFirings map[string]int // category -> count
}

// Harness runs the full pipeline on synthetic frames with specific detectors.
type Harness struct {
	cfg      *config.Config
	matchCtx *model.MatchContext
}

// NewHarness creates a test harness with all detectors in enforce mode.
func NewHarness(t *testing.T) *Harness {
	t.Helper()
	cfg := config.DefaultConfig()
	// Set all detectors to enforce mode for testing
	for id, dc := range cfg.Detectors {
		dc.Mode = "enforce"
		cfg.Detectors[id] = dc
	}
	return &Harness{cfg: cfg}
}

// NewBenchHarness creates a test harness suitable for benchmarks (no *testing.T required).
func NewBenchHarness() *Harness {
	cfg := config.DefaultConfig()
	for id, dc := range cfg.Detectors {
		dc.Mode = "enforce"
		cfg.Detectors[id] = dc
	}
	return &Harness{cfg: cfg}
}

// WithDetectors sets specific detectors to use (by ID). Others are disabled.
func (h *Harness) WithDetectors(ids ...string) *Harness {
	enabled := make(map[string]bool)
	for _, id := range ids {
		enabled[id] = true
	}
	for id, dc := range h.cfg.Detectors {
		dc.Enabled = enabled[id]
		h.cfg.Detectors[id] = dc
	}
	return h
}

// WithAllDetectors enables all detectors.
func (h *Harness) WithAllDetectors() *Harness {
	for id, dc := range h.cfg.Detectors {
		dc.Enabled = true
		h.cfg.Detectors[id] = dc
	}
	return h
}

// WithMatchContext sets a custom match context.
func (h *Harness) WithMatchContext(mc *model.MatchContext) *Harness {
	h.matchCtx = mc
	return h
}

// WithShadowMode sets all detectors to shadow mode.
func (h *Harness) WithShadowMode() *Harness {
	for id, dc := range h.cfg.Detectors {
		dc.Mode = "shadow"
		h.cfg.Detectors[id] = dc
	}
	h.cfg.Shadow.ShadowDetectors = nil
	// Put all detector IDs in shadow list
	for id := range h.cfg.Detectors {
		h.cfg.Shadow.ShadowDetectors = append(h.cfg.Shadow.ShadowDetectors, id)
	}
	return h
}

// WithDetectorParams overrides specific params for a detector.
func (h *Harness) WithDetectorParams(detectorID string, params map[string]any) *Harness {
	dc := h.cfg.Detectors[detectorID]
	if dc.Params == nil {
		dc.Params = make(map[string]any)
	}
	for k, v := range params {
		dc.Params[k] = v
	}
	h.cfg.Detectors[detectorID] = dc
	return h
}

// Run processes the given frames through the full pipeline and returns structured results.
func (h *Harness) Run(t *testing.T, frames []model.PlayerTelemetryFrame) *HarnessResult {
	t.Helper()

	// Build detectors from config
	detectors := catalog.Build(h.cfg, nil)

	// Create scorer
	scorer := scoring.NewSuspicionScorer(scoring.ScorerConfig{
		MaxSingleContribution:         h.cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: h.cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       h.cfg.Scoring.SameCategoryDiminishing,
		ReviewThreshold:               h.cfg.Scoring.ReviewThreshold,
		AutoEnforceThreshold:          h.cfg.Scoring.AutoEnforceThreshold,
		DecayHalfLifeHours:            h.cfg.Scoring.DecayHalfLifeHours,
		CooldownFrames:                h.cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           h.cfg.Scoring.CorrelationBonusCap,
	})

	// Create pipeline with quiet logger
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := pipeline.NewPipeline(h.cfg, detectors, scorer, logger)

	// Set up match context
	mc := h.matchCtx
	if mc == nil {
		mc = NewMatchContext()
		// Derive player ID from first frame if available
		if len(frames) > 0 {
			pid := frames[0].PlayerID
			mc.PlayerIDs = []string{pid}
			mc.TeamAssignments = map[string]string{pid: "blue"}
		}
	}

	// Run the pipeline
	result, err := p.ProcessMatch(context.Background(), mc, frames)
	if err != nil {
		t.Fatalf("harness: ProcessMatch failed: %v", err)
	}

	// Index events by detector and category
	detectorFirings := make(map[string]int)
	categoryFirings := make(map[string]int)
	for _, ev := range result.DetectionEvents {
		detectorFirings[ev.DetectorID]++
		cat := detectorCategory(ev.DetectorID)
		categoryFirings[cat]++
	}

	return &HarnessResult{
		T:               t,
		Result:          result,
		Events:          result.DetectionEvents,
		PlayerScores:    result.PlayerScores,
		DetectorFirings: detectorFirings,
		CategoryFirings: categoryFirings,
	}
}

// detectorCategory returns the category string from a detector ID prefix.
func detectorCategory(detectorID string) string {
	parts := strings.SplitN(detectorID, "_", 2)
	if len(parts) == 0 {
		return "unknown"
	}
	switch strings.ToUpper(parts[0]) {
	case "THROW":
		return "throw"
	case "BIO":
		return "bio"
	case "MOV":
		return "movement"
	case "STATE":
		return "state"
	case "PAT":
		return "pattern"
	default:
		return "unknown"
	}
}

// AssertNoDetections fails if any detection events were produced.
func (hr *HarnessResult) AssertNoDetections() {
	hr.T.Helper()
	if len(hr.Events) > 0 {
		hr.T.Errorf("expected no detections, got %d:\n%s", len(hr.Events), hr.Summary())
	}
}

// AssertDetectorFired fails if the given detector did NOT fire.
func (hr *HarnessResult) AssertDetectorFired(detectorID string) {
	hr.T.Helper()
	if hr.DetectorFirings[detectorID] == 0 {
		hr.T.Errorf("expected detector %s to fire, but it did not.\nFirings: %v", detectorID, hr.DetectorFirings)
	}
}

// AssertDetectorNotFired fails if the given detector DID fire.
func (hr *HarnessResult) AssertDetectorNotFired(detectorID string) {
	hr.T.Helper()
	if hr.DetectorFirings[detectorID] > 0 {
		hr.T.Errorf("expected detector %s not to fire, but it fired %d times.\n%s",
			detectorID, hr.DetectorFirings[detectorID], hr.summaryForDetector(detectorID))
	}
}

// AssertDetectorFiredN fails if the detector didn't fire exactly n times.
func (hr *HarnessResult) AssertDetectorFiredN(detectorID string, n int) {
	hr.T.Helper()
	got := hr.DetectorFirings[detectorID]
	if got != n {
		hr.T.Errorf("expected detector %s to fire exactly %d times, got %d", detectorID, n, got)
	}
}

// AssertDetectorFiredAtLeast fails if fewer than n firings.
func (hr *HarnessResult) AssertDetectorFiredAtLeast(detectorID string, n int) {
	hr.T.Helper()
	got := hr.DetectorFirings[detectorID]
	if got < n {
		hr.T.Errorf("expected detector %s to fire at least %d times, got %d", detectorID, n, got)
	}
}

// AssertMinSeverity fails if no detection from detectorID has severity >= min.
func (hr *HarnessResult) AssertMinSeverity(detectorID string, min float64) {
	hr.T.Helper()
	maxSev := 0.0
	for _, ev := range hr.Events {
		if ev.DetectorID == detectorID && ev.Severity > maxSev {
			maxSev = ev.Severity
		}
	}
	if maxSev < min {
		hr.T.Errorf("expected detector %s to have severity >= %.2f, max was %.2f", detectorID, min, maxSev)
	}
}

// AssertMaxSeverity fails if any detection from detectorID has severity > max.
func (hr *HarnessResult) AssertMaxSeverity(detectorID string, max float64) {
	hr.T.Helper()
	for _, ev := range hr.Events {
		if ev.DetectorID == detectorID && ev.Severity > max {
			hr.T.Errorf("expected detector %s severity <= %.2f, got %.2f at frame %d",
				detectorID, max, ev.Severity, ev.FrameIndex)
			return
		}
	}
}

// AssertScoreBelow fails if player score >= threshold.
func (hr *HarnessResult) AssertScoreBelow(playerID string, threshold float64) {
	hr.T.Helper()
	score, ok := hr.PlayerScores[playerID]
	if !ok {
		// No score means zero, which is below any positive threshold
		return
	}
	if score.TotalScore >= threshold {
		hr.T.Errorf("expected player %s score < %.1f, got %.1f", playerID, threshold, score.TotalScore)
	}
}

// AssertScoreAbove fails if player score < threshold.
func (hr *HarnessResult) AssertScoreAbove(playerID string, threshold float64) {
	hr.T.Helper()
	score, ok := hr.PlayerScores[playerID]
	if !ok {
		hr.T.Errorf("expected player %s score >= %.1f, but player has no score", playerID, threshold)
		return
	}
	if score.TotalScore < threshold {
		hr.T.Errorf("expected player %s score >= %.1f, got %.1f", playerID, threshold, score.TotalScore)
	}
}

// AssertNoAutoEnforce fails if any event has AutoEnforce=true.
func (hr *HarnessResult) AssertNoAutoEnforce() {
	hr.T.Helper()
	for _, ev := range hr.Events {
		if ev.AutoEnforce {
			hr.T.Errorf("expected no auto-enforce events, but %s at frame %d has AutoEnforce=true",
				ev.DetectorID, ev.FrameIndex)
			return
		}
	}
}

// AssertAllShadow fails if any event has IsShadow=false.
func (hr *HarnessResult) AssertAllShadow() {
	hr.T.Helper()
	for _, ev := range hr.Events {
		if !ev.IsShadow {
			hr.T.Errorf("expected all events to be shadow, but %s at frame %d has IsShadow=false",
				ev.DetectorID, ev.FrameIndex)
			return
		}
	}
}

// DetectorEvents returns events from a specific detector.
func (hr *HarnessResult) DetectorEvents(detectorID string) []model.DetectionEvent {
	var out []model.DetectionEvent
	for _, ev := range hr.Events {
		if ev.DetectorID == detectorID {
			out = append(out, ev)
		}
	}
	return out
}

// Summary returns a human-readable summary of what fired.
func (hr *HarnessResult) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Total events: %d, Frames processed: %d\n",
		len(hr.Events), hr.Result.FramesProcessed))
	if len(hr.DetectorFirings) > 0 {
		sb.WriteString("Detector firings:\n")
		for id, count := range hr.DetectorFirings {
			sb.WriteString(fmt.Sprintf("  %s: %d\n", id, count))
		}
	}
	for pid, score := range hr.PlayerScores {
		if score.EventCount > 0 {
			sb.WriteString(fmt.Sprintf("Player %s: score=%.1f level=%s events=%d\n",
				pid, score.TotalScore, score.Level(), score.EventCount))
		}
	}
	for _, ev := range hr.Events {
		sb.WriteString(fmt.Sprintf("  [%s] frame=%d sev=%.2f conf=%.2f shadow=%v observed=%s\n",
			ev.DetectorID, ev.FrameIndex, ev.Severity, ev.Confidence, ev.IsShadow, ev.ObservedValue))
	}
	return sb.String()
}

// summaryForDetector returns a summary filtered to a specific detector.
func (hr *HarnessResult) summaryForDetector(detectorID string) string {
	var sb strings.Builder
	for _, ev := range hr.Events {
		if ev.DetectorID == detectorID {
			sb.WriteString(fmt.Sprintf("  [%s] frame=%d sev=%.2f conf=%.2f observed=%s\n",
				ev.DetectorID, ev.FrameIndex, ev.Severity, ev.Confidence, ev.ObservedValue))
		}
	}
	return sb.String()
}

// RunBench runs the pipeline without assertions (for benchmarks).
// It returns the raw MatchResult and does not require *testing.T.
func (h *Harness) RunBench(frames []model.PlayerTelemetryFrame) *pipeline.MatchResult {
	// Build detectors from config
	detectors := catalog.Build(h.cfg, nil)

	// Create scorer
	scorer := scoring.NewSuspicionScorer(scoring.ScorerConfig{
		MaxSingleContribution:         h.cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: h.cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       h.cfg.Scoring.SameCategoryDiminishing,
		ReviewThreshold:               h.cfg.Scoring.ReviewThreshold,
		AutoEnforceThreshold:          h.cfg.Scoring.AutoEnforceThreshold,
		DecayHalfLifeHours:            h.cfg.Scoring.DecayHalfLifeHours,
		CooldownFrames:                h.cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           h.cfg.Scoring.CorrelationBonusCap,
	})

	// Create pipeline with quiet logger
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := pipeline.NewPipeline(h.cfg, detectors, scorer, logger)

	// Set up match context
	mc := h.matchCtx
	if mc == nil {
		mc = NewMatchContext()
		if len(frames) > 0 {
			pid := frames[0].PlayerID
			mc.PlayerIDs = []string{pid}
			mc.TeamAssignments = map[string]string{pid: "blue"}
		}
	}

	result, err := p.ProcessMatch(context.Background(), mc, frames)
	if err != nil {
		// In benchmark mode, we cannot call t.Fatal, so just return partial result.
		return &pipeline.MatchResult{}
	}
	return result
}

// BuildDetectors constructs all enabled detectors from config. Exported for benchmark use.
func BuildDetectors(cfg *config.Config) []detect.Detector {
	return catalog.Build(cfg, nil)
}
