package main

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestStoredMatchReopenTiming is an opt-in local measurement, not a pass/fail
// gate: set NEVR_PERF_REPLAY to a large recording (for example the synthetic
// fixture looped to ~36,000 frames) and NEVR_PERF_MATCH to its match id. It
// prints how long each stored-match route takes after the analysis, which is
// what a moderator waits for when reopening a match from History.
func TestStoredMatchReopenTiming(t *testing.T) {
	path, matchID := os.Getenv("NEVR_PERF_REPLAY"), os.Getenv("NEVR_PERF_MATCH")
	if path == "" || matchID == "" {
		t.Skip("set NEVR_PERF_REPLAY and NEVR_PERF_MATCH to measure stored-match reopen time")
	}
	_, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	started := time.Now()
	resp, out := upload(t, ts, false, map[string]string{"perf.echoreplay": path})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("upload status=%d", resp.StatusCode)
	}
	t.Logf("analyze+store: %s (frames=%d)", time.Since(started).Round(time.Millisecond), out.Results[0].Match.FramesProcessed)
	for _, route := range []string{"", "", "/investigation", "/report", "/summary.json", "/export.csv"} {
		started := time.Now()
		resp, err := http.Get(base + "/api/match/" + matchID + route)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %q status=%d", route, resp.StatusCode)
		}
		t.Logf("GET api/match/{id}%-15s %8s  %d bytes", route, time.Since(started).Round(time.Millisecond), n)
	}
}
