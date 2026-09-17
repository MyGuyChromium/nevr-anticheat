package replay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
)

// otherObserverReplay writes the synthetic fixture as another client recorded
// it: the same session id and ticks, a different recorder name in every tick.
func otherObserverReplay(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(syntheticReplay)
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

func firstRawTick(t *testing.T, e *Engine, matchID string) string {
	t.Helper()
	ticks, err := e.Store().GetMatchRawTicks(context.Background(), matchID, 0, 0)
	if err != nil || ticks[0] == "" {
		t.Fatalf("raw tick 0: %v %v", len(ticks), err)
	}
	return ticks[0]
}

// A session id names a match, not a recording. Force used to replace the
// stored frames with another observer's while match_ticks kept the first
// observer's raw ticks. Mutation: drop the SourceDifferent refusal in finish
// and the stored roster/ticks assertions below fail.
func TestAnalyzeForceNeverMixesAnotherRecordingOfTheMatch(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	const matchID = "SYN-FIXTURE-001"
	if _, err := e.AnalyzeFile(ctx, syntheticReplay, false); err != nil {
		t.Fatal(err)
	}
	other := otherObserverReplay(t)

	// Without Force the refusal now says which of the two cases it is.
	results, err := e.AnalyzeFileAll(ctx, syntheticReplay, false)
	if err != nil || len(results) != 1 || !results[0].AlreadyStored || results[0].StoredSource != SourceIdentical {
		t.Fatalf("same file, no force: %+v, %v", results, err)
	}
	results, err = e.AnalyzeFileAll(ctx, other, false)
	if err != nil || len(results) != 1 || !results[0].AlreadyStored || results[0].StoredSource != SourceDifferent ||
		!strings.Contains(results[0].SourceDetail, "tick 0 differs") {
		t.Fatalf("other observer, no force: %+v, %v", results[0], err)
	}

	results, err = e.AnalyzeFileAll(ctx, other, true)
	if err != nil || len(results) != 1 {
		t.Fatalf("forced other observer: %v, %v", results, err)
	}
	if r := results[0]; !r.AlreadyStored || r.Replaced || r.Result != nil || r.StoredSource != SourceDifferent {
		t.Fatalf("Force let another recording take over the match: %+v", r)
	}
	if raw := firstRawTick(t, e, matchID); !strings.Contains(raw, `"client_name":"Recorder"`) {
		t.Fatalf("stored raw tick changed: %.120s", raw)
	}
	if rows := tickRows(t, e.Store(), matchID); rows.ticks != 120 || rows.frames != 480 {
		t.Fatalf("refused recording was written: %+v", rows)
	}

	// The explicit replacement swaps the raw ticks too: one recording, not two.
	results, err = e.AnalyzeFileAllWith(ctx, other, true, true)
	if err != nil || len(results) != 1 || !results[0].Replaced || !results[0].SourceReplaced {
		t.Fatalf("explicit replacement: %+v, %v", results, err)
	}
	if raw := firstRawTick(t, e, matchID); !strings.Contains(raw, `"client_name":"ObserverB"`) {
		t.Fatalf("replacement kept the previous recording's raw ticks: %.120s", raw)
	}
	found := false
	for _, w := range results[0].Warnings() {
		found = found || strings.Contains(w, "replaced a different recording")
	}
	if !found {
		t.Errorf("replacement was not reported: %v", results[0].Warnings())
	}
}

// A cut-short stored copy (crash recovery of a partial upload) is completed by
// the full recording; the full recording is never replaced by the short copy.
func TestAnalyzeForceCompletesATruncatedStoredCopy(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	data, err := os.ReadFile(syntheticReplay)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	short := filepath.Join(t.TempDir(), "short.echoreplay")
	if err := os.WriteFile(short, []byte(strings.Join(lines[:50], "")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AnalyzeFile(ctx, short, false); err != nil {
		t.Fatal(err)
	}
	res, err := e.AnalyzeFile(ctx, syntheticReplay, true)
	if err != nil || !res.Replaced || res.StoredSource != SourceExtends || res.Telemetry.TicksInserted != 70 {
		t.Fatalf("full recording over its truncated copy: %+v, %v", res, err)
	}
	results, err := e.AnalyzeFileAll(ctx, short, true)
	if err != nil || !results[0].AlreadyStored || results[0].StoredSource != SourceDifferent ||
		!strings.Contains(results[0].SourceDetail, "shorter copy") {
		t.Fatalf("short copy over the full recording: %+v, %v", results[0], err)
	}
}

// With the raw ticks archived there is nothing byte-exact to compare; the
// match start and span still tell a late clip from the stored recording.
func TestAnalyzeForceWithoutStoredRawTicksFallsBackToTheContext(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	const matchID = "SYN-FIXTURE-001"
	if _, err := e.AnalyzeFile(ctx, syntheticReplay, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store().DeleteMatchRawTicks(ctx, matchID); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(syntheticReplay)
	lines := strings.SplitAfter(string(data), "\n")
	clip := filepath.Join(t.TempDir(), "late-clip.echoreplay")
	if err := os.WriteFile(clip, []byte(strings.Join(lines[60:], "")), 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := e.AnalyzeFileAll(ctx, clip, true)
	if err != nil || !results[0].AlreadyStored || results[0].StoredSource != SourceDifferent {
		t.Fatalf("late clip without stored raw ticks: %+v, %v", results[0], err)
	}
	res, err := e.AnalyzeFile(ctx, syntheticReplay, true)
	if err != nil || !res.Replaced || res.StoredSource != SourceUnverified {
		t.Fatalf("same file without stored raw ticks: %+v, %v", res, err)
	}
	warned := false
	for _, w := range res.Warnings() {
		warned = warned || strings.Contains(w, "without verifying")
	}
	if !warned {
		t.Errorf("unverified re-analysis carries no warning: %v", res.Warnings())
	}
}

// onceDetector emits one finding for one player at one frame, with a fresh
// random event id on every run, as every catalog detector does.
type onceDetector struct{ detect.BaseDetector }

func (d *onceDetector) Evaluate(mc *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	if frameIdx != 40 || players["echovr:1001"] == nil {
		return nil
	}
	return []model.DetectionEvent{d.MakeEvent(mc, "echovr:1001", frameIdx, 2.5, 0.8, 0.9,
		model.ThrowEvidence{ReleaseSpeed: 12.5, EffectiveCap: 10}, "observed 12.5", "<= 10", model.CausalKey{FrameStart: 38, FrameEnd: 40, AnomalyType: "test"})}
}
func (d *onceDetector) Reset()                         {}
func (d *onceDetector) Configure(map[string]any) error { return nil }

// A moderator's label is keyed by event id. Re-analysis regenerates every
// finding with a new random id, which used to orphan the label. Mutation:
// remove the keepEventIdentities call in finish and the id comparison fails.
func TestReanalysisKeepsTheEventIDOfAnIdenticalFinding(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	const matchID = "SYN-FIXTURE-001"
	opts := e.analyzeOptions(false)
	opts.NewPipeline = func() *pipeline.Pipeline {
		d := &onceDetector{detect.BaseDetector{DetectorID: "TEST_ONCE", DetectorVersion: "1.0.0", DetectorName: "once",
			DetectorCategory: "movement", Weight: 0.5}}
		return pipeline.NewPipeline(e.Config(), []detect.Detector{d}, scoring.NewSuspicionScorer(e.ScorerConfig()), e.Logger())
	}
	first, err := AnalyzeFile(ctx, e.Store(), syntheticReplay, opts)
	if err != nil || len(first.Result.DetectionEvents) != 1 {
		t.Fatalf("first analysis: %+v, %v", first, err)
	}
	id := first.Result.DetectionEvents[0].EventID
	if _, err := e.Store().StoreEventReview(ctx, id, "no", "legal play", "tester"); err != nil {
		t.Fatal(err)
	}

	opts.Force = true
	second, err := AnalyzeFile(ctx, e.Store(), syntheticReplay, opts)
	if err != nil || !second.Replaced || len(second.Result.DetectionEvents) != 1 {
		t.Fatalf("re-analysis: %+v, %v", second, err)
	}
	if got := second.Result.DetectionEvents[0].EventID; got != id || second.EventIDsKept != 1 {
		t.Fatalf("identical finding got a new id %q (was %q), kept=%d: its label is orphaned", got, id, second.EventIDsKept)
	}
	events, err := e.Store().GetMatchEvents(ctx, matchID)
	if err != nil || len(events) != 1 || events[0].EventID != id {
		t.Fatalf("stored events after re-analysis: %+v, %v", events, err)
	}
	reviews, err := e.Store().GetEventReviewsByMatch(ctx, matchID)
	if err != nil || reviews[events[0].EventID].Verdict != "no" {
		t.Fatalf("label no longer joins the finding: %+v, %v", reviews, err)
	}
}
