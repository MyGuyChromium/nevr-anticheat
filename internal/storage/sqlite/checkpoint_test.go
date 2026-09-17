package sqlite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointTruncateEmptiesTheWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wal.db")
	s := newTestStoreAt(t, dbPath)
	ticks := make(map[int]string, 400)
	payload := `{"pad":"` + strings.Repeat("x", 4096) + `"}`
	for i := range 400 {
		ticks[i] = payload
	}
	if _, _, err := s.RestoreMatchRawTicks(t.Context(), "WAL", ticks); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dbPath + "-wal")
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected a non-empty WAL before the checkpoint: %v %v", info, err)
	}
	result, err := s.CheckpointTruncate(t.Context())
	if err != nil || result.BusyConnections != 0 {
		t.Fatalf("checkpoint: %+v %v", result, err)
	}
	if info, err := os.Stat(dbPath + "-wal"); err != nil || info.Size() != 0 {
		t.Fatalf("WAL after TRUNCATE checkpoint: %v %v", info, err)
	}
	got, err := s.GetAllMatchRawTicks(t.Context(), "WAL")
	if err != nil || len(got) != 400 {
		t.Fatalf("data after checkpoint: %d %v", len(got), err)
	}
}
