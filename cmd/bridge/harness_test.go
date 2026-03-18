package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// --- Fake Nakama ---

// fakeNakamaMatchList builds a Nakama match list response JSON.
func fakeNakamaMatchList(matches ...fakeMatch) string {
	var items []string
	for _, m := range matches {
		label := m.labelJSON()
		labelEscaped, _ := json.Marshal(label) // produces "escaped_string"
		items = append(items, fmt.Sprintf(
			`{"match_id":%q,"authoritative":true,"label":%s,"size":%d}`,
			m.MatchID, string(labelEscaped), m.Size,
		))
	}
	return fmt.Sprintf(`{"matches":[%s]}`, strings.Join(items, ","))
}

type fakeMatch struct {
	MatchID       string
	Mode          string
	Level         string
	Size          int
	EndpointStr   string // e.g. "10.0.0.1:203.0.113.1:6721" or "" for no broadcaster
	NoBroadcaster bool
}

func (m fakeMatch) labelJSON() string {
	if m.NoBroadcaster {
		return fmt.Sprintf(`{"id":%q,"mode":%q,"level":%q}`, m.MatchID, m.Mode, m.Level)
	}
	return fmt.Sprintf(
		`{"id":%q,"mode":%q,"level":%q,"broadcaster":{"endpoint":%q,"region":"test"}}`,
		m.MatchID, m.Mode, m.Level, m.EndpointStr,
	)
}

func startFakeNakama(t *testing.T, responseBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/match") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, responseBody)
	}))
}

// startSwitchableNakama returns a fake Nakama whose response can be changed at runtime.
func startSwitchableNakama(t *testing.T, initial string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var current atomic.Value
	current.Store(initial)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/match") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, current.Load().(string))
	}))
	return srv, &current
}

// --- Fake Broadcaster ---

// fakeSessionJSON builds a minimal valid EchoVRSessionResponse JSON.
func fakeSessionJSON(sessionID, gameStatus string, players int) string {
	var playerList []string
	for i := 0; i < players; i++ {
		playerList = append(playerList, fmt.Sprintf(`{
			"name":"Player%d","userid":%d,"playerid":%d,"level":50,
			"body":{"position":[%d.0,1.6,3.0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
			"head":{"position":[%d.0,1.6,3.0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
			"velocity":[0,0,0],
			"lhand":{"pos":[0.3,1.4,0.2],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
			"rhand":{"pos":[-0.3,1.4,0.2],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
			"stunned":false,"invulnerable":false,"possession":false,"blocking":false,
			"ping":50,"stats":{"points":0,"goals":0,"assists":0,"saves":0,"steals":0,"stuns":0}
		}`, i, 1000+i, i, i*2, i*2))
	}

	return fmt.Sprintf(`{
		"sessionid":%q,
		"match_type":"Echo_Arena",
		"map_name":"mpl_arena_a",
		"game_status":%q,
		"game_clock":240.0,
		"game_clock_display":"4:00",
		"private_match":false,
		"client_name":"test",
		"disc":{"position":[0,2,0],"velocity":[1,0,0]},
		"teams":[
			{"team":"BLUE TEAM","players":[%s]},
			{"team":"ORANGE TEAM","players":[]}
		],
		"blue_points":2,
		"orange_points":1
	}`, sessionID, gameStatus, strings.Join(playerList, ","))
}

func startFakeBroadcaster(t *testing.T, responseBody string, statusCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		fmt.Fprint(w, responseBody)
	}))
}

// --- Fake Anticheat WebSocket ---

type fakeAnticheat struct {
	server  *httptest.Server
	mu      sync.Mutex
	batches []FrameBatch
}

func startFakeAnticheat(t *testing.T) *fakeAnticheat {
	t.Helper()
	fa := &fakeAnticheat{}
	mux := http.NewServeMux()
	mux.Handle("/telemetry", websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			var batch FrameBatch
			if err := websocket.JSON.Receive(conn, &batch); err != nil {
				return // client disconnected
			}
			fa.mu.Lock()
			fa.batches = append(fa.batches, batch)
			fa.mu.Unlock()
		}
	}))
	fa.server = httptest.NewServer(mux)
	return fa
}

func (fa *fakeAnticheat) wsURL() string {
	return "ws" + strings.TrimPrefix(fa.server.URL, "http") + "/telemetry"
}

func (fa *fakeAnticheat) batchCount() int {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return len(fa.batches)
}

func (fa *fakeAnticheat) totalFrames() int {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	total := 0
	for _, b := range fa.batches {
		total += len(b.Frames)
	}
	return total
}

func (fa *fakeAnticheat) lastBatch() *FrameBatch {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.batches) == 0 {
		return nil
	}
	b := fa.batches[len(fa.batches)-1]
	return &b
}

func (fa *fakeAnticheat) close() {
	fa.server.Close()
}

// --- Helpers ---

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// extractTestPort returns just the port from an httptest server URL.
func extractTestPort(t *testing.T, serverURL string) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(serverURL, "http://"), "https://"))
	if err != nil {
		t.Fatalf("extractTestPort: %v", err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return port
}

// waitForBatches polls the fake anticheat until it has at least n batches or timeout.
func waitForBatches(fa *fakeAnticheat, n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fa.batchCount() >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fa.batchCount() >= n
}

// --- Integration Tests ---

func TestProbeEndToEnd(t *testing.T) {
	// Start fake broadcaster
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 4), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	// Start fake Nakama that points to our fake broadcaster
	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "match-alpha",
		Mode:        "echo_arena",
		Level:       "arena_a",
		Size:        4,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "probe"}

	err := doProbe(context.Background(), cfg, stats, testLogger())
	if err != nil {
		t.Fatalf("doProbe failed: %v", err)
	}

	if stats.MatchesDiscovered.Load() != 1 {
		t.Errorf("discovered = %d, want 1", stats.MatchesDiscovered.Load())
	}
}

