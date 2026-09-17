package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// reanalyse replaces a stored event by the same sampled observation under a
// fresh event id, which is what every forced re-analysis does.
func reanalyse(t *testing.T, s *Store, oldID string, detector, player, match string, frame int) string {
	t.Helper()
	if _, err := s.DB().Exec(`DELETE FROM detection_events WHERE event_id = ?`, oldID); err != nil {
		t.Fatal(err)
	}
	next := mkEvent(detector, player, match, frame, 0.9, 0.9)
	mustStoreEvent(t, s, next)
	return next.EventID
}

func directCalibration(t *testing.T, s *Store, detector string) DetectorCalibration {
	t.Helper()
	rows, err := s.ComputeCalibration(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.DetectorID == detector {
			return row
		}
	}
	return DetectorCalibration{DetectorID: detector}
}

// Re-analysis gives the same observation a new event id and keeps the older
// event_reviews row, so a moderator who labels it again must not move the
// detector's precision a second and third time.
func TestCalibrationCountsARelabelledObservationOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first := mkEvent("THROW_001", "P1", "M1", 40, 0.9, 0.9)
	mustStoreEvent(t, s, first)
	id := first.EventID
	for round := 0; round < 3; round++ {
		if _, err := s.StoreEventReview(ctx, id, "no", "legal throw", "r"); err != nil {
			t.Fatal(err)
		}
		if round < 2 {
			id = reanalyse(t, s, id, "THROW_001", "P1", "M1", 40)
		}
	}
	if n := countRows(t, s, "event_reviews", ""); n != 3 {
		t.Fatalf("fixture: %d review rows, want the 3 retained labels", n)
	}
	got := directCalibration(t, s, "THROW_001")
	if got.DirectLabels != 1 || got.FalsePositive != 1 || got.EventsReviewed != 1 || got.Confirmed != 0 {
		t.Fatalf("one physical observation counted more than once: %+v", got)
	}
}

// The newest verdict for an observation is the one that counts, exactly as the
// scoring eligibility rule already treats it.
func TestCalibrationUsesTheNewestVerdictForAnObservation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first := mkEvent("THROW_001", "P1", "M1", 40, 0.9, 0.9)
	mustStoreEvent(t, s, first)
	if _, err := s.StoreEventReview(ctx, first.EventID, "no", "", "r"); err != nil {
		t.Fatal(err)
	}
	backdate(t, s, "event_reviews", "reviewed_at", "event_id = ?", time.Now().Add(-48*time.Hour), first.EventID)
	second := reanalyse(t, s, first.EventID, "THROW_001", "P1", "M1", 40)
	if _, err := s.StoreEventReview(ctx, second, "yes", "video shows it", "r"); err != nil {
		t.Fatal(err)
	}
	got := directCalibration(t, s, "THROW_001")
	if got.DirectLabels != 1 || got.Confirmed != 1 || got.FalsePositive != 0 {
		t.Fatalf("newest verdict did not replace the older one: %+v", got)
	}
}

// Deduplication is by the sampled observation, never by detector or frame
// alone: a different frame, player, match, detector version or evidence payload
// is a different observation and keeps its own label.
func TestCalibrationKeepsDistinctObservationsSeparate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := mkEvent("THROW_001", "P1", "M1", 40, 0.9, 0.9)
	otherFrame := mkEvent("THROW_001", "P1", "M1", 41, 0.9, 0.9)
	otherPlayer := mkEvent("THROW_001", "P2", "M1", 40, 0.9, 0.9)
	otherMatch := mkEvent("THROW_001", "P1", "M2", 40, 0.9, 0.9)
	otherVersion := mkEvent("THROW_001", "P1", "M1", 40, 0.9, 0.9)
	otherVersion.DetectorVersion = "2.0.0"
	otherValue := mkEvent("THROW_001", "P1", "M1", 40, 0.9, 0.9)
	otherValue.ObservedValue = "v: 31.0"
	for _, labelled := range []struct {
		event   model.DetectionEvent
		verdict string
	}{{base, "no"}, {otherFrame, "no"}, {otherPlayer, "yes"}, {otherMatch, "yes"}, {otherVersion, "uncertain"}, {otherValue, "no"}} {
		mustStoreEvent(t, s, labelled.event)
		if _, err := s.StoreEventReview(ctx, labelled.event.EventID, labelled.verdict, "", "r"); err != nil {
			t.Fatal(err)
		}
	}
	got := directCalibration(t, s, "THROW_001")
	if got.DirectLabels != 6 || got.FalsePositive != 3 || got.Confirmed != 2 || got.Inconclusive != 1 || got.EventsReviewed != 6 {
		t.Fatalf("distinct observations were merged: %+v", got)
	}
}
