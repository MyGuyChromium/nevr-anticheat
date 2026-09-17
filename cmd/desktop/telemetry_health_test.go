package main

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const healthMatchID = "SYN-FIXTURE-001"

func uploadFixtureForHealth(t *testing.T) (*server, string, matchView) {
	t.Helper()
	s, ts := newTestServer(t)
	resp, out := upload(t, ts, false, map[string]string{"fixture.echoreplay": fixturePath})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 1 || !out.Results[0].OK || out.Results[0].Match == nil {
		t.Fatalf("upload status=%d out=%+v", resp.StatusCode, out)
	}
	return s, ts.URL + "/" + testToken, *out.Results[0].Match
}

// Reopening a stored match must be served from the document the analysis
// persisted: no whole-match raw-tick scan on any view, summary, report,
// investigation or export path.
func TestStoredMatchViewsNeverScanRawTicks(t *testing.T) {
	s, base, fresh := uploadFixtureForHealth(t)
	store := s.engine.Store()
	raw, source, err := store.GetMatchTelemetryHealth(t.Context(), healthMatchID)
	if err != nil || source != sqlite.TelemetryHealthSourceAnalysis {
		t.Fatalf("analysis did not persist telemetry health: source=%q err=%v", source, err)
	}
	doc, err := replay.DecodeTelemetryHealth(raw)
	if err != nil || doc.Report == nil || doc.Scope != replay.TelemetryHealthScopeMatch || len(doc.Report.SamplePayloads) != 0 {
		t.Fatalf("persisted document = %+v err=%v", doc, err)
	}

	before := store.RawTickScans()
	var first, second matchView
	for _, target := range []*matchView{&first, &second} {
		if resp := getJSON(t, base+"/api/match/"+healthMatchID, target); resp.StatusCode != http.StatusOK {
			t.Fatalf("match status=%d", resp.StatusCode)
		}
	}
	for _, path := range []string{"/investigation", "/report", "/summary.json", "/export.csv"} {
		resp, err := http.Get(base + "/api/match/" + healthMatchID + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", path, resp.StatusCode)
		}
	}
	var inspection physicsInspection
	if resp := getJSON(t, base+"/api/match/"+healthMatchID+"/physics/frame/10?player="+url.QueryEscape("echovr:1001"), &inspection); resp.StatusCode != http.StatusOK || inspection.Telemetry == nil {
		t.Fatalf("physics status=%d telemetry=%+v", resp.StatusCode, inspection.Telemetry)
	}
	if scans := store.RawTickScans() - before; scans != 0 {
		t.Fatalf("stored views performed %d whole-match raw-tick scan(s), want 0", scans)
	}

	// The reopened match shows what the analysis showed, including the mapping
	// diagnostics that cannot be rebuilt from raw ticks alone.
	if first.TelemetryHealth == nil || fresh.TelemetryHealth == nil {
		t.Fatalf("telemetry health missing: fresh=%v stored=%v", fresh.TelemetryHealth, first.TelemetryHealth)
	}
	if first.TelemetryHealth.Source != sqlite.TelemetryHealthSourceAnalysis || first.TelemetryHealth.Scope != replay.TelemetryHealthScopeMatch {
		t.Fatalf("stored health provenance = %q/%q", first.TelemetryHealth.Source, first.TelemetryHealth.Scope)
	}
	stored := *first.TelemetryHealth
	stored.Source, stored.Scope = "", ""
	if !reflect.DeepEqual(stored, *fresh.TelemetryHealth) {
		t.Fatalf("stored telemetry health differs from the analysis view:\nfresh  %+v\nstored %+v", *fresh.TelemetryHealth, stored)
	}
	if first.Diagnostics == nil || fresh.Diagnostics == nil || first.Diagnostics.FramesMapped != fresh.Diagnostics.FramesMapped || first.Diagnostics.Snapshots != fresh.Diagnostics.Snapshots {
		t.Fatalf("stored diagnostics = %+v, fresh = %+v", first.Diagnostics, fresh.Diagnostics)
	}
	if !reflect.DeepEqual(first.TelemetryHealth, second.TelemetryHealth) {
		t.Fatal("telemetry health changed between two views of the same stored match")
	}
}

