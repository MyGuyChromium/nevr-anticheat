package model

import "testing"

func TestLevelTableBoundaries(t *testing.T) {
	tbl := DefaultLevelTable()
	cases := map[float64]ScoringLevel{
		0: LevelClean, 19.99: LevelClean,
		20: LevelInformational, 39.99: LevelInformational,
		40: LevelSuspicious, 59.99: LevelSuspicious,
		60: LevelHighRisk, 79.99: LevelHighRisk,
		80: LevelCritical, 94.99: LevelCritical,
		95: LevelActionWorthy, 100: LevelActionWorthy,
	}
	for score, want := range cases {
		if got := tbl.LevelFor(score); got != want {
			t.Errorf("LevelFor(%.2f)=%s want %s", score, got, want)
		}
		sc := SuspicionScore{TotalScore: score} // zero table -> default
		if got := sc.Level(); got != want {
			t.Errorf("zero-table Level() at %.2f=%s want %s", score, got, want)
		}
	}
	if err := tbl.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := tbl
	bad.Critical = 10
	if bad.Validate() == nil {
		t.Fatal("non-monotonic table should fail validation")
	}
}

func TestLevelTableWithReviewThreshold(t *testing.T) {
	got := DefaultLevelTable().WithReviewThreshold(15)
	if got.HighRisk != 15 || got.Suspicious != 15 || got.Informational != 15 || got.Critical != 80 || got.ActionWorthy != 95 {
		t.Fatalf("low threshold: %+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("clamped table must stay valid: %v", err)
	}
	got = DefaultLevelTable().WithReviewThreshold(90)
	if got.HighRisk != 90 || got.Critical != 90 || got.ActionWorthy != 95 || got.Suspicious != 40 {
		t.Fatalf("high threshold: %+v", got)
	}
	if got := DefaultLevelTable().WithReviewThreshold(0); got != DefaultLevelTable() {
		t.Fatalf("zero threshold should be a no-op: %+v", got)
	}
	if !LevelHighRisk.AtLeast(LevelSuspicious) || LevelClean.AtLeast(LevelInformational) {
		t.Fatal("rank ordering wrong")
	}
}

func TestSuspicionScoreClone(t *testing.T) {
	var nilScore *SuspicionScore
	if c := nilScore.Clone(); c.PlayerID != "" {
		t.Fatal("nil clone should be zero")
	}
	orig := SuspicionScore{PlayerID: "p", TotalScore: 5}
	orig.Init()
	orig.ScoreByDetector["a"] = 1
	orig.MatchIDs["m"] = true
	c := orig.Clone()
	c.ScoreByDetector["a"] = 2
	c.MatchIDs["x"] = true
	c.DetectorCounts["a"] = 1
	if orig.ScoreByDetector["a"] != 1 || len(orig.MatchIDs) != 1 || len(orig.DetectorCounts) != 0 {
		t.Fatalf("clone aliases original: %+v", orig)
	}
	empty := SuspicionScore{PlayerID: "p"}
	if c := empty.Clone(); c.ScoreByDetector != nil {
		t.Fatal("clone of nil maps should stay nil")
	}
}

func TestModeratorDecisionValidate(t *testing.T) {
	d := ModeratorDecision{CaseID: "c", ModeratorID: "m", Verdict: VerdictFalsePositive,
		DetectorFeedback: []DetectorVerdict{{DetectorID: "THROW_001", Correct: DetectorVerdictNo}}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.Verdict = "guilty"
	if d.Validate() == nil {
		t.Fatal("unknown verdict accepted")
	}
	d.Verdict = VerdictConfirmedCheat
	d.DetectorFeedback[0].Correct = "maybe"
	if d.Validate() == nil {
		t.Fatal("unknown detector verdict accepted")
	}
}
