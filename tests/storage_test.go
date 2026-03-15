package tests

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := sqlite.NewStore(dbPath)
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestStore_DetectionEventRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	evt := testutil.MakeDetectionEvent("THROW_001", "player1", 0.9, 0.85)
	evt.MatchID = "match1"

	if err := store.StoreDetectionEvent(ctx, evt); err != nil {
		t.Fatalf("storing event: %v", err)
	}

	events, err := store.GetPlayerEvents(ctx, "player1", 100, 0)
	if err != nil {
		t.Fatalf("getting events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].DetectorID != "THROW_001" {
		t.Errorf("expected THROW_001, got %s", events[0].DetectorID)
	}
	if events[0].PlayerID != "player1" {
		t.Errorf("expected player1, got %s", events[0].PlayerID)
	}
	if events[0].Severity != 0.9 {
		t.Errorf("expected severity 0.9, got %.2f", events[0].Severity)
	}
}

func TestStore_SuspicionScoreRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	score := model.SuspicionScore{
		PlayerID:        "player1",
		TotalScore:      42.5,
		ScoreByCategory: map[string]float64{"throw": 30.0, "movement": 12.5},
		ScoreByDetector: map[string]float64{},
		EventCount:      3,
		MatchCount:      1,
		SnapshotTime:    time.Now().Truncate(time.Second),
	}

	if err := store.StoreSuspicionScore(ctx, score); err != nil {
		t.Fatalf("storing score: %v", err)
	}

	got, err := store.GetPlayerScore(ctx, "player1")
	if err != nil {
		t.Fatalf("getting score: %v", err)
	}
	if got.TotalScore != 42.5 {
		t.Errorf("expected 42.5, got %.1f", got.TotalScore)
	}
	if got.EventCount != 3 {
		t.Errorf("expected 3 events, got %d", got.EventCount)
	}
	if got.ScoreByCategory["throw"] != 30.0 {
		t.Errorf("expected throw=30.0, got %.1f", got.ScoreByCategory["throw"])
	}
}

func TestStore_ReviewCaseRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rc := model.ReviewCase{
		CaseID:         "test-case-001",
		PlayerID:       "player1",
		MatchID:        "match1",
		SuspicionScore: 65.0,
		Severity:       "high",
		DetectorsTriggered: []model.TriggeredDetector{
			{DetectorID: "THROW_001", Confidence: 0.85},
		},
		RecommendedAction: "review_only",
		Explanation:       "Test review case",
		Status:            "pending",
		CreatedAt:         time.Now().Truncate(time.Second),
	}

	if err := store.StoreReviewCase(ctx, rc); err != nil {
		t.Fatalf("storing case: %v", err)
	}

	got, err := store.GetReviewCase(ctx, "test-case-001")
	if err != nil {
		t.Fatalf("getting case: %v", err)
	}
	if got.PlayerID != "player1" {
		t.Errorf("expected player1, got %s", got.PlayerID)
	}
	if got.SuspicionScore != 65.0 {
		t.Errorf("expected 65.0, got %.1f", got.SuspicionScore)
	}
	if got.Status != "pending" {
		t.Errorf("expected pending, got %s", got.Status)
	}
	if len(got.DetectorsTriggered) != 1 {
		t.Errorf("expected 1 detector in case, got %d", len(got.DetectorsTriggered))
	}
	if got.Explanation != "Test review case" {
		t.Errorf("expected 'Test review case', got %q", got.Explanation)
	}
}
