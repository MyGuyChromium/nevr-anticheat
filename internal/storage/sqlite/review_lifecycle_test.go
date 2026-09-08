package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Level, ThresholdVersion, the match window and CloseReason survive a round
// trip so calibration can group cases by the threshold set they were built under.
func TestReviewCases_PersistLevelThresholdAndWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	rc := model.ReviewCase{CaseID: "RC-M1-P1", PlayerID: "P1", MatchID: "M1", Severity: "high",
		SuspicionScore: 65, Level: "high_risk", ThresholdVersion: "tv-0123456789abcdef",
		TimestampStart: start, TimestampEnd: start.Add(10 * time.Minute),
		Status: "pending", CreatedAt: start}
	if err := s.StoreReviewCase(ctx, rc); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReviewCase(ctx, rc.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Level != "high_risk" || got.ThresholdVersion != rc.ThresholdVersion {
		t.Errorf("level/threshold not persisted: %+v", got)
	}
	if !got.TimestampStart.Equal(start) || !got.TimestampEnd.Equal(start.Add(10*time.Minute)) {
		t.Errorf("match window not persisted: %v .. %v", got.TimestampStart, got.TimestampEnd)
	}
	if got.CloseReason != "" {
		t.Errorf("fresh case has close reason %q", got.CloseReason)
	}

	// A re-analysis under a new threshold set refreshes the fingerprint.
	rc.ThresholdVersion = "tv-fedcba9876543210"
	rc.Level = "critical"
	if err := s.StoreReviewCase(ctx, rc); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetReviewCase(ctx, rc.CaseID)
	if got.ThresholdVersion != rc.ThresholdVersion || got.Level != "critical" {
		t.Errorf("upsert did not refresh level/threshold: %+v", got)
	}
	// A case without a match window stores NULLs and reads back zero.
	if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "RC-M1-P2", PlayerID: "P2", MatchID: "M1", CreatedAt: start}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetReviewCase(ctx, "RC-M1-P2")
	if !got.TimestampStart.IsZero() || !got.TimestampEnd.IsZero() {
		t.Errorf("zero window read back as %v .. %v", got.TimestampStart, got.TimestampEnd)
	}
	byMatch, err := s.GetReviewCasesByMatch(ctx, "M1")
	if err != nil || len(byMatch) != 2 || byMatch[0].PlayerID != "P1" {
		t.Errorf("GetReviewCasesByMatch = %v, %v", byMatch, err)
	}
}

// Re-analysis closes the pending cases of players it no longer flags, leaves
// moderator-touched cases alone, never deletes, and reopens a stale-closed
// case when the player is flagged again. A moderator's own closure is not
// reopened.
func TestCloseStaleReviewCases(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	for _, pid := range []string{"P1", "P2", "P3", "P4"} {
		if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "RC-M1-" + pid, PlayerID: pid, MatchID: "M1",
			SuspicionScore: 70, Status: "pending", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	// Another match must not be touched.
	if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "RC-M2-P2", PlayerID: "P2", MatchID: "M2", Status: "pending", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateReviewCaseStatus(ctx, "RC-M1-P3", CaseStatusInReview, "mod-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateReviewCaseStatus(ctx, "RC-M1-P4", CaseStatusClosed, "mod-a"); err != nil {
		t.Fatal(err)
	}

	closed, err := s.CloseStaleReviewCases(ctx, "M1", []string{"P1"}, "threshold raised")
	if err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("closed %d cases, want 1 (P2 only)", closed)
	}
	p2, _ := s.GetReviewCase(ctx, "RC-M1-P2")
	if p2.Status != CaseStatusClosed || p2.CloseReason != "threshold raised" {
		t.Errorf("stale case = %+v", p2)
	}
	if p1, _ := s.GetReviewCase(ctx, "RC-M1-P1"); p1.Status != CaseStatusPending {
		t.Errorf("kept player's case changed: %+v", p1)
	}
	if p3, _ := s.GetReviewCase(ctx, "RC-M1-P3"); p3.Status != CaseStatusInReview || p3.CloseReason != "" {
		t.Errorf("moderator-touched case changed: %+v", p3)
	}
	if other, _ := s.GetReviewCase(ctx, "RC-M2-P2"); other.Status != CaseStatusPending {
		t.Errorf("other match's case changed: %+v", other)
	}
	if countRows(t, s, "review_cases", "match_id='M1'") != 4 {
		t.Error("a case was deleted")
	}
	pending, _ := s.GetPendingReviewCases(ctx, 10)
	for _, rc := range pending {
		if rc.CaseID == "RC-M1-P2" {
			t.Error("stale case still listed as pending")
		}
	}

	// The player is flagged again: the stale closure is undone.
	if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "RC-M1-P2", PlayerID: "P2", MatchID: "M1",
		SuspicionScore: 80, Status: "pending", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p2, _ = s.GetReviewCase(ctx, "RC-M1-P2")
	if p2.Status != CaseStatusPending || p2.CloseReason != "" || p2.SuspicionScore != 80 {
		t.Errorf("stale-closed case not reopened: %+v", p2)
	}
	// A moderator's closure (no close reason) is preserved by the same upsert.
	if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "RC-M1-P4", PlayerID: "P4", MatchID: "M1",
		SuspicionScore: 80, Status: "pending", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if p4, _ := s.GetReviewCase(ctx, "RC-M1-P4"); p4.Status != CaseStatusClosed {
		t.Errorf("moderator-closed case reopened: %+v", p4)
	}
	// No keep list closes every pending case of the match.
	closed, err = s.CloseStaleReviewCases(ctx, "M1", nil, "")
	if err != nil || closed != 2 {
		t.Errorf("close all pending: %d, %v (want 2: P1, P2)", closed, err)
	}
	if _, err := s.CloseStaleReviewCases(ctx, "", nil, ""); err == nil {
		t.Error("empty match id accepted")
	}
}

