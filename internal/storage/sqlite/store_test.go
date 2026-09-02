package sqlite

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestEvents_EvidenceAndCausalKeyRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ev := mkEvent("THROW_006", "P1", "M1", 100, 0.9, 0.8)
	ev.Evidence = model.TrajectoryEvidence{
		CumulativeAngleChange: 42.5,
		ViolationFrameCount:   3,
		TrajectoryPoints:      []model.Vec3{{1, 2, 3}, {4, 5, 6}},
	}
	mustStoreEvent(t, s, ev)

	got, err := s.GetMatchPlayerEvents(ctx, "M1", "P1")
	if err != nil || len(got) != 1 {
		t.Fatalf("GetMatchPlayerEvents = %v, %v", got, err)
	}
	te, ok := got[0].Evidence.(model.TrajectoryEvidence)
	if !ok {
		t.Fatalf("evidence type = %T, want TrajectoryEvidence", got[0].Evidence)
	}
	if te.CumulativeAngleChange != 42.5 || len(te.TrajectoryPoints) != 2 || te.TrajectoryPoints[1][2] != 6 {
		t.Errorf("evidence content lost: %+v", te)
	}
	if got[0].CausalKey != ev.CausalKey {
		t.Errorf("causal key = %+v, want %+v", got[0].CausalKey, ev.CausalKey)
	}
	if got[0].StoredAt.IsZero() || time.Since(got[0].StoredAt) > time.Minute {
		t.Errorf("StoredAt = %v", got[0].StoredAt)
	}

	// Every reader shares the column list.
	for name, fn := range map[string]func() ([]model.DetectionEvent, error){
		"player": func() ([]model.DetectionEvent, error) { return s.GetPlayerEvents(ctx, "P1", 10, 0) },
		"match":  func() ([]model.DetectionEvent, error) { return s.GetMatchEvents(ctx, "M1") },
		"all":    func() ([]model.DetectionEvent, error) { return s.GetAllPlayerEvents(ctx, "P1") },
	} {
		evs, err := fn()
		if err != nil || len(evs) != 1 || evs[0].Evidence == nil {
			t.Errorf("%s reader lost evidence: %v %v", name, evs, err)
		}
	}
}

func TestEvents_UnknownAndUnmarshalableEvidence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	bad := mkEvent("BIO_001", "P1", "M1", 5, 0.5, 0.5)
	bad.Evidence = model.WristRotationEvidence{AngularVelocity: math.NaN()}
	mustStoreEvent(t, s, bad)

	got, err := s.GetMatchPlayerEvents(ctx, "M1", "P1")
	if err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	raw, ok := got[0].Evidence.(RawEvidence)
	if !ok || raw.EvidenceType() != EvidenceTypeMarshalError {
		t.Fatalf("NaN evidence should be a visible marker, got %#v", got[0].Evidence)
	}

	// Legacy row: evidence_json without evidence_type is preserved verbatim.
	if _, err := s.DB().Exec(`INSERT INTO detection_events (event_id, detector_id, match_id, player_id, frame_index,
		severity, confidence, evidence_json, created_at) VALUES ('legacy','X_1','M2','P1',1,0.1,0.1,'{"k":1}',?)`,
		fmtDBTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetMatchPlayerEvents(ctx, "M2", "P1")
	if raw, ok := got[0].Evidence.(RawEvidence); !ok || string(raw.JSON) != `{"k":1}` {
		t.Fatalf("legacy evidence = %#v", got[0].Evidence)
	}
	if len(KnownEvidenceTypes()) != 15 {
		t.Errorf("registry has %d types, want 15 (one per model evidence type)", len(KnownEvidenceTypes()))
	}
}