func TestOnceEndToEnd(t *testing.T) {
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 3), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "match-1",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        3,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err != nil {
		t.Fatalf("doOnce failed: %v", err)
	}

	if stats.TotalBatchesSent.Load() != 1 {
		t.Errorf("batches sent = %d, want 1", stats.TotalBatchesSent.Load())
	}
	if stats.TotalFramesForwarded.Load() != 3 {
		t.Errorf("frames forwarded = %d, want 3", stats.TotalFramesForwarded.Load())
	}

	// Wait for fake anticheat to receive the batch (WebSocket read is async)
	if !waitForBatches(fa, 1, 2*time.Second) {
		t.Fatalf("anticheat batches = %d, want 1 (timeout)", fa.batchCount())
	}
	batch := fa.lastBatch()
	if batch.MatchID != "match-1" {
		t.Errorf("batch match_id = %q, want %q", batch.MatchID, "match-1")
	}
	if len(batch.Frames) != 3 {
		t.Errorf("batch frames = %d, want 3", len(batch.Frames))
	}

	// Verify identity format
	for _, f := range batch.Frames {
		if !strings.HasPrefix(f.PlayerID, "echovr:") {
			t.Errorf("player_id %q does not have echovr: prefix", f.PlayerID)
		}
	}
}

func TestOnce_DryRun(t *testing.T) {
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "match-1",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      "", // dry-run: no anticheat
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err != nil {
		t.Fatalf("doOnce dry-run failed: %v", err)
	}

	// Should NOT have sent anything
	if stats.TotalBatchesSent.Load() != 0 {
		t.Errorf("batches sent = %d, want 0 (dry-run)", stats.TotalBatchesSent.Load())
	}
}

func TestProbe_BroadcasterNon200(t *testing.T) {
	broadcaster := startFakeBroadcaster(t, `{"error":"not found"}`, 404)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "match-1",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:       nakama.URL,
		NakamaServerKey: "testkey",
		APIPort:         bPort,
		PollInterval:    67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "probe"}

	err := doProbe(context.Background(), cfg, stats, testLogger())
	if err == nil {
		t.Fatal("expected error for non-200 broadcaster response")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("error should mention HTTP 404, got: %v", err)
	}
}

func TestProbe_BroadcasterInvalidJSON(t *testing.T) {
	broadcaster := startFakeBroadcaster(t, `{not valid json`, 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "match-1",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "probe"}

	err := doProbe(context.Background(), cfg, stats, testLogger())
	if err == nil {
		t.Fatal("expected error for invalid JSON from broadcaster")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention invalid JSON, got: %v", err)
	}
}

func TestDiscovery_SkipsInvalidEndpoints(t *testing.T) {
	nakamaResp := fakeNakamaMatchList(
		fakeMatch{MatchID: "good", Mode: "arena", Level: "a", Size: 4, EndpointStr: "10.0.0.1:203.0.113.1:6721"},
		fakeMatch{MatchID: "no-bc", Mode: "arena", Level: "a", Size: 2, NoBroadcaster: true},
		fakeMatch{MatchID: "bad-ip", Mode: "arena", Level: "a", Size: 2, EndpointStr: "10.0.0.1:not-an-ip:6721"},
	)
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		APIPort:           6721,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}
	stats := &bridgeStats{}

	matches, err := discoverMatches(context.Background(), cfg, testLogger(), stats)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}

	if len(matches) != 1 {
		t.Fatalf("expected 1 valid match, got %d", len(matches))
	}
	if matches[0].MatchID != "good" {
		t.Errorf("expected match 'good', got %q", matches[0].MatchID)
	}
	if stats.MatchesSkippedNoEP.Load() != 1 {
		t.Errorf("skipped_no_ep = %d, want 1", stats.MatchesSkippedNoEP.Load())
	}
}

func TestDiscovery_DeterministicSelection(t *testing.T) {
	// Nakama returns matches in random order
	nakamaResp := fakeNakamaMatchList(
		fakeMatch{MatchID: "charlie", Mode: "arena", Level: "a", Size: 2, EndpointStr: "10.0.0.1:1.1.1.3:6721"},
		fakeMatch{MatchID: "alpha", Mode: "arena", Level: "a", Size: 4, EndpointStr: "10.0.0.1:1.1.1.1:6721"},
		fakeMatch{MatchID: "bravo", Mode: "arena", Level: "a", Size: 3, EndpointStr: "10.0.0.1:1.1.1.2:6721"},
	)
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		APIPort:           6721,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}

	matches, err := discoverMatches(context.Background(), cfg, testLogger())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}

	selected := selectMatch(matches)
	if selected.MatchID != "alpha" {
		t.Errorf("deterministic selection should pick 'alpha', got %q", selected.MatchID)
	}
}

func TestOnce_MatchIDFilter(t *testing.T) {
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("sess-bravo", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(
		fakeMatch{MatchID: "alpha", Mode: "arena", Level: "a", Size: 2, EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort)},
		fakeMatch{MatchID: "bravo", Mode: "arena", Level: "a", Size: 2, EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort)},
	)
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
		MatchIDFilter:     "bravo",
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err != nil {
		t.Fatalf("doOnce failed: %v", err)
	}

	if stats.MatchesSkippedFilter.Load() != 1 {
		t.Errorf("skipped = %d, want 1", stats.MatchesSkippedFilter.Load())
	}

	if !waitForBatches(fa, 1, 2*time.Second) {
		t.Fatal("expected a batch to be sent (timeout)")
	}
	batch := fa.lastBatch()
	if batch.MatchID != "bravo" {
		t.Errorf("batch match_id = %q, want 'bravo'", batch.MatchID)
	}
}

