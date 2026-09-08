package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBroadcasterRedirectNeverReachesAnotherDestination(t *testing.T) {
	var reached atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		fmt.Fprint(w, fakeSessionJSON("M1", "playing", 1))
	}))
	defer target.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			advertised := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/private-service", status)
			}))
			defer advertised.Close()
			cfg := &BridgeConfig{APIPort: extractTestPort(t, advertised.URL), BroadcasterAllowlist: "127.0.0.1"}
			match := DiscoveredMatch{MatchID: "M1", BroadcasterIP: "127.0.0.1"}
			// An explicitly allowed private address remains usable; only the
			// subsequent redirect is refused, even to another port on that IP.
			if got := filterMatches([]DiscoveredMatch{match}, cfg, &bridgeStats{}, testLogger()); len(got) != 1 {
				t.Fatal("explicit development broadcaster was rejected")
			}
			if _, _, _, _, err := fetchAndMap(match, cfg, testLogger()); err == nil {
				t.Fatal("probe/once followed a broadcaster redirect")
			}
			p := newMatchPoller(match, cfg, nil, &bridgeStats{}, &frameEpoch{}, func() {}, testLogger())
			outcome, _, code, err := p.fetch(context.Background())
			if outcome != pollNon200 || code != status || err == nil {
				t.Fatalf("continuous poll redirect: outcome=%v code=%d err=%v", outcome, code, err)
			}
			if reached.Load() != 0 {
				t.Fatal("redirect destination received a request")
			}
		})
	}
}

func TestWSSenderBoundsHTTPUpgradeAndCancellation(t *testing.T) {
	for _, useParentDeadline := range []bool{false, true} {
		t.Run(fmt.Sprint(useParentDeadline), func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
			defer srv.Close()
			defer close(release)
			cfg := senderConfig("ws" + strings.TrimPrefix(srv.URL, "http") + "/telemetry")
			cfg.DialTimeout = 80 * time.Millisecond
			ctx := context.Background()
			if useParentDeadline {
				cfg.DialTimeout = 5 * time.Second
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 80*time.Millisecond)
				defer cancel()
			}
			s, err := newWSSender(cfg, &bridgeStats{}, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if err := s.connect(ctx); err == nil {
				t.Fatal("stalled HTTP upgrade unexpectedly connected")
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("HTTP upgrade was not bounded: %v", elapsed)
			}
		})
	}
}

func TestNakamaRedirectsNeverForwardCredentialsOrRefreshBody(t *testing.T) {
	var reached atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		fmt.Fprint(w, `{"token":"unexpected"}`)
	}))
	defer target.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/capture", status)
			}))
			defer origin.Close()
			cfg := &BridgeConfig{NakamaURL: origin.URL, NakamaAuthMode: nakamaAuthBasic, NakamaServerKey: "test-secret"}
			if _, err := newDiscoverer(cfg, &bridgeStats{}, testLogger()).discover(context.Background()); err == nil {
				t.Fatal("discovery followed redirect")
			}
			auth := newNakamaAuth(cfg, testLogger())
			if _, err := auth.postSession(context.Background(), origin.URL+"/refresh", []byte(`{"token":"test-refresh-secret"}`)); err == nil {
				t.Fatal("session request followed redirect")
			}
			if reached.Load() != 0 {
				t.Fatal("redirect destination received discovery credentials or refresh body")
			}
		})
	}
}
