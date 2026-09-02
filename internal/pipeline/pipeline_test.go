package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/pattern"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testConfig(mode string) *config.Config {
	cfg := config.DefaultConfig()
	for id, dc := range cfg.Detectors {
		dc.Mode = mode
		cfg.Detectors[id] = dc
	}
	cfg.Shadow.ShadowDetectors = nil
	return cfg
}

func newPipeline(cfg *config.Config, detectors []detect.Detector) (*Pipeline, *scoring.SuspicionScorer) {
	scorer := scoring.NewSuspicionScorer(scoring.ScorerConfig{
		MaxSingleContribution:         cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       cfg.Scoring.SameCategoryDiminishing,
		ReviewThreshold:               cfg.Scoring.ReviewThreshold,
		AutoEnforceThreshold:          cfg.Scoring.AutoEnforceThreshold,
		DecayHalfLifeHours:            cfg.Scoring.DecayHalfLifeHours,
		CooldownFrames:                cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           cfg.Scoring.CorrelationBonusCap,
	})
	return NewPipeline(cfg, detectors, scorer, quietLogger()), scorer
}

func matchCtx(players ...string) *model.MatchContext {
	teams := map[string]string{}
	for i, p := range players {
		if i%2 == 0 {
			teams[p] = "blue"
		} else {
			teams[p] = "orange"
		}
	}
	return &model.MatchContext{
		MatchID:         "M-TEST",
		GameMode:        "Echo_Arena",
		PlayerIDs:       players,
		TeamAssignments: teams,
		TickRate:        15,
		Source:          "test",
		Physics:         model.DefaultPhysics(),
	}
}

// cleanFrame is a plausible, slowly moving frame.
func cleanFrame(pid string, i int) model.PlayerTelemetryFrame {
	z := 5.0 + 0.1*float64(i)
	pos := model.Vec3{1, 1, z}
	return model.PlayerTelemetryFrame{
		PlayerID:          pid,
		FrameIndex:        i,
		Timestamp:         float64(i) * 0.067,
		DeltaTime:         0.067,
		Position:          pos,
		Rotation:          model.QuatIdentity(),
		LeftHandPosition:  model.Vec3{pos[0] - 0.3, 1.3, z + 0.2},
		RightHandPosition: model.Vec3{pos[0] + 0.3, 1.3, z - 0.2},
		LeftHandRotation:  model.QuatIdentity(),
		RightHandRotation: model.QuatIdentity(),
		GamePhase:         "playing",
	}
}

// speedHackFrames oscillates on Z at ~75 m/s (over the 55 m/s cap).
func speedHackFrames(pid string, n int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, n)
	for i := 0; i < n; i++ {
		f := cleanFrame(pid, i)
		f.Position[2] = 5.0 + float64(i%6)*5.0
		f.LeftHandPosition[2] = f.Position[2] + 0.2
		f.RightHandPosition[2] = f.Position[2] - 0.2
		frames[i] = f
	}
	return frames
}

type eventKey struct {
	Player, Detector string
	Start, End       int
}

func keysOf(evs []model.DetectionEvent) []eventKey {
	out := make([]eventKey, 0, len(evs))
	for _, ev := range evs {
		out = append(out, eventKey{ev.PlayerID, ev.DetectorID, ev.FrameRangeStart, ev.FrameRangeEnd})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Player != b.Player {
			return a.Player < b.Player
		}
		if a.Detector != b.Detector {
			return a.Detector < b.Detector
		}
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		return a.End < b.End
	})
	return out
}

