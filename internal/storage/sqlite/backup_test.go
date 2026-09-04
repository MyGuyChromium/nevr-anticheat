package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupCreatesVerifiedIndependentSnapshot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.StoreTelemetryFrames(ctx, "backup-match", mkFrames("p1", 0, 4)); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "nested", "snapshot.db")
	if err := s.Backup(ctx, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Size() == 0 {
		t.Fatalf("backup file = %v, %v", info, err)
	}

	copyStore, err := NewStore(destination)
	if err != nil {
		t.Fatalf("opening backup: %v", err)
	}
	defer copyStore.Close()
	got, err := copyStore.GetMatchFrames(ctx, "backup-match")
	if err != nil || len(got) != 4 {
		t.Fatalf("backup frames = %d, %v", len(got), err)
	}

	// A backup is immutable evidence by default; replacing it requires the
	// operator to choose a new path or remove it deliberately.
	if err := s.Backup(ctx, destination); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("existing destination error = %v", err)
	}
}

func TestBackupRejectsInvalidDestinationAndCleansCancelledCopy(t *testing.T) {
	s := newTestStore(t)
	if err := s.Backup(context.Background(), "  "); err == nil {
		t.Error("blank destination accepted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	destination := filepath.Join(t.TempDir(), "cancelled.db")
	if err := s.Backup(ctx, destination); err == nil {
		t.Error("cancelled backup succeeded")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed backup left a destination behind: %v", err)
	}
}

func TestCheckAndCheckpointVerifiesLiveDatabase(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.StoreTelemetryFrames(ctx, "maintenance-match", mkFrames("p1", 0, 8)); err != nil {
		t.Fatal(err)
	}
	result, err := s.CheckAndCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Integrity != "ok" || result.BusyConnections != 0 {
		t.Fatalf("maintenance result = %+v", result)
	}
}
