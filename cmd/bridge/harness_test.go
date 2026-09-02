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

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
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
	srv, _ := startSwitchableNakama(t, responseBody)
	return srv
}

// startSwitchableNakama returns a fake Nakama whose response can be changed at runtime.
// It only routes the exact "/v2/match" path so a "//v2/match" request 404s,
// like Nakama's gateway.
func startSwitchableNakama(t *testing.T, initial string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var current atomic.Value
	current.Store(initial)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/match" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, current.Load().(string))
	}))
	t.Cleanup(srv.Close)
	return srv, &current
}

// --- Fake Broadcaster ---

// fakeSessionJSON builds a minimal valid EchoVRSessionResponse JSON with the
// given number of blue players.
func fakeSessionJSON(sessionID, gameStatus string, players int) string {
	return fakeSessionJSONTeams(sessionID, gameStatus, players, 0, 0)
}

// fakeSessionJSONTeams builds a session with blue, orange and spectator entries.
// Blue userids start at 1000, orange at 2000, spectators at 9000.
func fakeSessionJSONTeams(sessionID, gameStatus string, blue, orange, spectators int) string {
	player := func(name string, userid, slot int) string {
		return fmt.Sprintf(`{
			"name":%q,"userid":%d,"playerid":%d,"level":50,
			"body":{"position":[%d.0,1.6,3.0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
			"head":{"position":[%d.0,1.6,3.0],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
			"velocity":[0,0,0],
			"lhand":{"pos":[0.3,1.4,0.2],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
			"rhand":{"pos":[-0.3,1.4,0.2],"forward":[1,0,0],"left":[0,1,0],"up":[0,0,1]},
			"stunned":false,"invulnerable":false,"possession":false,"blocking":false,
			"ping":50,"stats":{"points":0,"goals":0,"assists":0,"saves":0,"steals":0,"stuns":0}
		}`, name, userid, slot, slot*2, slot*2)
	}
	var b, o, s []string
	slot := 0
	for i := 0; i < blue; i++ {
		b = append(b, player(fmt.Sprintf("Blue%d", i), 1000+i, slot))
		slot++
	}
	for i := 0; i < orange; i++ {
		o = append(o, player(fmt.Sprintf("Orange%d", i), 2000+i, slot))
		slot++
	}
	for i := 0; i < spectators; i++ {
		s = append(s, player(fmt.Sprintf("Spec%d", i), 9000+i, slot))
		slot++
	}
	teams := fmt.Sprintf(`{"team":"BLUE TEAM","players":[%s]},{"team":"ORANGE TEAM","players":[%s]}`,
		strings.Join(b, ","), strings.Join(o, ","))
	if spectators > 0 {
		teams += fmt.Sprintf(`,{"team":"SPECTATORS","players":[%s]}`, strings.Join(s, ","))
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
		"teams":[%s],
		"blue_points":2,
		"orange_points":1
	}`, sessionID, gameStatus, teams)
}

// varySession makes the n-th response differ from the previous one (the
// game clock advances) so the bridge's identical-snapshot dedup does not
// collapse a static fixture into a single frame.
func varySession(body string, n int64) string {
	return strings.Replace(body, `"game_clock":240.0`, fmt.Sprintf(`"game_clock":%.3f`, 240-float64(n)*0.067), 1)
}

// fakeBroadcaster serves /session with a swappable body and a request counter.
type fakeBroadcaster struct {
	server   *httptest.Server
	body     atomic.Value // string
	status   atomic.Int32
	requests atomic.Int64
	static   bool // do not vary the body between requests
	// bytesOnly varies a field the mapper's fingerprint ignores
	// (client_name) so every body differs byte-wise while the game state
	// stays identical.
	bytesOnly bool
}

func startFakeBroadcaster(t *testing.T, responseBody string, statusCode int) *httptest.Server {
	t.Helper()
	return newFakeBroadcaster(t, responseBody, statusCode).server
}

func newFakeBroadcaster(t *testing.T, responseBody string, statusCode int) *fakeBroadcaster {
	t.Helper()
	fb := &fakeBroadcaster{}
	fb.body.Store(responseBody)
	fb.status.Store(int32(statusCode))
	fb.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			http.NotFound(w, r)
			return
		}
		n := fb.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(fb.status.Load()))
		body := fb.body.Load().(string)
		switch {
		case fb.bytesOnly:
			body = strings.Replace(body, `"client_name":"test"`, fmt.Sprintf(`"client_name":"test-%d"`, n), 1)
		case !fb.static:
			body = varySession(body, n)
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(fb.server.Close)
	return fb
}

func (fb *fakeBroadcaster) port(t *testing.T) int { return extractTestPort(t, fb.server.URL) }

func (fb *fakeBroadcaster) set(body string, status int) {
	fb.body.Store(body)
	fb.status.Store(int32(status))
}

// --- Fake Anticheat WebSocket (speaks the hello/ack protocol) ---

type fakeAnticheat struct {
	server   *httptest.Server
	mu       sync.Mutex
	batches  []FrameBatch
	controls []model.ControlMessage
	conns    []*websocket.Conn
	accepted int64

	// behaviour knobs (set before the bridge connects)
	expectToken string // reject connections whose bearer token differs
	noHello     bool   // never send hello (pre-protocol server)
	noAck       bool   // accept frames but never ack them
	rejectAll   bool   // ack every batch as rejected
	ackDelay    time.Duration
	connCount   atomic.Int64
}

func startFakeAnticheat(t *testing.T) *fakeAnticheat {
	t.Helper()
	fa := &fakeAnticheat{}
	mux := http.NewServeMux()
	mux.Handle("/telemetry", websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		fa.connCount.Add(1)
		fa.mu.Lock()
		fa.conns = append(fa.conns, conn)
		fa.mu.Unlock()

		if fa.expectToken != "" && conn.Request().Header.Get("Authorization") != "Bearer "+fa.expectToken {
			_ = websocket.JSON.Send(conn, model.ControlMessage{Type: model.ControlError, Reason: "unauthorized"})
			return
		}
		if !fa.noHello {
			if err := websocket.JSON.Send(conn, model.ControlMessage{Type: model.ControlHello, Auth: "ok"}); err != nil {
				return
			}
		}
		for {
			var raw json.RawMessage
			if err := websocket.JSON.Receive(conn, &raw); err != nil {
				return // client disconnected
			}
			var probe struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &probe)
			if probe.Type != "" {
				var cm model.ControlMessage
				_ = json.Unmarshal(raw, &cm)
				fa.mu.Lock()
				fa.controls = append(fa.controls, cm)
				fa.mu.Unlock()
				continue
			}
			var batch FrameBatch
			if err := json.Unmarshal(raw, &batch); err != nil {
				continue
			}
			fa.mu.Lock()
			fa.batches = append(fa.batches, batch)
			fa.mu.Unlock()
			if fa.noAck {
				continue
			}
			if fa.ackDelay > 0 {
				time.Sleep(fa.ackDelay)
			}
			ack := model.ControlMessage{Type: model.ControlAck, Accepted: len(batch.Frames)}
			if fa.rejectAll {
				ack = model.ControlMessage{Type: model.ControlAck, Rejected: len(batch.Frames)}
			}
			if err := websocket.JSON.Send(conn, ack); err != nil {
				return
			}
		}
	}))
	fa.server = httptest.NewServer(mux)
	t.Cleanup(fa.close)
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

func (fa *fakeAnticheat) allBatches() []FrameBatch {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	out := make([]FrameBatch, len(fa.batches))
	copy(out, fa.batches)
	return out
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

func (fa *fakeAnticheat) controlsOf(typ string) []model.ControlMessage {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	var out []model.ControlMessage
	for _, c := range fa.controls {
		if c.Type == typ {
			out = append(out, c)
		}
	}
	return out
}

// closeConnections drops every server-side socket to simulate a link failure.
func (fa *fakeAnticheat) closeConnections() {
	fa.mu.Lock()
	conns := fa.conns
	fa.conns = nil
	fa.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (fa *fakeAnticheat) close() {
	fa.closeConnections()
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

// refusedAddr returns a loopback host:port that was just bound and released,
// so a dial to it is refused promptly (port 1 can behave oddly on Windows).
func refusedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// waitFor polls cond until it is true or the timeout elapses.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// waitForBatches polls the fake anticheat until it has at least n batches or timeout.
func waitForBatches(fa *fakeAnticheat, n int, timeout time.Duration) bool {
	return waitFor(timeout, func() bool { return fa.batchCount() >= n })
}

// testConfig builds a bridge config with fast, test-friendly link settings.
// Session identity checking is off because fixtures use unrelated ids;
// tests that cover the check enable it explicitly.
func testConfig(nakamaURL string, bPort int) *BridgeConfig {
	return &BridgeConfig{
		NakamaURL:         nakamaURL,
		NakamaServerKey:   "testkey",
		APIPort:           bPort,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
		SessionCheck:      sessionCheckOff,
		HelloTimeout:      time.Second,
		DialTimeout:       time.Second,
		WriteTimeout:      time.Second,
		AckTimeout:        3 * time.Second,
		ConnectAttempts:   1,
	}
}

func matchesFor(bPort int, ids ...string) string {
	var ms []fakeMatch
	for _, id := range ids {
		ms = append(ms, fakeMatch{MatchID: id, Mode: "echo_arena", Level: "arena", Size: 2,
			EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", bPort)})
	}
	return fakeNakamaMatchList(ms...)
}

// runBridgeAsync runs the continuous bridge until cancel is called.
func runBridgeAsync(cfg *BridgeConfig, stats *bridgeStats, timeout time.Duration) (cancel func(), done chan struct{}) {
	ctx, c := context.WithTimeout(context.Background(), timeout)
	done = make(chan struct{})
	go func() {
		runBridge(ctx, cfg, stats, testLogger())
		close(done)
	}()
	return func() { c(); <-done }, done
}

// --- Probe / once ---

func TestProbeEndToEnd(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 4), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "match-alpha"))

	cfg := testConfig(nakama.URL, fb.port(t))
	stats := &bridgeStats{startTime: time.Now(), mode: "probe"}

	if err := doProbe(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("doProbe failed: %v", err)
	}
	if stats.MatchesDiscovered.Load() != 1 {
		t.Errorf("discovered = %d, want 1", stats.MatchesDiscovered.Load())
	}
}

func TestOnceEndToEnd(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 3), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "match-1"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	if err := doOnce(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("doOnce failed: %v", err)
	}

	if stats.TotalBatchesSent.Load() != 1 {
		t.Errorf("batches sent = %d, want 1", stats.TotalBatchesSent.Load())
	}
	if stats.TotalFramesForwarded.Load() != 3 {
		t.Errorf("frames forwarded (acked) = %d, want 3", stats.TotalFramesForwarded.Load())
	}

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
	wantServer := fmt.Sprintf("127.0.0.1:%d", fb.port(t))
	if batch.ServerID != wantServer {
		t.Errorf("batch server_id = %q, want broadcaster host:port %q", batch.ServerID, wantServer)
	}
	for _, f := range batch.Frames {
		if !strings.HasPrefix(f.PlayerID, "echovr:") {
			t.Errorf("player_id %q does not have echovr: prefix", f.PlayerID)
		}
		if f.Team != "blue" {
			t.Errorf("frame %s team = %q, want blue", f.PlayerID, f.Team)
		}
	}

	// match_start / match_end bracket the batch with provenance + metadata.
	starts := fa.controlsOf(model.ControlMatchStart)
	if len(starts) != 1 {
		t.Fatalf("match_start count = %d, want 1", len(starts))
	}
	if starts[0].MatchID != "match-1" || starts[0].ServerID != wantServer || starts[0].GameMode != "Echo_Arena" || starts[0].Map != "mpl_arena_a" {
		t.Errorf("match_start = %+v", starts[0])
	}
	if len(starts[0].Teams) != 3 {
		t.Errorf("match_start teams = %v, want 3 blue players", starts[0].Teams)
	}
	if ends := fa.controlsOf(model.ControlMatchEnd); len(ends) != 1 || ends[0].Reason != "once" {
		t.Errorf("match_end = %+v, want one with reason once", ends)
	}
}

func TestOnce_DryRun(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "match-1"))

	cfg := testConfig(nakama.URL, fb.port(t))
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	if err := doOnce(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("doOnce dry-run failed: %v", err)
	}
	if stats.TotalBatchesSent.Load() != 0 {
		t.Errorf("batches sent = %d, want 0 (dry-run)", stats.TotalBatchesSent.Load())
	}
}

func TestProbe_BroadcasterNon200(t *testing.T) {
	fb := newFakeBroadcaster(t, `{"error":"not found"}`, 404)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "match-1"))

	cfg := testConfig(nakama.URL, fb.port(t))
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
	fb := newFakeBroadcaster(t, `{not valid json`, 200)
	fb.static = true
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "match-1"))

	cfg := testConfig(nakama.URL, fb.port(t))
	stats := &bridgeStats{startTime: time.Now(), mode: "probe"}

	err := doProbe(context.Background(), cfg, stats, testLogger())
	if err == nil {
		t.Fatal("expected error for invalid JSON from broadcaster")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention invalid JSON, got: %v", err)
	}
}

func TestProbe_NakamaAuthRejectedIsClear(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"code":16,"message":"Auth token invalid"}`)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL, 6721)
	cfg.NakamaAuthMode = nakamaAuthDevice
	stats := &bridgeStats{startTime: time.Now(), mode: "probe"}
	err := doProbe(context.Background(), cfg, stats, testLogger())
	if err == nil {
		t.Fatal("expected probe to fail on 401")
	}
	if !isNakamaAuthError(err) {
		t.Errorf("error should be a nakamaAuthError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "rejected the bridge credentials") || !strings.Contains(err.Error(), "401") {
		t.Errorf("error should explain the credential problem, got: %v", err)
	}
}

