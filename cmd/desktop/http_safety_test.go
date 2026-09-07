package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestDesktopSafetyCrossOriginRequestsHaveNoSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name, origin, site string
		allow              bool
	}{
		{"local client", "", "", true},
		{"same origin", "http://127.0.0.1:18080", "same-origin", true},
		{"external", "https://attacker.invalid", "", false},
		{"same host other port", "http://127.0.0.1:18081", "", false},
		{"same host other scheme", "https://127.0.0.1:18080", "", false},
		{"null", "null", "", false},
		{"credentials", "http://user@127.0.0.1:18080", "", false},
		{"path", "http://127.0.0.1:18080/path", "", false},
		{"query", "http://127.0.0.1:18080?x=1", "", false},
		{"multiple origins", "http://127.0.0.1:18080 https://attacker.invalid", "", false},
		{"cross site without Origin", "", "cross-site", false},
		{"cross site contradicts Origin", "http://127.0.0.1:18080", "cross-site", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
				calls := 0
				h := desktopSafety(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(204) }))
				r := httptest.NewRequest(method, "http://127.0.0.1:18080/token/quit", nil)
				if tc.origin != "" {
					r.Header.Set("Origin", tc.origin)
				}
				r.Header.Set("Sec-Fetch-Site", tc.site)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if (calls == 1) != tc.allow {
					t.Fatalf("method=%s allowed=%t calls=%d status=%d", method, tc.allow, calls, w.Code)
				}
				if !tc.allow && w.Code != http.StatusForbidden {
					t.Fatalf("status=%d", w.Code)
				}
				for key, want := range map[string]string{"Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Cache-Control": "no-store", "Cross-Origin-Resource-Policy": "same-origin"} {
					if w.Header().Get(key) != want {
						t.Fatalf("%s=%q", key, w.Header().Get(key))
					}
				}
			}
		})
	}
}

func TestDesktopOriginDuplicateHeaderAndDefaultPort(t *testing.T) {
	r := httptest.NewRequest("POST", "http://localhost:80/token/api", nil)
	r.Header.Set("Origin", "http://LOCALHOST")
	if !validDesktopOrigin(r) {
		t.Fatal("equivalent default port refused")
	}
	r.Header.Add("Origin", "http://localhost")
	if validDesktopOrigin(r) {
		t.Fatal("duplicate Origin accepted")
	}
}

func TestDesktopIndexUsesFreshScriptNonce(t *testing.T) {
	_, ts := newTestServer(t)
	pattern := regexp.MustCompile(`<script nonce="([a-f0-9]{32})">`)
	previous := ""
	for i := 0; i < 2; i++ {
		resp, err := http.Get(ts.URL + "/" + testToken + "/")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		match := pattern.FindSubmatch(body)
		if resp.StatusCode != 200 || len(match) != 2 {
			t.Fatalf("status=%d missing nonce", resp.StatusCode)
		}
		nonce := string(match[1])
		policy := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(policy, "script-src 'nonce-"+nonce+"'") || strings.Contains(policy, "script-src 'unsafe-inline'") || strings.Contains(string(body), "<script>") {
			t.Fatal("script policy doesn't bind page content")
		}
		if nonce == previous {
			t.Fatal("nonce reused across page responses")
		}
		previous = nonce
	}
}
