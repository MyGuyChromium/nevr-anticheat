package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/throw"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func decisionCounts(t *testing.T, coverage *model.PlayerCoverage, detectorID string) map[string]int {
	t.Helper()
	if coverage == nil {
		t.Fatal("missing player coverage")
	}
	for _, d := range coverage.Detectors {
		if d.DetectorID == detectorID {
			if d.DecisionTrace == nil || d.DecisionTrace.Version != 1 {
				t.Fatalf("missing/versionless trace: %+v", d)
			}
			out := map[string]int{}
			for _, reason := range d.DecisionTrace.Reasons {
				out[reason.Code] = reason.Count
			}
			return out
		}
	}
	t.Fatalf("missing detector %s", detectorID)
	return nil
}

func TestDecisionTraceRecordsActualDispatchAndResets(t *testing.T) {
	detector := bio.NewBio001(nil)
	p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{detector})
	frames := make([]model.PlayerTelemetryFrame, 20)
	for i := range frames {
		frames[i] = qualityFrame(i)
		if i < 2 {
			frames[i].GamePhase = "round_start"
		}
	}
	frames[2].Position = model.Vec3{} // actual validator rejection
	result, err := p.ProcessMatch(context.Background(), matchCtx("P1", "absent"), frames)
	if err != nil {
		t.Fatal(err)
	}
	counts := decisionCounts(t, result.PlayerCoverage["P1"], "BIO_001")
	if counts["frame_rejected"] != 1 || counts["inactive_phase"] != 2 || counts["warming_up"] == 0 || counts["detector_evaluated"] == 0 {
		t.Fatalf("actual dispatch reasons missing: %v", counts)
	}
	if counts["no_raw_emission"] != counts["detector_evaluated"] || counts["wrist_at_or_below_threshold"] != 2*counts["detector_evaluated"] {
		t.Fatalf("per-hand/dispatch accounting wrong: %v", counts)
	}
	if absent := decisionCounts(t, result.PlayerCoverage["absent"], "BIO_001"); len(absent) != 0 {
		t.Fatalf("absent player inherited observed branches: %v", absent)
	}
	before, _ := json.Marshal(result.PlayerCoverage)
	// The observer must not retain an old result after ProcessMatch returns.
	detector.TraceDecision("P1", 500, "no_current_release")
	again, err := p.ProcessMatch(context.Background(), matchCtx("P1"), []model.PlayerTelemetryFrame{qualityFrame(0)})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(result.PlayerCoverage)
	if string(before) != string(after) {
		t.Fatal("later processing mutated previous result")
	}
	if c := decisionCounts(t, again.PlayerCoverage["P1"], "BIO_001"); c["frame_rejected"] != 0 || c["detector_evaluated"] != 0 {
		t.Fatalf("trace leaked between matches: %v", c)
	}
	if p.decisionCoverage != nil || p.dedup.decisionObserver != nil || p.rateLimiter.decisionObserver != nil {
		t.Fatal("pipeline observer not detached")
	}
}

func TestDecisionTraceBoundsKeysAndKeepsPlayersIndependent(t *testing.T) {
	c := newCoverageTracker([]detect.Detector{bio.NewBio001(nil)}, config.DefaultConfig(), []string{"A", "B"})
	for i := 0; i < MaxDecisionReasons+20; i++ {
		c.trace("BIO_001", "A", i, fmt.Sprintf("reason_%02d", i))
	}
	c.trace("BIO_001", "A", 100, "reason_00")
	c.trace("BIO_001", "A", -1, "reason_00")
	c.trace("BIO_001", "A", 101, "raw private message or path")
	c.trace("unknown_detector", "A", 102, "reason_00")
	c.trace("BIO_001", "", 102, "reason_00")
	finished := c.finish(TelemetryQualityReport{})
	for _, d := range finished["A"].Detectors {
		if d.DetectorID != "BIO_001" {
			continue
		}
		trace := d.DecisionTrace
		if !trace.InternalBranches || len(trace.Reasons) != MaxDecisionReasons || trace.OverflowCount != 21 {
			t.Fatalf("unbounded trace: %+v", trace)
		}
		if first := trace.Reasons[0]; first.Code != "reason_00" || first.Count != 3 || first.FirstFrame != -1 || first.LastFrame != 100 {
			t.Fatalf("count/range ordering: %+v", first)
		}
		for i := 2; i < len(trace.Reasons); i++ {
			if trace.Reasons[i-1].Code > trace.Reasons[i].Code {
				t.Fatal("ties not sorted by stable code")
			}
		}
	}
	if len(decisionCounts(t, finished["B"], "BIO_001")) != 0 {
		t.Fatal("reason slice shared between players")
	}
	if len(finished) != 2 {
		t.Fatal("unattributed event created phantom player")
	}
}

