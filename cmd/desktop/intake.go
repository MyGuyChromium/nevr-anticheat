package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
)

// Intake policy of the desktop app, shared by uploads, the watch folder and
// crash recovery.
//
// A session id names a match, not a recording: two observers, a late joiner
// and a clip all carry the same id. So a file whose match is already stored is
// never analyzed blindly:
//
//   - the same recording with a current analysis is "already analyzed": the
//     stored match is shown and nothing is written;
//   - the same recording is re-analyzed when that is asked for (force), when
//     the stored copy was cut short and this file completes it, or when the
//     stored analysis came from another build or detector configuration;
//   - a different recording never replaces or mixes into the stored match. The
//     stored analysis is kept and the conflict is reported, unless the request
//     explicitly asks to replace the stored recording (replace_source).

const (
	sourceIndexFileName   = "nevr-desktop-source-index.json"
	sourceIndexSchema     = "nevr-desktop-source-index/v1"
	maxSourceIndexEntries = 4000
)

// sourceIndexEntry remembers which matches one exact file (by SHA-256) holds,
// so the same file is recognised without parsing it again. It is a cache of
// what the engine's raw-tick comparison established, never evidence: an entry
// is trusted only while every match is still stored with the same number of
// raw ticks and normalized frames it had when the entry was written.
type sourceIndexEntry struct {
	Matches    []sourceIndexMatch `json:"matches"`
	RecordedAt string             `json:"recorded_at"`
}

type sourceIndexMatch struct {
	MatchID  string `json:"match_id"`
	RawTicks int    `json:"raw_ticks"`
	Frames   int    `json:"frames"`
}

type sourceIndex struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	entries map[string]sourceIndexEntry
}

func (x *sourceIndex) loadLocked() {
	if x.loaded {
		return
	}
	x.loaded, x.entries = true, make(map[string]sourceIndexEntry)
	doc, err := os.ReadFile(x.path)
	if err != nil || len(doc) > 16<<20 {
		return
	}
	var file struct {
		Schema  string                      `json:"schema"`
		Entries map[string]sourceIndexEntry `json:"entries"`
	}
	if json.Unmarshal(doc, &file) != nil || file.Schema != sourceIndexSchema || file.Entries == nil {
		return // A damaged cache only costs one parse per file.
	}
	x.entries = file.Entries
}

