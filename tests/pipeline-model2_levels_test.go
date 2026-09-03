package tests

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Fix pass 2, workstream pipeline-model: LevelTable.LevelFor on a partially
// populated or non-monotonic table (L1).

// TestPipelineModel2_LevelForPartialTableFallsBack (L1): a table that fails
// Validate (partially populated or non-monotonic) classifies like the
// default table instead of calling every score action_worthy.
func TestPipelineModel2_LevelForPartialTableFallsBack(t *testing.T) {
	def := model.DefaultLevelTable()
	bad := []model.LevelTable{
		{HighRisk: 60},
		{Informational: 20, Suspicious: 40, HighRisk: 60, Critical: 80},
		{Informational: 20, Suspicious: 10, HighRisk: 60, Critical: 80, ActionWorthy: 95},
		{Informational: -1, Suspicious: 40, HighRisk: 60, Critical: 80, ActionWorthy: 95},
	}
	for _, tbl := range bad {
		if tbl.Validate() == nil {
			t.Fatalf("test table %+v unexpectedly valid", tbl)
		}
		for _, score := range []float64{0, 5, 19.9, 20, 45, 60, 80, 95, 100} {
			if got, want := tbl.LevelFor(score), def.LevelFor(score); got != want {
				t.Errorf("table %+v score %.1f: %s, want %s", tbl, score, got, want)
			}
		}
	}
	// A valid custom table is still honoured.
	custom := model.LevelTable{Informational: 10, Suspicious: 20, HighRisk: 30, Critical: 40, ActionWorthy: 50}
	if got := custom.LevelFor(50); got != model.LevelActionWorthy {
		t.Errorf("custom table: %s, want action_worthy", got)
	}
	if got := custom.LevelFor(9); got != model.LevelClean {
		t.Errorf("custom table: %s, want clean", got)
	}
	// And a score decoded with a half-filled levels block is not
	// action_worthy at 0.
	s := model.SuspicionScore{TotalScore: 0, Levels: model.LevelTable{HighRisk: 60}}
	if got := s.Level(); got != model.LevelClean {
		t.Errorf("score 0 with partial levels: %s, want clean", got)
	}
}