func TestDecisionTraceSeparatesRawMergedDroppedAndRetained(t *testing.T) {
	cfg := testConfig("shadow")
	cfg.Pipeline.MaxEventsPerPlayerPerDetector = 1
	d := bio.NewBio001(nil)
	p, _ := newPipeline(cfg, []detect.Detector{d})
	c := newCoverageTracker(p.detectors, cfg, []string{"P1"})
	detach := p.attachDecisionCoverage(c)
	defer detach()
	p.dedup.mergeWindow = 2
	p.quality = TelemetryQualityReport{ConfidenceMultiplier: 0.5, Gated: true}
	result := newMatchResult("M-TEST")
	maker := newRecorder("BIO_001", "bio", 0)
	for _, fi := range []int{10, 11, 30} {
		event := maker.event(matchCtx("P1"), "P1", fi, 0.8, 0.9, "test_trace")
		p.dedupAndEmit(p.acceptEmissions([]model.DetectionEvent{*event}, fi, result), fi, result)
	}
	invalid := maker.event(matchCtx("P1"), "P1", 40, 0.8, 0.9, "test_trace")
	invalid.Confidence = 2
	p.acceptEmissions([]model.DetectionEvent{*invalid}, 40, result)
	p.emit(p.dedup.Flush(), result)
	counts := decisionCounts(t, c.finish(p.quality)["P1"], "BIO_001")
	for reason, want := range map[string]int{
		"raw_emission_returned": 4, "invalid_emission_dropped": 1,
		"quality_confidence_reduced": 3, "quality_forced_shadow": 3,
		"emission_merged": 1, "incident_rate_limited": 1,
		"incident_retained_shadow": 1, "incident_not_scored": 1,
	} {
		if counts[reason] != want {
			t.Errorf("%s=%d want %d; %v", reason, counts[reason], want, counts)
		}
	}
	if counts["incident_scored"] != 0 || len(result.DetectionEvents) != 1 || result.EventsInvalid != 1 || result.EventsRateLimited != 1 {
		t.Fatalf("trace disagrees with results: %v %+v", counts, result)
	}
}

// Hiding the optional observer interface leaves the exact detector in use.
// This verifies instrumentation changes diagnostics only, including score and
// event order; UUIDs are the sole intentionally nondeterministic event field.
type noBranchObserver struct{ detect.Detector }

func TestDecisionTraceDoesNotChangePipelineDecisions(t *testing.T) {
	frames := speedHackFrames("P1", 150)
	before, _ := json.Marshal(frames)
	run := func(observe bool) *MatchResult {
		var detector detect.Detector = movement.NewMov001(nil)
		if !observe {
			detector = noBranchObserver{detector}
		}
		p, _ := newPipeline(testConfig("enforce"), []detect.Detector{detector})
		result, err := p.ProcessMatch(context.Background(), matchCtx("P1"), frames)
		if err != nil {
			t.Fatal(err)
		}
		for i := range result.DetectionEvents {
			result.DetectionEvents[i].EventID = ""
		}
		return result
	}
	traced, plain := run(true), run(false)
	if len(traced.DetectionEvents) == 0 {
		t.Fatal("fixture did not exercise emission path")
	}
	if !reflect.DeepEqual(traced.DetectionEvents, plain.DetectionEvents) {
		t.Fatal("observed detector changed event fields or order")
	}
	if traced.PlayerScores["P1"].TotalScore != plain.PlayerScores["P1"].TotalScore {
		t.Fatal("observer changed score")
	}
	after, _ := json.Marshal(frames)
	if string(before) != string(after) {
		t.Fatal("observer mutated caller telemetry")
	}
	if decisionCounts(t, traced.PlayerCoverage["P1"], "MOV_001")["speed_window_pending"] == 0 {
		t.Fatal("actual internal branches not traced")
	}
	if decisionCounts(t, plain.PlayerCoverage["P1"], "MOV_001")["speed_window_pending"] != 0 {
		t.Fatal("internal reasons synthesized without observer")
	}
}

