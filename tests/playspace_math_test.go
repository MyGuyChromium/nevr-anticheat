package tests

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/pattern"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// pausedPlayspaceMathResult reuses event assertion helpers only. Result is nil:
// no production pipeline, scoring, storage, coverage or enforcement was run.
type pausedPlayspaceMathResult struct {
	*testutil.HarnessResult
	Reasons map[string]int
}

// runPausedPlayspaceMath lives only in _test.go so retaining detector-math
// regressions creates no production option for bypassing the compiled pause.
// It validates and extracts the single-player synthetic stream using actual
// production components, honors warmup and phase gates, and records the raw
// Evaluate results and visited branches. No thresholds or evidence are tuned.
func runPausedPlayspaceMath(t *testing.T, frames []model.PlayerTelemetryFrame, id string) *pausedPlayspaceMathResult {
	t.Helper()
	if len(frames) == 0 {
		t.Fatal("paused detector math fixture requires frames")
	}
	cfg := config.DefaultConfig()
	dc := cfg.Detectors[id] // original math parameters, not resolved enable policy
	var detector detect.Detector
	switch id {
	case "MOV_006":
		detector = movement.NewMov006(dc.Params)
	case "PAT_005":
		detector = pattern.NewPat005(dc.Params)
	default:
		t.Fatalf("test-only math helper does not support %q", id)
	}
	detector.(detect.WeightSetter).SetWeight(dc.EnforcementWeight)
	detector.(detect.AutoEnforceSetter).SetAutoEnforce(dc.AutoEnforce)
	result := &pausedPlayspaceMathResult{
		HarnessResult: &testutil.HarnessResult{T: t, DetectorFirings: map[string]int{}, CategoryFirings: map[string]int{}},
		Reasons:       map[string]int{},
	}
	if observed, ok := detector.(detect.DecisionObservable); ok {
		observed.SetDecisionObserver(func(detectorID, playerID string, frame int, reason string) {
			if detectorID != id || playerID != frames[0].PlayerID {
				t.Fatalf("unexpected branch attribution %s/%s at %d", detectorID, playerID, frame)
			}
			result.Reasons[reason]++
		})
		defer observed.SetDecisionObserver(nil)
	}
	mc := matchContextForPlayer(frames[0].PlayerID)
	validator := pipeline.NewFrameValidator(cfg)
	extractor := pipeline.NewFeatureExtractor(cfg.Pipeline.HistoryWindow)
	extractor.SetMaxFrameDt(cfg.Pipeline.MaxFrameDt)
	extractor.SetHighPingThreshold(cfg.Pipeline.HighPingThresholdMs)
	state := &model.PlayerState{PlayerID: frames[0].PlayerID, Team: frames[0].Team}
	players := map[string]*model.PlayerState{state.PlayerID: state}
	evaluated := 0
	for i, frame := range frames {
		if frame.PlayerID != state.PlayerID || (i > 0 && frame.FrameIndex <= frames[i-1].FrameIndex) {
			t.Fatal("math helper requires one ordered player stream")
		}
		if _, err := validator.Validate(&frame, mc); err != nil {
			t.Fatalf("math fixture frame %d failed production validation: %v", frame.FrameIndex, err)
		}
		extractor.UpdatePlayerState(state, &frame, mc)
		if state.FrameCount <= detector.WarmupFrames() || (frame.GamePhase != "" && !mc.IsActivePhase(frame.GamePhase)) {
			continue
		}
		evaluated++
		for _, event := range detector.Evaluate(mc, players, frame.FrameIndex) {
			if err := event.Validate(); err != nil {
				t.Fatalf("math helper received invalid event: %v", err)
			}
			result.Events = append(result.Events, event)
			result.DetectorFirings[event.DetectorID]++
			result.CategoryFirings[detector.Category()]++
		}
	}
	if evaluated == 0 {
		t.Fatal("fixture never evaluated the retained detector math")
	}
	return result
}

func TestProductionHarnessCannotResumePausedPlayspaceDetectors(t *testing.T) {
	walking := make([]model.PlayerTelemetryFrame, 100)
	for i := range walking {
		walking[i] = legalMechanicsFrame(i, model.Vec3{1, 1.7, float64(i) * .1})
		zero := model.Vec3{}
		walking[i].ReportedVelocity = &zero
	}
	for id, frames := range map[string][]model.PlayerTelemetryFrame{
		"MOV_006": walking,
		"PAT_005": player1().ExtendedReach(100),
	} {
		t.Run(id, func(t *testing.T) {
			// Prove these are trigger-capable fixtures, not silent inputs.
			runPausedPlayspaceMath(t, frames, id).AssertDetectorFired(id)
			// WithDetectors explicitly writes enabled=true; production must
			// still suppress dispatch, events and scoring on each fresh run.
			for run := 0; run < 2; run++ {
				result := testutil.NewHarness(t).WithDetectors(id).Run(t, frames)
				result.AssertAllFramesValid(len(frames))
				result.AssertNoDetections()
				result.AssertScoreBelow(frames[0].PlayerID, 1)
				coverage := result.Result.PlayerCoverage[frames[0].PlayerID]
				if coverage == nil {
					t.Fatal("production did not record player coverage")
				}
				found := false
				for _, row := range coverage.Detectors {
					if row.DetectorID == id {
						found = true
						if row.Enabled || row.Status != "disabled" || row.CandidateFrames != 0 {
							t.Fatalf("production evaluated a paused detector: %+v", row)
						}
					}
				}
				if !found {
					t.Fatal("production did not report paused detector coverage")
				}
			}
		})
	}
}
