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
// fixture looped to ~36,000 frames). It prints how long each stored-match
// route takes after the analysis, which is what a moderator waits for when
// reopening a match from History. It never prints the match id or any player
// data, so it is safe to point at a private recording.
func TestStoredMatchReopenTiming(t *testing.T) {
	path := os.Getenv("NEVR_PERF_REPLAY")
	if path == "" {
		t.Skip("set NEVR_PERF_REPLAY to measure stored-match reopen time")
	}
	_, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	started := time.Now()
	resp, out := upload(t, ts, false, map[string]string{"perf.echoreplay": path})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("upload status=%d", resp.StatusCode)
	}
	matchID := out.Results[0].MatchID
	t.Logf("analyze+store: %s (frames=%d, players=%d, events=%d)", time.Since(started).Round(time.Millisecond),
		out.Results[0].Match.FramesProcessed, len(out.Results[0].Match.Players), len(out.Results[0].Match.Events))
	for _, route := range []string{"", "", "/investigation", "/investigation", "/report", "/summary.json", "/export.csv"} {
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
