package replay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// batch --force re-analyzes the SAME recording. A different recording of a
// stored match (another observer's copy) must be refused: the stored ticks
// stay byte-identical, nothing is counted as processed, and the run says how
// the file differs.
func TestBatchAnalyzer_ForceRefusesADifferentRecordingOfAStoredMatch(t *testing.T) {
	src, err := os.ReadFile(syntheticReplay)
	if err != nil {
		t.Fatal(err)
	}
	first := t.TempDir()
	if err := os.WriteFile(filepath.Join(first, "first-observer.echoreplay"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	// Another observer's copy: same session id, one snapshot differs.
	lines := strings.SplitAfter(string(src), "\n")
	if len(lines) < 41 || !strings.Contains(lines[40], `"ping":45`) {
		t.Fatal("fixture no longer has the shape this test edits")
	}
	lines[40] = strings.Replace(lines[40], `"ping":45`, `"ping":46`, 1)
	second := t.TempDir()
	if err := os.WriteFile(filepath.Join(second, "second-observer.echoreplay"), []byte(strings.Join(lines, "")), 0o644); err != nil {
		t.Fatal(err)
	}

	store := testStore(t)
	ctx := context.Background()
	ba := NewBatchAnalyzer(emptyPipeline(), store, func() FrameParser { return NewJSONFrameParser() }, 2, quietLogger())
	ba.SetPipelineFactory(emptyPipeline)
	if res, err := ba.AnalyzeDirectory(ctx, first); err != nil || res.Processed != 1 {
		t.Fatalf("first run: %+v, %v", res, err)
	}
	before, err := store.GetMatchRawTicks(ctx, "SYN-FIXTURE-001", 0, 1<<30)
	if err != nil || len(before) == 0 {
		t.Fatalf("stored ticks: %d, %v", len(before), err)
	}

	ba.SetForce(true)
	res, err := ba.AnalyzeDirectory(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 0 || res.Skipped != 1 || res.SourceConflicts != 1 || res.Errors != 0 {
		t.Fatalf("forced run over another observer's copy: %+v (want 0 processed, 1 skipped, 1 source conflict)", res)
	}
	if len(res.ConflictDetails) != 1 || !strings.Contains(res.ConflictDetails[0], "second-observer.echoreplay") || !strings.Contains(res.ConflictDetails[0], "differs from the stored recording") {
		t.Errorf("conflict detail = %q", res.ConflictDetails)
	}
	after, err := store.GetMatchRawTicks(ctx, "SYN-FIXTURE-001", 0, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("stored tick count changed: %d -> %d", len(before), len(after))
	}
	for idx, raw := range before {
		if after[idx] != raw {
			t.Fatalf("stored tick %d was rewritten by a refused recording", idx)
		}
	}

	// The same recording is still re-analyzed under force, and a later run
	// starts with a clean conflict list.
	res, err = ba.AnalyzeDirectory(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 1 || res.SourceConflicts != 0 || len(res.ConflictDetails) != 0 {
		t.Errorf("forced run over the stored recording itself: %+v", res)
	}
}