// TestLiveContinuity_OneFramePerBatch is the regression for the live-mode
// blindness (F2/F4/F5/F131): feeding the same speed-hack stream one frame per
// ProcessMatch call with SetSkipReset must produce the same MOV_001 incidents
// as one call over the whole stream.
func TestLiveContinuity_OneFramePerBatch(t *testing.T) {
	cfg := testConfig("enforce")
	frames := speedHackFrames("P1", 90)

	pBatch, _ := newPipeline(cfg, []detect.Detector{movement.NewMov001(cfg.GetDetectorConfig("MOV_001").Params)})
	batchRes, err := pBatch.ProcessMatch(context.Background(), matchCtx("P1"), frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(batchRes.DetectionEvents) == 0 {
		t.Fatal("single-call processing produced no MOV_001 events; test stream is not a speed hack")
	}

	pLive, scorer := newPipeline(cfg, []detect.Detector{movement.NewMov001(cfg.GetDetectorConfig("MOV_001").Params)})
	pLive.SetSkipReset(true) // live contract: set before the first slice
	mc := matchCtx("P1")
	var liveEvents []model.DetectionEvent
	for i := range frames {
		res, err := pLive.ProcessMatch(context.Background(), mc, frames[i:i+1])
		if err != nil {
			t.Fatal(err)
		}
		liveEvents = append(liveEvents, res.DetectionEvents...)
	}
	fin := pLive.Finalize(mc)
	liveEvents = append(liveEvents, fin.DetectionEvents...)

	if len(liveEvents) == 0 {
		t.Fatal("live one-frame-per-batch processing produced no events: PlayerState is not persisting across batches")
	}
	got, want := keysOf(liveEvents), keysOf(batchRes.DetectionEvents)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("live incidents differ from batch incidents\n live=%v\nbatch=%v", got, want)
	}
	ps := pLive.Players()["P1"]
	if ps == nil || ps.FrameCount != 90 {
		t.Fatalf("expected persistent PlayerState with 90 frames, got %+v", ps)
	}
	if scorer.GetScore("P1").TotalScore <= 0 {
		t.Error("expected a positive live score")
	}
}

// TestDedup_SustainedAnomalyIsOneIncident covers F63/F125/F153: a detector
// that emits a sliding-window event on every frame of a sustained excursion
// must yield ONE incident spanning the excursion, with the peak severity, and
// a second excursion further than the merge window away yields a second one.
func TestDedup_SustainedAnomalyIsOneIncident(t *testing.T) {
	cfg := testConfig("enforce")
	rec := newRecorder("FAKE_SLIDE", "bio", 0)
	rec.emit = func(mc *model.MatchContext, ps *model.PlayerState, fi int) *model.DetectionEvent {
		var sev float64
		switch {
		case fi >= 10 && fi <= 70:
			sev = 0.3
			if fi == 40 {
				sev = 0.8 // peak
			}
		case fi >= 100 && fi <= 105:
			sev = 0.5
		default:
			return nil
		}
		ev := rec.event(mc, ps.PlayerID, fi, sev, 0.9, "wrist_rate")
		ev.FrameRangeStart, ev.CausalKey.FrameStart = fi-5, fi-5
		ev.ObservedValue = fmt.Sprintf("frame %d", fi)
		return ev
	}
	p, _ := newPipeline(cfg, []detect.Detector{rec})
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 140; i++ {
		frames = append(frames, cleanFrame("P1", i))
	}
	res, err := p.ProcessMatch(context.Background(), matchCtx("P1"), frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DetectionEvents) != 2 {
		t.Fatalf("got %d events, want 2 incidents: %+v", len(res.DetectionEvents), keysOf(res.DetectionEvents))
	}
	first, second := res.DetectionEvents[0], res.DetectionEvents[1]
	if first.FrameRangeStart != 5 || first.FrameRangeEnd != 70 || first.MergedCount != 61 {
		t.Errorf("first incident = [%d,%d] merged=%d, want [5,70] merged=61", first.FrameRangeStart, first.FrameRangeEnd, first.MergedCount)
	}
	if first.Severity != 0.8 || first.ObservedValue != "frame 40" || first.FrameIndex != 40 {
		t.Errorf("first incident did not keep peak severity: sev=%v observed=%q frame=%d", first.Severity, first.ObservedValue, first.FrameIndex)
	}
	if second.FrameRangeStart != 95 || second.FrameRangeEnd != 105 || second.MergedCount != 6 {
		t.Errorf("second incident = [%d,%d] merged=%d, want [95,105] merged=6", second.FrameRangeStart, second.FrameRangeEnd, second.MergedCount)
	}
	if res.EventsMerged != 65 {
		t.Errorf("EventsMerged=%d want 65", res.EventsMerged)
	}
}

