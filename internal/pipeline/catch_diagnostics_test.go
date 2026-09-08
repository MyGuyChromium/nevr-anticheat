package pipeline

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type diagnosticCatchDetector struct {
	detect.BaseDetector
	observer    detect.CatchObserver
	pending     *model.CatchReviewRecord
	lastMetrics map[string]float64
	onlyEOF     bool
}

func newDiagnosticCatchDetector() *diagnosticCatchDetector {
	return &diagnosticCatchDetector{BaseDetector: detect.BaseDetector{DetectorID: "STATE_008", DetectorVersion: "test", DetectorCategory: "state"}}
}
func (d *diagnosticCatchDetector) Configure(map[string]any) error { return nil }
func (d *diagnosticCatchDetector) Reset()                         { d.pending = nil }
func (d *diagnosticCatchDetector) SetCatchObserver(observer detect.CatchObserver) {
	d.observer = observer
}
func (d *diagnosticCatchDetector) Evaluate(_ *model.MatchContext, players map[string]*model.PlayerState, frame int) []model.DetectionEvent {
	for _, player := range detect.ActivePlayers(players, frame) {
		d.lastMetrics = map[string]float64{"duration_s": .2}
		record := model.CatchReviewRecord{FrameIndex: frame, Timestamp: player.LastTimestamp, Outcome: model.CatchReviewExcluded,
			Reason: "catch_baseline_pending", ReasonDescription: "untrusted producer text", Metrics: d.lastMetrics}
		if d.onlyEOF {
			d.pending = &record
		} else if d.observer != nil {
			d.observer(d.ID(), player.PlayerID, record)
		}
	}
	return nil
}
func (d *diagnosticCatchDetector) FlushTracks(_ *model.MatchContext, _ int) []model.DetectionEvent {
	if d.pending != nil {
		d.pending.Outcome, d.pending.Reason = model.CatchReviewUnconfirmed, "catch_confirmation_unavailable"
		if d.observer != nil {
			d.observer(d.ID(), "P1", *d.pending)
		}
		d.pending = nil
	}
	return nil
}
func catchLog(t *testing.T, coverage *model.PlayerCoverage) *model.CatchReviewLog {
	t.Helper()
	if coverage != nil {
		for _, detector := range coverage.Detectors {
			if detector.DetectorID == "STATE_008" && detector.CatchReview != nil {
				return detector.CatchReview
			}
		}
	}
	t.Fatal("missing catch diagnostics")
	return nil
}

func TestCatchDiagnosticsCopiesBoundsSeparatesPlayersAndDetaches(t *testing.T) {
	d := newDiagnosticCatchDetector()
	p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{d})
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < model.MaxCatchReviewRecords+3; i++ {
		frames = append(frames, qualityFrame(i))
	}
	result, err := p.ProcessMatch(context.Background(), matchCtx("P1", "absent"), frames)
	if err != nil {
		t.Fatal(err)
	}
	log := catchLog(t, result.PlayerCoverage["P1"])
	if log.Total != len(frames) || len(log.Records) != model.MaxCatchReviewRecords || log.Dropped != 3 || len(result.DetectionEvents) != 0 || len(result.PlayerScores) != 0 {
		t.Fatalf("diagnostics changed results or exceeded cap: %+v", log)
	}
	if catchLog(t, result.PlayerCoverage["absent"]).Total != 0 {
		t.Fatal("player logs share state")
	}
	if log.Records[0].ReasonDescription != detect.DecisionReasonDescription("catch_baseline_pending") {
		t.Fatal("producer controlled reason description")
	}
	before, _ := json.Marshal(result.PlayerCoverage)
	d.lastMetrics["duration_s"] = 999
	if d.observer != nil {
		t.Fatal("observer retained completed result")
	}
	_, err = p.ProcessMatch(context.Background(), matchCtx("P1"), []model.PlayerTelemetryFrame{qualityFrame(0)})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(result.PlayerCoverage)
	if string(before) != string(after) {
		t.Fatal("later detector state mutated retained diagnostics")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.ProcessMatch(ctx, matchCtx("P1"), frames); err == nil {
		t.Fatal("expected cancellation")
	}
	if d.observer != nil || p.decisionCoverage != nil {
		t.Fatal("cancellation leaked observer")
	}
}

func TestCatchDiagnosticsLiveChunksAndEOFExactlyOnce(t *testing.T) {
	d := newDiagnosticCatchDetector()
	d.onlyEOF = true
	p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{d})
	p.SetSkipReset(true)
	ctx, mc := context.Background(), matchCtx("P1")
	first, err := p.ProcessMatch(ctx, mc, []model.PlayerTelemetryFrame{qualityFrame(0)})
	if err != nil {
		t.Fatal(err)
	}
	if catchLog(t, first.PlayerCoverage["P1"]).Total != 0 || d.observer != nil {
		t.Fatal("unfinished transition emitted or hook leaked")
	}
	second, err := p.ProcessMatch(ctx, mc, []model.PlayerTelemetryFrame{qualityFrame(1)})
	if err != nil {
		t.Fatal(err)
	}
	if catchLog(t, second.PlayerCoverage["P1"]).Total != 0 {
		t.Fatal("chunk boundary was mistaken for EOF")
	}
	final := p.Finalize(mc)
	log := catchLog(t, final.PlayerCoverage["P1"])
	if log.Total != 1 || log.Unconfirmed != 1 || log.Records[0].FrameIndex != 1 || len(final.DetectionEvents) != 0 || len(final.PlayerScores) != 0 {
		t.Fatalf("EOF log: %+v", log)
	}
	if d.observer != nil {
		t.Fatal("EOF hook leaked")
	}
	before := log.Clone()
	again := p.Finalize(mc)
	if catchLog(t, again.PlayerCoverage["P1"]).Total != 0 || !reflect.DeepEqual(log, before) {
		t.Fatal("EOF repeated or mutated prior result")
	}
}
