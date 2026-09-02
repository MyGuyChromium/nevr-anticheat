package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Nakama authentication modes for the discovery API.
//
// Nakama's HTTP gateway only accepts server-key Basic auth on the
// Authenticate*/SessionRefresh endpoints; every other call (including
// GET /v2/match) requires a user session token. "device" therefore
// authenticates a dedicated bridge account with the server key, keeps the
// session token fresh with SessionRefresh, and is the production default.
const (
	nakamaAuthDevice = "device" // authenticate a bridge account with the server key, refresh before expiry
	nakamaAuthBearer = "bearer" // caller-supplied session token (refreshed only when a refresh token is given)
	nakamaAuthBasic  = "basic"  // raw server-key Basic auth on every request (only works behind a proxy that maps it)
)

// nakamaAuthError marks a 401/403 from Nakama: a credential problem that
// retrying will not fix.
type nakamaAuthError struct {
	Status int
	Body   string
	Mode   string
}

func (e *nakamaAuthError) Error() string {
	hint := "check --nakama-server-key"
	switch e.Mode {
	case nakamaAuthBearer:
		hint = "the --nakama-bearer-token is invalid or expired; supply --nakama-refresh-token or use --nakama-auth device"
	case nakamaAuthBasic:
		hint = "Nakama does not accept server-key Basic auth on /v2/match; use --nakama-auth device (default) so the bridge obtains a session token"
	case nakamaAuthDevice:
		hint = "the bridge session was rejected; check --nakama-server-key and that device authentication is enabled for the bridge account"
	}
	return fmt.Sprintf("Nakama rejected the bridge credentials (HTTP %d): %s — %s", e.Status, e.Body, hint)
}

// nakamaSession is the relevant part of Nakama's api.Session.
type nakamaSession struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
}

// nakamaAuth produces the Authorization header for Nakama requests and keeps
// session tokens fresh.
type nakamaAuth struct {
	cfg    *BridgeConfig
	logger *slog.Logger
	client *http.Client
	now    func() time.Time

	mu           sync.Mutex
	token        string
	refreshToken string
	expiresAt    time.Time
}

func newNakamaAuth(cfg *BridgeConfig, logger *slog.Logger) *nakamaAuth {
	a := &nakamaAuth{
		cfg:    cfg,
		logger: logger,
		client: &http.Client{Timeout: 10 * time.Second},
		now:    time.Now,
	}
	if cfg.NakamaBearerToken != "" {
		a.token = cfg.NakamaBearerToken
		a.refreshToken = cfg.NakamaRefreshToken
		a.expiresAt = jwtExpiry(cfg.NakamaBearerToken)
	}
	return a
}

// mode resolves the effective auth mode. An empty NakamaAuthMode keeps the
// historical behaviour (bearer if a token is given, otherwise basic) so
// callers that predate the mode flag are unaffected; main sets "device".
func (a *nakamaAuth) mode() string {
	switch a.cfg.NakamaAuthMode {
	case nakamaAuthDevice, nakamaAuthBearer, nakamaAuthBasic:
		return a.cfg.NakamaAuthMode
	}
	if a.cfg.NakamaBearerToken != "" {
		return nakamaAuthBearer
	}
	return nakamaAuthBasic
}

func (a *nakamaAuth) basicHeader() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(a.cfg.NakamaServerKey+":"))
}

// authorization returns the Authorization header value to use right now,
// authenticating or refreshing first when needed.
func (a *nakamaAuth) authorization(ctx context.Context) (string, error) {
	switch a.mode() {
	case nakamaAuthBasic:
		return a.basicHeader(), nil
	case nakamaAuthBearer:
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.refreshToken != "" && a.needsRefreshLocked() {
			if err := a.refreshLocked(ctx); err != nil {
				a.logger.Warn("Nakama session refresh failed; continuing with the current token", "error", err)
			}
		}
		return "Bearer " + a.token, nil
	default: // device
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.token == "" {
			if err := a.authenticateDeviceLocked(ctx); err != nil {
				return "", err
			}
		} else if a.needsRefreshLocked() {
			if err := a.refreshLocked(ctx); err != nil {
				a.logger.Info("Nakama session refresh failed; re-authenticating", "error", err)
				if err := a.authenticateDeviceLocked(ctx); err != nil {
					return "", err
				}
			}
		}
		return "Bearer " + a.token, nil
	}
}