// TestDeduplicator_MergeWindow exercises the unit directly: a gap within the
// window merges, a gap beyond it does not, and Flush closes what is open.
func TestDeduplicator_MergeWindow(t *testing.T) {
	d := NewDeduplicator(20)
	mk := func(start, end int, sev float64) model.DetectionEvent {
		return model.DetectionEvent{EventID: fmt.Sprintf("e%d", start), DetectorID: "X", PlayerID: "P",
			FrameIndex: end, FrameRangeStart: start, FrameRangeEnd: end, Severity: sev, Confidence: 0.5,
			CausalKey: model.CausalKey{PlayerID: "P", FrameStart: start, FrameEnd: end, AnomalyType: "a"}}
	}
	if out := d.Deduplicate([]model.DetectionEvent{mk(0, 2, 0.1)}, 2); len(out) != 0 {
		t.Fatalf("incident emitted too early: %v", out)
	}
	// 15 frames later: within window -> merged.
	if out := d.Deduplicate([]model.DetectionEvent{mk(17, 18, 0.4)}, 18); len(out) != 0 {
		t.Fatalf("within-window emission should merge, got %v", out)
	}
	if d.PendingCount() != 1 || d.MergedCount() != 1 {
		t.Fatalf("pending=%d merged=%d", d.PendingCount(), d.MergedCount())
	}
	// 30 frames after the end: beyond window -> old incident closes, new opens.
	out := d.Deduplicate([]model.DetectionEvent{mk(48, 49, 0.2)}, 49)
	if len(out) != 1 || out[0].FrameRangeStart != 0 || out[0].FrameRangeEnd != 18 || out[0].Severity != 0.4 || out[0].MergedCount != 2 {
		t.Fatalf("closed incident wrong: %+v", out)
	}
	// Silence closes the remaining one once the window has elapsed.
	if out := d.Deduplicate(nil, 69); len(out) != 0 {
		t.Fatalf("closed at 69 too early: %v", out)
	}
	if out := d.Deduplicate(nil, 70); len(out) != 1 || out[0].FrameRangeStart != 48 {
		t.Fatalf("expected close at 70: %v", out)
	}
	d.Deduplicate([]model.DetectionEvent{mk(200, 201, 0.9)}, 201)
	if out := d.Flush(); len(out) != 1 || out[0].FrameRangeStart != 200 || d.PendingCount() != 0 {
		t.Fatalf("flush wrong: %v pending=%d", out, d.PendingCount())
	}
}

// TestMov001_SelfThrottledIncidentsSurvive documents that a detector with its
// own cadence (MOV_001 clears its window after firing, so it emits every 30
// frames) still surfaces every firing as its own incident when the gap
// exceeds the merge window.
func TestMov001_SelfThrottledIncidentsSurvive(t *testing.T) {
	cfg := testConfig("enforce")
	p, _ := newPipeline(cfg, []detect.Detector{movement.NewMov001(cfg.GetDetectorConfig("MOV_001").Params)})
	res, err := p.ProcessMatch(context.Background(), matchCtx("P1"), speedHackFrames("P1", 120))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range res.DetectionEvents {
		if ev.CausalKey.AnomalyType == "impossible_speed" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("expected 3 impossible_speed incidents (frames 39/69/99), got %d: %v", n, keysOf(res.DetectionEvents))
	}
}

// TestRateLimiter_AccountsDrops covers F157's accounting half.
func TestRateLimiter_AccountsDrops(t *testing.T) {
	rl := NewRateLimiter(2)
	mk := func(i int) model.DetectionEvent {
		return model.DetectionEvent{EventID: fmt.Sprint(i), PlayerID: "P", DetectorID: "D"}
	}
	kept, dropped := rl.Filter([]model.DetectionEvent{mk(1), mk(2), mk(3), mk(4)})
	if len(kept) != 2 || dropped["P:D"] != 2 || rl.Dropped()["P:D"] != 2 {
		t.Errorf("kept=%d dropped=%v total=%v", len(kept), dropped, rl.Dropped())
	}
	if _, dropped := rl.Filter([]model.DetectionEvent{{EventID: "x", PlayerID: "Q", DetectorID: "D"}}); dropped != nil {
		t.Errorf("unexpected drop for fresh key: %v", dropped)
	}
}

