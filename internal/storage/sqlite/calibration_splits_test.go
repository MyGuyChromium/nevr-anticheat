package sqlite

import (
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"testing"
)

func splitMatch(id string, players ...string) StoredMatch {
	return StoredMatch{Context: &model.MatchContext{MatchID: id, PlayerIDs: players}}
}

func TestCalibrationSplitsPersistAndQuarantineCrossSplitConnections(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	initial := []StoredMatch{splitMatch("train", "P1"), splitMatch("held", "P2")}
	got, err := s.ReconcileCalibrationSplits(ctx, initial, map[string]string{"train": "training", "held": "holdout"})
	if err != nil {
		t.Fatal(err)
	}
	if got["held"].Split != "holdout" || got["held"].Quarantined {
		t.Fatalf("initial: %+v", got)
	}
	// Attempting to move assignments cannot change historical exposure.
	got, err = s.ReconcileCalibrationSplits(ctx, initial, map[string]string{"train": "holdout", "held": "training"})
	if err != nil || got["held"].Split != "holdout" || got["train"].Split != "training" {
		t.Fatalf("moved: %+v %v", got, err)
	}
	// A replay deleted from the current library still reserves its player's split.
	got, err = s.ReconcileCalibrationSplits(ctx, []StoredMatch{splitMatch("replacement", "P2")}, map[string]string{"replacement": "training"})
	if err != nil || got["replacement"].Split != "holdout" {
		t.Fatalf("deleted history lost: %+v %v", got, err)
	}
	connected := append(initial, splitMatch("bridge", "P1", "P2"))
	got, err = s.ReconcileCalibrationSplits(ctx, connected, map[string]string{"bridge": "validation"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"train", "held", "bridge"} {
		if !got[id].Quarantined {
			t.Fatalf("not quarantined: %+v", got)
		}
	}
	// Removing the bridge or shortening a later roster cannot cleanse exposure.
	got, err = s.ReconcileCalibrationSplits(ctx, initial, nil)
	if err != nil || !got["held"].Quarantined || !got["train"].Quarantined {
		t.Fatalf("quarantine cleared: %+v %v", got, err)
	}
}

func TestCalibrationSplitsHandleRosterChangesAndImportedEvidence(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	matches := []StoredMatch{splitMatch("A", "P1"), splitMatch("B", "P2")}
	if _, err := s.ReconcileCalibrationSplits(ctx, matches, map[string]string{"A": "training", "B": "validation"}); err != nil {
		t.Fatal(err)
	}
	matches[1] = splitMatch("B", "P1", "P2")
	got, err := s.ReconcileCalibrationSplits(ctx, matches, nil)
	if err != nil || !got["A"].Quarantined || !got["B"].Quarantined {
		t.Fatalf("roster change: %+v %v", got, err)
	}
	if err := s.RecordCalibrationExposure(ctx, "IMPORTED"); err != nil {
		t.Fatal(err)
	}
	got, err = s.ReconcileCalibrationSplits(ctx, []StoredMatch{splitMatch("IMPORTED", "P3")}, map[string]string{"IMPORTED": "holdout"})
	if err != nil || got["IMPORTED"].Split != "training" {
		t.Fatalf("import became unseen: %+v %v", got, err)
	}
	got, err = s.ReconcileCalibrationSplits(ctx, []StoredMatch{splitMatch("EMPTY")}, map[string]string{"EMPTY": "holdout"})
	if err != nil || !got["EMPTY"].Quarantined {
		t.Fatalf("missing identity: %+v %v", got, err)
	}
}

func TestImportedEvidenceCannotLaunderHoldoutExposure(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	for _, kind := range []string{"opportunity", "event", "label"} {
		m := splitMatch(kind, kind+"-player")
		if _, err := s.ReconcileCalibrationSplits(ctx, []StoredMatch{m}, map[string]string{kind: "holdout"}); err != nil {
			t.Fatal(err)
		}
		var err error
		switch kind {
		case "opportunity":
			err = s.ImportCalibrationOpportunity(ctx, CalibrationOpportunity{MatchID: kind, PlayerID: kind + "-player", DetectorID: "THROW_001", Kind: OpportunityThrow, GroundTruth: GroundTruthPositive})
		case "event":
			err = s.ImportEventReview(ctx, EventReview{EventID: "event", MatchID: kind, PlayerID: kind + "-player", DetectorID: "THROW_001", Verdict: "yes"})
		case "label":
			err = s.ImportMatchLabel(ctx, MatchLabel{MatchID: kind, Label: "known_clean"})
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.ReconcileCalibrationSplits(ctx, []StoredMatch{m}, nil)
		if err != nil || !got[kind].Quarantined || got[kind].Split != "holdout" {
			t.Fatalf("%s laundered: %+v %v", kind, got, err)
		}
	}
}

func TestImportedAbsentReplayReservesKnownPlayersAcrossNewMatches(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if err := s.ImportEventReview(ctx, EventReview{EventID: "E", MatchID: "ABSENT", PlayerID: "P1", DetectorID: "THROW_001", Verdict: "yes"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportCalibrationOpportunity(ctx, CalibrationOpportunity{MatchID: "ABSENT", PlayerID: "P2", DetectorID: "THROW_001", Kind: OpportunityThrow, GroundTruth: GroundTruthNegative}); err != nil {
		t.Fatal(err)
	}
	// Repeated imports of another player cannot replace the first membership.
	if err := s.RecordCalibrationExposure(ctx, "ABSENT", " P2 "); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReconcileCalibrationSplits(ctx, []StoredMatch{splitMatch("N1", "P1"), splitMatch("N2", "P2")}, map[string]string{"N1": "holdout", "N2": "holdout"})
	if err != nil || got["N1"].Split != "training" || got["N2"].Split != "training" {
		t.Fatalf("known imported players became unseen holdout: %+v, %v", got, err)
	}
	if _, err := s.ReconcileCalibrationSplits(ctx, []StoredMatch{splitMatch("HELD", "P3")}, map[string]string{"HELD": "holdout"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCalibrationExposure(ctx, "HELD", "P4"); err != nil {
		t.Fatal(err)
	}
	got, err = s.ReconcileCalibrationSplits(ctx, []StoredMatch{splitMatch("HELD", "P3"), splitMatch("NEXT", "P4")}, map[string]string{"NEXT": "holdout"})
	if err != nil || got["HELD"].Split != "holdout" || !got["HELD"].Quarantined || !got["NEXT"].Quarantined || len(got["HELD"].Players) != 2 {
		t.Fatalf("existing held-out membership/quarantine was lost: %+v, %v", got, err)
	}
}

func TestHoldoutExposureLocksCandidateAndSurvivesDeletedReplay(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	match := splitMatch("HELD", "P1")
	if _, err := s.ReconcileCalibrationSplits(ctx, []StoredMatch{match}, map[string]string{"HELD": "holdout"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.RecordCalibrationHoldoutExposure(ctx, map[string]string{"HELD": "candidate-A"}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ReconcileCalibrationSplits(ctx, []StoredMatch{match}, nil)
		if err != nil || got["HELD"].Quarantined || got["HELD"].ExposureFingerprint != "candidate-A" {
			t.Fatalf("same candidate: %+v %v", got, err)
		}
	}
	if err := s.RecordCalibrationHoldoutExposure(ctx, map[string]string{"HELD": "candidate-B"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCalibrationHoldoutExposure(ctx, map[string]string{"HELD": "candidate-A"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReconcileCalibrationSplits(ctx, []StoredMatch{match}, nil)
	if err != nil || !got["HELD"].Quarantined || got["HELD"].ExposureFingerprint != "candidate-A" {
		t.Fatalf("candidate lock overwritten: %+v %v", got, err)
	}
	got, err = s.ReconcileCalibrationSplits(ctx, []StoredMatch{splitMatch("NEW", "P1")}, map[string]string{"NEW": "validation"})
	if err != nil || !got["NEW"].Quarantined || got["NEW"].Split != "holdout" {
		t.Fatalf("deleted exposure lost: %+v %v", got, err)
	}
}

func TestHoldoutUnknownExposureAndHistoricalCandidateConflictFailClosed(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	matches := []StoredMatch{splitMatch("A", "P1"), splitMatch("B", "P2"), splitMatch("UNKNOWN", "P3")}
	if _, err := s.ReconcileCalibrationSplits(ctx, matches, map[string]string{"A": "holdout", "B": "holdout", "UNKNOWN": "holdout"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCalibrationHoldoutExposure(ctx, map[string]string{"A": "candidate-A", "B": "candidate-B", "UNKNOWN": ""}); err != nil {
		t.Fatal(err)
	}
	// Although both groups were held out, their evaluated candidates differ.
	matches = append(matches, splitMatch("BRIDGE", "P1", "P2"))
	got, err := s.ReconcileCalibrationSplits(ctx, matches, map[string]string{"BRIDGE": "training"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B", "BRIDGE", "UNKNOWN"} {
		if !got[id].Quarantined {
			t.Fatalf("unverifiable exposure accepted: %+v", got)
		}
	}
}
