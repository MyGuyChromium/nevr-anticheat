package sqlite

import (
	"errors"
	"testing"
)

// A revealed blind-review consensus is the only ground truth the promotion
// gate treats as independently verified. Deleting it would let a fresh session
// over the same window be voted again until the desired consensus appears.
func TestHashBoundOpportunityCannotBeDeletedAndRevoted(t *testing.T) {
	s, x, candidate := blindFixture(t)
	ctx := t.Context()
	ballotPair(t, s, x, candidate, GroundTruthPositive, GroundTruthPositive)
	if _, err := s.RevealBlindReview(ctx, x.SessionID, candidate); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListCalibrationOpportunities(ctx, "M", "")
	if err != nil || len(items) != 1 || items[0].ReviewSessionID == "" {
		t.Fatalf("bound opportunity fixture: %+v %v", items, err)
	}
	bound := items[0]

	deleted, err := s.DeleteCalibrationOpportunity(ctx, " "+bound.OpportunityID+" ")
	if deleted || !errors.Is(err, ErrBoundOpportunityImmutable) {
		t.Fatalf("bound annotation delete = %t, %v; want refusal", deleted, err)
	}
	if n := countRows(t, s, "calibration_opportunities", ""); n != 1 {
		t.Fatalf("bound annotation rows = %d, want 1", n)
	}
	if ok, _, err := s.VerifiedBlindOpportunity(ctx, bound, candidate); err != nil || !ok {
		t.Fatalf("refused delete damaged the proof: %t %v", ok, err)
	}

	// The window stays occupied, so a second session cannot be revealed over it.
	again, err := s.CreateBlindReviewSession(ctx, x.Binding, candidate)
	if err == nil {
		ballotPair(t, s, again, candidate, GroundTruthNegative, GroundTruthNegative)
		if _, err := s.RevealBlindReview(ctx, again.SessionID, candidate); err == nil {
			t.Fatal("same window was voted a second time")
		}
	}
	if n := countRows(t, s, "calibration_opportunities", "ground_truth = ?", GroundTruthPositive); n != 1 {
		t.Fatalf("original consensus was replaced: %d", n)
	}

	// Ordinary, unbound annotations stay removable, and a miss stays a miss.
	manual, err := s.StoreCalibrationOpportunity(ctx, CalibrationOpportunity{MatchID: "M", PlayerID: "P", DetectorID: "MOV_001",
		Kind: OpportunityMovementWindow, FrameStart: 10, FrameEnd: 11, GroundTruth: GroundTruthNegative})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DeleteCalibrationOpportunity(ctx, manual.OpportunityID); err != nil || !ok {
		t.Fatalf("unbound delete = %t, %v", ok, err)
	}
	if ok, err := s.DeleteCalibrationOpportunity(ctx, "no-such-opportunity"); err != nil || ok {
		t.Fatalf("missing delete = %t, %v", ok, err)
	}
}

// Re-importing a library that contains a hash-bound annotation is refused (the
// portable text cannot prove the ballots), but a refused import adds no
// information and therefore must not quarantine the held-out match.
func TestRefusedImportOfBoundOpportunityDoesNotQuarantineItsMatch(t *testing.T) {
	s, x, candidate := blindFixture(t)
	ctx := t.Context()
	held := []StoredMatch{splitMatch("M", "P")}
	if _, err := s.ReconcileCalibrationSplits(ctx, held, map[string]string{"M": "holdout"}); err != nil {
		t.Fatal(err)
	}
	ballotPair(t, s, x, candidate, GroundTruthNegative, GroundTruthNegative)
	if _, err := s.RevealBlindReview(ctx, x.SessionID, candidate); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListCalibrationOpportunities(ctx, "M", "")
	if err != nil || len(items) != 1 {
		t.Fatalf("fixture: %+v %v", items, err)
	}
	if err := s.ImportCalibrationOpportunity(ctx, items[0]); !errors.Is(err, ErrBoundOpportunityImmutable) {
		t.Fatalf("bound import error = %v", err)
	}
	got, err := s.ReconcileCalibrationSplits(ctx, held, nil)
	if err != nil || got["M"].Split != "holdout" || got["M"].Quarantined {
		t.Fatalf("refused import spent the holdout: %+v %v", got["M"], err)
	}
}