// TestValidator_SanitizesAndDerivesBounds covers F230.
func TestValidator_SanitizesAndDerivesBounds(t *testing.T) {
	cfg := config.DefaultConfig()
	v := NewFrameValidator(cfg)
	mc := matchCtx()

	f := cleanFrame("P1", 3)
	f.LeftHandPosition = model.Vec3{math.NaN(), 0, 0}
	f.RightHandRotation = model.Quat{math.Inf(1), 0, 0, 0}
	f.Disc = &model.DiscState{Position: model.Vec3{1, math.NaN(), 2}, Velocity: model.Vec3{1, 1, 1}, Speed: 1.7}
	sanitized, err := v.Validate(&f, mc)
	if err != nil {
		t.Fatalf("frame with bad hands/disc must not be rejected: %v", err)
	}
	if !f.LeftHandPosition.IsZero() || f.RightHandRotation != (model.Quat{}) || f.Disc != nil {
		t.Errorf("not sanitized: hand=%v rot=%v disc=%v", f.LeftHandPosition, f.RightHandRotation, f.Disc)
	}
	if len(sanitized) != 3 {
		t.Errorf("sanitized=%v", sanitized)
	}

	// Zero physics must fall back to defaults instead of rejecting everything.
	zero := matchCtx()
	zero.Physics = model.PhysicsConstants{}
	g := cleanFrame("P1", 0)
	g.Position = model.Vec3{2, 2, 60}
	if _, err := v.Validate(&g, zero); err != nil {
		t.Errorf("zero physics rejected an in-arena frame: %v", err)
	}
	// Bounds derive from physics: shrink the arena and the same frame fails.
	small := matchCtx()
	small.Physics.ArenaLength = 40
	if _, err := v.Validate(&g, small); err == nil {
		t.Error("expected out-of-bounds rejection with a 40 m arena")
	}
	// Reason codes are typed.
	h := cleanFrame("P1", 0)
	h.Position = model.Vec3{}
	_, err = v.Validate(&h, mc)
	var verr *ValidationError
	if !errorsAs(err, &verr) || verr.Reason != ReasonZeroPosition {
		t.Errorf("expected typed zero_position error, got %v", err)
	}
}

func errorsAs(err error, target **ValidationError) bool {
	v, ok := err.(*ValidationError)
	if ok {
		*target = v
	}
	return ok
}

// recorder is a fake detector that records which players it saw per frame
// and optionally emits events.
// recorder is a fake detector following the detector contract: it receives
// the full warmed-up player map (offered[fi]) and evaluates only the players
// with a fresh frame at fi via detect.ActivePlayers (seen[fi]).
type recorder struct {
	detect.BaseDetector
	seen    map[int][]string // players evaluated at fi (active only)
	offered map[int][]string // every player present in the map at fi
	emit    func(mc *model.MatchContext, ps *model.PlayerState, fi int) *model.DetectionEvent
	flush   func(mc *model.MatchContext, fi int) []model.DetectionEvent
	calls   int
}

func newRecorder(id, category string, warmup int) *recorder {
	return &recorder{
		BaseDetector: detect.BaseDetector{DetectorID: id, DetectorVersion: "t", DetectorName: id,
			DetectorCategory: category, Warmup: warmup, Weight: 0.9},
		seen:    map[int][]string{},
		offered: map[int][]string{},
	}
}

func (r *recorder) Reset() {
	r.seen = map[int][]string{}
	r.offered = map[int][]string{}
}
func (r *recorder) Configure(map[string]any) error { return nil }
func (r *recorder) Evaluate(mc *model.MatchContext, players map[string]*model.PlayerState, fi int) []model.DetectionEvent {
	r.calls++
	for pid := range players {
		r.offered[fi] = append(r.offered[fi], pid)
	}
	var evs []model.DetectionEvent
	for _, ps := range detect.ActivePlayers(players, fi) {
		r.seen[fi] = append(r.seen[fi], ps.PlayerID)
		if r.emit != nil {
			if ev := r.emit(mc, ps, fi); ev != nil {
				evs = append(evs, *ev)
			}
		}
	}
	return evs
}

// FlushTracks makes the recorder a TrackFlusher when flush is set.
func (r *recorder) FlushTracks(mc *model.MatchContext, fi int) []model.DetectionEvent {
	if r.flush == nil {
		return nil
	}
	return r.flush(mc, fi)
}

func (r *recorder) event(mc *model.MatchContext, pid string, fi int, sev, conf float64, anomaly string) *model.DetectionEvent {
	ev := r.MakeEvent(mc, pid, fi, float64(fi)*0.067, sev, conf,
		model.MovementEvidence{DetectorSpecific: "test"}, "observed", "expected",
		model.CausalKey{PlayerID: pid, FrameStart: fi, FrameEnd: fi, AnomalyType: anomaly})
	return &ev
}