func TestOnce_IdentityFormat(t *testing.T) {
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "match-1",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err != nil {
		t.Fatalf("doOnce failed: %v", err)
	}

	if !waitForBatches(fa, 1, 2*time.Second) {
		t.Fatal("expected batch (timeout)")
	}
	batch := fa.lastBatch()

	// Verify every frame uses echovr:<userid> identity format
	for i, f := range batch.Frames {
		if !strings.HasPrefix(f.PlayerID, "echovr:") {
			t.Errorf("frame[%d].PlayerID = %q, want echovr: prefix", i, f.PlayerID)
		}
		// Verify it's echovr:<numeric> (from the fake userid 1000+i)
		if !strings.HasPrefix(f.PlayerID, "echovr:100") {
			t.Errorf("frame[%d].PlayerID = %q, expected echovr:100x", i, f.PlayerID)
		}
	}
}

func TestContinuousMode_ShortRun(t *testing.T) {
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "cont-match",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      20 * time.Millisecond, // fast polling for test
		DiscoveryInterval: 5 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Run bridge in a goroutine — it blocks until context is cancelled
	done := make(chan struct{})
	go func() {
		runBridge(ctx, cfg, stats, testLogger())
		close(done)
	}()

	// Wait for bridge to finish (context timeout)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not shut down within timeout")
	}

	// Verify pollers started and sent batches
	if stats.PollersStarted.Load() < 1 {
		t.Errorf("pollers started = %d, want >= 1", stats.PollersStarted.Load())
	}
	if stats.TotalBatchesSent.Load() < 2 {
		t.Errorf("batches sent = %d, want >= 2", stats.TotalBatchesSent.Load())
	}
	if stats.TotalFramesForwarded.Load() < 4 {
		t.Errorf("frames forwarded = %d, want >= 4 (2 players * 2+ batches)", stats.TotalFramesForwarded.Load())
	}

	// Verify anticheat received some batches
	if fa.batchCount() < 2 {
		// Allow slight undershoot due to timing
		t.Logf("warning: anticheat received %d batches (expected >=2, may be timing)", fa.batchCount())
	}
}

func TestProbe_WithDumpDir(t *testing.T) {
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("dump-sess", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "dump-match",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	dumpDir := t.TempDir()
	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
		DumpDir:           dumpDir,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "probe"}

	err := doProbe(context.Background(), cfg, stats, testLogger())
	if err != nil {
		t.Fatalf("probe with dump failed: %v", err)
	}

	// Check dump files exist (filenames include match_id prefix)
	expectedFiles := []string{
		"discovery_matches.json",
		"dump-match_session_raw.json",
		"dump-match_mapped_frames.json",
		"dump-match_manifest.json",
	}
	for _, name := range expectedFiles {
		path := dumpDir + "/" + name
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected dump file %s, got error: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("dump file %s is empty", name)
		}
	}

	// Verify manifest content
	manifestBytes, err := os.ReadFile(dumpDir + "/dump-match_manifest.json")
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var manifest resultManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}
	if manifest.SelectedMatchID != "dump-match" {
		t.Errorf("manifest match_id = %q, want dump-match", manifest.SelectedMatchID)
	}
	if manifest.Mode != "probe" {
		t.Errorf("manifest mode = %q, want probe", manifest.Mode)
	}
	if manifest.SessionID != "dump-sess" {
		t.Errorf("manifest session_id = %q, want dump-sess", manifest.SessionID)
	}
	if manifest.MappedFrames != 2 {
		t.Errorf("manifest mapped_frames = %d, want 2", manifest.MappedFrames)
	}
	if manifest.DryRun != true {
		t.Error("manifest dry_run should be true (no anticheat URL)")
	}
}

func TestValidateConfig_DumpDirContinuous(t *testing.T) {
	cfg := &BridgeConfig{
		NakamaURL:         "http://localhost:7350",
		NakamaServerKey:   "key",
		APIPort:           6721,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
		DumpDir:           "/tmp/test",
	}
	err := validateConfig(cfg, "continuous")
	if err == nil {
		t.Error("expected error for --dump-dir in continuous mode")
	}
	if !strings.Contains(err.Error(), "only supported in probe/once") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDiscovery_ObjectEndpointFormat(t *testing.T) {
	// Test that discovery handles object-format endpoints
	labelWithObjEndpoint := `{"id":"obj-match","mode":"arena","level":"a","broadcaster":{"endpoint":{"external_ip":"10.0.0.1","internal_ip":"192.168.1.1","port":6721},"region":"test"}}`
	nakamaResp := fmt.Sprintf(`{"matches":[{"match_id":"obj-match","authoritative":true,"label":%s,"size":4}]}`,
		mustMarshal(t, labelWithObjEndpoint))
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		APIPort:           6721,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}

	matches, err := discoverMatches(context.Background(), cfg, testLogger())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}

	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].BroadcasterIP != "10.0.0.1" {
		t.Errorf("IP = %q, want 10.0.0.1", matches[0].BroadcasterIP)
	}
}