func TestDiscovery_TrailingSlashNakamaURL(t *testing.T) {
	nakama := startFakeNakama(t, matchesFor(6721, "m1"))
	cfg := testConfig(nakama.URL+"/", 6721)
	if err := validateConfig(cfg, "probe"); err != nil {
		t.Fatalf("validateConfig: %v", err)
	}
	if strings.HasSuffix(cfg.NakamaURL, "/") {
		t.Errorf("validateConfig should strip the trailing slash, got %q", cfg.NakamaURL)
	}
	// Even without validateConfig, discovery must not produce "//v2/match".
	cfg2 := testConfig(nakama.URL+"///", 6721)
	matches, err := discoverMatches(context.Background(), cfg2, testLogger())
	if err != nil {
		t.Fatalf("discovery with trailing slashes failed: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("matches = %d, want 1", len(matches))
	}
}

func TestDiscovery_SkipsInvalidEndpoints(t *testing.T) {
	nakama := startFakeNakama(t, fakeNakamaMatchList(
		fakeMatch{MatchID: "good", Mode: "arena", Level: "a", Size: 4, EndpointStr: "10.0.0.1:203.0.113.1:6721"},
		fakeMatch{MatchID: "no-bc", Mode: "arena", Level: "a", Size: 2, NoBroadcaster: true},
		fakeMatch{MatchID: "bad-ip", Mode: "arena", Level: "a", Size: 2, EndpointStr: "10.0.0.1:not-an-ip:6721"},
	))
	cfg := testConfig(nakama.URL, 6721)
	stats := &bridgeStats{}

	matches, err := discoverMatches(context.Background(), cfg, testLogger(), stats)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(matches) != 1 || matches[0].MatchID != "good" {
		t.Fatalf("expected only 'good', got %+v", matches)
	}
	if stats.MatchesSkippedNoEP.Load() != 1 {
		t.Errorf("skipped_no_ep = %d, want 1", stats.MatchesSkippedNoEP.Load())
	}
}

func TestDiscovery_DeterministicSelection(t *testing.T) {
	nakama := startFakeNakama(t, fakeNakamaMatchList(
		fakeMatch{MatchID: "charlie", Mode: "arena", Level: "a", Size: 2, EndpointStr: "10.0.0.1:1.1.1.3:6721"},
		fakeMatch{MatchID: "alpha", Mode: "arena", Level: "a", Size: 4, EndpointStr: "10.0.0.1:1.1.1.1:6721"},
		fakeMatch{MatchID: "bravo", Mode: "arena", Level: "a", Size: 3, EndpointStr: "10.0.0.1:1.1.1.2:6721"},
	))
	matches, err := discoverMatches(context.Background(), testConfig(nakama.URL, 6721), testLogger())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if selected := selectMatch(matches); selected.MatchID != "alpha" {
		t.Errorf("deterministic selection should pick 'alpha', got %q", selected.MatchID)
	}
}

func TestOnce_MatchIDFilter(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-bravo", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "alpha", "bravo"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.MatchIDFilter = "bravo"
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	if err := doOnce(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("doOnce failed: %v", err)
	}
	if stats.MatchesSkippedFilter.Load() != 1 {
		t.Errorf("skipped = %d, want 1", stats.MatchesSkippedFilter.Load())
	}
	if !waitForBatches(fa, 1, 2*time.Second) {
		t.Fatal("expected a batch to be sent (timeout)")
	}
	if batch := fa.lastBatch(); batch.MatchID != "bravo" {
		t.Errorf("batch match_id = %q, want 'bravo'", batch.MatchID)
	}
}

func TestOnce_IdentityFormat(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "match-1"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	if err := doOnce(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("doOnce failed: %v", err)
	}
	if !waitForBatches(fa, 1, 2*time.Second) {
		t.Fatal("expected batch (timeout)")
	}
	for i, f := range fa.lastBatch().Frames {
		if !strings.HasPrefix(f.PlayerID, "echovr:100") {
			t.Errorf("frame[%d].PlayerID = %q, expected echovr:100x", i, f.PlayerID)
		}
	}
}

// F11: spectators/moderators in the third team never become telemetry.
func TestOnce_SpectatorsNeverForwarded(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSONTeams("sess-spec", "playing", 2, 1, 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "spec-match"))
	fa := startFakeAnticheat(t)

	dumpDir := t.TempDir()
	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.DumpDir = dumpDir
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	if err := doOnce(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("doOnce failed: %v", err)
	}
	if !waitForBatches(fa, 1, 2*time.Second) {
		t.Fatal("expected batch")
	}
	batch := fa.lastBatch()
	if len(batch.Frames) != 3 {
		t.Fatalf("frames = %d, want 3 (2 blue + 1 orange, spectators dropped)", len(batch.Frames))
	}
	teams := map[string]string{}
	for _, f := range batch.Frames {
		if strings.HasPrefix(f.PlayerID, "echovr:900") {
			t.Errorf("spectator %s was forwarded as telemetry", f.PlayerID)
		}
		if f.Team != "blue" && f.Team != "orange" {
			t.Errorf("frame %s has team %q", f.PlayerID, f.Team)
		}
		teams[f.PlayerID] = f.Team
	}
	if teams["echovr:1000"] != "blue" || teams["echovr:2000"] != "orange" {
		t.Errorf("team assignment wrong: %v", teams)
	}
	start := fa.controlsOf(model.ControlMatchStart)
	if len(start) != 1 {
		t.Fatalf("match_start count = %d", len(start))
	}
	for pid := range start[0].Teams {
		if strings.HasPrefix(pid, "echovr:900") {
			t.Errorf("spectator %s listed in match_start teams", pid)
		}
	}

	data, err := os.ReadFile(dumpDir + "/" + dumpFilename("spec-match", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest resultManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.PlayersSeen != 3 || manifest.SpectatorsSeen != 2 || manifest.MappedFrames != 3 {
		t.Errorf("manifest players=%d spectators=%d frames=%d", manifest.PlayersSeen, manifest.SpectatorsSeen, manifest.MappedFrames)
	}
	if manifest.FramesAcked != 3 {
		t.Errorf("manifest frames_acked = %d, want 3", manifest.FramesAcked)
	}
}

// F8: a server that never says hello (wrong token silently dropped, or a
// pre-protocol server) must fail --once loudly instead of "succeeding".
func TestOnce_NoHello_FailsManifest(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "nohello"))
	fa := startFakeAnticheat(t)
	fa.noHello = true

	dumpDir := t.TempDir()
	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.HelloTimeout = 200 * time.Millisecond
	cfg.DumpDir = dumpDir
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err == nil {
		t.Fatal("expected once to fail without hello")
	}
	if !strings.Contains(err.Error(), "hello") {
		t.Errorf("error should mention the missing hello, got: %v", err)
	}
	if stats.AuthFailures.Load() != 1 {
		t.Errorf("auth failures = %d, want 1", stats.AuthFailures.Load())
	}
	if stats.TotalFramesForwarded.Load() != 0 {
		t.Errorf("frames forwarded = %d, want 0", stats.TotalFramesForwarded.Load())
	}
	manifest := readManifest(t, dumpDir, "nohello")
	if manifest.AnticheatSendSucceeded || manifest.AnticheatSendAttempted {
		t.Errorf("manifest should record no successful send: %+v", manifest)
	}
	if !strings.Contains(manifest.SendSkipReason, "connect failed") {
		t.Errorf("send_skip_reason = %q", manifest.SendSkipReason)
	}
}

