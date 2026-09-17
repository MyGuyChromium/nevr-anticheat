package main

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"syscall"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
)

// permanentFailurePrefixes are the analysis errors that depend only on the
// bytes of the recording (or on a source conflict with what is already
// stored), so analyzing the same spooled copy again can never succeed. The
// list is deliberately an allow-list: an error that is not recognised keeps
// the durable copy, which is the safe side for evidence.
var permanentFailurePrefixes = []string{
	"reading echoreplay:",    // parser refused the recording
	"reading native tape:",   // native capture failed validation
	"reading replay:",        // legacy JSON replay could not be parsed
	"replay has no match id", // nothing to store it under
	"replay holds no match",  // parsed, but empty
	"source conflict:",       // refused to replace stored evidence from another source
}

// analysisFailureIsPermanent reports whether a failed analysis of a spooled
// upload is deterministic, so the durable copy in the recovery queue should be
// dropped instead of being re-analyzed (and failing again) on every launch.
//
// Cancellation, shutdown, any storage failure and any operating-system I/O
// error are never permanent: those copies stay queued for crash recovery.
func analysisFailureIsPermanent(ctx context.Context, results []*replay.AnalyzeResult, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	for _, result := range results {
		if result != nil && result.PersistError() != nil {
			return false
		}
	}
	if err == nil {
		// No error and no match: the recording parsed but holds nothing.
		return len(results) == 0
	}
	var pathErr *fs.PathError
	var errno syscall.Errno
	if errors.As(err, &pathErr) || errors.As(err, &errno) {
		return false
	}
	if strings.Contains(err.Error(), "comes back after another session") {
		return true
	}
	for _, prefix := range permanentFailurePrefixes {
		if strings.HasPrefix(err.Error(), prefix) {
			return true
		}
	}
	return false
}

// checkpointAfterAnalysis folds the write-ahead log back into the database
// after an import. It is best effort: a failure is logged and changes nothing
// about the analysis that was just stored. Callers hold analyzeMu, so it never
// competes with another import.
func (rt *desktopRuntime) checkpointAfterAnalysis() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	started := time.Now()
	result, err := rt.engine.Store().CheckpointTruncate(ctx)
	if err != nil {
		rt.engine.Logger().Warn("post-analysis WAL checkpoint did not complete", "error", err)
		return
	}
	rt.engine.Logger().Debug("post-analysis WAL checkpoint", "wal_frames", result.WALFrames,
		"checkpointed_frames", result.Checkpointed, "duration_ms", time.Since(started).Milliseconds())
}
