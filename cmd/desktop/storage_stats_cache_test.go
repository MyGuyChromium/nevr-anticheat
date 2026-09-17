package main

import (
	"net/http"
	"testing"
	"time"
)

// The byte measurement shown on a match is cached between views, so it must
// follow every change that can alter it: pruning, restoring and re-analysis.
func TestMatchStorageStatsCacheFollowsArchiveRestoreAndReanalysis(t *testing.T) {
	s, base, _ := uploadFixtureForHealth(t)
	view := func() matchView {
		t.Helper()
		var mv matchView
		if resp := getJSON(t, base+"/api/match/"+healthMatchID, &mv); resp.StatusCode != http.StatusOK || mv.Storage == nil {
			t.Fatalf("view status=%d storage=%v", resp.StatusCode, mv.Storage)
		}
		return mv
	}
	first := view()
	if first.Storage.RawTicks == 0 || first.Storage.RawTickBytes == 0 || first.Storage.NormalizedFrames == 0 || first.Storage.NormalizedBytes == 0 {
		t.Fatalf("storage = %+v", first.Storage)
	}
	direct, err := s.engine.Store().GetMatchStorageStats(t.Context(), healthMatchID)
	if err != nil || direct != *first.Storage || *view().Storage != direct {
		t.Fatalf("cached %+v, measured %+v, err=%v", first.Storage, direct, err)
	}

	var archived rawArchiveResult
	if resp := postJSONTest(t, base+"/api/match/"+healthMatchID+"/archive", map[string]any{"prune_raw": true, "confirmation": healthMatchID}, &archived); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive status=%d", resp.StatusCode)
	}
	if pruned := view(); pruned.Storage.RawTicks != 0 || pruned.Storage.RawTickBytes != 0 || pruned.Storage.NormalizedBytes != direct.NormalizedBytes {
		t.Fatalf("after pruning the view still shows %+v", pruned.Storage)
	}
	if resp := postJSONTest(t, base+"/api/match/"+healthMatchID+"/restore-raw", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("restore status=%d", resp.StatusCode)
	}
	if restored := view(); *restored.Storage != direct {
		t.Fatalf("after restore the view shows %+v, want %+v", restored.Storage, direct)
	}

	// A re-analysis records a new run, which retires the cached measurement
	// even though the row counts are unchanged.
	s.storageMu.Lock()
	before := s.storageStats[healthMatchID].key
	s.storageMu.Unlock()
	started := time.Now()
	results, err := s.engine.AnalyzeFileAll(t.Context(), fixturePath, true)
	if err != nil || len(results) != 1 {
		t.Fatalf("re-analysis: %d result(s), %v", len(results), err)
	}
	recordAnalysisResults(t.Context(), s.engine, results, "upload", time.Since(started))
	view()
	s.storageMu.Lock()
	after := s.storageStats[healthMatchID].key
	s.storageMu.Unlock()
	if after.latestRun == before.latestRun || after.rawTicks != before.rawTicks || after.frames != before.frames {
		t.Fatalf("cache key before=%+v after=%+v; a re-analysis must retire the measurement", before, after)
	}
}