// TestWarmupIsPerPlayer_AndStalePlayersAreNotEvaluated covers F228 and the
// stale-state rule of contract 6.
func TestWarmupIsPerPlayer_AndStalePlayersAreNotEvaluated(t *testing.T) {
	cfg := testConfig("enforce")
	rec := newRecorder("FAKE_001", "movement", 5)
	p, _ := newPipeline(cfg, []detect.Detector{rec})

	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 40; i++ {
		frames = append(frames, cleanFrame("A", i))
		if i >= 20 { // B joins late
			frames = append(frames, cleanFrame("B", i))
		}
		if i%2 == 1 { // C only has frames on odd indices
			frames = append(frames, cleanFrame("C", i))
		}
	}
	if _, err := p.ProcessMatch(context.Background(), matchCtx("A"), frames); err != nil {
		t.Fatal(err)
	}
	has := func(fi int, pid string) bool {
		for _, s := range rec.seen[fi] {
			if s == pid {
				return true
			}
		}
		return false
	}
	// A: FrameCount after frame i is i+1; eligible when > 5, i.e. from frame 5.
	if has(4, "A") || !has(5, "A") {
		t.Errorf("A warmup wrong: seen@4=%v seen@5=%v", has(4, "A"), has(5, "A"))
	}
	// B joins at 20: eligible from frame 25, not from 20 (global index would allow).
	if has(20, "B") || has(24, "B") || !has(25, "B") {
		t.Errorf("late joiner B not gated by its own frame count: 20=%v 24=%v 25=%v", has(20, "B"), has(24, "B"), has(25, "B"))
	}
	// C has no frame at even indices: must never be evaluated there.
	for fi := 0; fi < 40; fi += 2 {
		if has(fi, "C") {
			t.Errorf("C evaluated at frame %d without a frame (stale state re-scored)", fi)
		}
	}
	if !has(39, "C") {
		t.Error("C should be evaluated at frame 39 once warmed up")
	}
	// Cross-player lookups: once warmed up, C is still OFFERED to the
	// detector on even frames (STATE_007 victims, MOV_003 collision
	// partners), it is just stale there and must not be scored.
	offered := func(fi int, pid string) bool {
		for _, s := range rec.offered[fi] {
			if s == pid {
				return true
			}
		}
		return false
	}
	if !offered(30, "C") || !offered(30, "A") || !offered(30, "B") {
		t.Errorf("full player map not passed at frame 30: %v", rec.offered[30])
	}
	if offered(10, "B") {
		t.Error("B has no frames before 20 and must not be in the map at frame 10")
	}
	if ps := p.Players()["C"]; ps == nil || !detect.IsStale(ps, 30) || detect.IsStale(ps, 39) {
		t.Errorf("C staleness wrong: %+v", ps)
	}
}

// TestFullMap_StalePlayerIsNotRescoredByRealDetector proves the contract end
// to end with MOV_001: a speed hacker who leaves the match at frame 40 must
// produce no incident after its last frame even though its PlayerState (with
// a 75 m/s velocity) stays in the map for the rest of the match.
func TestFullMap_StalePlayerIsNotRescoredByRealDetector(t *testing.T) {
	cfg := testConfig("enforce")
	p, _ := newPipeline(cfg, []detect.Detector{movement.NewMov001(cfg.GetDetectorConfig("MOV_001").Params)})
	var frames []model.PlayerTelemetryFrame
	hack := speedHackFrames("H", 40)
	for i := 0; i < 200; i++ {
		frames = append(frames, cleanFrame("A", i))
		if i < 40 {
			frames = append(frames, hack[i])
		}
	}
	res, err := p.ProcessMatch(context.Background(), matchCtx("A", "H"), frames)
	if err != nil {
		t.Fatal(err)
	}
	hackEvents := 0
	for _, ev := range res.DetectionEvents {
		if ev.PlayerID != "H" {
			t.Errorf("clean player scored: %+v", ev)
			continue
		}
		hackEvents++
		if ev.FrameRangeStart > 39 || ev.FrameIndex > 39 {
			t.Errorf("stale player re-scored after leaving: frames %d-%d (index %d)", ev.FrameRangeStart, ev.FrameRangeEnd, ev.FrameIndex)
		}
	}
	if hackEvents == 0 {
		t.Fatal("expected MOV_001 to fire for H while it was present")
	}
	if ps := p.Players()["H"]; ps == nil || ps.LastFrameIdx != 39 || ps.Speed == 0 {
		t.Fatalf("H's state should remain in the map with its last kinematics: %+v", ps)
	}
}

