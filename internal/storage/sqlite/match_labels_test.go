package sqlite

import (
	"context"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestMatchLabels(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.StoreMatchLabel(ctx, "missing", MatchLabelKnownClean, "", "", "0.3", "cfg"); err == nil {
		t.Fatal("expected unknown match to be rejected")
	}
	if err := s.StoreMatchContext(ctx, &model.MatchContext{MatchID: "M1"}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := s.StoreMatchLabel(ctx, "M1", MatchLabelKnownClean, "league-reviewed", "tester", "0.3", "cfg")
	if err != nil || got.Label != MatchLabelKnownClean || got.ReviewerID != "tester" || got.AppVersion != "0.3" || got.ConfigFingerprint != "cfg" || got.ReviewedAt.IsZero() {
		t.Fatalf("StoreMatchLabel = %+v, %v", got, err)
	}
	got, ok, err := s.GetMatchLabel(ctx, "M1")
	if err != nil || !ok || got.Comment != "league-reviewed" {
		t.Fatalf("GetMatchLabel = %+v, %v, %v", got, ok, err)
	}
	if _, err := s.StoreMatchLabel(ctx, "M1", "maybe", "", "", "", ""); err == nil {
		t.Fatal("expected invalid label to be rejected")
	}
	if _, err := s.StoreMatchLabel(ctx, "M1", MatchLabelConfirmedCheat, "confirmed manually", "", "0.3", "cfg2"); err != nil {
		t.Fatal(err)
	}
	labels, err := s.GetMatchLabels(ctx, nil)
	if err != nil || len(labels) != 1 || labels["M1"].Label != MatchLabelConfirmedCheat {
		t.Fatalf("GetMatchLabels = %+v, %v", labels, err)
	}
	counts, err := s.MatchLabelCounts(ctx)
	if err != nil || counts[MatchLabelConfirmedCheat] != 1 || counts[MatchLabelKnownClean] != 0 {
		t.Fatalf("MatchLabelCounts = %+v, %v", counts, err)
	}
}