func TestOnce_AuthRejected(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "m"))
	fa := startFakeAnticheat(t)
	fa.expectToken = "secret"

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.AnticheatToken = "wrong"
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected unauthorized error, got: %v", err)
	}
	if stats.AuthFailures.Load() != 1 {
		t.Errorf("auth failures = %d, want 1", stats.AuthFailures.Load())
	}

	// Correct token works.
	cfg.AnticheatToken = "secret"
	stats = &bridgeStats{startTime: time.Now(), mode: "once"}
	if err := doOnce(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("doOnce with the right token failed: %v", err)
	}
}

func TestOnce_RejectedFramesAreNotForwarded(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "rej"))
	fa := startFakeAnticheat(t)
	fa.rejectAll = true

	dumpDir := t.TempDir()
	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.DumpDir = dumpDir
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err == nil || !strings.Contains(err.Error(), "rejected all") {
		t.Fatalf("expected rejection error, got: %v", err)
	}
	if stats.TotalFramesForwarded.Load() != 0 || stats.TotalFramesRejected.Load() != 2 {
		t.Errorf("forwarded=%d rejected=%d", stats.TotalFramesForwarded.Load(), stats.TotalFramesRejected.Load())
	}
	manifest := readManifest(t, dumpDir, "rej")
	if manifest.AnticheatSendSucceeded || manifest.FramesRejected != 2 || !manifest.AnticheatSendAttempted {
		t.Errorf("manifest = %+v", manifest)
	}
}