func TestPrune_BoundariesAndChunking(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	fresh := mkEvent("THROW_001", "P1", "M1", 1, 0.5, 0.5)
	mustStoreEvent(t, s, fresh)
	if err := s.StoreSuspicionScore(ctx, model.SuspicionScore{PlayerID: "P1", TotalScore: 1}); err != nil {
		t.Fatal(err)
	}

	// Rows written now must survive a prune of anything older than 1h — this
	// is exactly the case the ' ' < 'T' bug deleted.
	if n, err := s.PruneOldEvents(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("fresh event pruned: n=%d err=%v", n, err)
	}
	if n, err := s.PruneOldScores(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("fresh score pruned: n=%d err=%v", n, err)
	}

	// More than one chunk of old rows.
	var old []model.DetectionEvent
	for i := 0; i < pruneChunkSize+50; i++ {
		e := mkEvent("BIO_003", "P2", "M-old", i, 0.2, 0.2)
		e.StoredAt = time.Now().Add(-2 * time.Hour)
		old = append(old, e)
	}
	if _, err := s.StoreDetectionEvents(ctx, old, "initial"); err != nil {
		t.Fatal(err)
	}
	backdate(t, s, "suspicion_scores", "snapshot_time", "player_id='P1'", time.Now().Add(-61*time.Minute))

	n, err := s.PruneOldEvents(ctx, time.Hour)
	if err != nil || n != int64(pruneChunkSize+50) {
		t.Fatalf("old events pruned = %d, %v", n, err)
	}
	if countRows(t, s, "detection_events", "") != 1 {
		t.Error("fresh event should remain")
	}
	if n, _ := s.PruneOldScores(ctx, time.Hour); n != 1 {
		t.Errorf("old score pruned = %d", n)
	}

	// The cutoff is strict: a row exactly at the cutoff second survives.
	backdate(t, s, "detection_events", "created_at", "player_id='P1'", time.Now().Add(-time.Hour).Add(2*time.Second))
	if n, _ := s.PruneOldEvents(ctx, time.Hour); n != 0 {
		t.Errorf("row just inside the window pruned")
	}
}

