package tests

import (
	"context"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/catalog"
	"github.com/nevr-anticheat/nevr-anticheat/internal/ingest"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func integrationCatchLog(t *testing.T, coverage map[string]*model.PlayerCoverage) *model.CatchReviewLog {
	t.Helper()
	if coverage["receiver"] != nil {
		for _, detector := range coverage["receiver"].Detectors {
			if detector.DetectorID == "STATE_008" && detector.CatchReview != nil {
				return detector.CatchReview
			}
		}
	}
	t.Fatal("missing receiver catch diagnostics")
	return nil
}

type withoutCatchDiagnosticObserver struct{ detect.Detector }

func TestCatchDiagnosticsObserverCannotChangeDetectionOrScores(t *testing.T) {
	h := testutil.NewHarness(t).WithDetectors("STATE_008").WithShadowMode()
	cfg := h.Config()
	mc := &model.MatchContext{MatchID: "catch-observer-equivalence", PlayerIDs: []string{"receiver", "other"}, TickRate: 15, Physics: cfg.Physics.Constants()}
	run := func(observe bool) *pipeline.MatchResult {
		_, scorer := h.NewPipeline()
		detectors := catalog.Build(cfg, nil)
		if !observe {
			for i := range detectors {
				detectors[i] = withoutCatchDiagnosticObserver{detectors[i]}
			}
		}
		p := pipeline.NewPipeline(cfg, detectors, scorer, quietLogger())
		result, err := p.ProcessMatch(context.Background(), mc, autopocketIntegrationFrames())
		if err != nil {
			t.Fatal(err)
		}
		for i := range result.DetectionEvents {
			result.DetectionEvents[i].EventID = ""
		}
		return result
	}
	observed, plain := run(true), run(false)
	if len(observed.DetectionEvents) != 1 || !reflect.DeepEqual(observed.DetectionEvents, plain.DetectionEvents) || !reflect.DeepEqual(observed.PlayerScores, plain.PlayerScores) {
		t.Fatal("diagnostic observer changed actual detection or score output")
	}
	if integrationCatchLog(t, observed.PlayerCoverage).Total != 1 {
		t.Fatal("observer fixture produced no diagnostic")
	}
	for _, detector := range plain.PlayerCoverage["receiver"].Detectors {
		if detector.CatchReview != nil {
			t.Fatal("pipeline synthesized catch diagnostics without observer capability")
		}
	}
}

func TestCatchDiagnosticsProductionPipelineStorageAndLiveEOF(t *testing.T) {
	for _, test := range []struct {
		confirmed   bool
		missingHead bool
	}{{true, false}, {false, false}, {true, true}, {false, true}} {
		confirmed := test.confirmed
		h := testutil.NewHarness(t).WithDetectors("STATE_008").WithShadowMode()
		cfg := h.Config()
		mc := &model.MatchContext{MatchID: "catch-diagnostics", PlayerIDs: []string{"receiver", "other"}, TickRate: 15, Physics: cfg.Physics.Constants()}
		frames := autopocketIntegrationFrames()
		if test.missingHead {
			for i := range frames {
				if frames[i].FrameIndex >= 11 {
					frames[i].HeadPosition = nil
				}
			}
		}
		if !confirmed {
			frames = frames[:len(frames)-2]
		} // explicit first-held sample, but no confirmation
		p, _ := h.NewPipeline()
		result, err := p.ProcessMatch(context.Background(), mc, frames)
		if err != nil {
			t.Fatal(err)
		}
		want := integrationCatchLog(t, result.PlayerCoverage)
		if want.Total != 1 || len(want.Records) != 1 || want.Records[0].Confirmed != confirmed || want.Records[0].FrameIndex != 11 {
			t.Fatalf("offline confirmed=%v diagnostic=%+v", confirmed, want)
		}
		if !confirmed && (want.Unconfirmed != 1 || len(result.DetectionEvents) != 0) {
			t.Fatalf("EOF became an event or confirmation: %+v", want)
		}
		if confirmed && test.missingHead && (want.InsufficientData != 1 || len(result.DetectionEvents) != 0) {
			t.Fatalf("explicit catch with unavailable tracking should be insufficient_data only: %+v", want)
		}
		if confirmed && !test.missingHead && (want.Observation != 1 || len(result.DetectionEvents) != 1) {
			t.Fatalf("synthetic observation disagrees with finalized catch log: %+v", want)
		}
		store := newTestStore(t)
		if _, err := replay.StoreMatchAnalysis(context.Background(), store, mc, result, "initial", replay.AnalysisOptions{Logger: quietLogger()}); err != nil {
			t.Fatal(err)
		}
		stored, err := store.GetMatchAnalysisCoverage(context.Background(), mc.MatchID)
		if err != nil || !reflect.DeepEqual(integrationCatchLog(t, stored), want) {
			t.Fatalf("offline storage lost diagnostics: %v", err)
		}
		requireAutopocketNoStoredConsequences(t, store)
		for _, size := range []int{1, 3, len(frames)} {
			liveStore := newTestStore(t)
			mm := ingest.NewMatchManager(cfg, liveStore, func() []detect.Detector { return catalog.Build(cfg, nil) }, quietLogger())
			t.Cleanup(mm.Close)
			for start := 0; start < len(frames); start += size {
				batch := append([]model.PlayerTelemetryFrame(nil), frames[start:min(start+size, len(frames))]...)
				got := mm.HandleFrames(mc.MatchID, "", batch)
				if got.Accepted != len(batch) || got.Ignored != 0 || got.Rejected != 0 {
					t.Fatalf("live chunk: %+v", got)
				}
			}
			mm.EndMatch(mc.MatchID)
			coverage, err := liveStore.GetMatchAnalysisCoverage(context.Background(), mc.MatchID)
			if err != nil || !reflect.DeepEqual(integrationCatchLog(t, coverage), want) {
				t.Fatalf("confirmed=%v chunk=%d lost/duplicated diagnostics: err=%v got=%+v want=%+v", confirmed, size, err, integrationCatchLog(t, coverage), want)
			}
			requireAutopocketNoStoredConsequences(t, liveStore)
		}
	}
}

func TestCatchDiagnosticsSourceSwitchPersistsInterruptedPendingCatch(t *testing.T) {
	h := testutil.NewHarness(t).WithDetectors("STATE_008").WithShadowMode()
	frames := autopocketIntegrationFrames()
	for i := range frames {
		if frames[i].FrameIndex == 12 {
			frames[i].Observation.SourceID = "other-capture"
		}
	}
	mc := &model.MatchContext{MatchID: "catch-source-switch", PlayerIDs: []string{"receiver", "other"}, TickRate: 15, Physics: h.Config().Physics.Constants()}
	p, _ := h.NewPipeline()
	result, err := p.ProcessMatch(context.Background(), mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	log := integrationCatchLog(t, result.PlayerCoverage)
	if len(result.DetectionEvents) != 0 || log.Total != 1 || log.InsufficientData != 1 || log.Records[0].Reason != "catch_source_changed" || log.Records[0].Confirmed {
		t.Fatalf("pending catch survived/disappeared at source boundary: events=%d log=%+v", len(result.DetectionEvents), log)
	}
	store := newTestStore(t)
	if _, err := replay.StoreMatchAnalysis(context.Background(), store, mc, result, "initial", replay.AnalysisOptions{Logger: quietLogger()}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetMatchAnalysisCoverage(context.Background(), mc.MatchID)
	if err != nil || !reflect.DeepEqual(integrationCatchLog(t, stored), log) {
		t.Fatalf("source boundary diagnostic lost: %v", err)
	}
	requireAutopocketNoStoredConsequences(t, store)
}
