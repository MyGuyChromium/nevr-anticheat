package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestRecommendationClaimRequiresPersistedEligibleBoundEvidence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	event := mkEvent("MOV_001", "p", "m", 10, 1, 1)
	action := model.EnforcementAction{ActionID: "recommendation-test", PlayerID: "p", ActionType: model.ActionFlag, IssuedAt: time.Now(), EvidenceIDs: []string{event.EventID}, MatchIDs: []string{"m"}}
	if claimed, err := s.StoreEnforcementActionOnce(ctx, action); err == nil || claimed {
		t.Fatal("missing evidence allowed a recommendation claim")
	}
	mustStoreEvent(t, s, event)
	wrong := action
	wrong.PlayerID = "other"
	if claimed, err := s.StoreEnforcementActionOnce(ctx, wrong); err == nil || claimed {
		t.Fatal("misattributed evidence allowed a claim")
	}
	if claimed, err := s.StoreEnforcementActionOnce(ctx, action); err != nil || !claimed {
		t.Fatalf("valid claim failed: %v %v", claimed, err)
	}
	if claimed, err := s.StoreEnforcementActionOnce(ctx, action); err != nil || claimed {
		t.Fatalf("retry was not idempotent: %v %v", claimed, err)
	}
	if _, err := s.StoreEventReview(ctx, event.EventID, "no", "legal", "reviewer"); err != nil {
		t.Fatal(err)
	}
	action.ActionID = "new-after-overturn"
	if claimed, err := s.StoreEnforcementActionOnce(ctx, action); err == nil || claimed {
		t.Fatal("overturned evidence permitted a new recommendation")
	}
	if countRows(t, s, "enforcement_actions", "") != 1 {
		t.Fatal("claim retries or invalid evidence inflated actions")
	}
}