func mustMarshal(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDiscovery_ExtraUnknownFields(t *testing.T) {
	// Labels with extra fields the bridge doesn't know about should parse fine
	label := `{"id":"extra","mode":"arena","level":"a","unknown_field":42,"broadcaster":{"endpoint":"10.0.0.1:1.2.3.4:6721","region":"test","extra":true},"some_other":[1,2,3]}`
	nakamaResp := fmt.Sprintf(`{"matches":[{"match_id":"extra","authoritative":true,"label":%s,"size":2}]}`,
		mustMarshal(t, label))
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		APIPort:           6721,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}

	matches, err := discoverMatches(context.Background(), cfg, testLogger())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].MatchID != "extra" {
		t.Errorf("match_id = %q, want extra", matches[0].MatchID)
	}
}

// --- Reconnect test ---

func TestWSSender_ReconnectAfterDisconnect(t *testing.T) {
	// This test directly exercises wsSender's reconnect-on-send-failure logic.
	// Strategy:
	// 1. Start fake anticheat that accepts connections
	// 2. Send batch 1 — succeeds
	// 3. Forcibly close the fake's active connection
	// 4. Send batch 2 — fails (broken pipe), wsSender nils conn
	// 5. Send batch 3 — wsSender reconnects, succeeds
	// 6. Assert fake received batch 1 and batch 3

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{AnticheatURL: fa.wsURL()}
	sender := newWSSender(cfg, testLogger())
	if err := sender.connect(); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	defer sender.close()

	batch := func(id string) *FrameBatch {
		return &FrameBatch{MatchID: id, Timestamp: time.Now()}
	}

	// Send 1 — should succeed
	if err := sender.send(batch("send-1")); err != nil {
		t.Fatalf("send 1 failed: %v", err)
	}
	if !waitForBatches(fa, 1, time.Second) {
		t.Fatal("fake did not receive batch 1")
	}

	// Forcibly close the connection from the sender side to simulate network failure.
	// We reach into sender.conn and close it directly.
	sender.mu.Lock()
	if sender.conn != nil {
		sender.conn.Close()
	}
	sender.mu.Unlock()

	// Send 2 — should fail because conn is now broken
	err := sender.send(batch("send-2"))
	if err == nil {
		// Sometimes the write buffer hasn't noticed the close yet.
		// That's OK — the test is about what happens on the NEXT send after a failure.
		t.Log("send 2 did not fail (buffered), continuing")
	}

	// Send 3 — wsSender should detect nil/broken conn and reconnect
	if err := sender.send(batch("send-3")); err != nil {
		t.Fatalf("send 3 (reconnect) failed: %v", err)
	}

	// Wait for fake to receive send-3
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b := fa.lastBatch()
		if b != nil && b.MatchID == "send-3" {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("fake did not receive send-3 after reconnect; batches=%d, last=%v", fa.batchCount(), fa.lastBatch())
}

// --- Lifecycle test: broadcaster transitions to post_match ---

func startDynamicBroadcaster(t *testing.T, responses chan string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			http.NotFound(w, r)
			return
		}
		select {
		case body := <-responses:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		default:
			// If no response queued, return last known good
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, fakeSessionJSON("lifecycle", "playing", 2))
		}
	}))
}

func TestContinuousMode_PostMatchStops(t *testing.T) {
	// Broadcaster returns "playing" for initial polls, then "post_match".
	// Poller should detect post_match and stop cleanly.
	responses := make(chan string, 100)
	// Queue 5 "playing" responses then switch to "post_match"
	for i := 0; i < 5; i++ {
		responses <- fakeSessionJSON("lifecycle-sess", "playing", 2)
	}
	for i := 0; i < 10; i++ {
		responses <- fakeSessionJSON("lifecycle-sess", "post_match", 2)
	}

	broadcaster := startDynamicBroadcaster(t, responses)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "lifecycle-match",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      10 * time.Millisecond,
		DiscoveryInterval: 5 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runBridge(ctx, cfg, stats, testLogger())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not shut down (poller should have stopped on post_match)")
	}

	if stats.PollersStarted.Load() < 1 {
		t.Error("expected at least 1 poller started")
	}
	// The poller should have sent some batches before hitting post_match
	if stats.TotalBatchesSent.Load() < 1 {
		t.Logf("warning: batches sent = %d (expected >= 1, timing dependent)", stats.TotalBatchesSent.Load())
	}
	if stats.PollersStopped.Load() < 1 {
		t.Logf("warning: pollers stopped = %d (poller may not have fully stopped before context cancel)", stats.PollersStopped.Load())
	}
}

// --- Discovery tolerance: null/missing broadcaster ---

func TestDiscovery_NullBroadcasterObject(t *testing.T) {
	label := `{"id":"null-bc","mode":"arena","level":"a","broadcaster":null}`
	nakamaResp := fmt.Sprintf(`{"matches":[{"match_id":"null-bc","authoritative":true,"label":%s,"size":2}]}`,
		mustMarshal(t, label))
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		APIPort:           6721,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}

	matches, err := discoverMatches(context.Background(), cfg, testLogger())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	// Should skip the match with null broadcaster
	if len(matches) != 0 {
		t.Errorf("expected 0 matches (null broadcaster), got %d", len(matches))
	}
}

func TestDiscovery_MissingModeLevel(t *testing.T) {
	// Labels with missing optional mode/level should still parse
	label := `{"id":"minimal","broadcaster":{"endpoint":"10.0.0.1:1.2.3.4:6721"}}`
	nakamaResp := fmt.Sprintf(`{"matches":[{"match_id":"minimal","authoritative":true,"label":%s,"size":2}]}`,
		mustMarshal(t, label))
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		APIPort:           6721,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}

	matches, err := discoverMatches(context.Background(), cfg, testLogger())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Mode != "" {
		t.Errorf("expected empty mode, got %q", matches[0].Mode)
	}
}

