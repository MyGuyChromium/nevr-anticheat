package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// assertPhaseRuns checks the invariants every phases[] list must hold: ordered,
// contiguous, no run of negative length, inside the cap, and Playing agreeing
// with the shared phase rule.
func assertPhaseRuns(t *testing.T, runs []PhaseRun, first, last float64) {
	t.Helper()
	if len(runs) == 0 {
		t.Fatal("no phase runs")
	}
	if len(runs) > MaxSummaryPhases {
		t.Fatalf("%d runs, cap is %d", len(runs), MaxSummaryPhases)
	}
	if runs[0].Start != first || runs[len(runs)-1].End != last {
		t.Fatalf("runs cover %.3f..%.3f, want %.3f..%.3f", runs[0].Start, runs[len(runs)-1].End, first, last)
	}
	for i, r := range runs {
		if !(r.End >= r.Start) || math.IsInf(r.End, 0) {
			t.Fatalf("run %d has no valid extent: %+v", i, r)
		}
		if r.Playing != model.IsActiveGamePhase(r.Status) {
			t.Fatalf("run %d: playing=%v disagrees with status %q", i, r.Playing, r.Status)
		}
		if i == 0 {
			continue
		}
		if r.Start != runs[i-1].End {
			t.Fatalf("run %d starts at %.3f but run %d ended at %.3f (not contiguous)", i, r.Start, i-1, runs[i-1].End)
		}
		if r.Status == runs[i-1].Status {
			t.Fatalf("runs %d and %d are both %q (not merged)", i-1, i, r.Status)
		}
	}
}

func statusSession(status string, bluePoints int) *adapter.EchoVRSessionResponse {
	return &adapter.EchoVRSessionResponse{
		SessionID: "PHASES-1", GameStatus: status, BluePoints: bluePoints,
		Teams: []adapter.EchoVRTeam{{TeamName: "BLUE TEAM", Players: []adapter.EchoVRPlayer{{Name: "Alice", UserID: 101, HoldingLeft: "none", HoldingRight: "none"}}}},
	}
}

// One goal cycle: playing -> score -> (unnamed) -> round_start -> playing. The
// runs carry the normaliser's labels and sit on the clock the goal is on.
func TestSummaryPhasesGoalCycle(t *testing.T) {
	b := NewSummaryBuilder(nil)
	tick := 0
	feed := func(status string, n, blue int) {
		for i := 0; i < n; i++ {
			b.Add(statusSession(status, blue), tick, float64(tick)*0.5)
			tick++
		}
	}
	feed("playing", 4, 0) // 0.0 .. 1.5
	feed("score", 4, 2)   // 2.0 .. 3.5, the goal lands on the first score tick
	feed("", 4, 2)        // 4.0 .. 5.5
	feed("round_start", 4, 2)
	feed("playing", 4, 2) // 8.0 .. 9.5
	s := b.Finish()

	want := []PhaseRun{
		{Start: 0, End: 2, Status: "playing", Playing: true},
		{Start: 2, End: 4, Status: "round_over"},
		{Start: 4, End: 6, Status: model.PhasePostScoreGap},
		{Start: 6, End: 8, Status: "round_start"},
		{Start: 8, End: 9.5, Status: "playing", Playing: true},
	}
	if len(s.Phases) != len(want) {
		t.Fatalf("phases = %+v, want %+v", s.Phases, want)
	}
	for i := range want {
		if s.Phases[i] != want[i] {
			t.Errorf("phase %d = %+v, want %+v", i, s.Phases[i], want[i])
		}
	}
	assertPhaseRuns(t, s.Phases, 0, 9.5)
	if s.PhasesMerged != 0 {
		t.Errorf("phases_merged = %d for a plain goal cycle", s.PhasesMerged)
	}
	// Same clock as the scoring timeline: the goal is the instant play stopped.
	if len(s.Goals) != 1 || s.Goals[0].Time != s.Phases[1].Start {
		t.Fatalf("goal %+v is not at the start of the round_over run %+v", s.Goals, s.Phases[1])
	}

	doc, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), `"phases":[{"start":0,"end":2,"status":"playing","playing":true},`) {
		t.Errorf("phases JSON shape changed: %s", doc)
	}
	if strings.Contains(string(doc), "phases_merged") {
		t.Errorf("phases_merged must be omitted when nothing was merged: %s", doc)
	}
}