func TestOnce_NoAckTimesOut(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "noack"))
	fa := startFakeAnticheat(t)
	fa.noAck = true

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.AckTimeout = 300 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err == nil || !strings.Contains(err.Error(), "no ack") {
		t.Fatalf("expected no-ack error, got: %v", err)
	}
	if stats.TotalFramesSent.Load() != 2 || stats.TotalFramesForwarded.Load() != 0 {
		t.Errorf("sent=%d forwarded=%d", stats.TotalFramesSent.Load(), stats.TotalFramesForwarded.Load())
	}
}

func TestOnce_SessionMismatchStrict(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("other-session", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "11111111-2222-3333-4444-555555555555.node1"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.SessionCheck = sessionCheckStrict
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	err := doOnce(context.Background(), cfg, stats, testLogger())
	if err == nil || !strings.Contains(err.Error(), "session_mismatch") {
		t.Fatalf("expected session_mismatch error, got: %v", err)
	}
	if stats.SessionMismatches.Load() != 1 || fa.batchCount() != 0 {
		t.Errorf("mismatches=%d batches=%d", stats.SessionMismatches.Load(), fa.batchCount())
	}

	// Matching UUID (case-insensitive) passes.
	fb.set(fakeSessionJSON("11111111-2222-3333-4444-555555555555", "playing", 2), 200)
	stats = &bridgeStats{startTime: time.Now(), mode: "once"}
	if err := doOnce(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("doOnce with matching session failed: %v", err)
	}
}

func readManifest(t *testing.T, dumpDir, matchID string) resultManifest {
	t.Helper()
	data, err := os.ReadFile(dumpDir + "/" + dumpFilename(matchID, "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var m resultManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}
	return m
}

func TestProbe_WithDumpDir(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("dump-sess", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "dump-match"))

	dumpDir := t.TempDir()
	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.DumpDir = dumpDir
	stats := &bridgeStats{startTime: time.Now(), mode: "probe"}

	if err := doProbe(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("probe with dump failed: %v", err)
	}
	for _, name := range []string{"discovery_matches.json", "dump-match_session_raw.json", "dump-match_mapped_frames.json", "dump-match_manifest.json"} {
		info, err := os.Stat(dumpDir + "/" + name)
		if err != nil {
			t.Errorf("expected dump file %s, got error: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("dump file %s is empty", name)
		}
	}
	manifest := readManifest(t, dumpDir, "dump-match")
	if manifest.SelectedMatchID != "dump-match" || manifest.Mode != "probe" || manifest.SessionID != "dump-sess" {
		t.Errorf("manifest = %+v", manifest)
	}
	if manifest.MappedFrames != 2 || !manifest.DryRun {
		t.Errorf("manifest frames=%d dry_run=%v", manifest.MappedFrames, manifest.DryRun)
	}
	if manifest.ServerID != fmt.Sprintf("127.0.0.1:%d", fb.port(t)) {
		t.Errorf("manifest server_id = %q", manifest.ServerID)
	}
}

func TestValidateConfig_DumpDirContinuous(t *testing.T) {
	cfg := &BridgeConfig{
		NakamaURL: "http://localhost:7350", NakamaServerKey: "key", APIPort: 6721,
		PollInterval: 67 * time.Millisecond, DiscoveryInterval: 10 * time.Second, DumpDir: "/tmp/test",
	}
	err := validateConfig(cfg, "continuous")
	if err == nil || !strings.Contains(err.Error(), "only supported in probe/once") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDiscovery_ObjectEndpointFormat(t *testing.T) {
	label := `{"id":"obj-match","mode":"arena","level":"a","broadcaster":{"endpoint":{"external_ip":"10.0.0.1","internal_ip":"192.168.1.1","port":6721},"region":"test"}}`
	nakama := startFakeNakama(t, fmt.Sprintf(`{"matches":[{"match_id":"obj-match","authoritative":true,"label":%s,"size":4}]}`, mustMarshal(t, label)))
	matches, err := discoverMatches(context.Background(), testConfig(nakama.URL, 6721), testLogger())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(matches) != 1 || matches[0].BroadcasterIP != "10.0.0.1" {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].LabelID != "obj-match" || matches[0].Region != "test" {
		t.Errorf("label id / region not carried: %+v", matches[0])
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
	label := `{"id":"extra","mode":"arena","level":"a","unknown_field":42,"broadcaster":{"endpoint":"10.0.0.1:1.2.3.4:6721","region":"test","extra":true},"some_other":[1,2,3]}`
	nakama := startFakeNakama(t, fmt.Sprintf(`{"matches":[{"match_id":"extra","authoritative":true,"label":%s,"size":2}]}`, mustMarshal(t, label)))
	matches, err := discoverMatches(context.Background(), testConfig(nakama.URL, 6721), testLogger())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(matches) != 1 || matches[0].MatchID != "extra" {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestDiscovery_NullBroadcasterObject(t *testing.T) {
	label := `{"id":"null-bc","mode":"arena","level":"a","broadcaster":null}`
	nakama := startFakeNakama(t, fmt.Sprintf(`{"matches":[{"match_id":"null-bc","authoritative":true,"label":%s,"size":2}]}`, mustMarshal(t, label)))
	matches, err := discoverMatches(context.Background(), testConfig(nakama.URL, 6721), testLogger())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 matches (null broadcaster), got %d", len(matches))
	}
}

func TestDiscovery_MissingModeLevel(t *testing.T) {
	label := `{"id":"minimal","broadcaster":{"endpoint":"10.0.0.1:1.2.3.4:6721"}}`
	nakama := startFakeNakama(t, fmt.Sprintf(`{"matches":[{"match_id":"minimal","authoritative":true,"label":%s,"size":2}]}`, mustMarshal(t, label)))
	matches, err := discoverMatches(context.Background(), testConfig(nakama.URL, 6721), testLogger())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(matches) != 1 || matches[0].Mode != "" {
		t.Fatalf("matches = %+v", matches)
	}
}

// --- Continuous mode ---

func TestContinuousMode_ShortRun(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "cont-match"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.DiscoveryInterval = 5 * time.Second
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	if !waitForBatches(fa, 3, 3*time.Second) {
		t.Fatalf("anticheat received %d batches, want >= 3", fa.batchCount())
	}
	if !waitFor(2*time.Second, func() bool { return stats.TotalFramesForwarded.Load() >= 6 }) {
		t.Errorf("acked frames = %d, want >= 6", stats.TotalFramesForwarded.Load())
	}
	cancel()

	if stats.PollersStarted.Load() != 1 {
		t.Errorf("pollers started = %d, want 1", stats.PollersStarted.Load())
	}
	// Every batch carries provenance and a strictly increasing frame index.
	prev := -1
	for _, b := range fa.allBatches() {
		if b.ServerID != fmt.Sprintf("127.0.0.1:%d", fb.port(t)) {
			t.Errorf("server_id = %q", b.ServerID)
		}
		for _, f := range b.Frames {
			if f.FrameIndex <= prev {
				t.Fatalf("frame index went from %d to %d", prev, f.FrameIndex)
			}
		}
		prev = b.Frames[0].FrameIndex
	}
	// Shutdown announced match_end.
	if !waitFor(time.Second, func() bool {
		ends := fa.controlsOf(model.ControlMatchEnd)
		return len(ends) == 1 && ends[0].Reason == string(stopCancelled)
	}) {
		t.Errorf("match_end at shutdown = %+v", fa.controlsOf(model.ControlMatchEnd))
	}
}

// F80: frames are stamped with real sample time, not an assumed 67 ms.
func TestContinuousMode_RealSampleTiming(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 1), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "timing"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 150 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	if !waitForBatches(fa, 4, 4*time.Second) {
		t.Fatalf("batches = %d", fa.batchCount())
	}
	cancel()

	batches := fa.allBatches()
	if batches[0].Frames[0].DeltaTime != 0 || batches[0].Frames[0].Timestamp != 0 {
		t.Errorf("first frame should have ts=0 dt=0, got ts=%v dt=%v", batches[0].Frames[0].Timestamp, batches[0].Frames[0].DeltaTime)
	}
	for i := 1; i < len(batches); i++ {
		f := batches[i].Frames[0]
		if f.DeltaTime < 0.1 || f.DeltaTime > 1.0 {
			t.Errorf("batch %d dt = %.3f, want ~0.15 (real spacing), not 0.067", i, f.DeltaTime)
		}
		if f.Timestamp <= batches[i-1].Frames[0].Timestamp {
			t.Errorf("timestamp not increasing at batch %d", i)
		}
	}
}

// Identical snapshots are not forwarded as new frames.
func TestContinuousMode_DedupsUnchangedSnapshots(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	fb.static = true
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "dup"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 5 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	waitFor(2*time.Second, func() bool { return stats.PollsDuplicate.Load() >= 10 })
	cancel()

	if fa.batchCount() != 1 {
		t.Errorf("batches = %d, want exactly 1 (all later snapshots identical)", fa.batchCount())
	}
	if stats.PollsDuplicate.Load() < 10 {
		t.Errorf("duplicate polls = %d", stats.PollsDuplicate.Load())
	}
}

func TestContinuousMode_DryRun(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("dryrun-sess", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "dryrun-match"))

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.PollInterval = 10 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	if !waitFor(2*time.Second, func() bool { return stats.TotalFramesForwarded.Load() >= 4 }) {
		t.Errorf("dry-run frames = %d, want >= 4", stats.TotalFramesForwarded.Load())
	}
	cancel()
	if stats.TotalBatchesSent.Load() != 0 || stats.TotalSendFailures.Load() != 0 {
		t.Errorf("dry-run sent=%d failures=%d, want 0", stats.TotalBatchesSent.Load(), stats.TotalSendFailures.Load())
	}
}

func TestContinuousMode_AllMatchesSkippedBadEndpoints(t *testing.T) {
	nakama := startFakeNakama(t, fakeNakamaMatchList(
		fakeMatch{MatchID: "bad-1", Mode: "arena", Level: "a", Size: 2, EndpointStr: "10.0.0.1:not-an-ip:6721"},
		fakeMatch{MatchID: "bad-2", Mode: "arena", Level: "a", Size: 4, NoBroadcaster: true},
	))
	cfg := testConfig(nakama.URL, 6721)
	cfg.PollInterval = 50 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	_, done := runBridgeAsync(cfg, stats, 200*time.Millisecond)
	<-done

	if stats.PollersStarted.Load() != 0 || stats.MatchesSkippedNoEP.Load() < 1 || stats.TotalFramesForwarded.Load() != 0 {
		t.Errorf("started=%d skipped=%d forwarded=%d", stats.PollersStarted.Load(), stats.MatchesSkippedNoEP.Load(), stats.TotalFramesForwarded.Load())
	}
}

// F85 / F177: post_match ends the match once, idles without churn, and a
// rematch (new sessionid, same match_id) resumes with a new match_start and a
// frame index that keeps increasing.
func TestContinuousMode_PostMatchIdlesThenRematch(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("game-A", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "lobby-match"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.IdleInterval = 20 * time.Millisecond
	cfg.DiscoveryInterval = 50 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 10*time.Second)
	defer cancel()

	if !waitForBatches(fa, 3, 3*time.Second) {
		t.Fatalf("batches = %d", fa.batchCount())
	}
	batchesBefore := fa.batchCount()
	maxIdxBefore := fa.lastBatch().Frames[0].FrameIndex

	// Game A ends.
	fb.set(fakeSessionJSON("game-A", "post_match", 2), 200)
	if !waitFor(3*time.Second, func() bool {
		ends := fa.controlsOf(model.ControlMatchEnd)
		return len(ends) == 1 && ends[0].Reason == string(stopPostMatch)
	}) {
		t.Fatalf("match_end(post_match) not received: %+v", fa.controlsOf(model.ControlMatchEnd))
	}
	settled := fa.batchCount()
	// Sit in post_match across several discovery cycles: no churn, no frames.
	time.Sleep(300 * time.Millisecond)
	if fa.batchCount() != settled {
		t.Errorf("batches kept flowing during post_match: %d -> %d", settled, fa.batchCount())
	}
	if stats.PollersStarted.Load() != 1 || stats.PollersStopped.Load() != 0 {
		t.Errorf("post_match churn: started=%d stopped=%d, want 1/0", stats.PollersStarted.Load(), stats.PollersStopped.Load())
	}
	if stats.PollerIdleTransitions.Load() != 1 {
		t.Errorf("idle transitions = %d, want 1", stats.PollerIdleTransitions.Load())
	}
	_ = batchesBefore

	// Rematch in the same lobby.
	fb.set(fakeSessionJSON("game-B", "playing", 2), 200)
	if !waitFor(3*time.Second, func() bool { return len(fa.controlsOf(model.ControlMatchStart)) == 2 }) {
		t.Fatalf("second match_start not received")
	}
	if !waitFor(3*time.Second, func() bool { return fa.batchCount() >= settled+3 }) {
		t.Fatalf("frames did not resume after rematch")
	}
	for _, b := range fa.allBatches()[settled:] {
		if b.Frames[0].FrameIndex <= maxIdxBefore {
			t.Fatalf("frame index restarted after rematch: %d <= %d", b.Frames[0].FrameIndex, maxIdxBefore)
		}
	}
	if stats.PollersStarted.Load() != 1 {
		t.Errorf("rematch created a new poller (started=%d)", stats.PollersStarted.Load())
	}
}

// A lobby stuck in post_match is eventually released.
func TestContinuousMode_PostMatchGivesUp(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("game-A", "post_match", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "stuck"))

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.PollInterval = 5 * time.Millisecond
	cfg.IdleInterval = 5 * time.Millisecond
	cfg.IdleGiveUp = 50 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	if !waitFor(2*time.Second, func() bool { return stats.PollersStopped.Load() >= 1 }) {
		t.Fatalf("poller never gave up on post_match")
	}
	cancel()
	if stats.TotalFramesForwarded.Load() != 0 {
		t.Errorf("post_match session produced %d frames", stats.TotalFramesForwarded.Load())
	}
}

// F0/F22: the frame epoch survives a poller restart for the same match_id.
func TestContinuousMode_FrameEpochSurvivesPollerRestart(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-1", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "restart"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 5 * time.Millisecond
	cfg.IdleInterval = 5 * time.Millisecond
	cfg.IdleGiveUp = 30 * time.Millisecond
	cfg.DiscoveryInterval = 50 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 10*time.Second)
	defer cancel()

	if !waitForBatches(fa, 3, 3*time.Second) {
		t.Fatalf("batches = %d", fa.batchCount())
	}
	// Broadcaster breaks long enough for the poller to give up and be recreated.
	fb.set(`{"error":"gone"}`, 500)
	if !waitFor(3*time.Second, func() bool { return stats.PollersStopped.Load() >= 1 }) {
		t.Fatalf("poller did not stop on persistent 500s")
	}
	if !waitFor(3*time.Second, func() bool {
		ends := fa.controlsOf(model.ControlMatchEnd)
		return len(ends) >= 1 && ends[0].Reason == string(stopBroadcasterError)
	}) {
		t.Errorf("match_end(broadcaster_error) not received: %+v", fa.controlsOf(model.ControlMatchEnd))
	}
	countAtStop := fa.batchCount()
	fb.set(fakeSessionJSON("sess-1", "playing", 2), 200)
	if !waitFor(3*time.Second, func() bool { return stats.PollersStarted.Load() >= 2 }) {
		t.Fatalf("replacement poller not started")
	}
	if !waitFor(3*time.Second, func() bool { return fa.batchCount() >= countAtStop+3 }) {
		t.Fatalf("frames did not resume after restart")
	}

	prev := -1
	prevTS := -1.0
	for i, b := range fa.allBatches() {
		idx := b.Frames[0].FrameIndex
		if idx <= prev {
			t.Fatalf("batch %d: frame index %d <= previous %d after poller restart (rows would be silently dropped)", i, idx, prev)
		}
		if b.Frames[0].Timestamp < prevTS {
			t.Fatalf("batch %d: timestamp went backwards", i)
		}
		prev, prevTS = idx, b.Frames[0].Timestamp
	}
}

// F84: persistent non-200 backs off to idle instead of logging forever, and
// recovers when the broadcaster comes back.
func TestContinuousMode_Non200GoesIdleAndRecovers(t *testing.T) {
	fb := newFakeBroadcaster(t, `{"error":"not in session"}`, 404)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "idle404"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 2 * time.Millisecond
	cfg.IdleInterval = 20 * time.Millisecond
	cfg.IdleGiveUp = time.Minute
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 10*time.Second)
	defer cancel()

	if !waitFor(3*time.Second, func() bool { return stats.PollerIdleTransitions.Load() >= 1 }) {
		t.Fatalf("poller never went idle; failures=%d", stats.TotalPollFailures.Load())
	}
	// Idle polling is slow: far fewer than 1 poll per 2 ms.
	before := stats.TotalPolls.Load()
	time.Sleep(200 * time.Millisecond)
	if delta := stats.TotalPolls.Load() - before; delta > 30 {
		t.Errorf("idle poller still polling fast: %d polls in 200ms", delta)
	}
	if stats.PollersStopped.Load() != 0 {
		t.Errorf("poller stopped instead of idling")
	}
	fb.set(fakeSessionJSON("sess", "playing", 2), 200)
	if !waitForBatches(fa, 2, 3*time.Second) {
		t.Fatalf("poller did not recover from idle: batches=%d", fa.batchCount())
	}
}

func TestContinuousMode_BroadcasterRecovery(t *testing.T) {
	var reqCount atomic.Int64
	broadcaster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		if n <= 5 {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":"temporary failure"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, varySession(fakeSessionJSON("recovery-sess", "playing", 2), n))
	}))
	defer broadcaster.Close()
	bPort := extractTestPort(t, broadcaster.URL)
	nakama := startFakeNakama(t, matchesFor(bPort, "recovery-match"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, bPort)
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 10 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	ok := waitForBatches(fa, 2, 3*time.Second)
	cancel()
	if !ok {
		t.Fatalf("no batches after recovery")
	}
	if stats.TotalPollFailures.Load() != 5 {
		t.Errorf("poll failures = %d, want 5", stats.TotalPollFailures.Load())
	}
	if stats.PollerIdleTransitions.Load() != 0 {
		t.Errorf("5 failures must not trigger idle")
	}
}