// TestSkipResetPreservesSeededState is the root-cause regression for the
// live path: with SetSkipReset the first ProcessMatch must only create the
// roster, never wipe a scorer (or detector/dedup state) that already holds
// data, and no batch may close incidents early.
func TestSkipResetPreservesSeededState(t *testing.T) {
	cfg := testConfig("enforce")
	rec := newRecorder("FAKE_003", "movement", 0)
	p, scorer := newPipeline(cfg, []detect.Detector{rec})
	p.SetSkipReset(true)

	mc := matchCtx("P1")
	seed := rec.event(mc, "P1", 100, 0.9, 0.9, "seeded")
	seed.EnforcementWeight = 0.8
	scorer.IngestEvent(*seed)
	before := scorer.GetScore("P1")
	if before.EventCount != 1 || before.TotalScore <= 0 {
		t.Fatalf("seed not scored: %+v", before)
	}

	res, err := p.ProcessMatch(context.Background(), mc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.PlayerScores["P1"]; got.EventCount != 1 || got.TotalScore != before.TotalScore {
		t.Fatalf("first live slice reset the seeded scorer: %+v", got)
	}
	if p.Players() == nil || p.Players()["P1"] == nil {
		t.Fatal("roster not created on the first live slice")
	}

	// A sliding-window emission on every frame of the first slice must stay
	// ONE open incident across the slice boundary (no offline flush).
	rec.emit = func(mc *model.MatchContext, ps *model.PlayerState, fi int) *model.DetectionEvent {
		return rec.event(mc, ps.PlayerID, fi, 0.5, 0.9, "slide")
	}
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 10; i++ {
		frames = append(frames, cleanFrame("P1", i))
	}
	res, err = p.ProcessMatch(context.Background(), mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DetectionEvents) != 0 || p.dedup.PendingCount() != 1 {
		t.Fatalf("live slice closed incidents early: emitted=%d pending=%d", len(res.DetectionEvents), p.dedup.PendingCount())
	}
	fin := p.Finalize(mc)
	if len(fin.DetectionEvents) != 1 || fin.DetectionEvents[0].FrameRangeStart != 0 || fin.DetectionEvents[0].FrameRangeEnd != 9 {
		t.Fatalf("Finalize should close the one open incident spanning 0-9: %+v", fin.DetectionEvents)
	}
}

// TestFlushTracksAtMatchEnd: a TrackFlusher's match-end emissions go through
// the same validate/dedup/rate-limit/scoring path as frame events, both at
// the end of an offline ProcessMatch and from Finalize on the live path.
func TestFlushTracksAtMatchEnd(t *testing.T) {
	cfg := testConfig("enforce")
	build := func() (*Pipeline, *recorder, *scoring.SuspicionScorer) {
		rec := newRecorder("FAKE_FLUSH", "throw", 0)
		rec.flush = func(mc *model.MatchContext, fi int) []model.DetectionEvent {
			good := rec.event(mc, "P1", fi, 0.7, 0.9, "in_flight")
			bad := rec.event(mc, "P1", fi, 0.7, 0.9, "broken")
			bad.ObservedValue = "" // fails Validate: must be dropped and counted
			return []model.DetectionEvent{*good, *bad}
		}
		p, scorer := newPipeline(cfg, []detect.Detector{rec})
		return p, rec, scorer
	}
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 12; i++ {
		frames = append(frames, cleanFrame("P1", i))
	}

	p, _, scorer := build()
	res, err := p.ProcessMatch(context.Background(), matchCtx("P1"), frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DetectionEvents) != 1 || res.DetectionEvents[0].CausalKey.AnomalyType != "in_flight" || res.DetectionEvents[0].FrameIndex != 11 {
		t.Fatalf("offline flush events not routed: %+v", res.DetectionEvents)
	}
	if res.EventsInvalid != 1 {
		t.Errorf("invalid flush emission not counted: %d", res.EventsInvalid)
	}
	if scorer.GetScore("P1").EventCount != 1 {
		t.Error("flushed event not scored")
	}

	pLive, _, scorerLive := build()
	pLive.SetSkipReset(true)
	mc := matchCtx("P1")
	res, err = pLive.ProcessMatch(context.Background(), mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DetectionEvents) != 0 {
		t.Fatalf("live slice must not flush tracks: %+v", res.DetectionEvents)
	}
	fin := pLive.Finalize(mc)
	if len(fin.DetectionEvents) != 1 || fin.DetectionEvents[0].CausalKey.AnomalyType != "in_flight" {
		t.Fatalf("Finalize did not flush tracks: %+v", fin.DetectionEvents)
	}
	if scorerLive.GetScore("P1").EventCount != 1 {
		t.Error("live flushed event not scored")
	}
	// Nothing to flush before any frame was processed.
	pEmpty, _, _ := build()
	if r, _ := pEmpty.ProcessMatch(context.Background(), matchCtx("P1"), nil); len(r.DetectionEvents) != 0 {
		t.Errorf("flush without frames emitted %+v", r.DetectionEvents)
	}
}

// TestMatchStartAnchorsEventTimes: offline and live, event times are match
// start + event timestamp, never the ingest wall clock.
func TestMatchStartAnchorsEventTimes(t *testing.T) {
	cfg := testConfig("enforce")
	rec := newRecorder("FAKE_TIME", "bio", 0)
	rec.emit = func(mc *model.MatchContext, ps *model.PlayerState, fi int) *model.DetectionEvent {
		if fi != 3 {
			return nil
		}
		return rec.event(mc, ps.PlayerID, fi, 0.5, 0.9, "t")
	}
	p, scorer := newPipeline(cfg, []detect.Detector{rec})
	mc := matchCtx("P1")
	mc.StartTime = time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC)
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 5; i++ {
		frames = append(frames, cleanFrame("P1", i))
	}
	if _, err := p.ProcessMatch(context.Background(), mc, frames); err != nil {
		t.Fatal(err)
	}
	want := mc.StartTime.Add(time.Duration(3 * 0.067 * float64(time.Second)))
	if got := scorer.GetScore("P1").FirstEventTime; got.Sub(want).Abs() > time.Millisecond {
		t.Errorf("FirstEventTime=%v want %v (match start + 0.201s)", got, want)
	}
}

