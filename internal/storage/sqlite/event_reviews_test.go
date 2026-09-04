package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEventReviews_RoundTripUpdateAndCalibration(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ev := mkEvent("THROW_003", "P1", "M1", 42, 0.9, 0.8)
	ev.DetectorVersion = "2.1.0"
	mustStoreEvent(t, s, ev)

	review, err := s.StoreEventReview(ctx, ev.EventID, "No", "legal headbutt", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict != "no" || review.Comment != "legal headbutt" || review.DetectorVersion != "2.1.0" || review.FrameIndex != 42 || review.ReviewedAt.IsZero() {
		t.Fatalf("stored review = %+v", review)
	}
	review, err = s.StoreEventReviewWithBlind(ctx, ev.EventID, "no", "reviewed before reveal", "owner", true)
	if err != nil || !review.BlindReview {
		t.Fatalf("blind review = %+v, %v", review, err)
	}

	review, err = s.StoreEventReview(ctx, ev.EventID, "yes", "confirmed on replay", "")
	if err != nil || review.ReviewerID != "local-owner" {
		t.Fatalf("updated review = %+v, %v", review, err)
	}
	byMatch, err := s.GetEventReviewsByMatch(ctx, "M1")
	if err != nil || len(byMatch) != 1 || byMatch[ev.EventID].Verdict != "yes" {
		t.Fatalf("reviews = %+v, %v", byMatch, err)
	}

	rows, err := s.ComputeCalibration(ctx, time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("calibration = %+v, %v", rows, err)
	}
	if got := rows[0]; got.DetectorID != "THROW_003" || got.Confirmed != 1 || got.DirectLabels != 1 || got.EventsReviewed != 1 || got.CasesReviewed != 0 {
		t.Errorf("calibration = %+v", got)
	}

	// Direct labels deliberately survive deletion of recomputable events.
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM detection_events WHERE event_id = ?`, ev.EventID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListEventReviews(ctx, time.Time{}); err != nil || len(got) != 1 {
		t.Fatalf("preserved labels = %+v, %v", got, err)
	}
}

func TestEventReviews_ValidateAndRequireExistingEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.StoreEventReview(ctx, "missing", "yes", "", "owner"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing event error = %v", err)
	}
	ev := mkEvent("MOV_006", "P1", "M1", 1, 0.5, 0.5)
	mustStoreEvent(t, s, ev)
	if _, err := s.StoreEventReview(ctx, ev.EventID, "maybe", "", "owner"); err == nil {
		t.Fatal("invalid verdict accepted")
	}
	if _, err := s.StoreEventReview(ctx, ev.EventID, "yes", string(make([]byte, 2001)), "owner"); err == nil {
		t.Fatal("oversized comment accepted")
	}
}
