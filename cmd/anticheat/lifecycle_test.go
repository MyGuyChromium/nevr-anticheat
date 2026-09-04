package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const fixtureReplay = "../../tests/fixtures/synthetic_session.echoreplay"

func testApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.toml")
	db := filepath.ToSlash(filepath.Join(dir, "app.db"))
	if err := os.WriteFile(cfgPath, []byte("[general]\ndb_path = \""+db+"\"\nlog_level = \"error\"\n[detector.MOV_006]\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := openApp(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.store.Close() })
	return a
}

// writeCorruptCopy writes the first n lines of the fixture followed by a line
// that exceeds the parser's line limit, so the replay parses its first ticks
// (match id known) and then fails.
func writeCorruptCopy(t *testing.T, dir string, n int) string {
	t.Helper()
	src, err := os.Open(fixtureReplay)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(dir, "corrupt.echoreplay")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	w := bufio.NewWriter(dst)
	for i := 0; i < n && sc.Scan(); i++ {
		w.WriteString(sc.Text())
		w.WriteString("\n")
	}
	w.WriteString("2026/03/01 12:00:09.000\t")
	w.WriteString(strings.Repeat("x", 9*1024*1024))
	w.WriteString("\n")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return path
}

// seedDerivedOutputs stores events, a score snapshot and a review case for a
// match, standing in for a previous analysis run under a configuration that
// produced detections (the fixture produces none under the defaults).
func seedDerivedOutputs(t *testing.T, a *app, matchID string) {
	t.Helper()
	ctx := context.Background()
	events := []model.DetectionEvent{{
		EventID: "seed-1", DetectorID: "MOV_001", DetectorVersion: "1.0.0", MatchID: matchID, PlayerID: "echovr:1001",
		FrameIndex: 10, Severity: 0.8, Confidence: 0.9, EnforcementWeight: 0.8, ObservedValue: "v",
		CausalKey: model.CausalKey{PlayerID: "echovr:1001", FrameStart: 8, FrameEnd: 12, AnomalyType: "speed"},
	}}
	if _, err := a.store.StoreDetectionEvents(ctx, events, "initial"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.StoreMatchSuspicionScore(ctx, matchID, model.SuspicionScore{PlayerID: "echovr:1001", TotalScore: 70, EventCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.StoreReviewCase(ctx, model.ReviewCase{CaseID: "RC-" + matchID + "-echovr:1001", PlayerID: "echovr:1001",
		MatchID: matchID, SuspicionScore: 70, Status: model.CaseStatusPending, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

type derivedCounts struct{ events, scores, cases, pending int }

func countDerived(t *testing.T, a *app, matchID string) derivedCounts {
	t.Helper()
	var c derivedCounts
	for _, q := range []struct {
		sql string
		dst *int
	}{
		{`SELECT COUNT(*) FROM detection_events WHERE match_id = ?`, &c.events},
		{`SELECT COUNT(*) FROM suspicion_scores WHERE match_id = ?`, &c.scores},
		{`SELECT COUNT(*) FROM review_cases WHERE match_id = ?`, &c.cases},
		{`SELECT COUNT(*) FROM review_cases WHERE match_id = ? AND status = 'pending'`, &c.pending},
	} {
		if err := a.store.DB().QueryRow(q.sql, matchID).Scan(q.dst); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// `analyze --force` must not clear the stored analysis until the replacement
// has parsed and been analyzed: a corrupt replay for a stored match fails and
// leaves events, score snapshots and cases exactly as they were. A good
// replay then replaces them, closing the case its analysis no longer justifies.
func TestAnalyzeReplay_ForceKeepsPreviousAnalysisOnParseFailure(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	const matchID = "SYN-FIXTURE-001"
	captureStdout(t, func() {
		if err := analyzeReplay(ctx, a, fixtureReplay, false); err != nil {
			t.Fatal(err)
		}
	})
	seedDerivedOutputs(t, a, matchID)
	before := countDerived(t, a, matchID)
	if before != (derivedCounts{1, 1, 1, 1}) {
		t.Fatalf("setup: %+v", before)
	}

	corrupt := writeCorruptCopy(t, t.TempDir(), 30)
	var err error
	captureStdout(t, func() { err = analyzeReplay(ctx, a, corrupt, true) })
	if err == nil || !strings.Contains(err.Error(), "reading echoreplay") {
		t.Fatalf("corrupt forced analyze: err = %v, want a parse error", err)
	}
	if after := countDerived(t, a, matchID); after != before {
		t.Errorf("failed --force destroyed the previous analysis: %+v -> %+v", before, after)
	}
	// Without --force a stored match is left alone and reported as such;
	// the file is still read through (a later session would be analyzed),
	// so the corrupt line is reported too.
	out := captureStdout(t, func() { err = analyzeReplay(ctx, a, corrupt, false) })
	if err == nil || !strings.Contains(err.Error(), "reading echoreplay") || !strings.Contains(out, "already stored") {
		t.Errorf("non-forced analyze of a stored, corrupt replay: err=%v out=%s", err, out)
	}
	if after := countDerived(t, a, matchID); after != before {
		t.Errorf("non-forced analyze changed the analysis: %+v -> %+v", before, after)
	}

	// A good replay with --force replaces the analysis exactly once: the
	// seeded events are cleared and the seeded pending case, whose player the
	// real analysis does not flag, is closed (not deleted).
	out = captureStdout(t, func() { err = analyzeReplay(ctx, a, fixtureReplay, true) })
	if err != nil {
		t.Fatalf("forced analyze: %v", err)
	}
	if !strings.Contains(out, "Cleared previous analysis for "+matchID+" (1 events, 1 score snapshots)") ||
		!strings.Contains(out, "(1 stale closed)") {
		t.Errorf("forced analyze output:\n%s", out)
	}
	if after := countDerived(t, a, matchID); after != (derivedCounts{0, 0, 1, 0}) {
		t.Errorf("after forced re-analysis: %+v (want seeded events/scores gone, case kept but not pending)", after)
	}
	rc, err := a.store.GetReviewCase(ctx, "RC-"+matchID+"-echovr:1001")
	if err != nil || rc.Status != model.CaseStatusClosed || rc.CloseReason == "" {
		t.Errorf("stale case = %+v, %v", rc, err)
	}

	// Reprocess from the database clears only after the pipeline ran.
	seedDerivedOutputs(t, a, matchID)
	captureStdout(t, func() {
		res, err := reprocessMatchFromDB(ctx, a, a.newPipeline(), matchID)
		if err != nil || res.MatchID != matchID {
			t.Errorf("reprocess: %v, %v", res, err)
		}
	})
	if after := countDerived(t, a, matchID); after != (derivedCounts{0, 0, 1, 0}) {
		t.Errorf("after reprocess: %+v", after)
	}
	if _, err := reprocessMatchFromDB(ctx, a, a.newPipeline(), "no-such-match"); err == nil {
		t.Error("reprocess of an unknown match succeeded")
	}
}

// `verdict` goes through the review queue: a second verdict on a decided case
// is refused instead of being recorded (and double-counted by calibration).
func TestRecordVerdict_RefusesRepeatVerdict(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if err := a.store.StoreReviewCase(ctx, model.ReviewCase{CaseID: "RC-M1-P1", PlayerID: "P1", MatchID: "M1",
		Status: model.CaseStatusPending, SuspicionScore: 70, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	d := model.ModeratorDecision{CaseID: "RC-M1-P1", ModeratorID: "mod", Verdict: sqlite.VerdictConfirmedCheat, DecidedAt: time.Now()}
	rc, err := recordVerdict(ctx, a, d)
	if err != nil || rc.Status != model.CaseStatusDecided || rc.AssignedTo != "mod" {
		t.Fatalf("first verdict: %+v, %v", rc, err)
	}
	d.Notes = "second attempt"
	if _, err := recordVerdict(ctx, a, d); err == nil || !strings.Contains(err.Error(), "cannot receive another verdict") {
		t.Fatalf("repeat verdict: err = %v", err)
	}
	decisions, _ := a.store.GetCaseDecisions(ctx, "RC-M1-P1")
	if len(decisions) != 1 {
		t.Errorf("repeat verdict recorded: %d decisions", len(decisions))
	}
	rows, err := a.store.ComputeCalibration(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.CasesReviewed > 1 {
			t.Errorf("calibration counted the case twice: %+v", r)
		}
	}
	if _, err := recordVerdict(ctx, a, model.ModeratorDecision{CaseID: "nope", ModeratorID: "mod", Verdict: sqlite.VerdictInconclusive}); err == nil {
		t.Error("verdict on a missing case succeeded")
	}
	if _, err := recordVerdict(ctx, a, model.ModeratorDecision{CaseID: "RC-M1-P1", ModeratorID: "", Verdict: sqlite.VerdictInconclusive}); err == nil {
		t.Error("verdict without moderator succeeded")
	}

	// Cross-match cases are decided through the store with the same guard.
	xm := sqlite.CrossMatchReviewCase{CaseID: "XM-P1", PlayerID: "P1", MatchIDs: []string{"M1", "M2"}, MatchCount: 2,
		Severity: "high", DecayedScore: 61, Status: "pending", CreatedAt: time.Now()}
	if err := a.store.StoreCrossMatchReviewCase(ctx, xm); err != nil {
		t.Fatal(err)
	}
	xd := model.ModeratorDecision{CaseID: "XM-P1", ModeratorID: "mod", Verdict: sqlite.VerdictFalsePositive, DecidedAt: time.Now()}
	if rc, err := recordVerdict(ctx, a, xd); err != nil || rc.Status != model.CaseStatusDecided {
		t.Fatalf("cross-match verdict: %+v, %v", rc, err)
	}
	if _, err := recordVerdict(ctx, a, xd); err == nil || !strings.Contains(err.Error(), "cannot receive another verdict") {
		t.Errorf("repeat cross-match verdict: %v", err)
	}
	if got, _ := a.store.GetCrossMatchReviewCase(ctx, "XM-P1"); got.Status != model.CaseStatusDecided {
		t.Errorf("cross-match case status = %q", got.Status)
	}
}

// The batch command's counters: a persist failure is an error, not a
// processed match (runBatch exits 1 on Errors > 0).
func TestBatchResult_PersistFailedCountsAsError(t *testing.T) {
	a := testApp(t)
	dir := t.TempDir()
	data, err := os.ReadFile(fixtureReplay)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m.echoreplay"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.DB().Exec("PRAGMA query_only = 1"); err != nil {
		t.Fatal(err)
	}
	analyzer := replay.NewBatchAnalyzer(a.newPipeline(), a.store,
		func() replay.FrameParser { return replay.NewJSONFrameParser() }, 1, nil)
	analyzer.SetPipelineFactory(a.newPipeline)
	analyzer.SetAnalysisOptions(a.analysisOptions())
	res, err := analyzer.AnalyzeDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 0 || res.Errors != 1 || res.PersistFailed != 1 {
		t.Errorf("read-only batch: %+v", res)
	}
}
