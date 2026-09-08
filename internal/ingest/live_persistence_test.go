package ingest

import (
	"context"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

func persistenceEvent(id, detector string, frame int) model.DetectionEvent {
	return model.DetectionEvent{
		EventID: id, DetectorID: detector, DetectorVersion: "test", MatchID: "M1", PlayerID: "P1",
		FrameIndex: frame, FrameRangeStart: frame, FrameRangeEnd: frame, Timestamp: float64(frame) / 15,
		Severity: 1, Confidence: 1, EnforcementWeight: 1, ObservedValue: "synthetic regression",
		CausalKey: model.CausalKey{PlayerID: "P1", AnomalyType: detector, FrameStart: frame, FrameEnd: frame},
	}
}

func TestLivePersistenceFailureWithholdsScoresCasesAndCallbacks(t *testing.T) {
	mm, store, _ := newTestManager(t)
	ctx := context.Background()
	mm.HandleFrames("M1", "", []model.PlayerTelemetryFrame{goodFrame("P1", 0)})
	match := mm.matches["M1"]
	first := persistenceEvent("first", "MOV_001", 0)
	match.Scorer.IngestEvent(first)
	if !mm.persistDerived(ctx, match, &pipeline.MatchResult{DetectionEvents: []model.DetectionEvent{first}, PlayerScores: match.Scorer.GetAllScores()}, false) {
		t.Fatal("healthy checkpoint failed")
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_event BEFORE INSERT ON detection_events WHEN NEW.event_id = 'fail' BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	second, failed := persistenceEvent("second", "BIO_001", 20), persistenceEvent("fail", "STATE_002", 20)
	match.Scorer.IngestEvent(second)
	match.Scorer.IngestEvent(failed)
	if mm.persistDerived(ctx, match, &pipeline.MatchResult{DetectionEvents: []model.DetectionEvent{second, failed}, PlayerScores: match.Scorer.GetAllScores()}, false) {
		t.Fatal("partial batch reported success")
	}
	status, ok := mm.GetMatchAnalysisStatus("M1")
	if !ok || !status.AnalysisIncomplete || status.PersistenceError != "derived_analysis_persistence_failed" || mm.GetMatchScores("M1") != nil {
		t.Fatalf("unsafe live status/scores: %+v", status)
	}
	if len(match.segmentEvents) != 1 || match.lastScore["P1"] != 15 {
		t.Fatal("uncommitted evidence advanced published score/evidence checkpoint")
	}
	callbacks := 0
	mm.OnReviewCase = func(model.ReviewCase) { callbacks++ }
	for frame := 1; frame <= 3; frame++ {
		if res := mm.HandleFrames("M1", "", []model.PlayerTelemetryFrame{goodFrame("P1", frame)}); res.Accepted != 1 {
			t.Fatalf("raw telemetry stopped after derived failure: %+v", res)
		}
	}
	mm.EndMatch("M1")
	mm.EndMatch("M1")
	if callbacks != 0 || countRows(t, store, `SELECT COUNT(*) FROM review_cases`) != 0 {
		t.Fatal("incomplete evidence produced a case/callback")
	}
	if countRows(t, store, `SELECT COUNT(*) FROM telemetry_frames`) != 4 || countRows(t, store, `SELECT COUNT(*) FROM detection_events`) != 1 || countRows(t, store, `SELECT COUNT(*) FROM suspicion_scores`) != 1 {
		t.Fatal("raw telemetry lost or failed derived batch partially persisted")
	}
	// A live-manager restart cannot silently re-enable incomplete analysis.
	mm.HandleFrames("M1", "", []model.PlayerTelemetryFrame{goodFrame("P1", 4)})
	status, ok = mm.GetMatchAnalysisStatus("M1")
	if !ok || !status.AnalysisIncomplete {
		t.Fatalf("restart lost persisted failure marker: %+v", status)
	}
	mm.Close()
}
