package main

import (
	"net/http"
	"net/url"
	"strings"
)

// desktopSafety adds browser defense in depth around the per-run secret URL.
// Headerless local clients remain supported; a remote page cannot use a known
// token to submit cross-origin browser requests or frame the review interface.
func desktopSafety(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") || !validDesktopOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-origin access to the local app is not allowed; open its local app window directly")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validDesktopOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 || values[0] == "" {
		return false
	}
	origin, err := url.Parse(values[0])
	if err != nil || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || origin.Opaque != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if origin.Scheme != scheme || origin.Host == "" {
		return false
	}
	target, err := url.Parse(scheme + "://" + r.Host)
	if err != nil || target.Hostname() == "" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(origin.Hostname(), target.Hostname()) && port(origin) == port(target)
}
