package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
)

func TestAnalysisFailureIsPermanent(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()
	persistFailed := []*replay.AnalyzeResult{{SummaryErr: errors.New("disk full")}}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		results []*replay.AnalyzeResult
		err     error
		want    bool
	}{
		{"garbage recording", live, nil, errors.New("reading echoreplay: line 1: no tab separator"), true},
		{"invalid native tape", live, nil, errors.New("reading native tape: bad footer"), true},
		{"legacy json without match id", live, nil, errors.New("replay has no match id"), true},
		{"unparseable legacy json", live, nil, fmt.Errorf("reading replay: %w", errors.New("unexpected end of JSON input")), true},
		{"empty recording", live, nil, nil, true},
		{"source conflict", live, nil, errors.New("source conflict: this session belongs to a different native capture; existing evidence was not replaced"), true},
		{"returning session", live, nil, errors.New(`session "A" comes back after another session at 2026/01/01 00:00:00.000; its frames would collide with the match already stored under that id`), true},
		{"later match of a file is corrupt", live, []*replay.AnalyzeResult{{}}, errors.New("reading echoreplay: truncated"), true},

		{"user cancelled", cancelled, nil, context.Canceled, false},
		{"cancelled while parsing", cancelled, nil, errors.New("reading echoreplay: context canceled"), false},
		{"wrapped cancellation", live, nil, fmt.Errorf("reading echoreplay: %w", context.Canceled), false},
		{"deadline", live, nil, context.DeadlineExceeded, false},
		{"storage failure on a result", live, persistFailed, nil, false},
		{"storage failure plus parse error", live, persistFailed, errors.New("reading echoreplay: truncated"), false},
		{"spool unreadable", live, nil, fmt.Errorf("reading replay: %w", &fs.PathError{Op: "open", Path: "x", Err: syscall.EACCES}), false},
		{"os error while reading", live, nil, fmt.Errorf("reading echoreplay: %w", syscall.EIO), false},
		{"database busy", live, nil, errors.New("database is locked"), false},
		{"staging evidence failed", live, nil, errors.New("stage original replay evidence: no space left on device"), false},
		{"unknown pipeline failure", live, nil, errors.New("detector panic"), false},
		{"success", live, []*replay.AnalyzeResult{{}}, nil, false},
	} {
		if got := analysisFailureIsPermanent(tc.ctx, tc.results, tc.err); got != tc.want {
			t.Errorf("%s: permanent=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func pendingCount(t *testing.T, base string) int {
	t.Helper()
	var recovery struct {
		Pending int `json:"pending"`
	}
	if resp := getJSON(t, base+"/api/recovery", &recovery); resp.StatusCode != http.StatusOK {
		t.Fatalf("recovery status=%d", resp.StatusCode)
	}
	return recovery.Pending
}

// Garbage and a session JSON without a match id can never be analyzed: they
// must not stay in the crash-recovery queue to fail again at every launch.
func TestUnparseableUploadsLeaveNothingPending(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.echoreplay")
	noMatch := filepath.Join(dir, "session.json")
	for path, content := range map[string]string{garbage: "this is not a replay\n", noMatch: `{"general": {}}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resp, out := upload(t, ts, false, map[string]string{"garbage.echoreplay": garbage, "session.json": noMatch})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 2 {
		t.Fatalf("status=%d results=%+v", resp.StatusCode, out.Results)
	}
	for _, result := range out.Results {
		if result.OK || result.Error == "" {
			t.Fatalf("bad upload was not reported as failed: %+v", result)
		}
	}
	if pending := pendingCount(t, base); pending != 0 {
		t.Fatalf("unparseable uploads left %d file(s) in the recovery queue", pending)
	}
	if entries, err := os.ReadDir(s.runtime.pendingDir); err != nil || len(entries) != 0 {
		t.Fatalf("recovery queue not empty: %v %v", entries, err)
	}
}

// An upload whose analysis was cancelled (here: by shutdown arriving while the
// file was still uploading) is exactly what crash recovery exists for.
func TestCancelledUploadStaysPending(t *testing.T) {
	s, ts := newTestServer(t)
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	wrote := make(chan error, 1)
	release := make(chan struct{})
	go func() {
		part, err := mw.CreateFormFile("files", "cancelled.echoreplay")
		if err == nil {
			_, err = part.Write(data[:len(data)/2])
		}
		<-release
		if err == nil {
			_, err = part.Write(data[len(data)/2:])
		}
		if err == nil {
			err = mw.Close()
		}
		wrote <- errors.Join(err, pw.Close())
	}()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/"+testToken+"/api/analyze", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	type outcome struct {
		resp *http.Response
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		done <- outcome{resp, err}
	}()

	// The handler is past its shutdown check once its spool directory exists.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if entries, _ := os.ReadDir(s.runtime.pendingDir); len(entries) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("upload never reached the durable queue")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.quitOnce.Do(func() { close(s.quit) })
	close(release)
	if err := <-wrote; err != nil {
		t.Fatalf("writing upload: %v", err)
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer result.resp.Body.Close()
	var out analyzeResponse
	if err := json.NewDecoder(result.resp.Body).Decode(&out); err != nil || len(out.Results) != 1 {
		t.Fatalf("status=%d decode=%v results=%+v", result.resp.StatusCode, err, out.Results)
	}
	if out.Results[0].OK || out.Results[0].Error != "analysis cancelled" {
		t.Fatalf("result = %+v, want a cancelled analysis", out.Results[0])
	}
	if files := s.runtime.pendingFiles(); len(files) != 1 {
		t.Fatalf("cancelled upload was not kept for recovery: %v", files)
	}
}

// Crash recovery drops a copy that can never be analyzed, says so once, and
// keeps going with the rest of the queue.
func TestRecoveryDiscardsPermanentlyUnreadablePendingUpload(t *testing.T) {
	s, _ := newTestServer(t)
	badDir := filepath.Join(s.runtime.pendingDir, "upload-bad")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(badDir, "bad.echoreplay")
	if err := os.WriteFile(bad, []byte("not a replay"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(s.runtime.pendingDir, "upload-good.echoreplay")
	if err := os.WriteFile(good, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s.runtime.resumePending(t.Context())
	if files := s.runtime.pendingFiles(); len(files) != 0 {
		t.Fatalf("recovery left files pending: %v", files)
	}
	if _, err := os.Stat(badDir); !os.IsNotExist(err) {
		t.Fatalf("empty job directory was kept: %v", err)
	}
	s.runtime.mu.Lock()
	recovered, recoveryErr := s.runtime.recovered, s.runtime.recoveryErr
	s.runtime.mu.Unlock()
	if recovered != 1 || recoveryErr == "" {
		t.Fatalf("recovered=%d error=%q; want the good replay recovered and the discard reported", recovered, recoveryErr)
	}
}

// One import must not leave a write-ahead log beside the database until the
// moderator finds the maintenance button.
func TestAnalysisCheckpointsTheWAL(t *testing.T) {
	s, ts := newTestServer(t)
	if resp, out := upload(t, ts, false, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != http.StatusOK || !out.Results[0].OK {
		t.Fatalf("upload status=%d out=%+v", resp.StatusCode, out)
	}
	wal := s.engine.Store().Path() + "-wal"
	info, err := os.Stat(wal)
	if err != nil {
		t.Fatalf("WAL file: %v", err)
	}
	// Without the checkpoint this fixture leaves a WAL of about 2.8 MB. Allow a
	// few pages in case a background writer ran between the two.
	if info.Size() > 256<<10 {
		t.Fatalf("WAL is %d bytes after an analysis; the import was not checkpointed", info.Size())
	}
}