func TestContinuousMode_MatchAppearsAfterEmptyDiscovery(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("late-sess", "playing", 2), 200)
	nakama, nakamaResp := startSwitchableNakama(t, `{"matches":[]}`)
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 15 * time.Millisecond
	cfg.DiscoveryInterval = 50 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	time.Sleep(150 * time.Millisecond)
	if stats.PollersStarted.Load() != 0 {
		t.Fatalf("poller started before any match existed")
	}
	nakamaResp.Store(matchesFor(fb.port(t), "late-match"))
	ok := waitForBatches(fa, 1, 3*time.Second)
	cancel()
	if !ok {
		t.Fatal("no batch after the match appeared")
	}
	if fa.lastBatch().MatchID != "late-match" {
		t.Errorf("batch match_id = %q", fa.lastBatch().MatchID)
	}
}

func TestContinuousMode_MatchDisappearsFromDiscovery(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("vanish-sess", "playing", 2), 200)
	nakama, nakamaResp := startSwitchableNakama(t, matchesFor(fb.port(t), "vanish-match"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 15 * time.Millisecond
	cfg.DiscoveryInterval = 50 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	if !waitForBatches(fa, 2, 3*time.Second) {
		t.Fatalf("no batches before the match vanished")
	}
	nakamaResp.Store(`{"matches":[]}`)
	if !waitFor(3*time.Second, func() bool { return stats.PollersStopped.Load() >= 1 }) {
		t.Fatalf("poller was not cancelled after the match left Nakama")
	}
	if !waitFor(2*time.Second, func() bool {
		ends := fa.controlsOf(model.ControlMatchEnd)
		return len(ends) == 1 && ends[0].Reason == string(stopCancelled)
	}) {
		t.Errorf("match_end(cancelled) not received: %+v", fa.controlsOf(model.ControlMatchEnd))
	}
	stopped := fa.batchCount()
	time.Sleep(100 * time.Millisecond)
	cancel()
	if fa.batchCount() != stopped {
		t.Errorf("frames kept flowing after cancel: %d -> %d", stopped, fa.batchCount())
	}
}

func TestContinuousMode_NakamaDiscoveryFailureThenRecovery(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("nakama-fail-sess", "playing", 2), 200)
	nakama, nakamaResp := startSwitchableNakama(t, "not valid json at all")
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 15 * time.Millisecond
	cfg.DiscoveryInterval = 50 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	time.Sleep(200 * time.Millisecond)
	nakamaResp.Store(matchesFor(fb.port(t), "nakama-recovery"))
	ok := waitForBatches(fa, 1, 3*time.Second)
	cancel()
	if !ok || stats.PollersStarted.Load() < 1 || stats.MatchesDiscovered.Load() < 1 {
		t.Errorf("recovery failed: batches=%d started=%d discovered=%d", fa.batchCount(), stats.PollersStarted.Load(), stats.MatchesDiscovered.Load())
	}
}

