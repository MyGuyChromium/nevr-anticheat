package sqlite

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func liveWriteFixture() MatchAnalysisWrite {
	return MatchAnalysisWrite{
		MatchID: "live", Source: "initial", LiveAppend: true,
		Events: []model.DetectionEvent{mkEvent("MOV_001", "p", "live", 10, .9, .9), mkEvent("BIO_001", "p", "live", 11, .9, .9)},
		Scores: []model.SuspicionScore{{PlayerID: "p", TotalScore: 30, EventCount: 2, MatchCount: 1,
			MatchIDs: map[string]bool{"live": true}, SnapshotTime: time.Now()}},
	}
}

func TestLiveAnalysisAtomicEvidenceAndScore(t *testing.T) {
	for _, target := range []string{"second_event", "score"} {
		t.Run(target, func(t *testing.T) {
			s := newTestStore(t)
			trigger := `CREATE TRIGGER reject_write BEFORE INSERT ON detection_events WHEN NEW.detector_id = 'BIO_001' BEGIN SELECT RAISE(ABORT, 'synthetic write failure'); END`
			if target == "score" {
				trigger = `CREATE TRIGGER reject_write BEFORE INSERT ON suspicion_scores BEGIN SELECT RAISE(ABORT, 'synthetic write failure'); END`
			}
			if _, err := s.DB().Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if out, err := s.WriteMatchAnalysis(context.Background(), liveWriteFixture()); err == nil || out.EventsStored != 0 || out.ScoresStored != 0 {
				t.Fatalf("failed batch must report no committed rows: %+v %v", out, err)
			}
			if countRows(t, s, "detection_events", "") != 0 || countRows(t, s, "suspicion_scores", "") != 0 {
				t.Fatal("partial evidence or score survived rollback")
			}
		})
	}
}

func TestLiveAnalysisPreservesCoverageCasesAndCannotDuplicateRetry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.MarkLiveAnalysisIncomplete(ctx, "live", []string{"p"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "prior-case", PlayerID: "p", MatchID: "live", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	in := liveWriteFixture()
	if _, err := s.WriteMatchAnalysis(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteMatchAnalysis(ctx, in); err == nil {
		t.Fatal("a repeated batch must not append a second score snapshot")
	}
	if countRows(t, s, "detection_events", "") != 2 || countRows(t, s, "suspicion_scores", "") != 1 {
		t.Fatal("retry inflated committed rows")
	}
	if incomplete, err := s.LiveAnalysisIncomplete(ctx, "live"); err != nil || !incomplete {
		t.Fatalf("live append erased coverage marker: %v %v", incomplete, err)
	}
	rc, err := s.GetReviewCase(ctx, "prior-case")
	if err != nil || rc.Status != "pending" {
		t.Fatalf("live append changed unrelated case: %+v %v", rc, err)
	}
}

func TestLiveAnalysisRejectsUnboundOrUnserializableEvidence(t *testing.T) {
	for _, mutate := range []func(*MatchAnalysisWrite){
		func(in *MatchAnalysisWrite) { in.Events[0].MatchID = "other" },
		func(in *MatchAnalysisWrite) { in.Events[0].CausalKey.PlayerID = "other" },
		func(in *MatchAnalysisWrite) { in.Scores[0].MatchIDs = map[string]bool{"other": true} },
		func(in *MatchAnalysisWrite) {
			in.Events[0].Evidence = model.PatternEvidence{Metrics: map[string]float64{"bad": math.NaN()}}
		},
	} {
		s := newTestStore(t)
		in := liveWriteFixture()
		mutate(&in)
		if _, err := s.WriteMatchAnalysis(context.Background(), in); err == nil {
			t.Fatal("invalid live batch accepted")
		}
		if countRows(t, s, "detection_events", "") != 0 || countRows(t, s, "suspicion_scores", "") != 0 {
			t.Fatal("invalid batch partially stored")
		}
	}
}