// A source that never names a status has no phase data: the list is omitted
// instead of claiming the whole recording was live play.
func TestSummaryPhasesOmittedWithoutStatus(t *testing.T) {
	for _, unnamed := range []string{"", "unknown"} {
		b := NewSummaryBuilder(nil)
		for i := 0; i < 10; i++ {
			b.Add(statusSession(unnamed, 0), i, float64(i))
		}
		s := b.Finish()
		if s.Phases != nil {
			t.Fatalf("status %q: phases = %+v, want none", unnamed, s.Phases)
		}
		doc, _ := json.Marshal(s)
		if strings.Contains(string(doc), `"phases"`) {
			t.Fatalf("status %q: phases key present: %s", unnamed, doc)
		}
	}
}

// A recording whose status flaps every tick (or was written to) stays inside
// the cap while it streams and in the stored document, still contiguous.
func TestSummaryPhasesAreCapped(t *testing.T) {
	b := NewSummaryBuilder(nil)
	const ticks = 20000
	for i := 0; i < ticks; i++ {
		status := "playing"
		switch {
		case i%2 == 1:
			status = fmt.Sprintf("made_up_%d", i%7)
		case i >= 5000 && i < 9000:
			status = "round_start" // one long real run must survive the folding
		}
		b.Add(statusSession(status, 0), i, float64(i)*0.05)
		if len(b.runs) > 2*MaxSummaryPhases+1 {
			t.Fatalf("builder holds %d runs at tick %d", len(b.runs), i)
		}
	}
	s := b.Finish()
	assertPhaseRuns(t, s.Phases, 0, float64(ticks-1)*0.05)
	if s.PhasesMerged == 0 {
		t.Error("phases_merged = 0 although runs were folded")
	}
	var total float64
	for _, r := range s.Phases {
		total += r.End - r.Start
	}
	if math.Abs(total-float64(ticks-1)*0.05) > 1e-6 {
		t.Errorf("runs sum to %.3f s, recording is %.3f s", total, float64(ticks-1)*0.05)
	}
}

// Sample time that runs backwards or is not a number never produces a run
// that ends before it starts.
func TestSummaryPhasesSurviveBadClock(t *testing.T) {
	b := NewSummaryBuilder(nil)
	times := []float64{0, 1, 2, 1.5, math.NaN(), 3, math.Inf(1), 4}
	statuses := []string{"playing", "playing", "score", "round_start", "playing", "playing", "score", "score"}
	for i, at := range times {
		b.Add(statusSession(statuses[i], 0), i, at)
	}
	s := b.Finish()
	assertPhaseRuns(t, s.Phases, 0, 4)
}

// An over-long status from the recording is bounded, on a rune boundary.
func TestSummaryPhaseStatusIsBounded(t *testing.T) {
	b := NewSummaryBuilder(nil)
	long := strings.Repeat("é", 100)
	b.Add(statusSession("playing", 0), 0, 0)
	b.Add(statusSession(long, 0), 1, 1)
	b.Add(statusSession(long, 0), 2, 2)
	s := b.Finish()
	if len(s.Phases) != 2 {
		t.Fatalf("phases = %+v", s.Phases)
	}
	got := s.Phases[1].Status
	if len(got) > maxPhaseStatusLen || got != strings.Repeat("é", maxPhaseStatusLen/2) {
		t.Fatalf("status %q (%d bytes) is not cut to %d bytes on a rune boundary", got, len(got), maxPhaseStatusLen)
	}
}