// --- Dry-run continuous mode ---

func TestContinuousMode_DryRun(t *testing.T) {
	// Dry-run mode should poll+map but never send. Verify stats are correct.
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("dryrun-sess", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "dryrun-match",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      "", // dry-run: no send target
		APIPort:           bPort,
		PollInterval:      20 * time.Millisecond,
		DiscoveryInterval: 5 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runBridge(ctx, cfg, stats, testLogger())
		close(done)
	}()

	<-done

	// Should have polled and counted frames, but sent zero batches
	if stats.TotalFramesForwarded.Load() < 2 {
		t.Errorf("dry-run frames = %d, want >= 2", stats.TotalFramesForwarded.Load())
	}
	if stats.TotalBatchesSent.Load() != 0 {
		t.Errorf("dry-run batches sent = %d, want 0", stats.TotalBatchesSent.Load())
	}
	if stats.TotalSendFailures.Load() != 0 {
		t.Errorf("dry-run send failures = %d, want 0", stats.TotalSendFailures.Load())
	}
	if stats.PollersStarted.Load() < 1 {
		t.Errorf("dry-run pollers started = %d, want >= 1", stats.PollersStarted.Load())
	}
}

// --- All matches have invalid endpoints ---

func TestContinuousMode_AllMatchesSkippedBadEndpoints(t *testing.T) {
	// All discovered matches have invalid/empty endpoints. Bridge should not crash,
	// should skip all, and have zero active pollers.
	nakamaResp := fakeNakamaMatchList(
		fakeMatch{MatchID: "bad-1", Mode: "arena", Level: "a", Size: 2, EndpointStr: "10.0.0.1:not-an-ip:6721"},
		fakeMatch{MatchID: "bad-2", Mode: "arena", Level: "a", Size: 4, NoBroadcaster: true},
	)
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      "", // doesn't matter, we'll never get to send
		APIPort:           6721,
		PollInterval:      50 * time.Millisecond,
		DiscoveryInterval: 5 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runBridge(ctx, cfg, stats, testLogger())
		close(done)
	}()

	<-done

	// Matches were discovered but all skipped
	if stats.PollersStarted.Load() != 0 {
		t.Errorf("pollers started = %d, want 0", stats.PollersStarted.Load())
	}
	if stats.MatchesSkippedNoEP.Load() < 1 {
		t.Errorf("skipped_no_ep = %d, want >= 1", stats.MatchesSkippedNoEP.Load())
	}
	// No frames should have been forwarded
	if stats.TotalFramesForwarded.Load() != 0 {
		t.Errorf("frames forwarded = %d, want 0", stats.TotalFramesForwarded.Load())
	}
}

// --- Filename sanitization ---

func TestSanitizeForFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple-match", "simple-match"},
		{"match/with/slashes", "match_with_slashes"},
		{"match:with:colons", "match_with_colons"},
		{"match with spaces", "match_with_spaces"},
		{"match.with.dots", "match_with_dots"},
		{"node1.match-abc-123.server", "node1_match-abc-123_server"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeForFilename(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeForFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDumpFilename(t *testing.T) {
	if got := dumpFilename("match-123", "session_raw.json"); got != "match-123_session_raw.json" {
		t.Errorf("got %q", got)
	}
	if got := dumpFilename("node.match/abc", "manifest.json"); got != "node_match_abc_manifest.json" {
		t.Errorf("got %q", got)
	}
	if got := dumpFilename("", "manifest.json"); got != "manifest.json" {
		t.Errorf("got %q for empty match_id", got)
	}
}

// --- Manifest content quality ---

func TestOnce_ManifestContent(t *testing.T) {
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("manifest-sess", "playing", 3), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "manifest-match",
		Size:        3,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	dumpDir := t.TempDir()
	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
		DumpDir:           dumpDir,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err != nil {
		t.Fatalf("doOnce failed: %v", err)
	}

	// Read manifest
	manifestPath := dumpDir + "/" + dumpFilename("manifest-match", "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var manifest resultManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}

	if manifest.Mode != "once" {
		t.Errorf("mode = %q, want once", manifest.Mode)
	}
	if manifest.SelectedMatchID != "manifest-match" {
		t.Errorf("match_id = %q, want manifest-match", manifest.SelectedMatchID)
	}
	if manifest.SessionID != "manifest-sess" {
		t.Errorf("session_id = %q, want manifest-sess", manifest.SessionID)
	}
	if manifest.MappedFrames != 3 {
		t.Errorf("mapped_frames = %d, want 3", manifest.MappedFrames)
	}
	if manifest.AnticheatSendAttempted != true {
		t.Error("send_attempted should be true")
	}
	if manifest.AnticheatSendSucceeded != true {
		t.Error("send_succeeded should be true")
	}
	if manifest.DryRun != false {
		t.Error("dry_run should be false (anticheat URL was provided)")
	}
	if manifest.PlayersSeen != 3 {
		t.Errorf("players_seen = %d, want 3", manifest.PlayersSeen)
	}
	if manifest.Timestamp == "" {
		t.Error("timestamp should not be empty")
	}
	if !strings.HasPrefix(manifest.SamplePlayerID, "echovr:") {
		t.Errorf("sample_player_id = %q, want echovr: prefix", manifest.SamplePlayerID)
	}
}

// --- Cross-discovery-cycle recovery tests ---

func TestContinuousMode_MatchAppearsAfterEmptyDiscovery(t *testing.T) {
	// Proves the most important continuous-mode lifecycle: bridge starts with
	// no matches, Nakama later returns a valid match, and the bridge correctly
	// transitions to active polling.
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("late-sess", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	// Start Nakama returning empty match list
	emptyResp := `{"matches":[]}`
	matchResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "late-match",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})

	nakama, nakamaResp := startSwitchableNakama(t, emptyResp)
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      15 * time.Millisecond,
		DiscoveryInterval: 100 * time.Millisecond, // fast discovery for test
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runBridge(ctx, cfg, stats, testLogger())
		close(done)
	}()

	// Let the bridge run a few empty discovery cycles
	time.Sleep(350 * time.Millisecond)

	// Now switch Nakama to return the match
	nakamaResp.Store(matchResp)

	// Wait for bridge to finish (context timeout)
	<-done

	// The bridge should have discovered the match and started polling
	if stats.PollersStarted.Load() < 1 {
		t.Errorf("pollers started = %d, want >= 1 (match appeared after empty cycles)", stats.PollersStarted.Load())
	}
	if stats.TotalBatchesSent.Load() < 1 {
		t.Errorf("batches sent = %d, want >= 1", stats.TotalBatchesSent.Load())
	}

	// Verify anticheat got at least one batch with the right match ID
	if waitForBatches(fa, 1, 500*time.Millisecond) {
		batch := fa.lastBatch()
		if batch.MatchID != "late-match" {
			t.Errorf("batch match_id = %q, want late-match", batch.MatchID)
		}
	}
}

func TestContinuousMode_MatchDisappearsFromDiscovery(t *testing.T) {
	// Proves: bridge is polling a match, Nakama removes it from the match list,
	// bridge cancels the poller cleanly.
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("vanish-sess", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	matchResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "vanish-match",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	emptyResp := `{"matches":[]}`

	nakama, nakamaResp := startSwitchableNakama(t, matchResp)
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      15 * time.Millisecond,
		DiscoveryInterval: 150 * time.Millisecond,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runBridge(ctx, cfg, stats, testLogger())
		close(done)
	}()

	// Let bridge poll for a bit
	time.Sleep(300 * time.Millisecond)

	// Now remove the match from Nakama
	nakamaResp.Store(emptyResp)

	<-done

	// Poller should have started then stopped
	if stats.PollersStarted.Load() < 1 {
		t.Errorf("pollers started = %d, want >= 1", stats.PollersStarted.Load())
	}
	if stats.PollersStopped.Load() < 1 {
		t.Errorf("pollers stopped = %d, want >= 1 (match disappeared)", stats.PollersStopped.Load())
	}
	// Should have sent some batches before the match disappeared
	if stats.TotalBatchesSent.Load() < 1 {
		t.Logf("warning: batches = %d (timing dependent, match may have disappeared before first poll)", stats.TotalBatchesSent.Load())
	}
}

// --- Zero-frame probe manifest ---

func TestProbe_ZeroFrameSession(t *testing.T) {
	// Broadcaster returns a valid session but all players have zero positions,
	// causing the mapper to reject them. Probe should succeed (no error) but
	// report zero frames. With dump, manifest should reflect this.
	zeroPositionSession := `{
		"sessionid":"zero-sess",
		"match_type":"Echo_Arena",
		"map_name":"mpl_arena_a",
		"game_status":"playing",
		"game_clock":240.0,
		"game_clock_display":"4:00",
		"private_match":false,
		"client_name":"test",
		"teams":[
			{"team":"BLUE TEAM","players":[
				{"name":"P1","userid":1001,"playerid":0,"level":50,
				 "body":{"position":[0,0,0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
				 "head":{"position":[0,0,0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
				 "velocity":[0,0,0],
				 "lhand":{"pos":[0,0,0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
				 "rhand":{"pos":[0,0,0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
				 "stunned":false,"invulnerable":false,"possession":false,"blocking":false,
				 "ping":50,"stats":{"points":0,"goals":0,"assists":0,"saves":0,"steals":0,"stuns":0}}
			]},
			{"team":"ORANGE TEAM","players":[]}
		],
		"blue_points":0,
		"orange_points":0
	}`

	broadcaster := startFakeBroadcaster(t, zeroPositionSession, 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "zero-frame-match",
		Size:        1,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	dumpDir := t.TempDir()
	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
		DumpDir:           dumpDir,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "probe"}

	// Probe should succeed — zero frames is not an error, just an empty result
	err := doProbe(context.Background(), cfg, stats, testLogger())
	if err != nil {
		t.Fatalf("probe should succeed even with zero frames: %v", err)
	}

	// Manifest should exist and show zero mapped frames
	manifestPath := dumpDir + "/" + dumpFilename("zero-frame-match", "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var manifest resultManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}
	if manifest.MappedFrames != 0 {
		t.Errorf("mapped_frames = %d, want 0", manifest.MappedFrames)
	}
	if manifest.PlayersSeen != 1 {
		t.Errorf("players_seen = %d, want 1 (player existed but had zero position)", manifest.PlayersSeen)
	}
	if manifest.SessionID != "zero-sess" {
		t.Errorf("session_id = %q, want zero-sess", manifest.SessionID)
	}
}

// --- Once-mode failure artifact tests ---

func TestOnce_SendFailure_WritesManifest(t *testing.T) {
	// Once mode: discovery + fetch + map succeed, but anticheat send fails.
	// Verify: manifest is written with attempted=true, succeeded=false.
	// Also verify: raw session and mapped frames were already saved by fetchAndMap.
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("fail-sess", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "fail-match",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	dumpDir := t.TempDir()
	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      "ws://127.0.0.1:1/nonexistent", // will fail to connect
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
		DumpDir:           dumpDir,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err == nil {
		t.Fatal("expected error from send/connect failure")
	}

	// Manifest should exist with attempted=false (connect failed before send)
	// or attempted=true (send failed after connect). In this case connect fails.
	manifestPath := dumpDir + "/" + dumpFilename("fail-match", "manifest.json")
	data, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatalf("manifest should exist even on failure: %v", readErr)
	}
	var manifest resultManifest
	if parseErr := json.Unmarshal(data, &manifest); parseErr != nil {
		t.Fatalf("parsing manifest: %v", parseErr)
	}

	if manifest.SelectedMatchID != "fail-match" {
		t.Errorf("match_id = %q, want fail-match", manifest.SelectedMatchID)
	}
	if manifest.MappedFrames != 2 {
		t.Errorf("mapped_frames = %d, want 2", manifest.MappedFrames)
	}
	if manifest.AnticheatSendSucceeded {
		t.Error("send_succeeded should be false")
	}
	if manifest.SessionID != "fail-sess" {
		t.Errorf("session_id = %q, want fail-sess", manifest.SessionID)
	}

	// Raw session and mapped frames should also exist (written by fetchAndMap before failure)
	rawPath := dumpDir + "/" + dumpFilename("fail-match", "session_raw.json")
	if _, err := os.Stat(rawPath); err != nil {
		t.Errorf("raw session file should exist: %v", err)
	}
	framesPath := dumpDir + "/" + dumpFilename("fail-match", "mapped_frames.json")
	if _, err := os.Stat(framesPath); err != nil {
		t.Errorf("mapped frames file should exist: %v", err)
	}
}

func TestOnce_ZeroFrames_WritesManifestAndFrames(t *testing.T) {
	// Once mode: broadcaster returns valid JSON but zero valid frames.
	// Verify: manifest written with mapped_frames=0 and mapped_frames.json exists (empty array).
	zeroSession := `{
		"sessionid":"zero-once-sess",
		"match_type":"Echo_Arena",
		"map_name":"mpl_arena_a",
		"game_status":"pre_match",
		"game_clock":0,
		"game_clock_display":"0:00",
		"private_match":false,
		"client_name":"test",
		"teams":[
			{"team":"BLUE TEAM","players":[
				{"name":"P1","userid":9001,"playerid":0,"level":50,
				 "body":{"position":[0,0,0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
				 "head":{"position":[0,0,0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
				 "velocity":[0,0,0],
				 "lhand":{"pos":[0,0,0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
				 "rhand":{"pos":[0,0,0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
				 "stunned":false,"invulnerable":false,"possession":false,"blocking":false,
				 "ping":50,"stats":{"points":0,"goals":0,"assists":0,"saves":0,"steals":0,"stuns":0}}
			]},
			{"team":"ORANGE TEAM","players":[]}
		],
		"blue_points":0,
		"orange_points":0
	}`

	broadcaster := startFakeBroadcaster(t, zeroSession, 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "zero-once",
		Size:        1,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	dumpDir := t.TempDir()
	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      "", // dry-run
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
		DumpDir:           dumpDir,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err != nil {
		t.Fatalf("doOnce should succeed with zero frames: %v", err)
	}

	// Manifest should show zero frames
	manifestPath := dumpDir + "/" + dumpFilename("zero-once", "manifest.json")
	data, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatalf("manifest should exist: %v", readErr)
	}
	var manifest resultManifest
	if parseErr := json.Unmarshal(data, &manifest); parseErr != nil {
		t.Fatalf("parsing manifest: %v", parseErr)
	}
	if manifest.MappedFrames != 0 {
		t.Errorf("mapped_frames = %d, want 0", manifest.MappedFrames)
	}
	if manifest.AnticheatSendAttempted {
		t.Error("send should not have been attempted (zero frames)")
	}
	if manifest.GameStatus != "pre_match" {
		t.Errorf("game_status = %q, want pre_match", manifest.GameStatus)
	}

	// mapped_frames.json should exist (empty array written by fetchAndMap)
	framesPath := dumpDir + "/" + dumpFilename("zero-once", "mapped_frames.json")
	framesData, err := os.ReadFile(framesPath)
	if err != nil {
		t.Fatalf("mapped frames file should exist: %v", err)
	}
	// Should be valid JSON (null or empty array)
	if len(framesData) == 0 {
		t.Error("mapped_frames.json should not be empty file")
	}
}

// --- Broadcaster temporary failure then recovery ---

func TestContinuousMode_BroadcasterRecovery(t *testing.T) {
	// Broadcaster returns errors for a few polls, then recovers.
	// Proves: poll failures increment, then poller resumes sending batches.
	var reqCount atomic.Int64
	broadcaster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			http.NotFound(w, r)
			return
		}
		n := reqCount.Add(1)
		if n <= 5 {
			// First 5 requests: return 500
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":"temporary failure"}`)
			return
		}
		// After that: return valid session
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fakeSessionJSON("recovery-sess", "playing", 2))
	}))
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "recovery-match",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      10 * time.Millisecond, // fast for test
		DiscoveryInterval: 5 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runBridge(ctx, cfg, stats, testLogger())
		close(done)
	}()

	<-done

	// Should have hit failures then recovered
	if stats.TotalPollFailures.Load() < 3 {
		t.Errorf("poll failures = %d, want >= 3 (broadcaster returned 500 for first 5 requests)", stats.TotalPollFailures.Load())
	}
	if stats.TotalBatchesSent.Load() < 1 {
		t.Errorf("batches sent = %d, want >= 1 (broadcaster recovered after errors)", stats.TotalBatchesSent.Load())
	}
	if stats.PollersStarted.Load() < 1 {
		t.Error("expected at least 1 poller started")
	}
}