func TestDecisionTraceDetachesOnCancellation(t *testing.T) {
	d := bio.NewBio001(nil)
	p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{d})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.ProcessMatch(ctx, matchCtx("P1"), []model.PlayerTelemetryFrame{qualityFrame(0)}); err == nil {
		t.Fatal("expected canceled match")
	}
	if p.decisionCoverage != nil || p.dedup.decisionObserver != nil || p.rateLimiter.decisionObserver != nil {
		t.Fatal("observer retained canceled match")
	}
}

func TestDecisionTraceDoesNotCallStaleContextASampledRelease(t *testing.T) {
	for _, detector := range []detect.Detector{throw.NewThrow001(nil), throw.NewThrow002(nil), throw.NewThrow003(nil)} {
		t.Run(detector.ID(), func(t *testing.T) {
			var frames []model.PlayerTelemetryFrame
			for i := 0; i < 20; i++ {
				if i < 10 {
					frames = append(frames, qualityFrame(i))
				}
				other := qualityFrame(i)
				other.PlayerID = "P2"
				frames = append(frames, other)
			}
			p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{detector})
			result, err := p.ProcessMatch(context.Background(), matchCtx("P1", "P2"), frames)
			if err != nil {
				t.Fatal(err)
			}
			counts := decisionCounts(t, result.PlayerCoverage["P1"], detector.ID())
			if counts["stale_player_context"] != 10 || counts["no_current_release"] != counts["detector_evaluated"] {
				t.Fatalf("stale player was counted as sampled: %v", counts)
			}
			other := decisionCounts(t, result.PlayerCoverage["P2"], detector.ID())
			if other["stale_player_context"] != 0 || other["no_current_release"] != other["detector_evaluated"] {
				t.Fatalf("fresh player received stale-context reason: %v", other)
			}
		})
	}
}

func TestDecisionTraceLegalContextNeverAmplifiesOrRewritesEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, detector, reason string
		legal                  model.LegalMotionContext
		factor                 float64
	}{
		{"slap or block push", "BIO_001", "context_possible_slap_or_push", model.LegalMotionContext{PossibleSlapOrPush: true, Confidence: 1}, .6},
		{"possible headbutt", "BIO_001", "context_possible_head_contact", model.LegalMotionContext{PossibleHeadContact: true, Confidence: 1}, .35},
		{"lean", "MOV_001", "context_lean_or_step", model.LegalMotionContext{Leaning: true, Confidence: 1}, .65},
		{"boost", "MOV_001", "context_boost", model.LegalMotionContext{BoostingKnown: true, Boosting: true, Confidence: 1}, .75},
		{"tracking uncertain", "MOV_006", "context_tracking_limited", model.LegalMotionContext{TrackingLimited: true, Confidence: 1}, .55},
		{"no blanket playspace exemption", "MOV_006", "", model.LegalMotionContext{Leaning: true, PlayspaceStep: true, Confidence: 1}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder(tt.detector, "test", 0)
			p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{rec})
			p.players = map[string]*model.PlayerState{"P1": {PlayerID: "P1", LegalContext: tt.legal}}
			c := newCoverageTracker(p.detectors, p.cfg, []string{"P1"})
			detach := p.attachDecisionCoverage(c)
			defer detach()
			event := rec.event(matchCtx("P1"), "P1", 20, .8, .9, "synthetic_contact")
			before := *event
			p.applyLegalContext(event)
			if event.Confidence != before.Confidence*tt.factor || event.Confidence > before.Confidence {
				t.Fatalf("unexpected contact weighting: %v", event.Confidence)
			}
			event.Confidence = before.Confidence
			if !reflect.DeepEqual(*event, before) {
				t.Fatal("contact handling rewrote source evidence, thresholds or enforcement fields")
			}
			counts := decisionCounts(t, c.finish(TelemetryQualityReport{})["P1"], tt.detector)
			if tt.reason == "" {
				if len(counts) != 0 {
					t.Fatalf("invented context suppression: %v", counts)
				}
			} else if counts[tt.reason] != 1 || len(counts) != 1 {
				t.Fatalf("wrong actual context branch: %v", counts)
			}
		})
	}
}
