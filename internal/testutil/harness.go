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
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/pattern"
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
	history  pattern.HistoryProvider

	blueGoalSide int
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

// WithEnabledDetectors runs exactly the detectors DefaultConfig enables
// (the production set), in enforce mode.
func (h *Harness) WithEnabledDetectors() *Harness {
	def := config.DefaultConfig()
	for id, dc := range h.cfg.Detectors {
		dc.Enabled = def.Detectors[id].Enabled
		h.cfg.Detectors[id] = dc
	}
	return h
}

// WithBlueGoalSide tells the feature extractor which goal the blue team
// attacks (+1: the goal at +GoalZ, -1: at -GoalZ), as production learns it
// from the first scored goal. Without it goal-directed throws are measured
// against the goal the release velocity points at.
func (h *Harness) WithBlueGoalSide(sign int) *Harness {
	h.blueGoalSide = sign
	return h
}

// WithHistoryProvider wires a cross-match history source into PAT_003
// (catalog.Build injects it the way cmd/anticheat and cmd/server do).
func (h *Harness) WithHistoryProvider(hp pattern.HistoryProvider) *Harness {
	h.history = hp
	return h
}

// Config exposes the harness configuration (production defaults with every
// detector in enforce mode) for tests that need the configured thresholds.
func (h *Harness) Config() *config.Config { return h.cfg }

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

// NewPipeline builds a production-shaped pipeline from the harness config:
// the catalog's detectors (with the history provider, if any), a scorer
// configured from the [scoring] block including its level table, and a
// quiet logger.
func (h *Harness) NewPipeline() (*pipeline.Pipeline, *scoring.SuspicionScorer) {
	detectors := catalog.Build(h.cfg, h.history)
	scorer := scoring.NewSuspicionScorer(scoring.ScorerConfig{
		MaxSingleContribution:         h.cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: h.cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       h.cfg.Scoring.SameCategoryDiminishing,
		ReviewThreshold:               h.cfg.Scoring.ReviewThreshold,
		AutoEnforceThreshold:          h.cfg.Scoring.AutoEnforceThreshold,
		DecayHalfLifeHours:            h.cfg.Scoring.DecayHalfLifeHours,
		CooldownFrames:                h.cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           h.cfg.Scoring.CorrelationBonusCap,
		Levels:                        h.cfg.Scoring.LevelTable(),
	})
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := pipeline.NewPipeline(h.cfg, detectors, scorer, logger)
	if h.blueGoalSide != 0 {
		p.Extractor().SetBlueGoalSide(h.blueGoalSide)
	}
	return p, scorer
}

// MatchContext returns the configured match context, or one derived from
// the first frame's player when none was set.
func (h *Harness) MatchContext(frames []model.PlayerTelemetryFrame) *model.MatchContext {
	if h.matchCtx != nil {
		return h.matchCtx
	}
	mc := NewMatchContext()
	if len(frames) > 0 {
		pid := frames[0].PlayerID
		mc.PlayerIDs = []string{pid}
		mc.TeamAssignments = map[string]string{pid: "blue"}
	}
	return mc
}

// Run processes the given frames through the full pipeline and returns structured results.
func (h *Harness) Run(t *testing.T, frames []model.PlayerTelemetryFrame) *HarnessResult {
	t.Helper()
	p, _ := h.NewPipeline()
	mc := h.MatchContext(frames)

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

// RunBench runs the pipeline without assertions and without *testing.T. A
// ProcessMatch error is returned, never swallowed: a benchmark timing a
// failing pipeline must fail (see MustRunBench).
func (h *Harness) RunBench(frames []model.PlayerTelemetryFrame) (*pipeline.MatchResult, error) {
	p, _ := h.NewPipeline()
	return p.ProcessMatch(context.Background(), h.MatchContext(frames), frames)
}

// MustRunBench is RunBench for benchmarks: it fails the benchmark on error
// or when no frame was processed.
func (h *Harness) MustRunBench(b *testing.B, frames []model.PlayerTelemetryFrame) *pipeline.MatchResult {
	b.Helper()
	result, err := h.RunBench(frames)
	if err != nil {
		b.Fatalf("harness: ProcessMatch failed: %v", err)
	}
	if len(frames) > 0 && result.FramesProcessed == 0 {
		b.Fatalf("harness: %d frames given, none processed (%d invalid: %v)", len(frames), result.InvalidFrames, result.InvalidFrameReasons)
	}
	return result
}

// MaxSeverity returns the highest severity among detectorID's events (0
// when it did not fire).
func (hr *HarnessResult) MaxSeverity(detectorID string) float64 {
	m := 0.0
	for _, ev := range hr.Events {
		if ev.DetectorID == detectorID && ev.Severity > m {
			m = ev.Severity
		}
	}
	return m
}

// MaxConfidence returns the highest confidence among detectorID's events.
func (hr *HarnessResult) MaxConfidence(detectorID string) float64 {
	m := 0.0
	for _, ev := range hr.Events {
		if ev.DetectorID == detectorID && ev.Confidence > m {
			m = ev.Confidence
		}
	}
	return m
}

// AssertMinConfidence fails if no detection from detectorID has confidence >= min.
func (hr *HarnessResult) AssertMinConfidence(detectorID string, min float64) {
	hr.T.Helper()
	if got := hr.MaxConfidence(detectorID); got < min {
		hr.T.Errorf("expected detector %s to have confidence >= %.2f, max was %.2f", detectorID, min, got)
	}
}

// AssertAllFramesValid fails unless every frame was accepted by the
// validator and processed.
func (hr *HarnessResult) AssertAllFramesValid(nFrames int) {
	hr.T.Helper()
	if hr.Result.InvalidFrames != 0 {
		hr.T.Errorf("expected no invalid frames, got %d: %v", hr.Result.InvalidFrames, hr.Result.InvalidFrameReasons)
	}
	if hr.Result.FramesProcessed != nFrames {
		hr.T.Errorf("expected %d frames processed, got %d", nFrames, hr.Result.FramesProcessed)
	}
}

// AssertNoDetectionsFor fails if any event names playerID.
func (hr *HarnessResult) AssertNoDetectionsFor(playerID string) {
	hr.T.Helper()
	for _, ev := range hr.Events {
		if ev.PlayerID == playerID {
			hr.T.Errorf("expected no detections for %s, got %s at frame %d (%s)", playerID, ev.DetectorID, ev.FrameIndex, ev.ObservedValue)
		}
	}
}

// PlayerEvents returns the events naming playerID.
func (hr *HarnessResult) PlayerEvents(playerID string) []model.DetectionEvent {
	var out []model.DetectionEvent
	for _, ev := range hr.Events {
		if ev.PlayerID == playerID {
			out = append(out, ev)
		}
	}
	return out
}

// BuildDetectors constructs all enabled detectors from config. Exported for benchmark use.
func BuildDetectors(cfg *config.Config) []detect.Detector {
	return catalog.Build(cfg, nil)
}