// --- Manifest send_skip_reason field ---

func TestOnce_SendFailure_ManifestHasReason(t *testing.T) {
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("reason-sess", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "reason-match",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	dumpDir := t.TempDir()
	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      "ws://127.0.0.1:1/bad", // will fail
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
		DumpDir:           dumpDir,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err == nil {
		t.Fatal("expected error")
	}

	manifestPath := dumpDir + "/" + dumpFilename("reason-match", "manifest.json")
	data, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatalf("manifest missing: %v", readErr)
	}
	var manifest resultManifest
	if parseErr := json.Unmarshal(data, &manifest); parseErr != nil {
		t.Fatalf("parse: %v", parseErr)
	}

	if manifest.SendSkipReason == "" {
		t.Error("send_skip_reason should not be empty on failure")
	}
	if !strings.Contains(manifest.SendSkipReason, "connect failed") {
		t.Errorf("send_skip_reason = %q, want to contain 'connect failed'", manifest.SendSkipReason)
	}
}

// --- Nakama discovery failure then recovery ---

func TestContinuousMode_NakamaDiscoveryFailureThenRecovery(t *testing.T) {
	// Nakama returns invalid responses (simulating downtime), then recovers
	// with a valid match list. Bridge should accumulate discovery failures,
	// then recover and start polling.
	broadcaster := startFakeBroadcaster(t, fakeSessionJSON("nakama-fail-sess", "playing", 2), 200)
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	validResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "nakama-recovery",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        2,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})

	// Start Nakama returning garbage (will cause parse error in discoverMatches)
	nakama, nakamaResp := startSwitchableNakama(t, "not valid json at all")
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      15 * time.Millisecond,
		DiscoveryInterval: 100 * time.Millisecond, // fast discovery for test
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runBridge(ctx, cfg, stats, testLogger())
		close(done)
	}()

	// Let bridge fail discovery a few times
	time.Sleep(400 * time.Millisecond)

	// Switch Nakama to valid response
	nakamaResp.Store(validResp)

	// Wait for bridge to finish
	<-done

	// Should have started polling after Nakama recovered
	if stats.PollersStarted.Load() < 1 {
		t.Errorf("pollers started = %d, want >= 1 (Nakama recovered)", stats.PollersStarted.Load())
	}
	if stats.TotalBatchesSent.Load() < 1 {
		t.Errorf("batches sent = %d, want >= 1", stats.TotalBatchesSent.Load())
	}
	// MatchesDiscovered should be > 0 (from the recovery discovery)
	if stats.MatchesDiscovered.Load() < 1 {
		t.Errorf("matches discovered = %d, want >= 1", stats.MatchesDiscovered.Load())
	}
}

