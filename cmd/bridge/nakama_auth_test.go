package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// makeJWT builds an unsigned JWT-shaped token with the given expiry.
func makeJWT(exp time.Time, nonce string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "uid": "bridge", "n": nonce})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// fakeNakamaWithAuth models Nakama's real gateway: Basic server-key auth is
// accepted only on authenticate/refresh, /v2/match needs a valid session.
type fakeNakamaWithAuth struct {
	srv          *httptest.Server
	serverKey    string
	tokenTTL     time.Duration
	authCalls    atomic.Int64
	refreshCalls atomic.Int64
	listCalls    atomic.Int64
	validTokens  map[string]bool
	lastDeviceID string
	lastUsername string
	now          func() time.Time
}

func startFakeNakamaWithAuth(t *testing.T, serverKey string, ttl time.Duration) *fakeNakamaWithAuth {
	t.Helper()
	f := &fakeNakamaWithAuth{serverKey: serverKey, tokenTTL: ttl, validTokens: map[string]bool{}, now: time.Now}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(serverKey+":"))
	issue := func(w http.ResponseWriter, nonce string) {
		tok := makeJWT(f.now().Add(f.tokenTTL), nonce)
		f.validTokens[tok] = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":%q,"refresh_token":%q,"created":true}`, tok, "refresh-"+nonce)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/account/authenticate/device":
			if r.Header.Get("Authorization") != basic {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"code":16,"message":"Server key invalid"}`)
				return
			}
			var body struct {
				ID string `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastDeviceID = body.ID
			f.lastUsername = r.URL.Query().Get("username")
			n := f.authCalls.Add(1)
			issue(w, fmt.Sprintf("auth%d", n))
		case r.URL.Path == "/v2/account/session/refresh":
			if r.Header.Get("Authorization") != basic {
				w.WriteHeader(401)
				return
			}
			var body struct {
				Token string `json:"token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !strings.HasPrefix(body.Token, "refresh-") {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"code":16,"message":"Refresh token invalid"}`)
				return
			}
			n := f.refreshCalls.Add(1)
			issue(w, fmt.Sprintf("refresh%d", n))
		case r.URL.Path == "/v2/match":
			f.listCalls.Add(1)
			authz := r.Header.Get("Authorization")
			tok := strings.TrimPrefix(authz, "Bearer ")
			if !strings.HasPrefix(authz, "Bearer ") || !f.validTokens[tok] || jwtExpiry(tok).Before(f.now()) {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"code":16,"message":"Auth token invalid"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"matches":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// F7: server-key Basic auth alone cannot list matches; device auth can, and
// the session is refreshed before it expires.
func TestNakamaAuth_DeviceModeAuthenticatesAndRefreshes(t *testing.T) {
	fake := startFakeNakamaWithAuth(t, "serverkey", 10*time.Second)
	cfg := &BridgeConfig{NakamaURL: fake.srv.URL, NakamaServerKey: "serverkey", NakamaAuthMode: nakamaAuthDevice,
		NakamaDeviceID: "bridge-dev", NakamaUsername: "bridge-user"}
	stats := &bridgeStats{}
	d := newDiscoverer(cfg, stats, testLogger())
	base := time.Now()
	var offset atomic.Int64
	now := func() time.Time { return base.Add(time.Duration(offset.Load())) }
	d.auth.now = now
	fake.now = now

	if _, err := d.discover(context.Background()); err != nil {
		t.Fatalf("first discover: %v", err)
	}
	if fake.authCalls.Load() != 1 || fake.lastDeviceID != "bridge-dev" || fake.lastUsername != "bridge-user" {
		t.Errorf("auth calls=%d device=%q user=%q", fake.authCalls.Load(), fake.lastDeviceID, fake.lastUsername)
	}
	// Still fresh: no refresh.
	if _, err := d.discover(context.Background()); err != nil {
		t.Fatalf("second discover: %v", err)
	}
	if fake.refreshCalls.Load() != 0 {
		t.Errorf("refreshed too early")
	}
	// Enter the refresh window (20% of a 10s token = 2s, floored to 5s => 5s lead).
	offset.Add(int64(6 * time.Second))
	if _, err := d.discover(context.Background()); err != nil {
		t.Fatalf("third discover: %v", err)
	}
	if fake.refreshCalls.Load() != 1 || fake.authCalls.Load() != 1 {
		t.Errorf("refresh=%d auth=%d, want 1/1", fake.refreshCalls.Load(), fake.authCalls.Load())
	}
	// Past the (refreshed) expiry without a refresh opportunity: re-auth on 401.
	offset.Add(int64(time.Hour))
	d.auth.mu.Lock()
	d.auth.expiresAt = now().Add(time.Hour) // pretend we think it is valid
	d.auth.mu.Unlock()
	_, err := d.discover(context.Background())
	if !isNakamaAuthError(err) {
		t.Fatalf("expected auth error on expired token, got %v", err)
	}
	if _, err := d.discover(context.Background()); err != nil {
		t.Fatalf("discover after invalidate should re-authenticate: %v", err)
	}
	if fake.authCalls.Load() != 2 {
		t.Errorf("auth calls = %d, want 2 (re-authenticated after 401)", fake.authCalls.Load())
	}
}

func TestNakamaAuth_BasicModeIsRejectedByRealGateway(t *testing.T) {
	fake := startFakeNakamaWithAuth(t, "serverkey", time.Minute)
	cfg := &BridgeConfig{NakamaURL: fake.srv.URL, NakamaServerKey: "serverkey", NakamaAuthMode: nakamaAuthBasic}
	_, err := discoverMatches(context.Background(), cfg, testLogger())
	if !isNakamaAuthError(err) {
		t.Fatalf("expected auth error, got %v", err)
	}
	if !strings.Contains(err.Error(), "device") {
		t.Errorf("error should point the operator at device auth: %v", err)
	}
}

func TestNakamaAuth_WrongServerKeyIsClear(t *testing.T) {
	fake := startFakeNakamaWithAuth(t, "serverkey", time.Minute)
	cfg := &BridgeConfig{NakamaURL: fake.srv.URL, NakamaServerKey: "wrong", NakamaAuthMode: nakamaAuthDevice}
	_, err := discoverMatches(context.Background(), cfg, testLogger())
	if !isNakamaAuthError(err) || !strings.Contains(err.Error(), "Server key invalid") {
		t.Fatalf("expected server-key error, got %v", err)
	}
}

func TestNakamaAuth_BearerWithRefreshToken(t *testing.T) {
	fake := startFakeNakamaWithAuth(t, "serverkey", 10*time.Second)
	base := time.Now()
	var offset atomic.Int64
	now := func() time.Time { return base.Add(time.Duration(offset.Load())) }
	fake.now = now
	initial := makeJWT(base.Add(10*time.Second), "given")
	fake.validTokens[initial] = true
	cfg := &BridgeConfig{NakamaURL: fake.srv.URL, NakamaServerKey: "serverkey", NakamaAuthMode: nakamaAuthBearer,
		NakamaBearerToken: initial, NakamaRefreshToken: "refresh-given"}
	d := newDiscoverer(cfg, nil, testLogger())
	d.auth.now = now
	if _, err := d.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	offset.Add(int64(7 * time.Second))
	if _, err := d.discover(context.Background()); err != nil {
		t.Fatalf("discover in refresh window: %v", err)
	}
	if fake.refreshCalls.Load() != 1 {
		t.Errorf("refresh calls = %d, want 1", fake.refreshCalls.Load())
	}
}

// C0 (fix pass 2): the refresh lead is 20% of the token's lifetime, so a 60s
// token is refreshed once ~12s remain — not, as before, only when the 5s
// floor kicked in (the lead used to be 20% of the *remaining* TTL, which by
// construction never exceeds the remaining TTL).
func TestNakamaAuth_RefreshLeadUsesTokenLifetime(t *testing.T) {
	fake := startFakeNakamaWithAuth(t, "serverkey", 60*time.Second)
	cfg := &BridgeConfig{NakamaURL: fake.srv.URL, NakamaServerKey: "serverkey", NakamaAuthMode: nakamaAuthDevice}
	d := newDiscoverer(cfg, &bridgeStats{}, testLogger())
	base := time.Now().Truncate(time.Second) // JWT exp has second resolution
	var offset atomic.Int64
	now := func() time.Time { return base.Add(time.Duration(offset.Load())) }
	d.auth.now = now
	fake.now = now

	if _, err := d.discover(context.Background()); err != nil {
		t.Fatalf("first discover: %v", err)
	}
	if got := d.auth.refreshLeadLocked(); got != 12*time.Second {
		t.Fatalf("refresh lead for a 60s token = %v, want 12s", got)
	}
	// 13s left: outside the window.
	offset.Store(int64(47 * time.Second))
	if _, err := d.discover(context.Background()); err != nil {
		t.Fatalf("discover at 47s: %v", err)
	}
	if fake.refreshCalls.Load() != 0 {
		t.Fatalf("refreshed with 13s left; the window is 12s")
	}
	// 11s left: inside the window, well before the old 5s floor.
	offset.Store(int64(49 * time.Second))
	if _, err := d.discover(context.Background()); err != nil {
		t.Fatalf("discover at 49s: %v", err)
	}
	if fake.refreshCalls.Load() != 1 || fake.authCalls.Load() != 1 {
		t.Errorf("refresh=%d auth=%d, want 1/1 (refresh at ~48s, not at 55s+)", fake.refreshCalls.Load(), fake.authCalls.Load())
	}
	// The refreshed token gets a fresh 60s lifetime and the same 12s lead.
	if got := d.auth.refreshLeadLocked(); got != 12*time.Second {
		t.Errorf("refresh lead after refresh = %v, want 12s", got)
	}
}

func TestNakamaAuth_RefreshLeadBounds(t *testing.T) {
	cases := []struct {
		name      string
		lifetime  time.Duration
		discovery time.Duration
		want      time.Duration
	}{
		{"unknown lifetime uses Nakama's 60s default", 0, 0, 12 * time.Second},
		{"short token floors at 5s", 10 * time.Second, 0, 5 * time.Second},
		{"60s token", time.Minute, 0, 12 * time.Second},
		{"10m token", 10 * time.Minute, 0, 60 * time.Second},
		{"1h token caps at 60s", time.Hour, 0, 60 * time.Second},
		{"lead covers one discovery cycle", time.Minute, 10 * time.Second, 15 * time.Second},
		{"discovery floor is bounded by half the lifetime", time.Minute, 30 * time.Second, 30 * time.Second},
		{"short token: discovery floor cannot exceed half the lifetime", 10 * time.Second, 30 * time.Second, 5 * time.Second},
		{"long token, long discovery interval", time.Hour, 2 * time.Minute, 125 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &nakamaAuth{cfg: &BridgeConfig{DiscoveryInterval: tc.discovery}, now: time.Now, lifetime: tc.lifetime}
			if got := a.refreshLeadLocked(); got != tc.want {
				t.Errorf("lead(lifetime=%v, discovery=%v) = %v, want %v", tc.lifetime, tc.discovery, got, tc.want)
			}
		})
	}
}

