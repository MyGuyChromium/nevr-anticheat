package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestAnalysisCoverageAtomicReplacementAndLegacyClear(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	coverage := map[string]*model.PlayerCoverage{"P": {Version: 1, Status: "limited", ValidFrames: 30}}
	if _, err := s.WriteMatchAnalysis(ctx, MatchAnalysisWrite{MatchID: "M", Coverage: coverage}); err != nil {
		t.Fatal(err)
	}
	assertCoverage := func(want map[string]*model.PlayerCoverage) {
		t.Helper()
		got, err := s.GetMatchAnalysisCoverage(ctx, "M")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("coverage=%+v want=%+v err=%v", got, want, err)
		}
	}
	assertCoverage(coverage)
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_coverage_test BEFORE INSERT ON detection_events BEGIN SELECT RAISE(ABORT, 'test rollback'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.WriteMatchAnalysis(ctx, MatchAnalysisWrite{MatchID: "M", Replace: true, Events: []model.DetectionEvent{{EventID: "fails", MatchID: "M"}}})
	if err == nil {
		t.Fatal("expected failed transaction")
	}
	assertCoverage(coverage)
	if _, err := s.WriteMatchAnalysis(ctx, MatchAnalysisWrite{MatchID: "M", Replace: true}); err != nil {
		t.Fatal(err)
	}
	assertCoverage(nil)
}

func TestAnalysisCoverageCorruptionIsNotSilentlyReportedAsClean(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO match_analysis_coverage VALUES ('M','broken')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMatchAnalysisCoverage(context.Background(), "M"); err == nil {
		t.Fatal("corrupt coverage was ignored")
	}
}
