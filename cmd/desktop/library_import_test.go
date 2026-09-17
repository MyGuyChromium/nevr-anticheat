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
		AllKept  map[string]int `json:"kept_local"`
	}
	if resp := postAPI(t, base+"/api/library", lib, &out); resp.StatusCode != http.StatusOK {
		t.Fatalf("import status %d", resp.StatusCode)
	}
	// kept_local is the whole category and includes the newer-local rows.
	if out.AllKept["reviews"] != 1 || out.AllKept["labels"] != 1 {
		t.Fatalf("kept_local does not include the newer local rows: %+v", out.AllKept)
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

// The storage importer keeps the local row in cases the handler's own
// "strictly newer" pre-check does not see: an imported row without a review
// time, and one with the same time but different content. Those rows used to be
// counted under "rejected" with ok:false, as if the library were damaged.
// Mutation: drop the errors.Is(err, sqlite.ErrImportKeptLocal) branch in
// handleImportLibrary and kept_local stays 0 while rejected becomes 3.
func TestLibraryImportReportsKeptLocalRowsSeparatelyFromRejectedOnes(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	ctx := context.Background()
	store := s.engine.Store()
	const matchID, playerID = "SYN-FIXTURE-001", "echovr:1001"
	if r := uploadBytes(t, ts, nil, map[string][]byte{"a.echoreplay": fixtureBytes(t)}).Results[0]; !r.OK {
		t.Fatalf("upload: %+v", r)
	}
	event := model.DetectionEvent{EventID: "evt-kept-local", DetectorID: "THROW_003", DetectorVersion: "2.0.0",
		MatchID: matchID, PlayerID: playerID, FrameIndex: 9, Severity: .8, Confidence: .9}
	if err := store.StoreDetectionEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreEventReview(ctx, event.EventID, "no", "local verdict", "local-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreMatchLabel(ctx, matchID, sqlite.MatchLabelKnownClean, "local label", "local-owner", appVersion, "cfg"); err != nil {
		t.Fatal(err)
	}
	var opportunity sqlite.CalibrationOpportunity
	if resp := postJSONTest(t, base+"/api/calibration/opportunities", map[string]any{
		"match_id": matchID, "player_id": playerID, "detector_id": "THROW_001", "opportunity_kind": "throw",
		"frame_start": 10, "frame_end": 12, "ground_truth": "positive", "behavior_type": "local window",
	}, &opportunity); resp.StatusCode != http.StatusOK || opportunity.OpportunityID == "" {
		t.Fatalf("opportunity status=%d body=%+v", resp.StatusCode, opportunity)
	}
	undatedOpportunity := opportunity
	undatedOpportunity.Comment, undatedOpportunity.ReviewedAt = "undated copy from another PC", time.Time{}

	lib := evidenceLibrary{Version: 2,
		// No review time: the handler's pre-check skips these, storage keeps local.
		Labels: []sqlite.MatchLabel{{MatchID: matchID, Label: sqlite.MatchLabelConfirmedCheat, Comment: "undated", ReviewerID: "someone-else"}},
		Reviews: []sqlite.EventReview{
			{EventID: event.EventID, MatchID: matchID, PlayerID: playerID, DetectorID: "THROW_003", Verdict: "yes", Comment: "undated", ReviewerID: "someone-else"},
			// A damaged row is still a rejection and still clears ok.
			{EventID: "evt-damaged", MatchID: matchID, PlayerID: playerID, DetectorID: "THROW_003", Verdict: "not-a-verdict"},
		},
		Opportunities: []sqlite.CalibrationOpportunity{undatedOpportunity},
	}
	var out struct {
		OK         bool           `json:"ok"`
		Imported   map[string]int `json:"imported"`
		Rejected   map[string]int `json:"rejected"`
		FirstError string         `json:"first_error"`
		KeptLocal  map[string]int `json:"kept_local"`
		Examples   []string       `json:"kept_local_examples"`
	}
	if resp := postAPI(t, base+"/api/library", lib, &out); resp.StatusCode != http.StatusOK {
		t.Fatalf("import status %d", resp.StatusCode)
	}
	if out.KeptLocal["labels"] != 1 || out.KeptLocal["reviews"] != 1 || out.KeptLocal["opportunities"] != 1 || len(out.Examples) != 3 {
		t.Fatalf("kept_local = %v examples = %q", out.KeptLocal, out.Examples)
	}
	if out.Rejected["labels"] != 0 || out.Rejected["opportunities"] != 0 || out.Rejected["reviews"] != 1 || out.OK ||
		!strings.Contains(out.FirstError, "invalid imported event review") {
		t.Fatalf("only the damaged review is a rejection: rejected=%v ok=%v first_error=%q", out.Rejected, out.OK, out.FirstError)
	}
	if out.Imported["labels"]+out.Imported["reviews"]+out.Imported["opportunities"] != 0 {
		t.Fatalf("a kept-local row was counted as imported: %v", out.Imported)
	}
	if reviews, _ := store.GetEventReviewsByMatch(ctx, matchID); reviews[event.EventID].Verdict != "no" {
		t.Fatalf("the local verdict was replaced: %+v", reviews[event.EventID])
	}

	// Without the damaged row the same import is a clean merge: ok stays true.
	lib.Reviews = lib.Reviews[:1]
	out.KeptLocal, out.Rejected, out.Examples = nil, nil, nil
	if resp := postAPI(t, base+"/api/library", lib, &out); resp.StatusCode != http.StatusOK || !out.OK ||
		out.KeptLocal["reviews"] != 1 || out.Rejected["reviews"] != 0 || out.FirstError != "" {
		t.Fatalf("kept-local rows alone must not fail the import: status=%d %+v", resp.StatusCode, out)
	}
}