func TestScores_LatestIsDeterministicAndScoped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	// Two snapshots in the same second: the later insert must win.
	for _, total := range []float64{10, 30, 20} {
		err := s.StoreMatchSuspicionScore(ctx, "M1", model.SuspicionScore{
			PlayerID: "P1", TotalScore: total, SnapshotTime: at,
			ScoreByCategory: map[string]float64{"throw": total},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetPlayerScore(ctx, "P1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalScore != 20 {
		t.Fatalf("latest = %.0f, want 20 (last inserted)", got.TotalScore)
	}
	if !got.SnapshotTime.Equal(at) {
		t.Errorf("snapshot time = %v", got.SnapshotTime)
	}

	// A cross-match snapshot never leaks into the per-match score or history.
	summary := PlayerCrossMatchSummary{PlayerID: "P1", DecayedScore: 99, ScoredEvents: 7, DistinctMatches: 3,
		PointsByDetector: map[string]float64{"THROW_001": 99}, PointsByCategory: map[string]float64{"throw": 99}}
	if err := s.StoreCrossMatchScore(ctx, summary); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPlayerScore(ctx, "P1")
	if got.TotalScore != 20 {
		t.Fatalf("cross-match snapshot corrupted GetPlayerScore: %.0f", got.TotalScore)
	}
	xm, err := s.GetPlayerCrossMatchScore(ctx, "P1")
	if err != nil || xm.TotalScore != 99 || xm.MatchCount != 3 {
		t.Fatalf("cross-match score = %+v, %v", xm, err)
	}
	hist, err := s.GetPlayerHistory(ctx, "P1", at.Add(-time.Minute))
	if err != nil || len(hist) != 3 {
		t.Fatalf("history = %d rows, %v", len(hist), err)
	}
	for _, h := range hist {
		if h.SnapshotTime.IsZero() {
			t.Error("history snapshot time not parsed")
		}
	}
	if hist, _ := s.GetPlayerHistory(ctx, "P1", at.Add(time.Second)); len(hist) != 0 {
		t.Error("history since-filter ignored")
	}

	// DeleteMatchScores removes only that match's per-match snapshots.
	if err := s.StoreMatchSuspicionScore(ctx, "M2", model.SuspicionScore{PlayerID: "P1", TotalScore: 5}); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteMatchScores(ctx, "M1")
	if err != nil || n != 3 {
		t.Fatalf("DeleteMatchScores = %d, %v", n, err)
	}
	if countRows(t, s, "suspicion_scores", "") != 2 {
		t.Error("M2 and cross-match snapshots must survive")
	}
	// StoreSuspicionScore derives match_id from a single-entry MatchIDs map.
	if err := s.StoreSuspicionScore(ctx, model.SuspicionScore{PlayerID: "P3", TotalScore: 1, MatchIDs: map[string]bool{"M9": true}}); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "suspicion_scores", "match_id='M9'") != 1 {
		t.Error("match_id not derived from MatchIDs")
	}
}

func TestReviewCases_UpsertPreservesModeratorStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rc := model.ReviewCase{CaseID: "C1", PlayerID: "P1", MatchID: "M1", Severity: "high",
		SuspicionScore: 65, Status: "pending", CreatedAt: time.Now(),
		DetectorsTriggered: []model.TriggeredDetector{{DetectorID: "THROW_001"}}}
	if err := s.StoreReviewCase(ctx, rc); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateReviewCaseStatus(ctx, "C1", CaseStatusInReview, "mod-a", time.Now()); err != nil {
		t.Fatal(err)
	}
	rc.SuspicionScore = 70
	rc.Status = "pending"
	if err := s.StoreReviewCase(ctx, rc); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReviewCase(ctx, "C1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CaseStatusInReview || got.AssignedTo != "mod-a" {
		t.Errorf("status/assignee reset by re-analysis: %+v", got)
	}
	if got.SuspicionScore != 70 {
		t.Errorf("analytical columns not refreshed: %v", got.SuspicionScore)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("updated_at not set")
	}
	pending, _ := s.GetPendingReviewCases(ctx, 10)
	if len(pending) != 0 {
		t.Error("in_review case listed as pending")
	}
	if err := s.UpdateReviewCaseStatus(ctx, "C1", "bogus", "", time.Now()); err == nil {
		t.Error("invalid status accepted")
	}
	if err := s.UpdateReviewCaseStatus(ctx, "nope", CaseStatusClosed, "", time.Now()); err == nil {
		t.Error("missing case accepted")
	}

	// Cross-match cases: same rule.
	xm := CrossMatchReviewCase{CaseID: "XM-P1", PlayerID: "P1", MatchIDs: []string{"M1", "M2"}, MatchCount: 2,
		Severity: "high", DecayedScore: 61, Status: "pending", CreatedAt: time.Now().Add(-time.Hour)}
	if err := s.StoreCrossMatchReviewCase(ctx, xm); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateReviewCaseStatus(ctx, "XM-P1", CaseStatusClosed, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	xm.DecayedScore = 80
	xm.Severity = "critical"
	if err := s.StoreCrossMatchReviewCase(ctx, xm); err != nil {
		t.Fatal(err)
	}
	gx, err := s.GetCrossMatchReviewCase(ctx, "XM-P1")
	if err != nil {
		t.Fatal(err)
	}
	if gx.Status != CaseStatusClosed || gx.DecayedScore != 80 || gx.Severity != "critical" {
		t.Errorf("cross-match upsert wrong: %+v", gx)
	}
	if p, _ := s.GetPendingCrossMatchReviewCases(ctx, 10); len(p) != 0 {
		t.Error("closed cross-match case listed as pending")
	}
}

func TestModeratorDecisions_RoundTripAndCaseStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "C1", PlayerID: "P1", MatchID: "M1", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	d := model.ModeratorDecision{CaseID: "C1", ModeratorID: "mod-a", Verdict: VerdictFalsePositive,
		Notes: "legit snap throw", DetectorFeedback: []model.DetectorVerdict{{DetectorID: "THROW_001", Correct: "no"}}}
	if err := s.StoreModeratorDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	rc, _ := s.GetReviewCase(ctx, "C1")
	if rc.Status != CaseStatusDecided || rc.AssignedTo != "mod-a" {
		t.Errorf("case not marked decided: %+v", rc)
	}
	list, err := s.ListModeratorDecisions(ctx, time.Time{}, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if list[0].DecisionID == "" || list[0].DecidedAt.IsZero() || list[0].Verdict != VerdictFalsePositive ||
		len(list[0].DetectorFeedback) != 1 || list[0].DetectorFeedback[0].Correct != "no" {
		t.Errorf("decision round trip: %+v", list[0])
	}
	if l, _ := s.ListModeratorDecisions(ctx, time.Now().Add(time.Hour), 0); len(l) != 0 {
		t.Error("since filter ignored")
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "C1", ModeratorID: "m", Verdict: "meh"}); err == nil {
		t.Error("invalid verdict accepted")
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "missing", ModeratorID: "m", Verdict: VerdictInconclusive}); err == nil {
		t.Error("decision on missing case accepted")
	}
	if countRows(t, s, "moderator_decisions", "") != 1 {
		t.Error("failed decisions must not be inserted")
	}
	cd, _ := s.GetCaseDecisions(ctx, "C1")
	if len(cd) != 1 {
		t.Errorf("GetCaseDecisions = %d", len(cd))
	}
}