func (x *sourceIndex) lookup(sum string) (sourceIndexEntry, bool) {
	if x == nil || sum == "" {
		return sourceIndexEntry{}, false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.loadLocked()
	entry, ok := x.entries[sum]
	return entry, ok
}

func (x *sourceIndex) forget(sum string) {
	if x == nil || sum == "" {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.loadLocked()
	if _, ok := x.entries[sum]; ok {
		delete(x.entries, sum)
		_ = x.saveLocked()
	}
}

// forgetMatch drops every file that was recorded as holding matchID: its
// stored recording was just replaced by another one.
func (x *sourceIndex) forgetMatch(matchID string) {
	if x == nil || matchID == "" {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.loadLocked()
	changed := false
	for sum, entry := range x.entries {
		for _, m := range entry.Matches {
			if m.MatchID == matchID {
				delete(x.entries, sum)
				changed = true
				break
			}
		}
	}
	if changed {
		_ = x.saveLocked()
	}
}

func (x *sourceIndex) remember(sum string, entry sourceIndexEntry) error {
	if x == nil || sum == "" || len(entry.Matches) == 0 {
		return nil
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.loadLocked()
	x.entries[sum] = entry
	if len(x.entries) > maxSourceIndexEntries {
		sums := make([]string, 0, len(x.entries))
		for s := range x.entries {
			sums = append(sums, s)
		}
		sort.Slice(sums, func(i, j int) bool { return x.entries[sums[i]].RecordedAt < x.entries[sums[j]].RecordedAt })
		for _, s := range sums[:len(sums)-maxSourceIndexEntries] {
			delete(x.entries, s)
		}
	}
	return x.saveLocked()
}

func (x *sourceIndex) saveLocked() error {
	doc, err := json.Marshal(map[string]any{"schema": sourceIndexSchema, "entries": x.entries})
	if err != nil {
		return err
	}
	tmp := x.path + ".tmp"
	if err := os.WriteFile(tmp, doc, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, x.path)
}

// recordingSHA256 hashes a recording that was not uploaded through the page (watch
// folder, crash recovery); uploads are hashed while they are spooled.
func recordingSHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- the caller chose this recording; it is only read.
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		h.Write(buf[:n])
		if errors.Is(readErr, io.EOF) {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if readErr != nil {
			return "", readErr
		}
	}
}

// intakeRequest is what the caller asked for. The zero value is the default:
// analyze new matches, recognise stored ones, change nothing else.
type intakeRequest struct {
	Force         bool // re-analyze the same recording even if its analysis is current
	ReplaceSource bool // let a different recording replace the stored one (implies Force)
}

// intakeOutcome is what happened to one file.
type intakeOutcome struct {
	Results []*replay.AnalyzeResult
	Err     error
	// Instant is true when the file was recognised by its hash and not parsed.
	Instant bool
	// Refreshed names why a stored match of the same recording was re-analyzed
	// without being asked ("" when it was not): "completed" or "stale".
	Refreshed string
}

// conflicts lists the matches of this file that were refused because a
// different recording is stored under their id.
func (o intakeOutcome) conflicts() []*replay.AnalyzeResult {
	var out []*replay.AnalyzeResult
	for _, res := range o.Results {
		if res != nil && res.AlreadyStored && res.StoredSource == replay.SourceDifferent {
			out = append(out, res)
		}
	}
	return out
}

// conflictError renders the refused matches as one error for the queue, the
// watch folder status and the recovery status. nil when nothing was refused.
func (o intakeOutcome) conflictError() error {
	refused := o.conflicts()
	if len(refused) == 0 {
		return nil
	}
	first := refused[0]
	return fmt.Errorf("kept the stored analysis of match %s: this file is a different recording of it (%s)",
		first.MatchCtx.MatchID, first.SourceDetail)
}

// analysisIsStale reports whether the stored analysis of a match was produced
// by another build or detector configuration than the running one. A match
// without a recorded run (analyzed by the CLI, or before runs were recorded)
// counts as stale: nothing says it matches this build.
func (rt *desktopRuntime) analysisIsStale(ctx context.Context, matchID string) bool {
	runs, err := rt.engine.Store().ListAnalysisRuns(ctx, matchID, 1)
	if err != nil || len(runs) != 1 {
		return true
	}
	run := runs[0]
	return run.AppVersion != appVersion || run.BuildCommit != analysisBuildRevision() ||
		run.ConfigFingerprint != displayConfigFingerprint(rt.engine.Config())
}

// knownSource answers from the index alone: the stored matches of a file that
// was analyzed before, or false when anything about the entry no longer holds.
func (rt *desktopRuntime) knownSource(ctx context.Context, path, sum string) ([]*replay.AnalyzeResult, bool) {
	entry, ok := rt.sources.lookup(sum)
	if !ok || len(entry.Matches) == 0 {
		return nil, false
	}
	store := rt.engine.Store()
	results := make([]*replay.AnalyzeResult, 0, len(entry.Matches))
	for _, m := range entry.Matches {
		exists, err := store.HasMatchContext(ctx, m.MatchID)
		if err != nil {
			return nil, false
		}
		ticks, frames, err := store.GetMatchStorageCounts(ctx, m.MatchID)
		// Archiving removes the raw ticks of a match on purpose; that alone does
		// not make it another recording. Any other difference does.
		if err != nil || !exists || frames != m.Frames || (ticks != m.RawTicks && ticks != 0) || rt.analysisIsStale(ctx, m.MatchID) {
			rt.sources.forget(sum)
			return nil, false
		}
		mc, err := store.GetMatchContext(ctx, m.MatchID)
		if err != nil || mc == nil {
			return nil, false
		}
		results = append(results, &replay.AnalyzeResult{Path: path, MatchCtx: mc, AlreadyStored: true,
			StoredSource: replay.SourceIdentical,
			SourceDetail: "this exact file (SHA-256 " + sum[:12] + "…) was analyzed before"})
	}
	return results, true
}

// rememberSource records the file in the index once every match of it is
// stored from this very recording.
func (rt *desktopRuntime) rememberSource(ctx context.Context, sum string, results []*replay.AnalyzeResult) {
	if sum == "" || len(results) == 0 {
		return
	}
	entry := sourceIndexEntry{RecordedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, res := range results {
		if res == nil || res.MatchCtx == nil {
			return
		}
		switch {
		case res.AlreadyStored && res.StoredSource == replay.SourceIdentical:
		case !res.AlreadyStored && res.Result != nil && res.PersistError() == nil:
		default:
			return
		}
		ticks, frames, err := rt.engine.Store().GetMatchStorageCounts(ctx, res.MatchCtx.MatchID)
		if err != nil {
			return
		}
		entry.Matches = append(entry.Matches, sourceIndexMatch{MatchID: res.MatchCtx.MatchID, RawTicks: ticks, Frames: frames})
	}
	if err := rt.sources.remember(sum, entry); err != nil {
		rt.engine.Logger().Warn("could not update the analyzed-recordings index", "error", err)
	}
}

// analyzeRecording applies the intake policy to one file. sum is the file's
// SHA-256 when the caller already has it ("" to compute it here). The caller
// holds analyzeMu and records provenance for the returned results.
func (rt *desktopRuntime) analyzeRecording(ctx context.Context, path, sum string, req intakeRequest) intakeOutcome {
	if req.ReplaceSource {
		req.Force = true
	}
	if sum == "" {
		// Best effort: without a hash the file is simply parsed and compared.
		sum, _ = recordingSHA256(ctx, path)
	}
	if !req.Force {
		if results, ok := rt.knownSource(ctx, path, sum); ok {
			return intakeOutcome{Results: results, Instant: true}
		}
	}
	out := intakeOutcome{}
	out.Results, out.Err = rt.engine.AnalyzeFileAllWith(ctx, path, req.Force, req.ReplaceSource)
	if out.Err == nil && !req.Force {
		for _, res := range out.Results {
			if res == nil || !res.AlreadyStored || res.StoredSource == replay.SourceDifferent || res.MatchCtx == nil {
				continue
			}
			if res.StoredSource == replay.SourceExtends {
				out.Refreshed = "completed"
				break
			}
			if rt.analysisIsStale(ctx, res.MatchCtx.MatchID) {
				out.Refreshed = "stale"
			}
		}
		if out.Refreshed != "" {
			// The same recording, so nothing can be mixed: the engine compares
			// again and still refuses any match of this file that differs.
			out.Results, out.Err = rt.engine.AnalyzeFileAllWith(ctx, path, true, false)
		}
	}
	for _, res := range out.Results {
		if res != nil && res.SourceReplaced && res.MatchCtx != nil {
			rt.sources.forgetMatch(res.MatchCtx.MatchID)
		}
	}
	if out.Err == nil {
		rt.rememberSource(ctx, sum, out.Results)
	}
	return out
}

// formFlag reads a boolean form or query value the way the page sends it.
func formFlag(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// partialSpoolSuffix marks an upload that is still being written. Crash
// recovery only ever analyzes files under their final name, which they get by
// an atomic rename after the last byte was synced.
const partialSpoolSuffix = ".part"

// removePartialSpools deletes uploads that never finished (the process was
// killed or the machine lost power mid-upload). The caller holds analyzeMu, so
// no upload is being written while this runs.
func removePartialSpools(pendingDir string) (removed int) {
	_ = filepath.WalkDir(pendingDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), partialSpoolSuffix) {
			return nil
		}
		if os.Remove(path) == nil {
			removed++
			if dir := filepath.Dir(path); filepath.Clean(dir) != filepath.Clean(pendingDir) {
				_ = os.Remove(dir) // only when empty
				if parent := filepath.Dir(dir); filepath.Clean(parent) != filepath.Clean(pendingDir) {
					_ = os.Remove(parent)
				}
			}
		}
		return nil
	})
	return removed
}
