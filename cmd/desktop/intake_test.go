package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// uploadBytes POSTs in-memory recordings with extra multipart fields, sent
// after the files on purpose: the flags must work in any position.
func uploadBytes(t *testing.T, ts *httptest.Server, fields map[string]string, files map[string][]byte) analyzeResponse {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, data := range files {
		part, err := mw.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(data)
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	resp, err := http.Post(ts.URL+"/"+testToken+"/api/analyze", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out analyzeResponse
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil || len(out.Results) != len(files) {
		t.Fatalf("upload: HTTP %d %s", resp.StatusCode, raw)
	}
	return out
}

func fixtureBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// observerB is the fixture as another client recorded it: same session id,
// another recorder name in every raw tick.
func observerB(t *testing.T) []byte {
	t.Helper()
	const recorder = `"client_name":"Recorder"`
	data := fixtureBytes(t)
	if !bytes.Contains(data, []byte(recorder)) {
		t.Fatal("fixture no longer carries the recorder name this test rewrites")
	}
	return bytes.ReplaceAll(data, []byte(recorder), []byte(`"client_name":"ObserverB"`))
}

func storedRecorder(t *testing.T, s *server, matchID string) string {
	t.Helper()
	ticks, err := s.engine.Store().GetMatchRawTicks(context.Background(), matchID, 0, 0)
	if err != nil || ticks[0] == "" {
		t.Fatalf("raw tick 0: %v", err)
	}
	for _, name := range []string{"Recorder", "ObserverB"} {
		if strings.Contains(ticks[0], `"client_name":"`+name+`"`) {
			return name
		}
	}
	return "?"
}

// The desktop used to force every intake, so a second observer's recording of
// a stored match silently replaced its frames while the first observer's raw
// ticks stayed. The engine refuses the mix (internal/replay); this pins what
// the page is told. Mutation: drop the SourceDifferent branch of matchEntry
// and the refusal is rendered as "already analyzed" with the stored match.
func TestDesktopKeepsStoredMatchWhenAnotherRecordingOfItIsUploaded(t *testing.T) {
	s, ts := newTestServer(t)
	const matchID = "SYN-FIXTURE-001"
	first := uploadBytes(t, ts, nil, map[string][]byte{"observer-a.echoreplay": fixtureBytes(t)})
	if r := first.Results[0]; !r.OK || r.AlreadyAnalyzed || r.SHA256 == "" {
		t.Fatalf("first upload: %+v", r)
	}

	out := uploadBytes(t, ts, nil, map[string][]byte{"observer-b.echoreplay": observerB(t)})
	r := out.Results[0]
	if r.OK || !r.AlreadyStored || r.Match != nil || r.SourceStatus != "different" || r.SourceConflict == nil ||
		!strings.Contains(r.Error, "different recording") || !strings.Contains(r.Error, "was kept") {
		t.Fatalf("second observer: %+v", r)
	}
	if c := r.SourceConflict; c.MatchID != matchID || c.StoredSourceFile != "observer-a.echoreplay" || c.StoredRawTicks != 120 ||
		c.ReplaceField != "replace_source" || c.Detail == "" || c.Consequence == "" {
		t.Fatalf("conflict details: %+v", c)
	}
	if got := storedRecorder(t, s, matchID); got != "Recorder" {
		t.Fatalf("stored raw ticks now come from %s", got)
	}
	items, _ := s.runtime.queueSnapshot()
	if len(items) == 0 || items[0].Status != "failed" || !strings.Contains(items[0].Error, "different recording") {
		t.Fatalf("queue does not show the refusal: %+v", items)
	}
	if files := s.runtime.pendingFiles(); len(files) != 0 {
		t.Fatalf("refused upload stays queued for recovery: %v", files)
	}

	// force alone is a re-analysis of the same recording, never a replacement.
	out = uploadBytes(t, ts, map[string]string{"force": "1"}, map[string][]byte{"observer-b.echoreplay": observerB(t)})
	if r := out.Results[0]; r.OK || r.SourceConflict == nil || storedRecorder(t, s, matchID) != "Recorder" {
		t.Fatalf("force replaced a different recording: %+v", r)
	}

	// The explicit request replaces the whole recording, raw ticks included.
	out = uploadBytes(t, ts, map[string]string{"replace_source": "1"}, map[string][]byte{"observer-b.echoreplay": observerB(t)})
	r = out.Results[0]
	if !out.ReplaceSource || !r.OK || r.Match == nil || !r.Match.Replaced || r.Match.SourceFile != "observer-b.echoreplay" ||
		!strings.Contains(strings.Join(r.Match.Warnings, "|"), "replaced a different recording") {
		t.Fatalf("explicit replacement: %+v", r)
	}
	if got := storedRecorder(t, s, matchID); got != "ObserverB" {
		t.Fatalf("after replacement the raw ticks come from %s", got)
	}

	// The first file was remembered by its hash as "already analyzed". That
	// memory must not outlive the recording it described.
	out = uploadBytes(t, ts, nil, map[string][]byte{"observer-a.echoreplay": fixtureBytes(t)})
	if r := out.Results[0]; r.OK || r.AlreadyAnalyzed || r.SourceConflict == nil {
		t.Fatalf("hash index claims a replaced recording is still stored: %+v", r)
	}
}

// The same bytes are recognised without being parsed again. Mutation: drop
// the knownSource lookup and RawTickScans grows on the second upload.
func TestDesktopRecognisesTheSameFileWithoutParsingIt(t *testing.T) {
	s, ts := newTestServer(t)
	uploadBytes(t, ts, nil, map[string][]byte{"a.echoreplay": fixtureBytes(t)})
	scans := s.engine.Store().RawTickScans()
	runs, _ := s.engine.Store().ListAnalysisRuns(context.Background(), "SYN-FIXTURE-001", 10)

	out := uploadBytes(t, ts, nil, map[string][]byte{"renamed-copy.echoreplay": fixtureBytes(t)})
	r := out.Results[0]
	if !r.OK || !r.AlreadyAnalyzed || r.Match == nil || r.Match.Replaced || r.Match.SourceFile != "a.echoreplay" {
		t.Fatalf("same bytes: %+v", r)
	}
	if got := s.engine.Store().RawTickScans(); got != scans {
		t.Fatalf("recognising the same file scanned raw ticks %d more time(s)", got-scans)
	}
	if after, _ := s.engine.Store().ListAnalysisRuns(context.Background(), "SYN-FIXTURE-001", 10); len(after) != len(runs) {
		t.Fatalf("an already analyzed upload recorded an analysis run: %d -> %d", len(runs), len(after))
	}

	// A lost index only costs a parse: the engine's raw tick comparison decides.
	if err := os.Remove(s.runtime.sources.path); err != nil {
		t.Fatal(err)
	}
	s.runtime.sources = &sourceIndex{path: s.runtime.sources.path}
	out = uploadBytes(t, ts, nil, map[string][]byte{"again.echoreplay": fixtureBytes(t)})
	if r := out.Results[0]; !r.OK || !r.AlreadyAnalyzed || r.SourceStatus != "identical" {
		t.Fatalf("without the index: %+v", r)
	}
}

// An analysis from another detector configuration is refreshed by the same
// recording without being asked, and a cut-short stored copy is completed.
func TestDesktopRefreshesStaleOrTruncatedAnalysisOfTheSameRecording(t *testing.T) {
	s, ts := newTestServer(t)
	data := fixtureBytes(t)
	lines := bytes.SplitAfter(data, []byte("\n"))
	short := bytes.Join(lines[:50], nil)
	if r := uploadBytes(t, ts, nil, map[string][]byte{"short.echoreplay": short}).Results[0]; !r.OK || r.Match.FramesProcessed != 50 {
		t.Fatalf("short upload: %+v", r)
	}
	r := uploadBytes(t, ts, nil, map[string][]byte{"full.echoreplay": data}).Results[0]
	if !r.OK || r.AlreadyAnalyzed || !r.Match.Replaced || r.Match.FramesProcessed != 120 || r.SourceStatus != "extends" {
		t.Fatalf("full recording over its truncated copy: %+v", r)
	}
	// The short copy can never take the full recording's place.
	if r := uploadBytes(t, ts, nil, map[string][]byte{"short.echoreplay": short}).Results[0]; r.OK || r.SourceConflict == nil {
		t.Fatalf("short copy over the full recording: %+v", r)
	}

	s.engine.Config().Scoring.ReviewThreshold++
	r = uploadBytes(t, ts, nil, map[string][]byte{"full.echoreplay": data}).Results[0]
	if !r.OK || r.AlreadyAnalyzed || !r.Match.Replaced {
		t.Fatalf("stale analysis was not refreshed: %+v", r)
	}
	r = uploadBytes(t, ts, nil, map[string][]byte{"full.echoreplay": data}).Results[0]
	if !r.OK || !r.AlreadyAnalyzed {
		t.Fatalf("current analysis was re-run: %+v", r)
	}
}

// An upload is spooled as "<name>.part" and only renamed once complete, so a
// process killed mid-upload leaves nothing crash recovery would analyze.
// Mutation: spool straight to the final name and the mid-upload check fails.
func TestUploadIsInvisibleToRecoveryUntilItIsComplete(t *testing.T) {
	s, ts := newTestServer(t)
	data := fixtureBytes(t)
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	half := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	finishUpload := func() { releaseOnce.Do(func() { close(release) }) }
	go func() {
		part, _ := mw.CreateFormFile("files", "slow.echoreplay")
		_, _ = part.Write(data[:len(data)/2])
		close(half)
		<-release
		_, _ = part.Write(data[len(data)/2:])
		_ = mw.Close()
		_ = pw.Close()
	}()
	done := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Post(ts.URL+"/"+testToken+"/api/analyze", mw.FormDataContentType(), pr)
		if err != nil {
			t.Error(err)
		}
		done <- resp
	}()
	answered := false
	defer func() { // a failed assertion must not leave the request open
		finishUpload()
		if !answered {
			if resp := <-done; resp != nil {
				resp.Body.Close()
			}
		}
	}()
	<-half
	deadline := time.Now().Add(5 * time.Second)
	var partial []string
	for len(partial) == 0 && time.Now().Before(deadline) {
		partial, _ = filepath.Glob(filepath.Join(s.runtime.pendingDir, "upload-*", "*", "*"+partialSpoolSuffix))
		time.Sleep(10 * time.Millisecond)
	}
	if len(partial) != 1 {
		t.Fatalf("no partial spool while uploading: %v", partial)
	}
	if files := s.runtime.pendingFiles(); len(files) != 0 {
		t.Fatalf("a half-written upload is visible to crash recovery: %v", files)
	}
	finishUpload()
	resp := <-done
	answered = true
	if resp != nil {
		var out analyzeResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if len(out.Results) != 1 || !out.Results[0].OK || out.Results[0].Match.FramesProcessed != 120 {
			t.Fatalf("completed upload: %+v", out.Results)
		}
	}
}

// What a killed process leaves behind is deleted at the next launch and never
// stored as a match. Mutation: drop removePartialSpools from resumePending.
func TestRecoveryDeletesInterruptedUploadsInsteadOfAnalyzingThem(t *testing.T) {
	s, _ := newTestServer(t)
	data := fixtureBytes(t)
	dir := filepath.Join(s.runtime.pendingDir, "upload-crashed", "0")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(dir, "match.echoreplay"+partialSpoolSuffix)
	if err := os.WriteFile(partial, data[:len(data)*5/12], 0o600); err != nil {
		t.Fatal(err)
	}
	s.runtime.resumePending(t.Context())
	if exists, err := s.engine.Store().HasMatch(t.Context(), "SYN-FIXTURE-001"); err != nil || exists {
		t.Fatalf("a half-written upload was stored as a match: %v %v", exists, err)
	}
	if _, err := os.Stat(filepath.Join(s.runtime.pendingDir, "upload-crashed")); !os.IsNotExist(err) {
		t.Fatalf("interrupted upload was not cleaned up: %v", err)
	}
}

// Crash recovery and the watch folder follow the same policy as an upload.
func TestRecoveryNeverReplacesAStoredMatchWithAnotherRecording(t *testing.T) {
	s, ts := newTestServer(t)
	uploadBytes(t, ts, nil, map[string][]byte{"observer-a.echoreplay": fixtureBytes(t)})
	pending := filepath.Join(s.runtime.pendingDir, "upload-b.echoreplay")
	if err := os.WriteFile(pending, observerB(t), 0o600); err != nil {
		t.Fatal(err)
	}
	s.runtime.resumePending(t.Context())
	if got := storedRecorder(t, s, "SYN-FIXTURE-001"); got != "Recorder" {
		t.Fatalf("recovery replaced the stored recording with %s", got)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("refused recording stays in the recovery queue: %v", err)
	}
	s.runtime.mu.Lock()
	msg := s.runtime.recoveryErr
	s.runtime.mu.Unlock()
	if !strings.Contains(msg, "different recording") {
		t.Fatalf("recovery did not report the refusal: %q", msg)
	}

	watch := t.TempDir()
	file := filepath.Join(watch, "observer-b.echoreplay")
	if err := os.WriteFile(file, observerB(t), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(file, old, old)
	s.runtime.mu.Lock()
	s.runtime.settings.WatchFolder = watch
	s.runtime.mu.Unlock()
	if _, err := s.runtime.scanWatchFolder(t.Context()); err == nil || !strings.Contains(err.Error(), "different recording") {
		t.Fatalf("watch folder scan: %v", err)
	}
	if got := storedRecorder(t, s, "SYN-FIXTURE-001"); got != "Recorder" {
		t.Fatalf("watch folder replaced the stored recording with %s", got)
	}
}
