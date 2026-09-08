package main

import (
	"net/http"
	"time"
)

// scopedHTTPClient must not follow a source to another destination.
// The configured endpoint/advertised IP defines the request's scope; a redirect
// does not authorize another host, port, or path (including local services),
// nor forwarding discovery credentials or refresh-token request bodies.
// Return the redirect response so existing non-200 handling reports the failure.
func scopedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