// The repository fixture through the whole engine: phases are stored with the
// summary, every throw lies inside a live-play run (one clock), and a summary
// stored by an older build gets its phases from the lazy rebuild.
func TestAnalyzeStoresPhasesAndOlderSummariesAreRebuilt(t *testing.T) {
	ctx := context.Background()
	engine := newTestEngine(t)
	result, err := engine.AnalyzeFile(ctx, syntheticReplay, false)
	if err != nil || result.PersistError() != nil {
		t.Fatalf("analysis failed: %v / %v", err, result.PersistError())
	}
	sum := result.MatchSummary
	if sum == nil || sum.Version != SummaryVersion {
		t.Fatalf("summary = %+v", sum)
	}
	assertPhaseRuns(t, sum.Phases, 0, sum.Phases[len(sum.Phases)-1].End)
	statuses := map[string]bool{}
	playing := 0.0
	for _, r := range sum.Phases {
		statuses[r.Status] = true
		if r.Playing {
			playing += r.End - r.Start
		}
	}
	if !statuses["playing"] || !statuses["round_over"] || !statuses["round_start"] {
		t.Fatalf("fixture names playing, score and round_start; runs = %+v", sum.Phases)
	}
	if last := sum.Phases[len(sum.Phases)-1].End; playing <= 0 || playing >= last {
		t.Fatalf("playing %.2f s of %.2f s", playing, last)
	}
	for _, th := range sum.Throws {
		inside := false
		for _, r := range sum.Phases {
			if r.Playing && th.Time >= r.Start && th.Time <= r.End {
				inside = true
			}
		}
		if !inside {
			t.Errorf("throw at %.2f s is outside every live-play run %+v", th.Time, sum.Phases)
		}
	}

	stored := func() *MatchSummary {
		t.Helper()
		doc, err := engine.Store().GetMatchSummaryJSON(ctx, result.MatchCtx.MatchID)
		if err != nil {
			t.Fatal(err)
		}
		var s MatchSummary
		if err := json.Unmarshal(doc, &s); err != nil {
			t.Fatal(err)
		}
		return &s
	}
	if got := stored(); len(got.Phases) != len(sum.Phases) {
		t.Fatalf("stored summary has %d phases, analysis produced %d", len(got.Phases), len(sum.Phases))
	}

	// What a build before phases stored: version 1, no phases.
	old := *sum
	old.Version, old.Phases = 1, nil
	old.Map = "kept-from-the-stored-document"
	oldDoc, err := json.Marshal(&old)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Store().StoreMatchSummaryJSON(ctx, old.Meta(), oldDoc); err != nil {
		t.Fatal(err)
	}
	loaded, err := engine.LoadMatchSummary(ctx, result.MatchCtx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != SummaryVersion || len(loaded.Phases) != len(sum.Phases) {
		t.Fatalf("older summary was not rebuilt: version %d, %d phases", loaded.Version, len(loaded.Phases))
	}
	for i := range sum.Phases {
		if loaded.Phases[i] != sum.Phases[i] {
			t.Errorf("rebuilt phase %d = %+v, analysis stored %+v", i, loaded.Phases[i], sum.Phases[i])
		}
	}
	if got := stored(); got.Version != SummaryVersion || len(got.Phases) != len(sum.Phases) {
		t.Fatalf("rebuilt summary was not stored: version %d, %d phases", got.Version, len(got.Phases))
	}

	// Without raw ticks (archived) the stored document is served as it is.
	if err := engine.Store().StoreMatchSummaryJSON(ctx, old.Meta(), oldDoc); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Store().DeleteMatchRawTicks(ctx, result.MatchCtx.MatchID); err != nil {
		t.Fatal(err)
	}
	loaded, err = engine.LoadMatchSummary(ctx, result.MatchCtx, nil, nil)
	if err != nil {
		t.Fatalf("an older summary without raw ticks must still load: %v", err)
	}
	if loaded.Version != 1 || loaded.Phases != nil || loaded.Map != old.Map || len(loaded.Players) != len(sum.Players) {
		t.Fatalf("stored document was not kept: version %d phases %d map %q", loaded.Version, len(loaded.Phases), loaded.Map)
	}
}

// A stored older summary whose rebuild fails for a reason other than missing
// raw ticks is still served, and the failed rebuild is not attempted again by
// this engine (it would re-read every raw tick on each reopen of the match).
func TestOlderSummaryIsServedWhenItsRebuildFails(t *testing.T) {
	ctx := context.Background()
	engine := newTestEngine(t)
	result, err := engine.AnalyzeFile(ctx, syntheticReplay, false)
	if err != nil || result.PersistError() != nil {
		t.Fatalf("analysis failed: %v / %v", err, result.PersistError())
	}
	old := *result.MatchSummary
	old.Version, old.Phases = 1, nil
	oldDoc, err := json.Marshal(&old)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Store().StoreMatchSummaryJSON(ctx, old.Meta(), oldDoc); err != nil {
		t.Fatal(err)
	}
	// RebuildSummary refuses legacy ticks for a context that claims a native
	// capture: a rebuild failure that has nothing to do with missing ticks.
	claimsTape := *result.MatchCtx
	claimsTape.Source = "tape"
	if _, err := RebuildSummary(ctx, engine.Store(), &claimsTape); err == nil {
		t.Fatal("setup: the rebuild was expected to fail for this context")
	}
	loaded, err := engine.LoadMatchSummary(ctx, &claimsTape, nil, nil)
	if err != nil || loaded.Version != 1 || len(loaded.Players) != len(old.Players) {
		t.Fatalf("stored summary was not served after a failed rebuild: %+v, %v", loaded, err)
	}
	// Remembered: even the context that could be rebuilt is not retried.
	loaded, err = engine.LoadMatchSummary(ctx, result.MatchCtx, nil, nil)
	if err != nil || loaded.Version != 1 || loaded.Phases != nil {
		t.Fatalf("failed rebuild was retried: version %d, %d phases, %v", loaded.Version, len(loaded.Phases), err)
	}
}
