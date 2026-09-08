package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// TestOperatorUIFixture serves the actual embedded app and production handlers
// against a temporary synthetic database. It is NEVER included in desktop.exe.
//
// PowerShell: $env:NEVR_UI_FIXTURE='1'; go test ./cmd/desktop -run '^TestOperatorUIFixture$' -count=1 -v -timeout=30m
// App: http://127.0.0.1:19015/0123456789abcdef0123456789abcdef/
// GET http://127.0.0.1:19016/control returns synthetic counts/current fault.
// POST /control, header X-NEVR-Fixture-Control:test-only, JSON examples:
// {"path":"/api/match/SYN-UI-REVIEW-001/notes","method":"POST","status":503}
// {"path":"/api/event/","method":"POST","status":409}
// {"path":"/api/matches","status":403}
// {"path":"/api/match/","delay_ms":2500} or {} to clear the fault.
// POST /shutdown with the same header stops ONLY this fixture gracefully.
// NEVR_UI_FIXTURE_EMPTY=1 starts a real empty database for first-launch review.
// Original recordings, installed databases, network update checks, file watching,
// viewer launches and installer/open-folder actions are never used by this test.
func TestOperatorUIFixture(t *testing.T) {
	if os.Getenv("NEVR_UI_FIXTURE") != "1" {
		t.Skip("manual rendered UI fixture; opt in with NEVR_UI_FIXTURE=1")
	}
	cfg := config.DefaultConfig()
	cfg.General.LogLevel = "error"
	store, err := sqlite.NewStore(filepath.Join(t.TempDir(), "synthetic-ui-only.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	engine := replay.NewEngine(cfg, store)
	counts := map[string]int{"players": 0, "matches": 0, "events": 0, "notes": 0, "frames": 0}
	if os.Getenv("NEVR_UI_FIXTURE_EMPTY") != "1" {
		counts = seedOperatorUIFixture(t, engine)
	}
	s := newServer(engine, testToken)
	s.clipDir = t.TempDir()
	s.runtime.mu.Lock()
	s.runtime.settings.AutomaticUpdates = false
	s.runtime.settings.WatchEnabled = false
	s.runtime.mu.Unlock()
	t.Cleanup(func() {
		s.quitOnce.Do(func() { close(s.quit) })
		select {
		case <-s.runtime.stopped:
		case <-time.After(5 * time.Second):
			t.Error("fixture runtime did not stop")
		}
	})
	control := &operatorUIFixtureControl{counts: counts, stopped: make(chan struct{})}
	appListener, err := net.Listen("tcp", "127.0.0.1:19015")
	if err != nil {
		t.Fatal(err)
	}
	defer appListener.Close()
	controlListener, err := net.Listen("tcp", "127.0.0.1:19016")
	if err != nil {
		t.Fatal(err)
	}
	defer controlListener.Close()
	app := &http.Server{Handler: control.wrap(s.Handler()), ReadHeaderTimeout: 5 * time.Second}
	operator := &http.Server{Handler: desktopSafety(http.HandlerFunc(control.serveControl)), ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { _ = app.Close(); _ = operator.Close() })
	serveErrors := make(chan error, 2)
	go func() { serveErrors <- app.Serve(appListener) }()
	go func() { serveErrors <- operator.Serve(controlListener) }()
	t.Logf("SYNTHETIC UI FIXTURE ready: http://127.0.0.1:19015/%s/; counts=%v; control=http://127.0.0.1:19016/control", testToken, counts)
	select {
	case <-control.stopped:
	case <-s.Done():
	case <-t.Context().Done():
	case err := <-serveErrors:
		if err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	}
}

type operatorUIFixtureFault struct {
	Path    string `json:"path"`
	Method  string `json:"method"`
	Status  int    `json:"status"`
	DelayMS int    `json:"delay_ms"`
}

type operatorUIFixtureControl struct {
	mu      sync.RWMutex
	fault   operatorUIFixtureFault
	counts  map[string]int
	stopped chan struct{}
	once    sync.Once
}

func (c *operatorUIFixtureControl) serveControl(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/control" {
		c.mu.RLock()
		defer c.mu.RUnlock()
		writeJSON(w, 200, map[string]any{"fixture": "SYNTHETIC TEST DATA ONLY", "counts": c.counts, "fault": c.fault})
		return
	}
	if r.Method != http.MethodPost || r.Header.Get("X-NEVR-Fixture-Control") != "test-only" {
		writeError(w, 403, "fixture control requires POST and X-NEVR-Fixture-Control: test-only")
		return
	}
	if r.URL.Path == "/shutdown" {
		writeJSON(w, 200, map[string]bool{"stopped": true})
		c.once.Do(func() { close(c.stopped) })
		return
	}
	if r.URL.Path != "/control" {
		writeError(w, 404, "unknown fixture control")
		return
	}
	var fault operatorUIFixtureFault
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&fault); err != nil {
		writeError(w, 400, "invalid fixture fault")
		return
	}
	if (fault.Path != "" && !strings.HasPrefix(fault.Path, "/api/")) ||
		(fault.Status != 0 && fault.Status != 403 && fault.Status != 409 && fault.Status != 503) ||
		fault.DelayMS < 0 || fault.DelayMS > 10000 ||
		(fault.Method != "" && fault.Method != "GET" && fault.Method != "POST" && fault.Method != "DELETE") {
		writeError(w, 400, "use an /api/ path, status 403/409/503, and delay_ms between 0 and 10000")
		return
	}
	c.mu.Lock()
	c.fault = fault
	c.mu.Unlock()
	writeJSON(w, 200, map[string]any{"fixture": true, "fault": fault})
}