func TestContinuousMode_ZeroFramesThenRecovery(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("zf-sess", "playing", 0), 200)
	// 0 players is structurally invalid (no roster) -> counted as failures
	// but must not stop the poller; a valid roster then produces frames.
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "zf-match"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.IdleInterval = 10 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	waitFor(2*time.Second, func() bool { return stats.TotalPolls.Load() >= 5 })
	fb.set(fakeSessionJSON("zf-sess", "playing", 3), 200)
	ok := waitForBatches(fa, 1, 3*time.Second)
	cancel()
	if !ok || stats.TotalFramesForwarded.Load() < 3 {
		t.Errorf("no recovery: batches=%d forwarded=%d", fa.batchCount(), stats.TotalFramesForwarded.Load())
	}
}

// F79: a /session that belongs to another instance stops the poller.
func TestContinuousMode_SessionMismatchStopsPoller(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("aaaaaaaa-0000-0000-0000-000000000001", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "bbbbbbbb-0000-0000-0000-000000000002.node"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.SessionCheck = sessionCheckStrict
	cfg.PollInterval = 10 * time.Millisecond
	cfg.DiscoveryInterval = 30 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	ok := waitFor(2*time.Second, func() bool { return stats.PollersStopped.Load() >= 1 })
	// Several discovery cycles pass; the parked match must not be recreated.
	waitFor(300*time.Millisecond, func() bool { return stats.MatchesSkippedMismatch.Load() >= 3 })
	cancel()
	if !ok {
		t.Fatal("poller did not stop on session mismatch")
	}
	if fa.batchCount() != 0 || stats.SessionMismatches.Load() != 1 {
		t.Errorf("batches=%d mismatches=%d", fa.batchCount(), stats.SessionMismatches.Load())
	}
	if stats.PollersStarted.Load() != 1 || stats.MatchesSkippedMismatch.Load() < 3 {
		t.Errorf("mismatch churn: started=%d skipped=%d", stats.PollersStarted.Load(), stats.MatchesSkippedMismatch.Load())
	}

	// warn mode keeps forwarding.
	cfg.SessionCheck = sessionCheckWarn
	stats = &bridgeStats{startTime: time.Now(), mode: "continuous"}
	cancel, _ = runBridgeAsync(cfg, stats, 5*time.Second)
	ok = waitForBatches(fa, 2, 3*time.Second)
	cancel()
	if !ok {
		t.Errorf("warn mode should still forward frames")
	}
}

// F82: a bridge started before the ingest server keeps running and forwards
// once the server appears; it never exits on the initial connect failure.
func TestContinuousMode_InitialConnectFailureIsNotFatal(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "late-ingest"))

	// Reserve a port, then release it so the first dial fails.
	addr := refusedAddr(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = "ws://" + addr + "/telemetry"
	cfg.PollInterval = 10 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 10*time.Second)
	defer cancel()
	if !waitFor(2*time.Second, func() bool { return stats.PollersStarted.Load() >= 1 && stats.TotalFramesDropped.Load() >= 2 }) {
		t.Fatalf("bridge did not keep polling after the initial connect failure: started=%d dropped=%d",
			stats.PollersStarted.Load(), stats.TotalFramesDropped.Load())
	}
	if stats.TotalFramesForwarded.Load() != 0 {
		t.Errorf("frames were counted as forwarded without a server: %d", stats.TotalFramesForwarded.Load())
	}

	// Ingest comes up on the reserved address; the bridge reconnects.
	fa := &fakeAnticheat{}
	mux := http.NewServeMux()
	mux.Handle("/telemetry", websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		_ = websocket.JSON.Send(conn, model.ControlMessage{Type: model.ControlHello, Auth: "ok"})
		for {
			var raw json.RawMessage
			if err := websocket.JSON.Receive(conn, &raw); err != nil {
				return
			}
			var batch FrameBatch
			if json.Unmarshal(raw, &batch) == nil && len(batch.Frames) > 0 {
				fa.mu.Lock()
				fa.batches = append(fa.batches, batch)
				fa.mu.Unlock()
				_ = websocket.JSON.Send(conn, model.ControlMessage{Type: model.ControlAck, Accepted: len(batch.Frames)})
			}
		}
	}))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("could not re-bind %s: %v", addr, err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	if !waitFor(6*time.Second, func() bool { return stats.TotalFramesForwarded.Load() >= 2 }) {
		t.Fatalf("bridge did not reconnect once the ingest server appeared: forwarded=%d failures=%d",
			stats.TotalFramesForwarded.Load(), stats.TotalSendFailures.Load())
	}
}

