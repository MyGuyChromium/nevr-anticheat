package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// otherObserverFixture writes the synthetic fixture as another client
// recorded it: the same session id and ticks, another recorder name.
func otherObserverFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixtureReplay)
	if err != nil {
		t.Fatal(err)
	}
	const recorder = `"client_name":"Recorder"`
	if !strings.Contains(string(data), recorder) {
		t.Fatal("fixture no longer carries the recorder name this test rewrites")
	}
	path := filepath.Join(t.TempDir(), "observer-b.echoreplay")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), recorder, `"client_name":"ObserverB"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func storedRecorder(t *testing.T, a *app, matchID string) string {
	t.Helper()
	ticks, err := a.store.GetMatchRawTicks(context.Background(), matchID, 0, 0)
	if err != nil || len(ticks) == 0 {
		t.Fatalf("raw ticks of %s: %d, %v", matchID, len(ticks), err)
	}
	for _, name := range []string{"Recorder", "ObserverB"} {
		if strings.Contains(ticks[0], `"client_name":"`+name+`"`) {
			return name
		}
	}
	t.Fatalf("raw tick 0 names no known recorder: %.120s", ticks[0])
	return ""
}

// `analyze --force` on a DIFFERENT recording of a stored match used to print
// "already stored; derived outputs left unchanged. Re-run with --force ...",
// which is wrong twice: the file is not what is stored, and --force was given.
// The CLI now prints the engine's comparison, fails the forced run, and offers
// --replace-source with the desktop's semantics (it implies --force).
func TestAnalyzeReplay_DifferentRecordingOfAStoredMatch(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	const matchID = "SYN-FIXTURE-001"
	captureStdout(t, func() {
		if err := analyzeReplay(ctx, a, fixtureReplay, analyzeIntake{}); err != nil {
			t.Fatal(err)
		}
	})
	seedDerivedOutputs(t, a, matchID)
	before := countDerived(t, a, matchID)
	other := otherObserverFixture(t)

	// The stored recording again: the refusal says it is the same recording.
	var err error
	out := captureStdout(t, func() { err = analyzeReplay(ctx, a, fixtureReplay, analyzeIntake{}) })
	if err != nil || !strings.Contains(out, "this file is the stored recording") || !strings.Contains(out, "all 120 stored ticks match") {
		t.Fatalf("same recording, no force: err=%v\n%s", err, out)
	}

	for _, intake := range []analyzeIntake{{}, {Force: true}} {
		out = captureStdout(t, func() { err = analyzeReplay(ctx, a, other, intake) })
		if !strings.Contains(out, "a DIFFERENT recording of this match is already stored") ||
			!strings.Contains(out, "tick 0 differs") || !strings.Contains(out, "--replace-source") {
			t.Fatalf("%+v: the source conflict was not reported:\n%s", intake, out)
		}
		if strings.Contains(out, "derived outputs left unchanged") || strings.Contains(out, "Re-run with --force") {
			t.Fatalf("%+v: the misleading already-stored text is still printed:\n%s", intake, out)
		}
		if intake.Force && (err == nil || !strings.Contains(err.Error(), "different recording") || !strings.Contains(err.Error(), matchID)) {
			t.Fatalf("a forced analysis that was refused must fail the command, err = %v", err)
		}
		if !intake.Force && err != nil {
			t.Fatalf("an unforced refusal is not an error: %v", err)
		}
		if got := countDerived(t, a, matchID); got != before {
			t.Fatalf("%+v: refused recording changed derived outputs: %+v, want %+v", intake, got, before)
		}
		if name := storedRecorder(t, a, matchID); name != "Recorder" {
			t.Fatalf("%+v: stored raw ticks now come from %s", intake, name)
		}
	}

	// --replace-source alone is enough (it implies --force, as on the desktop).
	out = captureStdout(t, func() { err = analyzeReplay(ctx, a, other, analyzeIntake{ReplaceSource: true}) })
	if err != nil {
		t.Fatalf("replace-source: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Replaced the stored recording of "+matchID) || !strings.Contains(out, "tick 0 differs") ||
		!strings.Contains(out, "Cleared previous analysis") {
		t.Fatalf("replace-source outcome not reported:\n%s", out)
	}
	if name := storedRecorder(t, a, matchID); name != "ObserverB" {
		t.Fatalf("after --replace-source the stored raw ticks come from %s", name)
	}
	if got := countDerived(t, a, matchID); got.events != 0 || got.pending != 0 {
		t.Fatalf("the previous recording's findings survived the replacement: %+v", got)
	}

	// A forced re-analysis of what is now stored reports the verified source.
	out = captureStdout(t, func() { err = analyzeReplay(ctx, a, other, analyzeIntake{Force: true}) })
	if err != nil || !strings.Contains(out, "Source check: identical") {
		t.Fatalf("forced re-analysis of the stored recording: err=%v\n%s", err, out)
	}
}

// Only a case the store really wrote as reviewable counts as
// created/refreshed. Mutation: count every outcome as Created and the
// ignored / revoked / unknown rows below fail.
func TestCrossMatchRunCountsOnlyWrittenCases(t *testing.T) {
	var run crossMatchRun
	for _, outcome := range []string{
		sqlite.CrossMatchCaseCreated, sqlite.CrossMatchCaseRefreshed, sqlite.CrossMatchCaseRefreshed, sqlite.CrossMatchCaseNewCase,
		sqlite.CrossMatchCaseIgnored, sqlite.CrossMatchCaseIgnored, sqlite.CrossMatchCaseRevoked, "an_outcome_from_a_newer_store", "",
	} {
		run.count(outcome)
	}
	if run.Cases() != 4 || run.Created != 1 || run.Refreshed != 2 || run.NewCase != 1 || run.Revoked != 1 || run.Ignored != 4 {
		t.Fatalf("counts: %+v (cases %d)", run, run.Cases())
	}
	text := run.caseSummary()
	if !strings.HasPrefix(text, "4 review cases created/refreshed (1 new, 2 refreshed, 1 successor)") ||
		!strings.Contains(text, "1 recorded closed") || !strings.Contains(text, "4 not written") {
		t.Fatalf("summary: %q", text)
	}
	if quiet := (crossMatchRun{Created: 2}).caseSummary(); strings.Contains(quiet, "not written") || strings.Contains(quiet, "recorded closed") {
		t.Fatalf("summary mentions outcomes that did not happen: %q", quiet)
	}
}

// The aggregation pass reports the store's outcome: a first run creates the
// player's case, a second run refreshes it (it used to say "1 case" both
// times without knowing which, and would have counted an ignored write too).
func TestCrossMatchAggregationReportsStoreOutcomes(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var events []model.DetectionEvent
	for m := 1; m <= 4; m++ {
		for i, detector := range []string{"MOV_001", "THROW_001", "STATE_001"} {
			frame := 100 + 50*i
			events = append(events, model.DetectionEvent{
				EventID: fmt.Sprintf("xm-%d-%d", m, i), DetectorID: detector, DetectorVersion: "1.0.0",
				MatchID: fmt.Sprintf("XM-MATCH-%d", m), PlayerID: "echovr:4242", FrameIndex: frame,
				FrameRangeStart: frame - 2, FrameRangeEnd: frame + 2, Timestamp: float64(frame) * 0.067,
				Severity: 1, Confidence: 1, EnforcementWeight: 1, ObservedValue: "v: 25.0", ExpectedRange: "v: < 20.0",
				CausalKey: model.CausalKey{PlayerID: "echovr:4242", FrameStart: frame - 2, FrameEnd: frame + 2, AnomalyType: "test"},
			})
		}
	}
	if _, err := a.store.StoreDetectionEvents(ctx, events, "initial"); err != nil {
		t.Fatal(err)
	}

	var first, second crossMatchRun
	captureStdout(t, func() { first = runCrossMatchAggregation(a) })
	captureStdout(t, func() { second = runCrossMatchAggregation(a) })
	if first.Players != 1 || first.Cases() != 1 || first.Created != 1 || first.Refreshed != 0 || first.Ignored != 0 {
		t.Fatalf("first pass: %+v", first)
	}
	if second.Players != 1 || second.Cases() != 1 || second.Created != 0 || second.Refreshed != 1 || second.Ignored != 0 {
		t.Fatalf("second pass: %+v", second)
	}
	pending, err := a.store.GetPendingCrossMatchReviewCases(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].PlayerID != "echovr:4242" {
		t.Fatalf("pending cross-match cases: %+v, %v", pending, err)
	}
}