func TestPhysicsFromConfig(t *testing.T) {
	if got, want := PhysicsFromConfig(nil), model.DefaultPhysics(); got != want {
		t.Errorf("nil config: %+v", got)
	}
	cfg := config.DefaultConfig()
	cfg.Physics.DiscSpeedCap = 21.5
	cfg.Physics.GrabRange = 0 // unset -> default
	ph := PhysicsFromConfig(cfg)
	if ph.DiscSpeedCap != 21.5 || ph.GrabRange != model.DefaultPhysics().GrabRange || ph.GoalZ != model.DefaultPhysics().GoalZ {
		t.Errorf("physics from config: %+v", ph)
	}
}

// TestDeterministicEventOrder covers F229.
func TestDeterministicEventOrder(t *testing.T) {
	cfg := testConfig("enforce")
	run := func() []string {
		rec := newRecorder("FAKE_002", "movement", 0)
		rec.emit = func(mc *model.MatchContext, ps *model.PlayerState, fi int) *model.DetectionEvent {
			if fi != 3 {
				return nil
			}
			return rec.event(mc, ps.PlayerID, fi, 0.5, 0.9, "x_"+ps.PlayerID)
		}
		p, _ := newPipeline(cfg, []detect.Detector{rec})
		var frames []model.PlayerTelemetryFrame
		for i := 0; i < 5; i++ {
			for _, pid := range []string{"zeta", "alpha", "mid", "beta", "omega"} {
				frames = append(frames, cleanFrame(pid, i))
			}
		}
		res, err := p.ProcessMatch(context.Background(), matchCtx(), frames)
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, ev := range res.DetectionEvents {
			order = append(order, ev.PlayerID)
		}
		return order
	}
	want := []string{"alpha", "beta", "mid", "omega", "zeta"}
	for i := 0; i < 20; i++ {
		got := run()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("run %d: order %v, want %v", i, got, want)
		}
	}
}

// TestPat004_IgnoresShadowEvents covers F132/F156.
func TestPat004_IgnoresShadowEvents(t *testing.T) {
	build := func(shadow bool) (*Pipeline, []detect.Detector) {
		cfg := testConfig("enforce")
		mode := "enforce"
		if shadow {
			mode = "shadow"
		}
		var dets []detect.Detector
		for _, spec := range []struct{ id, cat string }{{"FAKE_MOV", "movement"}, {"FAKE_BIO", "bio"}, {"FAKE_THROW", "throw"}} {
			cfg.Detectors[spec.id] = config.DetectorConfig{Enabled: true, Mode: mode, EnforcementWeight: 0.9}
			rec := newRecorder(spec.id, spec.cat, 0)
			anomaly := spec.id
			rec.emit = func(mc *model.MatchContext, ps *model.PlayerState, fi int) *model.DetectionEvent {
				if fi != 0 {
					return nil
				}
				return rec.event(mc, ps.PlayerID, fi, 0.9, 0.95, anomaly)
			}
			dets = append(dets, rec)
		}
		dets = append(dets, pattern.NewPat004(cfg.GetDetectorConfig("PAT_004").Params))
		p, _ := newPipeline(cfg, dets)
		return p, dets
	}
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 80; i++ {
		frames = append(frames, cleanFrame("P1", i))
	}
	countPat := func(res *MatchResult) int {
		n := 0
		for _, ev := range res.DetectionEvents {
			if ev.DetectorID == "PAT_004" {
				n++
			}
		}
		return n
	}

	p, _ := build(true)
	res, err := p.ProcessMatch(context.Background(), matchCtx("P1"), frames)
	if err != nil {
		t.Fatal(err)
	}
	if n := countPat(res); n != 0 {
		t.Errorf("PAT_004 fired %d times from shadow-only events", n)
	}

	p, _ = build(false)
	res, err = p.ProcessMatch(context.Background(), matchCtx("P1"), frames)
	if err != nil {
		t.Fatal(err)
	}
	if n := countPat(res); n != 1 {
		t.Errorf("PAT_004 fired %d times from three scored categories, want 1", n)
	}
}

