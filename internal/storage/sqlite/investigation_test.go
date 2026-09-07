package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestInvestigationNotesRunsProfilesAndFilters(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	note, err := store.StoreInvestigationNote(ctx, InvestigationNote{MatchID: "M1", FrameIndex: 12, Kind: "bookmark"})
	if err != nil || note.NoteID == "" {
		t.Fatalf("store note = %+v, %v", note, err)
	}
	if notes, err := store.ListInvestigationNotes(ctx, "M1"); err != nil || len(notes) != 1 {
		t.Fatalf("list notes = %+v, %v", notes, err)
	}
	run, err := store.StoreAnalysisRun(ctx, AnalysisRun{MatchID: "M1", Source: "test", AppVersion: "v", ExecutableSHA256: strings.Repeat("a", 64), ConfigFingerprint: "abc", CalibrationFingerprint: "behavior", TelemetryQuality: 95})
	if err != nil || run.RunID == 0 {
		t.Fatalf("store run = %+v, %v", run, err)
	}
	if runs, err := store.ListAnalysisRuns(ctx, "M1", 10); err != nil || len(runs) != 1 || runs[0].TelemetryQuality != 95 || runs[0].CalibrationFingerprint != "behavior" || runs[0].ExecutableSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("list runs = %+v, %v", runs, err)
	}
	detectors, _ := json.Marshal(map[string]any{"THROW_001": map[string]any{"enabled": true, "mode": "shadow"}})
	if _, err := store.StoreConfigProfile(ctx, ConfigProfile{Name: "Conservative", DetectorsJSON: string(detectors)}); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateConfigProfile(ctx, "Conservative"); err != nil {
		t.Fatal(err)
	}
	if active, ok, err := store.GetActiveConfigProfile(ctx); err != nil || !ok || active.Name != "Conservative" {
		t.Fatalf("active profile = %+v, %t, %v", active, ok, err)
	}
	if _, err := store.StoreSavedFilter(ctx, SavedFilter{Name: "Throws", FilterJSON: `{"detector":"THROW_001"}`}); err != nil {
		t.Fatal(err)
	}
	if filters, err := store.ListSavedFilters(ctx); err != nil || len(filters) != 1 {
		t.Fatalf("filters = %+v, %v", filters, err)
	}
}

func TestAnalysisExecutableHashRejectsMalformedButPreservesUnknownLegacy(t *testing.T) {
	s := newTestStore(t)
	for _, hash := range []string{"development", "fake", strings.Repeat("a", 63), strings.Repeat("A", 64)} {
		if _, err := s.StoreAnalysisRun(t.Context(), AnalysisRun{MatchID: "M", ExecutableSHA256: hash}); err == nil {
			t.Fatalf("invalid hash %q accepted", hash)
		}
	}
	if _, err := s.StoreAnalysisRun(t.Context(), AnalysisRun{MatchID: "M"}); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListAnalysisRuns(t.Context(), "M", 1)
	if err != nil || len(runs) != 1 || runs[0].ExecutableSHA256 != "" {
		t.Fatal("legacy missing hash was fabricated")
	}
}
