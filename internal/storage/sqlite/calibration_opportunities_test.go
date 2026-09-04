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