// invalidate forgets the current session so the next call re-authenticates.
// Called after a 401 so a token revoked server-side does not wedge discovery.
func (a *nakamaAuth) invalidate() {
	if a.mode() != nakamaAuthDevice {
		return
	}
	a.mu.Lock()
	a.token = ""
	a.refreshToken = ""
	a.expiresAt = time.Time{}
	a.mu.Unlock()
}

// needsRefreshLocked reports whether the token is within its refresh window:
// 20% of its lifetime (at least 5s, at most 60s) before expiry. Unknown
// expiry is treated as Nakama's 60s default.
func (a *nakamaAuth) needsRefreshLocked() bool {
	if a.expiresAt.IsZero() {
		return false
	}
	return a.now().Add(a.refreshLeadLocked()).After(a.expiresAt)
}

func (a *nakamaAuth) refreshLeadLocked() time.Duration {
	lead := 10 * time.Second
	if !a.expiresAt.IsZero() {
		ttl := a.expiresAt.Sub(a.now())
		lead = ttl / 5
		if lead < 5*time.Second {
			lead = 5 * time.Second
		}
		if lead > 60*time.Second {
			lead = 60 * time.Second
		}
	}
	return lead
}

func (a *nakamaAuth) authenticateDeviceLocked(ctx context.Context) error {
	deviceID := a.cfg.NakamaDeviceID
	if deviceID == "" {
		deviceID = "nevr-anticheat-bridge"
	}
	username := a.cfg.NakamaUsername
	if username == "" {
		username = "nevr-anticheat-bridge"
	}
	url := fmt.Sprintf("%s/v2/account/authenticate/device?create=true&username=%s", nakamaBaseURL(a.cfg), username)
	body, _ := json.Marshal(map[string]any{"id": deviceID})
	sess, err := a.postSession(ctx, url, body)
	if err != nil {
		return fmt.Errorf("Nakama device authentication failed: %w", err)
	}
	a.adoptLocked(sess)
	a.logger.Info("authenticated with Nakama",
		"mode", nakamaAuthDevice,
		"username", username,
		"expires_at", a.expiresAt.UTC().Format(time.RFC3339),
	)
	return nil
}

func (a *nakamaAuth) refreshLocked(ctx context.Context) error {
	if a.refreshToken == "" {
		return errors.New("no refresh token")
	}
	url := nakamaBaseURL(a.cfg) + "/v2/account/session/refresh"
	body, _ := json.Marshal(map[string]any{"token": a.refreshToken})
	sess, err := a.postSession(ctx, url, body)
	if err != nil {
		return err
	}
	a.adoptLocked(sess)
	a.logger.Debug("Nakama session refreshed", "expires_at", a.expiresAt.UTC().Format(time.RFC3339))
	return nil
}

func (a *nakamaAuth) adoptLocked(sess *nakamaSession) {
	a.token = sess.Token
	if sess.RefreshToken != "" {
		a.refreshToken = sess.RefreshToken
	}
	a.expiresAt = jwtExpiry(sess.Token)
	if a.expiresAt.IsZero() {
		// Nakama's default session.token_expiry_sec is 60s.
		a.expiresAt = a.now().Add(60 * time.Second)
	}
}

// postSession performs a server-key-authenticated POST that returns a session.
func (a *nakamaAuth) postSession(ctx context.Context, url string, body []byte) (*nakamaSession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", a.basicHeader())
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &nakamaAuthError{Status: resp.StatusCode, Body: truncate(string(respBody), 200), Mode: a.mode()}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	var sess nakamaSession
	if err := json.Unmarshal(respBody, &sess); err != nil {
		return nil, fmt.Errorf("parsing session response: %w", err)
	}
	if sess.Token == "" {
		return nil, errors.New("session response has no token")
	}
	return &sess, nil
}

// jwtExpiry extracts the exp claim from a JWT without verifying it. Returns
// the zero time when the token is not a JWT or carries no exp.
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(claims.Exp), 0)
}

// nakamaBaseURL returns the configured Nakama URL without trailing slashes so
// paths can be appended without producing "//v2/...".
func nakamaBaseURL(cfg *BridgeConfig) string {
	return strings.TrimRight(cfg.NakamaURL, "/")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
