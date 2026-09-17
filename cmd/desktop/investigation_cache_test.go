package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func investigationParts(t *testing.T, base string) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if resp := getJSON(t, base+"/api/match/"+healthMatchID+"/investigation", &doc); resp.StatusCode != http.StatusOK {
		t.Fatalf("investigation status=%d", resp.StatusCode)
	}
	return doc
}

// The frame-derived part of the investigation is computed once per analysis
// run and config, persisted, and served from there; the cached answer must be
// exactly the computed one.
func TestInvestigationTelemetryIsCachedPerAnalysisRun(t *testing.T) {
	s, base, _ := uploadFixtureForHealth(t)
	store := s.engine.Store()
	first := investigationParts(t, base)
	key, raw, err := store.GetMatchViewCache(t.Context(), healthMatchID, investigationTelemetryKind)
	if err != nil || !strings.Contains(key, "config="+s.configFingerprint()) || !strings.Contains(key, "frames=480") {
		t.Fatalf("cache row: key=%q err=%v", key, err)
	}
	var cached investigationTelemetry
	if err := json.Unmarshal(raw, &cached); err != nil || len(cached.Timeline) == 0 || cached.Quality.Rows != 480 {
		t.Fatalf("cached document: rows=%d timeline=%d err=%v", cached.Quality.Rows, len(cached.Timeline), err)
	}
	mc, err := store.GetMatchContext(t.Context(), healthMatchID)
	if err != nil {
		t.Fatal(err)
	}
	built, err := s.buildInvestigationTelemetry(t.Context(), healthMatchID, mc)
	if err != nil {
		t.Fatal(err)
	}
	cached.normalize()
	if !reflect.DeepEqual(*built, cached) {
		t.Fatal("the cached telemetry differs from a fresh computation")
	}

	second := investigationParts(t, base)
	for _, field := range []string{"quality", "timeline", "legal_context_counts", "throws"} {
		if string(first[field]) != string(second[field]) {
			t.Fatalf("%s changed between the computed and the cached response", field)
		}
	}

	// Prove the second response really came from the cache: a marked cache row
	// under the current key is what gets served...
	cached.Quality.Grade = "served-from-cache"
	marked, _ := json.Marshal(cached)
	if err := store.StoreMatchViewCache(t.Context(), healthMatchID, investigationTelemetryKind, key, marked); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(investigationParts(t, base)["quality"]), "served-from-cache") {
		t.Fatal("the investigation recomputed its telemetry instead of reading the cache")
	}
	// ...until a re-analysis records a new run, which retires it.
	started := time.Now()
	results, err := s.engine.AnalyzeFileAll(t.Context(), fixturePath, true)
	if err != nil || len(results) != 1 {
		t.Fatalf("re-analysis: %d result(s), %v", len(results), err)
	}
	recordAnalysisResults(t.Context(), s.engine, results, "upload", time.Since(started))
	after := investigationParts(t, base)
	if strings.Contains(string(after["quality"]), "served-from-cache") || string(after["quality"]) != string(first["quality"]) {
		t.Fatalf("a re-analysis did not retire the cached telemetry: %s", after["quality"])
	}
	if newKey, _, err := store.GetMatchViewCache(t.Context(), healthMatchID, investigationTelemetryKind); err != nil || newKey == key {
		t.Fatalf("cache key after re-analysis = %q (was %q) err=%v", newKey, key, err)
	}
}