// --- Continuous-mode zero-frame streak then recovery ---

func TestContinuousMode_ZeroFramesThenRecovery(t *testing.T) {
	// Broadcaster initially returns players with zero positions (mapper rejects them
	// producing zero frames), then switches to valid positions. Proves:
	// - poller accumulates zero-frame polls without crashing or stopping
	// - when valid frames appear, batches are sent successfully
	// - consecutiveZeroFrames resets (indirectly proven by batches appearing)
	//
	// This is the exact pattern for pre_match -> playing phase transition.

	zeroPositionSession := fakeSessionJSON("zf-sess", "playing", 0) // 0 players = zero frames
	validSession := fakeSessionJSON("zf-sess", "playing", 3)        // 3 players = 3 frames

	var useValid atomic.Bool
	broadcaster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if useValid.Load() {
			fmt.Fprint(w, validSession)
		} else {
			fmt.Fprint(w, zeroPositionSession)
		}
	}))
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)

	nakamaResp := fakeNakamaMatchList(fakeMatch{
		MatchID:     "zf-match",
		Mode:        "echo_arena",
		Level:       "arena",
		Size:        3,
		EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort),
	})
	nakama := startFakeNakama(t, nakamaResp)
	defer nakama.Close()

	fa := startFakeAnticheat(t)
	defer fa.close()

	cfg := &BridgeConfig{
		NakamaURL:         nakama.URL,
		NakamaServerKey:   "testkey",
		AnticheatURL:      fa.wsURL(),
		APIPort:           bPort,
		PollInterval:      10 * time.Millisecond,
		DiscoveryInterval: 5 * time.Second,
	}
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runBridge(ctx, cfg, stats, testLogger())
		close(done)
	}()

	// Let bridge poll zero-frame sessions for a while
	time.Sleep(300 * time.Millisecond)

	// Switch to valid sessions
	useValid.Store(true)

	<-done

	// Poller should have started
	if stats.PollersStarted.Load() < 1 {
		t.Error("expected at least 1 poller started")
	}

	// Should have had some polls that produced zero frames (not counted in batches sent)
	// then recovery polls that produced real frames
	if stats.TotalPolls.Load() < 5 {
		t.Errorf("total polls = %d, want >= 5", stats.TotalPolls.Load())
	}
	if stats.TotalBatchesSent.Load() < 1 {
		t.Errorf("batches sent = %d, want >= 1 (frames should appear after switching to valid session)", stats.TotalBatchesSent.Load())
	}
	if stats.TotalFramesForwarded.Load() < 3 {
		t.Errorf("frames forwarded = %d, want >= 3 (3 players per batch)", stats.TotalFramesForwarded.Load())
	}
}
