package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newTestStoreAt(t *testing.T, dbPath string) *Store {
	t.Helper()
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func mkEvent(detector, player, match string, frame int, sev, conf float64) model.DetectionEvent {
	return model.DetectionEvent{
		EventID:           uuid.New().String(),
		DetectorID:        detector,
		DetectorVersion:   "1.0.0",
		MatchID:           match,
		PlayerID:          player,
		FrameIndex:        frame,
		FrameRangeStart:   frame - 2,
		FrameRangeEnd:     frame + 2,
		Timestamp:         float64(frame) * 0.067,
		Severity:          sev,
		Confidence:        conf,
		ObservedValue:     "v: 25.0",
		ExpectedRange:     "v: < 20.0",
		CausalKey:         model.CausalKey{PlayerID: player, FrameStart: frame - 2, FrameEnd: frame + 2, AnomalyType: "test"},
		EnforcementWeight: 0.8,
	}
}

func mkFrames(player string, from, to int) []model.PlayerTelemetryFrame {
	var out []model.PlayerTelemetryFrame
	for i := from; i < to; i++ {
		out = append(out, model.PlayerTelemetryFrame{
			PlayerID:   player,
			Team:       "blue",
			FrameIndex: i,
			Timestamp:  float64(i) * 0.067,
			DeltaTime:  0.067,
			Position:   model.Vec3{float64(i) * 0.1, 0, 0},
			Rotation:   model.QuatIdentity(),
		})
	}
	return out
}

func mustStoreEvent(t *testing.T, s *Store, ev model.DetectionEvent) {
	t.Helper()
	if err := s.StoreDetectionEvent(context.Background(), ev); err != nil {
		t.Fatalf("storing event: %v", err)
	}
}

// backdate rewrites a timestamp column for rows matching the WHERE clause so
// tests can simulate old data without sleeping.
func backdate(t *testing.T, s *Store, table, column, where string, to time.Time, args ...any) {
	t.Helper()
	q := "UPDATE " + table + " SET " + column + " = ? WHERE " + where
	all := append([]any{fmtDBTime(to)}, args...)
	if _, err := s.DB().Exec(q, all...); err != nil {
		t.Fatalf("backdating %s.%s: %v", table, column, err)
	}
}

func countRows(t *testing.T, s *Store, table, where string, args ...any) int {
	t.Helper()
	var n int
	q := "SELECT COUNT(*) FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	if err := s.DB().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}