// A second verdict on a decided or closed case is refused (single-match and
// cross-match), so calibration never sees one case counted twice; an appeal
// reopens the case and a new verdict is then accepted.
func TestStoreModeratorDecision_RefusesDecidedOrClosedCase(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "C1", PlayerID: "P1", MatchID: "M1", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	first := model.ModeratorDecision{CaseID: "C1", ModeratorID: "mod-a", Verdict: VerdictConfirmedCheat}
	if err := s.StoreModeratorDecision(ctx, first); err != nil {
		t.Fatal(err)
	}
	err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "C1", ModeratorID: "mod-a", Verdict: VerdictConfirmedCheat, Notes: "fixing notes"})
	if !errors.Is(err, ErrCaseNotDecidable) {
		t.Fatalf("repeat verdict: err = %v, want ErrCaseNotDecidable", err)
	}
	if n := countRows(t, s, "moderator_decisions", "case_id='C1'"); n != 1 {
		t.Errorf("repeat verdict inserted a decision: %d rows", n)
	}
	// Appeal path: decided -> appealed -> decided again is legitimate.
	if err := s.UpdateReviewCaseStatus(ctx, "C1", CaseStatusAppealed, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "C1", ModeratorID: "mod-b", Verdict: VerdictFalsePositive}); err != nil {
		t.Fatalf("verdict after appeal refused: %v", err)
	}
	if err := s.UpdateReviewCaseStatus(ctx, "C1", CaseStatusClosed, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "C1", ModeratorID: "mod-b", Verdict: VerdictInconclusive}); !errors.Is(err, ErrCaseNotDecidable) {
		t.Errorf("verdict on closed case: %v", err)
	}

	// Cross-match cases get the same guard. Use an independent player/scope:
	// the negative review above deliberately prevents reopening P1/M1 through
	// a new aggregate recommendation (covered by review_invalidation_test).
	xm := CrossMatchReviewCase{CaseID: "XM-P2", PlayerID: "P2", MatchIDs: []string{"M3", "M4"}, MatchCount: 2,
		Severity: "high", DecayedScore: 61, Status: "pending", CreatedAt: time.Now()}
	if err := s.StoreCrossMatchReviewCase(ctx, xm); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "XM-P2", ModeratorID: "m", Verdict: VerdictConfirmedCheat}); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "XM-P2", ModeratorID: "m", Verdict: VerdictConfirmedCheat}); !errors.Is(err, ErrCaseNotDecidable) {
		t.Errorf("repeat cross-match verdict: %v", err)
	}
	if n := countRows(t, s, "moderator_decisions", "case_id='XM-P2'"); n != 1 {
		t.Errorf("cross-match repeat inserted: %d rows", n)
	}
}

