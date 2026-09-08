package ingest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"golang.org/x/net/websocket"
)

func TestConcurrentConnectionAdmissionIsBounded(t *testing.T) {
	cfg := testServerConfig()
	cfg.MaxConnectionsPerServer = 3
	s, url, _ := startServer(t, cfg, &fakeHandler{})
	type result struct {
		conn *websocket.Conn
		msg  model.ControlMessage
		err  error
	}
	results := make(chan result, 30)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			wc, _ := websocket.NewConfig(url, "http://localhost/")
			wc.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
			conn, err := websocket.DialConfig(wc)
			res := result{conn: conn, err: err}
			if err == nil {
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				res.err = websocket.JSON.Receive(conn, &res.msg)
			}
			results <- res
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted, refused := 0, 0
	for res := range results {
		if res.conn != nil {
			defer res.conn.Close()
		}
		if res.err != nil {
			t.Fatal(res.err)
		}
		switch {
		case res.msg.Type == model.ControlHello:
			accepted++
		case res.msg.Type == model.ControlError && res.msg.Reason == "too_many_connections":
			refused++
		default:
			t.Fatalf("unexpected admission response: %+v", res.msg)
		}
	}
	if accepted != 3 || refused != 27 || s.activeConns.Load() != 3 {
		t.Fatalf("admission exceeded cap: accepted=%d refused=%d active=%d", accepted, refused, s.activeConns.Load())
	}
}

func TestBookkeepingIdentityCardinalityIsBounded(t *testing.T) {
	s := NewServer(testServerConfig(), &fakeHandler{}, quietLogger())
	for i := 0; i < maxRateLimiterKeys; i++ {
		if !s.checkRateLimit("match", fmt.Sprint(i)) {
			t.Fatalf("premature capacity rejection at %d", i)
		}
	}
	if s.checkRateLimit("match", "overflow") || s.RateLimiterCapacityRejected.Load() != 1 || len(s.playerRates) != maxRateLimiterKeys {
		t.Fatal("rate bookkeeping grew beyond cap")
	}
	if !s.checkRateLimit("match", "0") {
		t.Fatal("capacity exhaustion removed an existing player's budget")
	}
	for _, rl := range s.playerRates {
		rl.lastFrame = time.Now().Add(-6 * time.Minute)
	}
	for i := 0; i < 2*maxWarningKeys; i++ {
		s.warnThrottled(fmt.Sprint(i), "test")
	}
	if len(s.warnCounts) > maxWarningKeys || s.WarningKeysCoalesced.Load() == 0 {
		t.Fatal("warning bookkeeping did not coalesce overflow")
	}
	s.CleanupStaleRateLimiters()
	if len(s.playerRates) != 0 || len(s.warnCounts) != 0 || !s.checkRateLimit("match", "overflow") {
		t.Fatal("expired bookkeeping capacity did not recover")
	}
}

func TestRateLimitIdentityHasNoDelimiterCollision(t *testing.T) {
	cfg := testServerConfig()
	cfg.MaxFrameRatePerPlayer = 1
	s := NewServer(cfg, &fakeHandler{}, quietLogger())
	if !s.checkRateLimit("match:part", "player") || !s.checkRateLimit("match", "part:player") || len(s.playerRates) != 2 {
		t.Fatal("different match/player pairs shared a rate budget")
	}
}

type incompleteHealthHandler struct{ fakeHandler }

func (*incompleteHealthHandler) IncompleteAnalysisCount() int { return 2 }

func TestHealthReportsOnlyBoundedIncompleteAnalysisCount(t *testing.T) {
	_, url, _ := startServer(t, testServerConfig(), &incompleteHealthHandler{})
	healthURL := strings.TrimSuffix(strings.Replace(url, "ws://", "http://", 1), "/telemetry") + "/health"
	resp, err := http.Get(healthURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "degraded" || body["analysis_incomplete_matches"] != float64(2) {
		t.Fatalf("health did not expose incomplete analysis: %v", body)
	}
}