// A match analyzed before the document existed is rebuilt from raw ticks
// exactly once; the rebuild is labelled and never invents mapping counters.
func TestTelemetryHealthBackfillsOnceForOlderMatches(t *testing.T) {
	s, base, fresh := uploadFixtureForHealth(t)
	store := s.engine.Store()
	if err := store.DeleteMatchTelemetryHealth(t.Context(), healthMatchID); err != nil {
		t.Fatal(err)
	}

	before := store.RawTickScans()
	var first, second matchView
	if resp := getJSON(t, base+"/api/match/"+healthMatchID, &first); resp.StatusCode != http.StatusOK {
		t.Fatalf("first view status=%d", resp.StatusCode)
	}
	afterFirst := store.RawTickScans()
	if afterFirst-before != 1 {
		t.Fatalf("backfill performed %d raw-tick scan(s), want exactly 1", afterFirst-before)
	}
	if resp := getJSON(t, base+"/api/match/"+healthMatchID, &second); resp.StatusCode != http.StatusOK {
		t.Fatalf("second view status=%d", resp.StatusCode)
	}
	if scans := store.RawTickScans() - afterFirst; scans != 0 {
		t.Fatalf("second view performed %d raw-tick scan(s), want 0", scans)
	}
	if _, source, err := store.GetMatchTelemetryHealth(t.Context(), healthMatchID); err != nil || source != sqlite.TelemetryHealthSourceBackfill {
		t.Fatalf("backfill row: source=%q err=%v", source, err)
	}
	if first.TelemetryHealth == nil || first.TelemetryHealth.Source != sqlite.TelemetryHealthSourceBackfill ||
		first.TelemetryHealth.Snapshots != fresh.TelemetryHealth.Snapshots || first.TelemetryHealth.Snapshots == 0 {
		t.Fatalf("backfilled health = %+v, fresh = %+v", first.TelemetryHealth, fresh.TelemetryHealth)
	}
	if first.Diagnostics != nil {
		t.Fatalf("a backfilled report has no mapper counters and must not present any: %+v", first.Diagnostics)
	}
	if !reflect.DeepEqual(first.TelemetryHealth, second.TelemetryHealth) {
		t.Fatal("backfilled health changed between views")
	}

	// Re-analysis is the authority and replaces the backfilled row.
	if _, err := s.engine.AnalyzeFileAll(t.Context(), fixturePath, true); err != nil {
		t.Fatal(err)
	}
	if _, source, err := store.GetMatchTelemetryHealth(t.Context(), healthMatchID); err != nil || source != sqlite.TelemetryHealthSourceAnalysis {
		t.Fatalf("re-analysis did not replace the backfill: source=%q err=%v", source, err)
	}
}

// A match without raw ticks records "nothing to inspect" once instead of
// rescanning on every view, and restoring raw ticks clears that marker.
func TestTelemetryHealthWithoutRawTicksIsRecordedOnce(t *testing.T) {
	s, base, _ := uploadFixtureForHealth(t)
	store := s.engine.Store()
	if err := store.DeleteMatchTelemetryHealth(t.Context(), healthMatchID); err != nil {
		t.Fatal(err)
	}
	var archived rawArchiveResult
	if resp := postJSONTest(t, base+"/api/match/"+healthMatchID+"/archive", map[string]any{"prune_raw": true, "confirmation": healthMatchID}, &archived); resp.StatusCode != http.StatusOK || archived.TicksPruned == 0 {
		t.Fatalf("archive status=%d body=%+v", resp.StatusCode, archived)
	}
	// Pruning preserved the health of the older match before its ticks left.
	var view matchView
	if resp := getJSON(t, base+"/api/match/"+healthMatchID, &view); resp.StatusCode != http.StatusOK || view.TelemetryHealth == nil || view.TelemetryHealth.Snapshots == 0 {
		t.Fatalf("health lost by pruning: status=%d health=%+v", resp.StatusCode, view.TelemetryHealth)
	}

	// Simulate a match whose ticks were pruned by a build that predates this.
	if err := store.DeleteMatchTelemetryHealth(t.Context(), healthMatchID); err != nil {
		t.Fatal(err)
	}
	before := store.RawTickScans()
	for range 2 {
		view = matchView{}
		if resp := getJSON(t, base+"/api/match/"+healthMatchID, &view); resp.StatusCode != http.StatusOK || view.TelemetryHealth != nil {
			t.Fatalf("view without raw ticks: status=%d health=%+v", resp.StatusCode, view.TelemetryHealth)
		}
	}
	if scans := store.RawTickScans() - before; scans != 1 {
		t.Fatalf("views without raw ticks performed %d scan(s), want 1", scans)
	}
	if resp := postJSONTest(t, base+"/api/match/"+healthMatchID+"/restore-raw", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("restore status=%d", resp.StatusCode)
	}
	view = matchView{}
	if resp := getJSON(t, base+"/api/match/"+healthMatchID, &view); resp.StatusCode != http.StatusOK || view.TelemetryHealth == nil || view.TelemetryHealth.Snapshots == 0 {
		t.Fatalf("health not rebuilt after restore: status=%d health=%+v", resp.StatusCode, view.TelemetryHealth)
	}
}