// Modes and allowlist filters are applied in continuous mode.
func TestContinuousMode_ModeAndAllowlistFilters(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess", "playing", 2), 200)
	nakama := startFakeNakama(t, fakeNakamaMatchList(
		fakeMatch{MatchID: "arena", Mode: "echo_arena_private", Size: 2, EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", fb.port(t))},
		fakeMatch{MatchID: "combat", Mode: "echo_combat", Size: 2, EndpointStr: fmt.Sprintf("10.0.0.1:127.0.0.1:%d", fb.port(t))},
		fakeMatch{MatchID: "foreign", Mode: "echo_arena", Size: 2, EndpointStr: "10.0.0.1:203.0.113.9:6721"},
	))
	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.Modes = "echo_arena"
	cfg.BroadcasterAllowlist = "127.0.0.0/8"
	cfg.PollInterval = 10 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	waitFor(2*time.Second, func() bool { return stats.TotalFramesForwarded.Load() >= 2 })
	cancel()
	if stats.PollersStarted.Load() != 1 {
		t.Errorf("pollers started = %d, want 1 (only the allowed arena match)", stats.PollersStarted.Load())
	}
	if stats.MatchesSkippedMode.Load() != 1 || stats.MatchesSkippedAllow.Load() != 1 {
		t.Errorf("skipped mode=%d allow=%d, want 1/1", stats.MatchesSkippedMode.Load(), stats.MatchesSkippedAllow.Load())
	}
}

// --- Filename / manifest helpers ---

func TestSanitizeForFilename(t *testing.T) {
	tests := []struct{ input, want string }{
		{"simple-match", "simple-match"},
		{"match/with/slashes", "match_with_slashes"},
		{"match:with:colons", "match_with_colons"},
		{"match with spaces", "match_with_spaces"},
		{"match.with.dots", "match_with_dots"},
		{"node1.match-abc-123.server", "node1_match-abc-123_server"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := sanitizeForFilename(tt.input); got != tt.want {
			t.Errorf("sanitizeForFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
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

func TestOnce_ManifestContent(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("manifest-sess", "playing", 3), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "manifest-match"))
	fa := startFakeAnticheat(t)

	dumpDir := t.TempDir()
	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.DumpDir = dumpDir
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	if err := doOnce(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("doOnce failed: %v", err)
	}
	m := readManifest(t, dumpDir, "manifest-match")
	if m.Mode != "once" || m.SelectedMatchID != "manifest-match" || m.SessionID != "manifest-sess" {
		t.Errorf("manifest = %+v", m)
	}
	if m.MappedFrames != 3 || !m.AnticheatSendAttempted || !m.AnticheatSendSucceeded || m.DryRun || m.PlayersSeen != 3 {
		t.Errorf("manifest = %+v", m)
	}
	if m.FramesAcked != 3 || m.FramesRejected != 0 {
		t.Errorf("manifest acked=%d rejected=%d", m.FramesAcked, m.FramesRejected)
	}
	if m.Timestamp == "" || !strings.HasPrefix(m.SamplePlayerID, "echovr:") {
		t.Errorf("manifest = %+v", m)
	}
}

func TestProbe_ZeroFrameSession(t *testing.T) {
	zeroPositionSession := `{
		"sessionid":"zero-sess","match_type":"Echo_Arena","map_name":"mpl_arena_a","game_status":"playing",
		"game_clock":240.0,"game_clock_display":"4:00","private_match":false,"client_name":"test",
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
		"blue_points":0,"orange_points":0
	}`
	fb := newFakeBroadcaster(t, zeroPositionSession, 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "zero-frame-match"))

	dumpDir := t.TempDir()
	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.DumpDir = dumpDir
	stats := &bridgeStats{startTime: time.Now(), mode: "probe"}

	if err := doProbe(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("probe should succeed even with zero frames: %v", err)
	}
	m := readManifest(t, dumpDir, "zero-frame-match")
	if m.MappedFrames != 0 || m.PlayersSeen != 1 || m.SessionID != "zero-sess" {
		t.Errorf("manifest = %+v", m)
	}
}

func TestOnce_SendFailure_WritesManifest(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("fail-sess", "playing", 2), 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "fail-match"))

	dumpDir := t.TempDir()
	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = "ws://" + refusedAddr(t) + "/nonexistent" // will fail to connect
	cfg.DumpDir = dumpDir
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	if err := doOnce(context.Background(), cfg, stats, testLogger()); err == nil {
		t.Fatal("expected error from connect failure")
	}
	m := readManifest(t, dumpDir, "fail-match")
	if m.SelectedMatchID != "fail-match" || m.MappedFrames != 2 || m.AnticheatSendSucceeded || m.SessionID != "fail-sess" {
		t.Errorf("manifest = %+v", m)
	}
	if !strings.Contains(m.SendSkipReason, "connect failed") {
		t.Errorf("send_skip_reason = %q, want to contain 'connect failed'", m.SendSkipReason)
	}
	for _, f := range []string{"session_raw.json", "mapped_frames.json"} {
		if _, err := os.Stat(dumpDir + "/" + dumpFilename("fail-match", f)); err != nil {
			t.Errorf("%s should exist: %v", f, err)
		}
	}
}

func TestOnce_ZeroFrames_WritesManifestAndFrames(t *testing.T) {
	zeroSession := strings.Replace(fakeSessionJSON("zero-once-sess", "pre_match", 1), `"position":[0.0,1.6,3.0]`, `"position":[0,0,0]`, -1)
	fb := newFakeBroadcaster(t, zeroSession, 200)
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "zero-once"))

	dumpDir := t.TempDir()
	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.DumpDir = dumpDir
	stats := &bridgeStats{startTime: time.Now(), mode: "once"}

	if err := doOnce(context.Background(), cfg, stats, testLogger()); err != nil {
		t.Fatalf("doOnce should succeed with zero frames: %v", err)
	}
	m := readManifest(t, dumpDir, "zero-once")
	if m.MappedFrames != 0 || m.AnticheatSendAttempted || m.GameStatus != "pre_match" {
		t.Errorf("manifest = %+v", m)
	}
	framesData, err := os.ReadFile(dumpDir + "/" + dumpFilename("zero-once", "mapped_frames.json"))
	if err != nil || len(framesData) == 0 {
		t.Errorf("mapped frames file should exist and be non-empty: %v", err)
	}
}

