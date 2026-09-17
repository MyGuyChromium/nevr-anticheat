package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// Importing an evidence library was an unconditional upsert: an export from
// 2020 flipped a verdict the moderator recorded today, with ok:true and no
// trace. Mutation: drop the reviewed_at comparison in handleImportLibrary and
// the stored verdict becomes "yes".
func TestLibraryImportKeepsNewerLocalVerdicts(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	ctx := context.Background()
	store := s.engine.Store()
	const matchID = "SYN-FIXTURE-001"
	if r := uploadBytes(t, ts, nil, map[string][]byte{"a.echoreplay": fixtureBytes(t)}).Results[0]; !r.OK {
		t.Fatalf("upload: %+v", r)
	}
	event := model.DetectionEvent{EventID: "evt-local", DetectorID: "THROW_003", DetectorVersion: "2.0.0",
		MatchID: matchID, PlayerID: "p1", FrameIndex: 9, Severity: .8, Confidence: .9}
	if err := store.StoreDetectionEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreEventReview(ctx, event.EventID, "no", "legal slap, reviewed today", "local-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreMatchLabel(ctx, matchID, sqlite.MatchLabelKnownClean, "reviewed today", "local-owner", appVersion, "cfg"); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	lib := evidenceLibrary{Version: 2,
		Labels: []sqlite.MatchLabel{
			{MatchID: matchID, Label: sqlite.MatchLabelConfirmedCheat, Comment: "stale export", ReviewerID: "someone-else", ReviewedAt: old},
			{MatchID: "m-other-pc", Label: sqlite.MatchLabelSuspected, ReviewerID: "someone-else", ReviewedAt: old},
		},
		Reviews: []sqlite.EventReview{
			{EventID: event.EventID, MatchID: matchID, PlayerID: "p1", DetectorID: "THROW_003", Verdict: "yes", Comment: "stale export", ReviewerID: "someone-else", ReviewedAt: old},
			{EventID: "evt-from-another-pc", MatchID: "m-other-pc", PlayerID: "p2", DetectorID: "THROW_003", Verdict: "yes", ReviewerID: "someone-else", ReviewedAt: old},
		},
	}
	var out struct {
		OK       bool           `json:"ok"`
		Imported map[string]int `json:"imported"`
		Kept     map[string]int `json:"kept_newer_local"`
		Examples []string       `json:"kept_newer_local_examples"`
	}
	if resp := postAPI(t, base+"/api/library", lib, &out); resp.StatusCode != http.StatusOK {
		t.Fatalf("import status %d", resp.StatusCode)
	}
	if out.Kept["reviews"] != 1 || out.Kept["labels"] != 1 || out.Imported["reviews"] != 1 || out.Imported["labels"] != 1 ||
		len(out.Examples) != 2 || !strings.Contains(strings.Join(out.Examples, "|"), "evt-local") {
		t.Fatalf("import report: %+v", out)
	}
	reviews, err := store.GetEventReviewsByMatch(ctx, matchID)
	if err != nil || reviews[event.EventID].Verdict != "no" || reviews[event.EventID].ReviewerID != "local-owner" {
		t.Fatalf("an older imported verdict replaced the local one: %+v, %v", reviews[event.EventID], err)
	}
	if label, ok, err := store.GetMatchLabel(ctx, matchID); err != nil || !ok || label.Label != sqlite.MatchLabelKnownClean {
		t.Fatalf("an older imported label replaced the local one: %+v, %v", label, err)
	}
	// Labels for a match this PC has not seen yet still arrive: the library is
	// meant to travel ahead of the recordings.
	if label, ok, _ := store.GetMatchLabel(ctx, "m-other-pc"); !ok || label.Label != sqlite.MatchLabelSuspected {
		t.Fatalf("portable label was not imported: %+v", label)
	}

	// A newer imported verdict does update the local one.
	lib.Reviews = lib.Reviews[:1]
	lib.Labels = nil
	lib.Reviews[0].ReviewedAt = time.Now().Add(time.Hour).UTC()
	lib.Reviews[0].Verdict = "uncertain"
	if resp := postAPI(t, base+"/api/library", lib, &out); resp.StatusCode != http.StatusOK || out.Kept["reviews"] != 0 {
		t.Fatalf("newer import: %d %+v", resp.StatusCode, out)
	}
	if reviews, _ := store.GetEventReviewsByMatch(ctx, matchID); reviews[event.EventID].Verdict != "uncertain" {
		t.Fatalf("a newer imported verdict was ignored: %+v", reviews[event.EventID])
	}
}