// A caller-supplied bearer token carries its own iat, so its lifetime (and
// therefore its refresh window) is known even though the bridge did not
// issue it.
func TestNakamaAuth_BearerLifetimeFromIatClaim(t *testing.T) {
	base := time.Unix(1_900_000_000, 0)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"exp": base.Add(60 * time.Second).Unix(), "iat": base.Unix()})
	tok := header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"

	// Adopted 50s after issue: lifetime is still the full 60s, not the 10s left.
	exp, lifetime := jwtLifetime(tok, base.Add(50*time.Second))
	if !exp.Equal(base.Add(60*time.Second)) || lifetime != 60*time.Second {
		t.Errorf("jwtLifetime = (%v, %v), want (exp, 60s)", exp, lifetime)
	}
	// No iat: the token counts as issued now.
	_, lifetime = jwtLifetime(makeJWT(base.Add(30*time.Second), "x"), base)
	if lifetime != 30*time.Second {
		t.Errorf("lifetime without iat = %v, want 30s", lifetime)
	}
	cfg := &BridgeConfig{NakamaURL: "http://unused", NakamaServerKey: "k", NakamaAuthMode: nakamaAuthBearer,
		NakamaBearerToken: tok, NakamaRefreshToken: "refresh-x"}
	a := newNakamaAuth(cfg, testLogger())
	a.now = func() time.Time { return base.Add(50 * time.Second) }
	if a.lifetime != 60*time.Second || a.refreshLeadLocked() != 12*time.Second {
		t.Errorf("bearer lifetime=%v lead=%v, want 60s/12s", a.lifetime, a.refreshLeadLocked())
	}
	if !a.needsRefreshLocked() {
		t.Error("10s left on a 60s token must be inside the 12s refresh window")
	}
}

func TestJWTExpiry(t *testing.T) {
	exp := time.Unix(1_900_000_000, 0)
	if got := jwtExpiry(makeJWT(exp, "x")); !got.Equal(exp) {
		t.Errorf("jwtExpiry = %v, want %v", got, exp)
	}
	if !jwtExpiry("opaque-token").IsZero() {
		t.Error("non-JWT should yield zero time")
	}
}

func TestNakamaBaseURL_TrailingSlash(t *testing.T) {
	if got := nakamaBaseURL(&BridgeConfig{NakamaURL: "http://host:7350/"}); got != "http://host:7350" {
		t.Errorf("got %q", got)
	}
}