func (c *operatorUIFixtureControl) wrap(next http.Handler) http.Handler {
	return desktopSafety(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-NEVR-UI-Fixture", "synthetic-test-only")
		prefix := "/" + testToken
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			next.ServeHTTP(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, prefix)
		if operatorUIFixtureSideEffect(path, r.Method) {
			writeError(w, http.StatusServiceUnavailable, "Unavailable in the synthetic UI fixture: external launches, updates, file watching and uploads are disabled. No files were changed.")
			return
		}
		c.mu.RLock()
		fault := c.fault
		c.mu.RUnlock()
		if fault.Path != "" && strings.HasPrefix(path, fault.Path) && (fault.Method == "" || fault.Method == r.Method) {
			if fault.DelayMS > 0 {
				timer := time.NewTimer(time.Duration(fault.DelayMS) * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					return
				}
			}
			if fault.Status != 0 {
				writeError(w, fault.Status, "Synthetic UI fixture: %s. Clear the test fault and retry; your test input was not saved.", http.StatusText(fault.Status))
				return
			}
		}
		if path == "/" && r.Method == http.MethodGet {
			recorder := httptest.NewRecorder()
			next.ServeHTTP(recorder, r)
			for key, values := range recorder.Header() {
				w.Header()[key] = values
			}
			body := bytes.Replace(recorder.Body.Bytes(), []byte("<body>"), []byte(`<body><aside role="note" style="padding:8px 20px;border-bottom:1px solid currentColor;font:600 12px system-ui;text-align:center">SYNTHETIC TEST FIXTURE — no real players, recordings or verdicts. External actions are disabled.</aside>`), 1)
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(body)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func operatorUIFixtureSideEffect(path, method string) bool {
	if strings.HasPrefix(path, "/api/update") || path == "/api/replay-viewer" || strings.Contains(path, "/replay/") {
		return true
	}
	if method != http.MethodGet {
		return strings.HasPrefix(path, "/api/maintenance/") || strings.HasPrefix(path, "/api/watch/") ||
			strings.HasPrefix(path, "/api/recovery/") || path == "/api/analyze" || path == "/api/settings" ||
			strings.Contains(path, "/archive") || strings.Contains(path, "/restore-raw") ||
			strings.HasPrefix(path, "/api/blind-review/artifacts")
	}
	return false
}

func seedOperatorUIFixture(t *testing.T, engine *replay.Engine) map[string]int {
	t.Helper()
	ctx, store := context.Background(), engine.Store()
	const matchID, ticks = "SYN-UI-REVIEW-001", 360
	start := time.Date(2026, 9, 8, 18, 0, 0, 0, time.UTC)
	mc := &model.MatchContext{MatchID: matchID, Map: "mpl_arena_a", GameMode: "Echo Arena — synthetic UI fixture",
		StartTime: start, Duration: 24 * time.Second, Source: "replay", ReplayFile: "SYNTHETIC-review-workspace.echoreplay",
		TickRate: 15, Physics: model.DefaultPhysics(), TeamAssignments: map[string]string{}, PlayerNames: map[string]string{}}
	var frames []model.PlayerTelemetryFrame
	summary := &replay.MatchSummary{Version: replay.SummaryVersion, MatchID: matchID, GameMode: mc.GameMode,
		Map: mc.Map, StartTime: start, Ticks: ticks, DurationSeconds: 24, Teams: map[string]*replay.TeamSummary{}, ThrowSource: "holding"}
	for p := 0; p < 8; p++ {
		id := fmt.Sprintf("synthetic-ui-player-%02d", p+1)
		name := fmt.Sprintf("Synthetic Player %02d", p+1)
		if p == 0 {
			name = "Synthetic Player With An Exceptionally Long Display Name — <not HTML> & Replay Review"
		}
		team := []string{"blue", "orange"}[p/4]
		mc.PlayerIDs = append(mc.PlayerIDs, id)
		mc.PlayerNames[id], mc.TeamAssignments[id] = name, team
		rows := testutil.NewFrameBuilder(id).WithTeam(team).WithStartPos(model.Vec3{float64(p) - 4, 2, -20}).NormalMovingPlayer(ticks, 2)
		for i := range rows {
			velocity := model.Vec3{}
			if i > 0 {
				velocity = rows[i].Position.Sub(rows[i-1].Position).Scale(15)
			}
			rows[i].ReportedVelocity = &velocity
			rows[i].Observation.SessionID = matchID
			rows[i].Disc.Position = model.Vec3{0, 2, float64(i%40) / 2}
			rows[i].Disc.Speed = 7.5
			rows[i].Disc.Velocity = model.Vec3{0, 0, 7.5}
			if i >= 80 && i <= 100 {
				rows[i].ReportedVelocity, rows[i].Disc = nil, nil
			}
		}
		frames = append(frames, rows...)
		summary.Players = append(summary.Players, &replay.PlayerSummary{PlayerID: id, Name: name, Team: team,
			Frames: ticks, LastSeen: float64(ticks-1) / 15, PingAvg: 20, PingMax: 20})
	}
	if err := store.StoreMatchContext(ctx, mc, ticks); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreTelemetryFrames(ctx, matchID, frames); err != nil {
		t.Fatal(err)
	}
	// Coverage is actually computed from synthetic observations. Large findings
	// are explicitly seeded layout fixtures, not claims that these frames cheat.
	result, err := engine.NewPipeline().ProcessMatch(ctx, mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	var events []model.DetectionEvent
	for i := 0; i < 160; i++ {
		id, frame := mc.PlayerIDs[i%8], 10+(i*2)%330
		events = append(events, model.DetectionEvent{EventID: fmt.Sprintf("SYN-UI-EVENT-%03d", i+1), DetectorID: "THROW_001",
			DetectorVersion: "synthetic-ui-fixture", MatchID: matchID, PlayerID: id, FrameIndex: frame,
			FrameRangeStart: frame - 2, FrameRangeEnd: frame + 2, Timestamp: float64(frame) / 15,
			Severity: .4, Confidence: .5, IsShadow: true, MergedCount: 1,
			ObservedValue: "Synthetic layout finding — not a detector result or a player verdict",
			ExpectedRange: "Review fixture only; actual rule verification and independent evidence are unavailable.",
			CausalKey:     model.CausalKey{PlayerID: id, AnomalyType: "synthetic-ui-fixture"},
			Evidence:      model.ThrowEvidence{ReleaseSpeed: 19.2, ReleaseVelocity: model.Vec3{0, 0, 19.2}, EffectiveCap: 18.9, PingMs: 20}, StoredAt: start})
		if i < 100 {
			summary.Throws = append(summary.Throws, replay.ThrowEvent{PlayerID: id, Player: mc.PlayerNames[id], Team: mc.TeamAssignments[id],
				Time: float64(frame) / 15, Clock: fmt.Sprintf("00:%02d", frame/15), FrameIndex: frame, Speed: 12 + float64(i%7)})
		}
	}
	if _, err := store.WriteMatchAnalysis(ctx, sqlite.MatchAnalysisWrite{MatchID: matchID, Source: "synthetic-ui-fixture",
		Events: events, Coverage: result.PlayerCoverage}); err != nil {
		t.Fatal(err)
	}
	for p, id := range mc.PlayerIDs {
		if err := store.StoreReviewCase(ctx, model.ReviewCase{CaseID: fmt.Sprintf("SYN-UI-CASE-%02d", p+1), PlayerID: id, MatchID: matchID,
			TimestampStart: start, TimestampEnd: start.Add(mc.Duration), Severity: "low", RecommendedAction: "review_only",
			Status: model.CaseStatusPending, CreatedAt: start, Explanation: "Synthetic queue layout fixture; no cheating conclusion is asserted.",
			DetectorsTriggered: []model.TriggeredDetector{{DetectorID: "THROW_001", EventID: events[p].EventID, FrameIndex: events[p].FrameIndex,
				Details: "Synthetic evidence fixture for long queues, names, keyboard focus and review controls."}}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		if _, err := store.StoreInvestigationNote(ctx, sqlite.InvestigationNote{NoteID: fmt.Sprintf("SYN-UI-NOTE-%02d", i+1), MatchID: matchID,
			Kind: "note", FrameIndex: i * 40, PlayerID: mc.PlayerIDs[i], Body: "Synthetic reviewer note: inspect nearby frames and missing source measurements before drawing any conclusion. " + strings.Repeat("Long note text exercises wrapping and preserves room for the controls. ", i)}); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StoreMatchSummaryJSON(ctx, summary.Meta(), doc); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 30; i++ {
		history := *mc
		history.MatchID, history.ReplayFile = fmt.Sprintf("SYN-UI-HISTORY-%03d", i), fmt.Sprintf("SYNTHETIC-history-%03d.echoreplay", i)
		history.StartTime = start.Add(-time.Duration(i) * time.Hour)
		if err := store.StoreMatchContext(ctx, &history, 0); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]int{"players": 8, "matches": 30, "events": 160, "notes": 6, "frames": len(frames), "throws": 100, "cases": 8}
}

func TestOperatorUIFixtureBlocksSideEffects(t *testing.T) {
	for _, path := range []string{"/api/update", "/api/update/install", "/api/replay-viewer", "/api/match/test/replay/frame/1",
		"/api/maintenance/open-data-folder", "/api/maintenance/support-bundle", "/api/watch/scan", "/api/settings", "/api/analyze"} {
		if !operatorUIFixtureSideEffect(path, "POST") {
			t.Errorf("fixture permits side effect %s", path)
		}
	}
	if operatorUIFixtureSideEffect("/api/match/test/notes", "POST") || operatorUIFixtureSideEffect("/api/settings", "GET") {
		t.Fatal("fixture blocks ordinary isolated notes or settings inspection")
	}
}

func TestOperatorUIFixtureFaultsAreExplicitAndRecoverable(t *testing.T) {
	c := &operatorUIFixtureControl{stopped: make(chan struct{})}
	var called bool
	wrapped := c.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		writeJSON(w, 200, map[string]bool{"saved": true})
	}))
	setFault := func(body string, authorized bool) int {
		t.Helper()
		r := httptest.NewRequest("POST", "/control", strings.NewReader(body))
		if authorized {
			r.Header.Set("X-NEVR-Fixture-Control", "test-only")
		}
		w := httptest.NewRecorder()
		c.serveControl(w, r)
		return w.Code
	}
	if got := setFault(`{"path":"/api/match/","status":503}`, false); got != 403 {
		t.Fatalf("unauthorized control status=%d", got)
	}
	for _, status := range []int{403, 409, 503} {
		if got := setFault(fmt.Sprintf(`{"path":"/api/match/","method":"POST","status":%d}`, status), true); got != 200 {
			t.Fatalf("set fault status=%d", got)
		}
		called = false
		w := httptest.NewRecorder()
		wrapped.ServeHTTP(w, httptest.NewRequest("POST", "/"+testToken+"/api/match/test/notes", nil))
		if w.Code != status || called || !strings.Contains(w.Body.String(), "Synthetic UI fixture") {
			t.Fatalf("injected %d: response=%d called=%t body=%s", status, w.Code, called, w.Body.String())
		}
	}
	if got := setFault(`{}`, true); got != 200 {
		t.Fatalf("clear fault status=%d", got)
	}
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, httptest.NewRequest("POST", "/"+testToken+"/api/match/test/notes", nil))
	if !called || w.Code != 200 {
		t.Fatalf("cleared fault did not restore handler: status=%d called=%t", w.Code, called)
	}
	if got := setFault(`{"path":"/api/match/","delay_ms":10001}`, true); got != 400 {
		t.Fatalf("unbounded delay accepted: %d", got)
	}
}