// TestInvalidFrameReasons covers F155.
func TestInvalidFrameReasons(t *testing.T) {
	cfg := testConfig("enforce")
	p, _ := newPipeline(cfg, nil)
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 10; i++ {
		f := cleanFrame("P1", i)
		if i%2 == 0 {
			f.Position = model.Vec3{}
		}
		frames = append(frames, f)
		g := cleanFrame("P2", i)
		g.Position[0] = 500
		frames = append(frames, g)
	}
	res, err := p.ProcessMatch(context.Background(), matchCtx("P1", "P2"), frames)
	if err != nil {
		t.Fatal(err)
	}
	if res.InvalidFrames != 15 {
		t.Errorf("InvalidFrames=%d want 15", res.InvalidFrames)
	}
	if res.InvalidFrameReasons[ReasonZeroPosition] != 5 || res.InvalidFrameReasons[ReasonOutOfBounds] != 10 {
		t.Errorf("reasons=%v", res.InvalidFrameReasons)
	}
	if res.InvalidFramesByPlayer["P2"] != 10 {
		t.Errorf("by player=%v", res.InvalidFramesByPlayer)
	}
}

// TestInvalidEmissionsAreDropped covers F204: a detector that bypasses
// MakeEvent cannot push NaN severities into the scorer.
func TestInvalidEmissionsAreDropped(t *testing.T) {
	cfg := testConfig("enforce")
	rec := newRecorder("FAKE_NAN", "movement", 0)
	rec.emit = func(mc *model.MatchContext, ps *model.PlayerState, fi int) *model.DetectionEvent {
		if fi != 2 {
			return nil
		}
		ev := rec.event(mc, ps.PlayerID, fi, 0.5, 0.9, "nan")
		ev.Severity = math.NaN()
		return ev
	}
	p, scorer := newPipeline(cfg, []detect.Detector{rec})
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 5; i++ {
		frames = append(frames, cleanFrame("P1", i))
	}
	res, err := p.ProcessMatch(context.Background(), matchCtx("P1"), frames)
	if err != nil {
		t.Fatal(err)
	}
	if res.EventsInvalid != 1 || len(res.DetectionEvents) != 0 {
		t.Errorf("EventsInvalid=%d events=%d", res.EventsInvalid, len(res.DetectionEvents))
	}
	if s := scorer.GetScore("P1").TotalScore; s != 0 || math.IsNaN(s) {
		t.Errorf("score polluted: %v", s)
	}
}

// TestTeamFromFrame covers contract 4 on the pipeline side.
func TestTeamFromFrame(t *testing.T) {
	cfg := testConfig("enforce")
	p, _ := newPipeline(cfg, nil)
	f := cleanFrame("P9", 0)
	f.Team = "orange"
	if _, err := p.ProcessMatch(context.Background(), matchCtx(), []model.PlayerTelemetryFrame{f}); err != nil {
		t.Fatal(err)
	}
	if got := p.Players()["P9"].Team; got != "orange" {
		t.Errorf("team=%q", got)
	}
}

// TestSuddenDeathIsActive covers F126.
func TestSuddenDeathIsActive(t *testing.T) {
	mc := matchCtx()
	if !mc.IsActivePhase("sudden_death") {
		t.Error("sudden_death must be an active phase")
	}
	if mc.IsActivePhase("pre_sudden_death") || mc.IsActivePhase("post_sudden_death") {
		t.Error("pre/post sudden death transitions must stay inactive")
	}
}