func TestEnforcement_PersistsDurationAndMatches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := model.EnforcementAction{ActionID: "A1", PlayerID: "P1", ActionType: "temp_ban", Reason: "r",
		Duration: 7 * 24 * time.Hour, IssuedBy: "auto", IssuedAt: time.Now().Truncate(time.Second),
		EvidenceIDs: []string{"e1"}, MatchIDs: []string{"M1", "M2"}, ScoreAtTime: 88}
	if err := s.StoreEnforcementAction(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEnforcementActions(ctx, "P1", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Duration != 7*24*time.Hour || len(got[0].MatchIDs) != 2 || got[0].ScoreAtTime != 88 || !got[0].IssuedAt.Equal(a.IssuedAt) {
		t.Errorf("round trip lost fields: %+v", got[0])
	}
}

func TestHistoryProvider_ExcludesShadowAndMetaWithMatchAwareLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	// Three matches with recorded start times; M3 newest.
	for i, mid := range []string{"M1", "M2", "M3"} {
		mc := &model.MatchContext{MatchID: mid, StartTime: now.Add(time.Duration(i-3) * 24 * time.Hour), PlayerIDs: []string{"P1"}}
		if err := s.StoreMatchContext(ctx, mc, 0); err != nil {
			t.Fatal(err)
		}
		for f := 0; f < 60; f++ { // more events per match than any row cap would like
			mustStoreEvent(t, s, mkEvent("THROW_001", "P1", mid, f*10, 0.5, 0.5))
		}
		sh := mkEvent("PAT_001", "P1", mid, 5, 0.5, 0.5)
		sh.IsShadow = true
		mustStoreEvent(t, s, sh)
		mustStoreEvent(t, s, mkEvent("PAT_003", "P1", mid, 6, 0.5, 0.5))
		mustStoreEvent(t, s, mkEvent("PAT_004", "P1", mid, 7, 0.5, 0.5))
	}

	hp := NewStoreHistoryProvider(s)
	evs, err := hp.GetPlayerDetections("P1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 120 {
		t.Fatalf("got %d events, want 120 (2 matches x 60, whole matches, no shadow/meta)", len(evs))
	}
	seen := map[string]bool{}
	for _, ev := range evs {
		seen[ev.MatchID] = true
		if ev.IsShadow || ev.DetectorID == "PAT_003" || ev.DetectorID == "PAT_004" {
			t.Fatalf("leaked event %+v", ev)
		}
	}
	if seen["M1"] || !seen["M2"] || !seen["M3"] {
		t.Errorf("wrong matches selected: %v (want the two most recent by match start)", seen)
	}
}
