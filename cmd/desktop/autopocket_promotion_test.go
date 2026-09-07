package main

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func TestAutopocketPromotionBlockedEvenWithRepresentativeMetrics(t *testing.T) {
	metric := representativeGateMetric()
	metric.DetectorID = "STATE_001"
	metric.ByLegalContext["transition"] = metric.ByLegalContext["normal"]
	applyPromotionGate(&metric)
	if !metric.Eligible {
		t.Fatalf("setup: otherwise representative state metric fails: %v", metric.Reasons)
	}
	metric.DetectorID = "STATE_008"
	applyPromotionGate(&metric)
	if metric.Eligible || !strings.Contains(metric.PromotionBlock, "observation-only") ||
		len(metric.Reasons) != 1 || metric.Reasons[0] != metric.PromotionBlock {
		t.Fatalf("catch review must remain unpromotable independent of labels: %+v", metric)
	}
}

func TestAutopocketPromotionRequestDoesNotWriteProfileOrApproval(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()
	profilesBefore, err := s.engine.Store().ListConfigProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	promotionsBefore, err := s.engine.Store().ListDetectorPromotions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	resp := postJSONTest(t, ts.URL+"/"+testToken+"/api/lab/promotions/state_008", map[string]any{}, &response)
	block, _ := response["promotion_block"].(string)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(block, "observation-only") {
		t.Fatalf("promotion response=%d %+v", resp.StatusCode, response)
	}
	profilesAfter, err := s.engine.Store().ListConfigProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	promotionsAfter, err := s.engine.Store().ListDetectorPromotions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(profilesBefore, profilesAfter) || !reflect.DeepEqual(promotionsBefore, promotionsAfter) {
		t.Fatalf("rejected catch promotion mutated profiles or approvals")
	}
	dashboard, err := s.buildCalibrationDashboard(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	metric, ok := detectorMetric(dashboard, "STATE_008")
	if !ok || metric.Eligible || metric.PromotionBlock != promotionBlocked["STATE_008"] {
		t.Fatalf("dashboard must suppress promotion and explain why: %+v", metric)
	}
}

func TestAutopocketStalePromotionIsRolledBack(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := s.engine.Store().StoreDetectorPromotion(ctx, sqlite.DetectorPromotion{
		DetectorID: "STATE_008", ProfileName: "stale approval", Status: sqlite.PromotionActive,
	}); err != nil {
		t.Fatal(err)
	}
	metric := representativeGateMetric()
	metric.DetectorID = "STATE_008"
	metric.ByLegalContext["transition"] = metric.ByLegalContext["normal"]
	applyPromotionGate(&metric)
	rolledBack := s.reconcilePromotions(ctx, calibrationDashboard{Detectors: []detectorCalibrationMetric{metric}})
	if !reflect.DeepEqual(rolledBack, []string{"STATE_008"}) {
		t.Fatalf("stale catch approval not revoked: %v", rolledBack)
	}
	promotion, ok, err := s.engine.Store().GetDetectorPromotion(ctx, "STATE_008")
	if err != nil || !ok || promotion.Status != sqlite.PromotionRolledBack {
		t.Fatalf("stored approval=%+v ok=%v error=%v", promotion, ok, err)
	}
	dc := s.engine.Config().GetDetectorConfig("STATE_008")
	if dc.Mode != "shadow" || dc.AutoEnforce || dc.EnforcementWeight != 0 {
		t.Fatalf("rollback left scoring enabled: %+v", dc)
	}
}