// Calibration counts a case once, using its latest decision: an appeal that
// overturns confirmed_cheat into false_positive replaces the earlier tally.
func TestComputeCalibration_OneDecisionPerCase(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "C1", PlayerID: "P1", MatchID: "M1", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mustStoreEvent(t, s, mkEvent("THROW_001", "P1", "M1", 10, 0.9, 0.9))
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "C1", ModeratorID: "a", Verdict: VerdictConfirmedCheat, DecidedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateReviewCaseStatus(ctx, "C1", CaseStatusAppealed, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "C1", ModeratorID: "b", Verdict: VerdictFalsePositive, DecidedAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ComputeCalibration(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].DetectorID != "THROW_001" {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if r.CasesReviewed != 1 || r.Confirmed != 0 || r.FalsePositive != 1 || r.EventsReviewed != 1 {
		t.Errorf("case counted more than once or wrong verdict used: %+v", r)
	}
	// Two decisions still show in the case's history.
	if cd, _ := s.GetCaseDecisions(ctx, "C1"); len(cd) != 2 {
		t.Errorf("GetCaseDecisions = %d, want 2", len(cd))
	}
}

// merged_count round-trips through both event writers.
func TestDetectionEvents_MergedCountRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	single := mkEvent("MOV_001", "P1", "M1", 10, 0.5, 0.5)
	single.MergedCount = 3
	mustStoreEvent(t, s, single)
	batch := mkEvent("MOV_001", "P1", "M1", 40, 0.5, 0.5)
	batch.MergedCount = 7
	if _, err := s.StoreDetectionEvents(ctx, []model.DetectionEvent{batch}, "initial"); err != nil {
		t.Fatal(err)
	}
	evs, err := s.GetMatchPlayerEvents(ctx, "M1", "P1")
	if err != nil || len(evs) != 2 {
		t.Fatalf("events = %v, %v", evs, err)
	}
	if evs[0].MergedCount != 3 || evs[1].MergedCount != 7 {
		t.Errorf("merged counts = %d, %d; want 3, 7", evs[0].MergedCount, evs[1].MergedCount)
	}
}

// A zero "since" means no lower bound for every range reader; it must never
// be rendered as "now".
func TestRangeReaders_ZeroSinceMeansAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if fmtDBTimeSince(time.Time{}) != dbTimeMin || fmtDBTimeSince(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) != "2026-01-02T03:04:05Z" {
		t.Fatal("fmtDBTimeSince")
	}
	if err := s.StoreMatchSuspicionScore(ctx, "M1", model.SuspicionScore{PlayerID: "P1", TotalScore: 10, EventCount: 1,
		SnapshotTime: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	hist, err := s.GetPlayerHistory(ctx, "P1", time.Time{})
	if err != nil || len(hist) != 1 {
		t.Errorf("GetPlayerHistory(zero since) = %v, %v; want the snapshot", hist, err)
	}
	ev := mkEvent("MOV_001", "P1", "M1", 1, 0.5, 0.5)
	ev.StoredAt = time.Now().Add(-time.Hour)
	mustStoreEvent(t, s, ev)
	players, err := s.GetDistinctPlayersWithEvents(ctx, time.Time{})
	if err != nil || len(players) != 1 {
		t.Errorf("GetDistinctPlayersWithEvents(zero) = %v, %v", players, err)
	}
	if _, err := s.StoreTelemetryFrames(ctx, "M1", mkFrames("P1", 0, 3)); err != nil {
		t.Fatal(err)
	}
	ids, err := s.GetMatchIDsByTimeRange(ctx, time.Time{}, time.Now().Add(time.Hour))
	if err != nil || len(ids) != 1 {
		t.Errorf("GetMatchIDsByTimeRange(zero since) = %v, %v", ids, err)
	}
}

// Migration 11 normalizes schema_migrations.applied_at rows written by the
// pre-v10 runner's datetime('now') DEFAULT, which migration 10 skipped.
func TestMigration11_NormalizesSchemaMigrationsAppliedAt(t *testing.T) {
	sqlPath := filepath.Join("..", "..", "..", "migrations", "001_initial.sql")
	initial, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Skipf("initial migration not available: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(initial)); err != nil {
		t.Fatalf("bootstrapping: %v", err)
	}
	// The old runner's table and its DEFAULT, with version 1 recorded the old way.
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, description TEXT,
			applied_at TEXT NOT NULL DEFAULT (datetime('now')))`,
		`INSERT INTO schema_migrations (version, description) VALUES (1, 'initial schema')`,
	}
	for _, st := range stmts {
		if _, err := raw.Exec(st); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}
	var legacy string
	if err := raw.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 1`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 19 || legacy[10] != ' ' {
		t.Fatalf("seed did not produce the legacy layout: %q", legacy)
	}
	raw.Close()

	s := newTestStoreAt(t, dbPath)
	rows, err := s.DB().Query(`SELECT version, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var v int
		var at string
		if err := rows.Scan(&v, &at); err != nil {
			t.Fatal(err)
		}
		seen++
		if _, err := time.Parse(dbTimeLayout, at); err != nil {
			t.Errorf("version %d applied_at %q is not in dbTimeLayout", v, at)
		}
	}
	if seen != SchemaVersion() {
		t.Errorf("schema_migrations rows = %d, want %d", seen, SchemaVersion())
	}
	if len(timestampColumns) != len(timestampColumnsV10)+15 {
		t.Errorf("timestampColumns should extend the frozen v10 list: %d vs %d", len(timestampColumns), len(timestampColumnsV10))
	}
}
