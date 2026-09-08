package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/enforce"
)

func TestDesktop_StatusRespondsWhileStorageConnectionIsOccupied(t *testing.T) {
	s, ts := newTestServer(t)
	_, analysisID := s.beginAnalysis()
	defer s.finishAnalysis(analysisID)
	// Occupy the real store's only connection, like a replay write transaction.
	// A status implementation that queries SQLite will fail the client deadline.
	tx, err := s.engine.Store().DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(ts.URL + "/" + testToken + "/api/status")
	if err != nil {
		t.Fatalf("local status waited for the occupied evidence store: %v", err)
	}
	defer resp.Body.Close()
	var status statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || status.SchemaVersion != "nevr-desktop-status/v1" || status.Version != appVersion || !status.AnalysisActive {
		t.Fatalf("status = %d, %+v", resp.StatusCode, status)
	}
	if !status.Provenance.ReviewOnly || status.Provenance.EnforcementPolicy != enforce.PolicyVersion || status.Provenance.ConfigFingerprint != s.configFingerprint() || len(status.Provenance.ExecutableSHA256) != 64 {
		t.Fatalf("local status omitted current runtime identity or policy: %+v", status.Provenance)
	}
	// Detailed health retains its real database dependency and errors. The new
	// polling surface must not turn a failed measurement into healthy zeroes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/"+testToken+"/api/health", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "counting stored matches") {
		t.Fatalf("detailed health hid storage failure: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestDesktop_StatusIsScopedAndNeverClaimsStorageMeasurements(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	var status map[string]json.RawMessage
	if resp := getJSON(t, base+"/api/status", &status); resp.StatusCode != http.StatusOK || len(status) != 4 {
		t.Fatalf("unexpected status shape: status %d, fields %+v", resp.StatusCode, status)
	}
	for _, key := range []string{"schema_version", "version", "provenance", "analysis_active"} {
		if _, ok := status[key]; !ok {
			t.Errorf("missing status field %s", key)
		}
	}
	var active bool
	if err := json.Unmarshal(status["analysis_active"], &active); err != nil || active {
		t.Fatalf("idle activity = %t, %v", active, err)
	}
	for _, route := range []string{"/api/status", "/wrong-token/api/status"} {
		if resp := getJSON(t, ts.URL+route, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("unscoped status %s = %d", route, resp.StatusCode)
		}
	}
	request, err := http.NewRequest(http.MethodGet, base+"/api/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://untrusted.invalid")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", resp.StatusCode)
	}
	if resp := getJSON(t, base+"/api/health", &healthResponse{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("detailed health no longer works: %d", resp.StatusCode)
	}
	_, analysisID := s.beginAnalysis()
	s.finishAnalysis(analysisID)
	var finished statusResponse
	if resp := getJSON(t, base+"/api/status", &finished); resp.StatusCode != http.StatusOK || finished.AnalysisActive {
		t.Fatalf("completed analysis status = %d, %+v", resp.StatusCode, finished)
	}
}
