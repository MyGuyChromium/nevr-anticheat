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
	// Enter the refresh window (20% of a 10s token = 2s lead, min 5s => 5s lead).
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
