package main

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

var immutableMechanicsDetectors = []string{"STATE_001", "THROW_005", "THROW_006", "STATE_008"}

func TestAutopocketPromotionBlockedEvenWithRepresentativeMetrics(t *testing.T) {
	for _, id := range immutableMechanicsDetectors {
		t.Run(id, func(t *testing.T) {
			metric := representativeGateMetric()
			metric.DetectorID = "STATE_002"
			metric.ByLegalContext["transition"] = metric.ByLegalContext["normal"]
			applyPromotionGate(&metric)
			if !metric.Eligible {
				t.Fatalf("setup: otherwise representative metric fails: %v", metric.Reasons)
			}
			metric.DetectorID = id
			applyPromotionGate(&metric)
			if metric.Eligible || metric.PromotionBlock == "" || metric.PromotionBlock != promotionBlocked[id] || len(metric.Reasons) != 1 || metric.Reasons[0] != metric.PromotionBlock {
				t.Fatalf("unvalidated review became promotable from labels: %+v", metric)
			}
		})
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
	for _, id := range immutableMechanicsDetectors {
		t.Run(id, func(t *testing.T) {
			var response map[string]any
			resp := postJSONTest(t, ts.URL+"/"+testToken+"/api/lab/promotions/"+strings.ToLower(id), map[string]any{}, &response)
			block, _ := response["promotion_block"].(string)
			if resp.StatusCode != http.StatusUnprocessableEntity || block == "" || block != promotionBlocked[id] {
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
				t.Fatal("rejected promotion mutated profiles or approvals")
			}
			dashboard, err := s.buildCalibrationDashboard(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			metric, ok := detectorMetric(dashboard, id)
			if !ok || metric.Eligible || metric.PromotionBlock != promotionBlocked[id] {
				t.Fatalf("dashboard must suppress promotion and explain why: %+v", metric)
			}
		})
	}
}

func TestAutopocketStalePromotionIsRolledBack(t *testing.T) {
	for _, id := range immutableMechanicsDetectors {
		t.Run(id, func(t *testing.T) {
			s, _ := newTestServer(t)
			ctx := context.Background()
			if _, err := s.engine.Store().StoreDetectorPromotion(ctx, sqlite.DetectorPromotion{DetectorID: id, ProfileName: "stale approval", Status: sqlite.PromotionActive}); err != nil {
				t.Fatal(err)
			}
			// Simulate a stale in-memory approval too, so rollback must actually
			// clear unsafe fields rather than merely retain already-safe defaults.
			dc := s.engine.Config().GetDetectorConfig(id)
			dc.Mode, dc.AutoEnforce, dc.EnforcementWeight = "review", true, 1
			s.engine.Config().Detectors[id] = dc
			metric := representativeGateMetric()
			metric.DetectorID = id
			metric.ByLegalContext["transition"] = metric.ByLegalContext["normal"]
			applyPromotionGate(&metric)
			rolledBack := s.reconcilePromotions(ctx, calibrationDashboard{Detectors: []detectorCalibrationMetric{metric}})
			if !reflect.DeepEqual(rolledBack, []string{id}) {
				t.Fatalf("stale approval not revoked: %v", rolledBack)
			}
			promotion, ok, err := s.engine.Store().GetDetectorPromotion(ctx, id)
			if err != nil || !ok || promotion.Status != sqlite.PromotionRolledBack {
				t.Fatalf("stored approval=%+v ok=%v error=%v", promotion, ok, err)
			}
			dc = s.engine.Config().GetDetectorConfig(id)
			if dc.Mode != "shadow" || dc.AutoEnforce || dc.EnforcementWeight != 0 {
				t.Fatalf("rollback left scoring enabled: %+v", dc)
			}
		})
	}
}
