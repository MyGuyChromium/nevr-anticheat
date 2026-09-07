package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestCalibrationOpportunitiesRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.StoreMatchContext(ctx, &model.MatchContext{MatchID: "M1", PlayerIDs: []string{"P1"}}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreTelemetryFrames(ctx, "M1", mkFrames("P1", 10, 12)); err != nil {
		t.Fatal(err)
	}
	got, err := s.StoreCalibrationOpportunity(ctx, CalibrationOpportunity{
		MatchID: "M1", PlayerID: "P1", DetectorID: "throw_001", BehaviorType: "over-cap release",
		Kind: OpportunityThrow, FrameStart: 10, FrameEnd: 11, TimestampStart: .67,
		TimestampEnd: .74, GroundTruth: GroundTruthPositive, Comment: "verified in Spark", BlindReview: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.OpportunityID == "" || got.DetectorID != "THROW_001" || !got.BlindReview || got.ReviewerID != "local-owner" {
		t.Fatalf("stored opportunity = %+v", got)
	}
	rows, err := s.ListCalibrationOpportunities(ctx, "M1", "THROW_001")
	if err != nil || len(rows) != 1 || rows[0].Comment != "verified in Spark" {
		t.Fatalf("opportunities = %+v, %v", rows, err)
	}
	if _, err := s.StoreCalibrationOpportunity(ctx, CalibrationOpportunity{
		MatchID: "M1", PlayerID: "P1", DetectorID: "THROW_001", Kind: OpportunityThrow,
		FrameStart: 11, FrameEnd: 13, GroundTruth: GroundTruthNegative,
	}); err == nil {
		t.Fatal("overlapping ground-truth window accepted")
	}
	if ok, err := s.DeleteCalibrationOpportunity(ctx, got.OpportunityID); err != nil || !ok {
		t.Fatalf("delete = %v, %v", ok, err)
	}
}

func TestCalibrationOpportunitiesValidateAndPromotions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.StoreCalibrationOpportunity(ctx, CalibrationOpportunity{}); err == nil {
		t.Fatal("empty opportunity accepted")
	}
	p, err := s.StoreDetectorPromotion(ctx, DetectorPromotion{
		DetectorID: "throw_001", ProfileName: "promote THROW_001", Status: PromotionCandidate,
		Metrics: json.RawMessage(`{"eligible":true}`), ConfigFingerprint: "abc",
	})
	if err != nil || p.DetectorID != "THROW_001" {
		t.Fatalf("promotion = %+v, %v", p, err)
	}
	p.Status = PromotionActive
	if _, err := s.StoreDetectorPromotion(ctx, p); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := s.GetDetectorPromotion(ctx, "THROW_001")
	if err != nil || !ok || loaded.Status != PromotionActive {
		t.Fatalf("loaded promotion = %+v, %v, %v", loaded, ok, err)
	}
	if changed, err := s.RollBackDetectorPromotion(ctx, "THROW_001"); err != nil || !changed {
		t.Fatalf("rollback = %v, %v", changed, err)
	}
	loaded, _, _ = s.GetDetectorPromotion(ctx, "THROW_001")
	if loaded.Status != PromotionRolledBack {
		t.Fatalf("rollback status = %q", loaded.Status)
	}
}

func TestCalibrationOpportunityWindowRequiresPlayerSamplesButImportPreservesEvidence(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if err := s.StoreMatchContext(ctx, &model.MatchContext{MatchID: "M", PlayerIDs: []string{"P", "OTHER"}}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreTelemetryFrames(ctx, "M", mkFrames("P", 10, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreTelemetryFrames(ctx, "M", mkFrames("OTHER", 20, 22)); err != nil {
		t.Fatal(err)
	}
	for _, interval := range [][2]int{{0, 9}, {13, 15}, {20, 22}, {999, 1000}} {
		x := CalibrationOpportunity{MatchID: "M", PlayerID: "P", DetectorID: "THROW_001", Kind: OpportunityThrow,
			FrameStart: interval[0], FrameEnd: interval[1], GroundTruth: GroundTruthNegative}
		if _, err := s.StoreCalibrationOpportunity(ctx, x); err == nil {
			t.Fatalf("empty player window accepted: %+v", interval)
		}
		if err := s.ImportCalibrationOpportunity(ctx, x); err != nil {
			t.Fatalf("unavailable portable evidence was discarded: %v", err)
		}
		if available, err := s.CalibrationWindowHasSamples(ctx, "M", "P", interval[0], interval[1]); err != nil || available {
			t.Fatalf("empty window availability = %v, %v", available, err)
		}
	}
	x := CalibrationOpportunity{MatchID: "M", PlayerID: "P", DetectorID: "THROW_001", Kind: OpportunityThrow,
		FrameStart: 10, FrameEnd: 11, GroundTruth: GroundTruthPositive}
	if _, err := s.StoreCalibrationOpportunity(ctx, x); err != nil {
		t.Fatalf("available window rejected: %v", err)
	}
	items, err := s.ListCalibrationOpportunities(ctx, "M", "")
	if err != nil || len(items) != 5 {
		t.Fatalf("preserved annotations = %d, %v", len(items), err)
	}
}

func TestIndependentCalibrationEvidenceRoundTripAndDisagreement(t *testing.T) {
	s := newTestStore(t)
	x := CalibrationOpportunity{MatchID: "M", PlayerID: "P", DetectorID: "THROW_001", Kind: OpportunityThrow,
		FrameStart: 10, FrameEnd: 20, GroundTruth: GroundTruthPositive, ReviewerID: "Alice", VerifierID: "Bob",
		BlindReview: true, VerifiedGroundTruth: GroundTruthPositive, EvidenceMethod: "synchronized_video", EvidenceReference: "clip-42@10-20"}
	if err := s.ImportCalibrationOpportunity(t.Context(), x); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListCalibrationOpportunities(t.Context(), "M", "")
	if err != nil || len(items) != 1 || !items[0].IndependentEvidenceVerified() || items[0].EvidenceReference != x.EvidenceReference {
		t.Fatalf("evidence round trip: %+v %v", items, err)
	}
	for name, modify := range map[string]func(*CalibrationOpportunity){
		"sameReviewer":      func(x *CalibrationOpportunity) { x.VerifierID = "ALICE" },
		"placeholder":       func(x *CalibrationOpportunity) { x.ReviewerID = "local-owner" },
		"disputed":          func(x *CalibrationOpportunity) { x.VerifiedGroundTruth = GroundTruthNegative },
		"unblinded":         func(x *CalibrationOpportunity) { x.BlindReview = false },
		"missingArtifact":   func(x *CalibrationOpportunity) { x.EvidenceReference = "" },
		"reportedAdmission": func(x *CalibrationOpportunity) { x.EvidenceMethod = "reported_admission" },
		"uncertain": func(x *CalibrationOpportunity) {
			x.GroundTruth = GroundTruthUncertain
			x.VerifiedGroundTruth = GroundTruthUncertain
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := x
			modify(&copy)
			if copy.IndependentEvidenceVerified() {
				t.Fatal("ineligible attestation accepted")
			}
		})
	}
	legacy := CalibrationOpportunity{MatchID: "OLD", PlayerID: "P", DetectorID: "THROW_001", Kind: OpportunityThrow, GroundTruth: GroundTruthPositive}
	if err := s.ImportCalibrationOpportunity(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListCalibrationOpportunities(t.Context(), "OLD", "")
	if err != nil || len(items) != 1 || items[0].IndependentEvidenceVerified() {
		t.Fatalf("legacy label not preserved safely: %+v %v", items, err)
	}
}
