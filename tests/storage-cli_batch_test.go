package tests

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// sessionLineForMatch rewrites the shared fixture line for a different match id.
func sessionLineForMatch(matchID string) string {
	return strings.Replace(validSessionLine, `"sessionid":"TEST-001"`, `"sessionid":"`+matchID+`"`, 1)
}

func batchPipelineFactory() func() *pipeline.Pipeline {
	return func() *pipeline.Pipeline {
		p, _ := newTestPipeline([]detect.Detector{movement.NewMov001(nil)})
		return p
	}
}

func TestBatchAnalyzer_IdempotentIngestAndFileFiltering(t *testing.T) {
	dir := t.TempDir()
	lines := []string{validSessionLine, validSessionLine, validSessionLine, validSessionLine}
	writeNDJSON(t, dir, "a_match.echoreplay", lines)
	// Same match under another name and an upper-case extension: must be
	// enqueued (case-insensitive) but skipped as a duplicate of the first.
	writeNDJSON(t, dir, "b_copy.ECHOREPLAY", lines)
	writeNDJSON(t, filepath.Join(dir), "other.echoreplay", []string{sessionLineForMatch("TEST-002"), sessionLineForMatch("TEST-002")})
	// A bridge dump is a .json file that is not a legacy replay: ignored, not an error.
	if err := os.WriteFile(filepath.Join(dir, "1234_session_raw.json"), []byte(`{"sessionid":"X","teams":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newTestStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := replay.NewBatchAnalyzer(batchPipelineFactory()(), store,
		func() replay.FrameParser { return replay.NewJSONFrameParser() }, 4, logger)
	ba.SetPipelineFactory(batchPipelineFactory())

	ctx := context.Background()
	res, err := ba.AnalyzeDirectory(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalFiles != 3 || res.IgnoredFiles != 1 {
		t.Fatalf("files: total=%d ignored=%d, want 3/1", res.TotalFiles, res.IgnoredFiles)
	}
	if res.Processed != 2 || res.Skipped != 1 || res.Errors != 0 {
		t.Fatalf("first run: %+v", res)
	}
	if res.FramesInserted != 6 || res.FramesIgnored != 0 {
		t.Fatalf("frames: inserted=%d ignored=%d, want 6/0", res.FramesInserted, res.FramesIgnored)
	}
	if n, _ := store.GetMatchTickCount(ctx, "TEST-001"); n != 4 {
		t.Errorf("raw ticks for TEST-001 = %d, want 4 (once per tick)", n)
	}
	if cnt, _ := store.GetStoredMatchCount(ctx); cnt != 2 {
		t.Errorf("stored matches = %d", cnt)
	}
	eventsAfterFirst := countEvents(t, store, "TEST-001")

	// Second run over the same directory: everything already stored, nothing duplicated.
	res, err = ba.AnalyzeDirectory(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 0 || res.Skipped != 3 || res.FramesInserted != 0 {
		t.Fatalf("second run should skip stored matches: %+v", res)
	}
	if got := countEvents(t, store, "TEST-001"); got != eventsAfterFirst {
		t.Fatalf("events duplicated on re-ingest: %d -> %d", eventsAfterFirst, got)
	}

	// --force replaces derived outputs without duplicating them.
	ba.SetForce(true)
	res, err = ba.AnalyzeDirectory(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 2 || res.Skipped != 1 || res.FramesInserted != 0 || res.FramesIgnored != 6 {
		t.Fatalf("forced run: %+v", res)
	}
	if got := countEvents(t, store, "TEST-001"); got != eventsAfterFirst {
		t.Fatalf("forced re-analysis changed event count: %d -> %d", eventsAfterFirst, got)
	}
	var srcCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM detection_events WHERE analysis_source != 'reprocess'`).Scan(&srcCount); err != nil {
		t.Fatal(err)
	}
	if srcCount != 0 {
		t.Errorf("forced re-analysis must tag events as reprocess; %d rows untagged", srcCount)
	}
}

func countEvents(t *testing.T, store *sqlite.Store, matchID string) int {
	t.Helper()
	evs, err := store.GetMatchEvents(context.Background(), matchID)
	if err != nil {
		t.Fatal(err)
	}
	return len(evs)
}

func TestBatchAnalyzer_EmptyAndMissingDirectory(t *testing.T) {
	store := newTestStore(t)
	ba := replay.NewBatchAnalyzer(batchPipelineFactory()(), store,
		func() replay.FrameParser { return replay.NewJSONFrameParser() }, 2, nil)
	res, err := ba.AnalyzeDirectory(context.Background(), t.TempDir())
	if err != nil || res.TotalFiles != 0 || res.Processed != 0 {
		t.Fatalf("empty dir: %+v %v", res, err)
	}
}