// A snapshot whose bytes differ but whose game state is unchanged (the
// broadcaster ticks slower than the poll) is "no new state" per the mapper's
// change detection: not forwarded, not counted as a zero-frame failure, and
// visible as polls_duplicate_state.
func TestContinuousMode_DedupsUnchangedGameState(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess-state", "playing", 2), 200)
	fb.bytesOnly = true
	nakama := startFakeNakama(t, matchesFor(fb.port(t), "dupstate"))
	fa := startFakeAnticheat(t)

	cfg := testConfig(nakama.URL, fb.port(t))
	cfg.AnticheatURL = fa.wsURL()
	cfg.PollInterval = 5 * time.Millisecond
	stats := &bridgeStats{startTime: time.Now(), mode: "continuous"}

	cancel, _ := runBridgeAsync(cfg, stats, 5*time.Second)
	waitFor(2*time.Second, func() bool { return stats.PollsDuplicateState.Load() >= 10 })
	cancel()

	if fa.batchCount() != 1 {
		t.Errorf("batches = %d, want exactly 1 (game state never changed)", fa.batchCount())
	}
	if stats.PollsDuplicateState.Load() < 10 {
		t.Errorf("duplicate-state polls = %d", stats.PollsDuplicateState.Load())
	}
	if stats.PollsDuplicate.Load() != 0 {
		t.Errorf("bodies differed byte-wise; byte dedup should not have fired: %d", stats.PollsDuplicate.Load())
	}
	// The mapped frames carry the mapper's spectator exclusion count only
	// through stats; spectators never reach the batch.
	for _, b := range fa.allBatches() {
		for _, f := range b.Frames {
			if f.Team != "blue" && f.Team != "orange" {
				t.Errorf("frame for non-team player forwarded: %+v", f.PlayerID)
			}
		}
	}
}
