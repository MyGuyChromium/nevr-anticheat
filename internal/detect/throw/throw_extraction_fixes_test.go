package throw

import (
	"math"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// A throw 0.1 m/s over the cap must rank by its over-cap margin whatever the
// one-interval hand sample reads. Mutation: restoring
// `severity = math.Max(severity, ratioSev)` for ratios above max_speed_ratio
// makes the 2 m/s-hand case 0.998 and fails the equality below.
func TestThrow001_HandRatioNeverRaisesSeverity(t *testing.T) {
	want := model.SigmoidConfidence(19.0, 18.9, 2.0)
	if want > 0.6 {
		t.Fatalf("fixture no longer barely over the cap: %.3f", want)
	}
	for _, hand := range []float64{15, 4.2, 2} {
		players := withThrow("p1", 100, 19.0, 0)
		players["p1"].LastThrow.HandSpeed = hand
		events := NewThrow001(nil).Evaluate(testCtx(), players, 100)
		if len(events) != 1 {
			t.Fatalf("hand %.1f: events=%d, want 1", hand, len(events))
		}
		evidence := events[0].Evidence.(model.ThrowEvidence)
		if !near(evidence.SpeedRatio, 19.0/hand, 1e-9) {
			t.Fatalf("hand %.1f: speed ratio %.3f must stay as evidence context", hand, evidence.SpeedRatio)
		}
		if !near(events[0].Severity, want, 1e-12) || !near(events[0].Confidence, want*0.9, 1e-12) {
			t.Fatalf("hand %.1f: severity %.4f confidence %.4f, want the over-cap sigmoid %.4f for a 0.1 m/s excess",
				hand, events[0].Severity, events[0].Confidence, want)
		}
	}
}

func releaseWindowWithVelocities(frame int, lastHeld, firstFree *model.Vec3) *model.ReleaseObservation {
	return &model.ReleaseObservation{PlayerID: "p1", FirstFreeFrame: frame, StartFrame: frame - 1, EndFrame: frame,
		StartTime: float64(frame-1) * 0.067, EndTime: float64(frame) * 0.067,
		PlayerMovement: []model.MovementObservation{
			{FrameIndex: frame - 1, Timestamp: float64(frame-1) * 0.067, ReportedVelocity: lastHeld},
			{FrameIndex: frame, Timestamp: float64(frame) * 0.067, ReportedVelocity: firstFree},
		}}
}

// The player was boosting at 8 m/s along the throw (engine-reported), but the
// one-interval position difference read 3 m/s. The movement context must come
// from the engine value. Mutation: computing playerRelativeVelocity from
// t.PlayerVelocity again yields 19.75 (over the cap) instead of 14.75.
func TestThrow001_PlayerRelativeUsesEngineReportedVelocity(t *testing.T) {
	players := withThrow("p1", 100, 22.75, 0)
	release := players["p1"].LastThrow
	release.PlayerVelocity = model.Vec3{3, 0, 0}
	release.ReleaseWindow = releaseWindowWithVelocities(100, &model.Vec3{7, 0, 0}, &model.Vec3{8, 0, 0})
	events := NewThrow001(nil).Evaluate(testCtx(), players, 100)
	if len(events) != 1 {
		t.Fatalf("events=%d, want 1 (the compared speed is unchanged)", len(events))
	}
	evidence := events[0].Evidence.(model.ThrowEvidence)
	if !near(evidence.PlayerSpeed, 8, 1e-9) || !near(evidence.AlignedMovementSpeed, 8, 1e-9) || !near(evidence.PlayerRelativeSpeed, 14.75, 1e-9) {
		t.Fatalf("movement context not from the first-free engine velocity: %+v", evidence)
	}
	if !near(evidence.ReleaseSpeed, 22.75, 1e-9) || !near(evidence.EffectiveCap, 18.9, 1e-9) {
		t.Fatalf("compared speed or cap changed: %+v", evidence)
	}
	if !strings.Contains(events[0].ObservedValue, "player-relative 14.75") || !strings.Contains(events[0].ObservedValue, playerVelocityEngineFirstFree) {
		t.Fatalf("observed text does not name the velocity source: %q", events[0].ObservedValue)
	}

	// Only the last held sample carries a reported velocity: use it, labelled.
	release.ReleaseWindow = releaseWindowWithVelocities(100, &model.Vec3{7, 0, 0}, nil)
	events = NewThrow001(nil).Evaluate(testCtx(), players, 100)
	if evidence = events[0].Evidence.(model.ThrowEvidence); !near(evidence.PlayerRelativeSpeed, 15.75, 1e-9) ||
		!strings.Contains(events[0].ObservedValue, playerVelocityEngineLastHeld) {
		t.Fatalf("last-held fallback wrong: %+v %q", evidence, events[0].ObservedValue)
	}
}

// Without any engine-reported velocity the text must not present a
// position-difference figure as the player-relative speed. Mutation: dropping
// the playerVelocityPositionDifference text branch prints
// "player-relative 19.75" and fails the first assertion.
func TestThrow001_PlayerRelativeTextAbstainsWithoutEngineVelocity(t *testing.T) {
	for name, window := range map[string]*model.ReleaseObservation{
		"no_window":   nil,
		"no_velocity": releaseWindowWithVelocities(100, nil, nil),
		"non_finite":  releaseWindowWithVelocities(100, nil, &model.Vec3{math.NaN(), 0, 0}),
	} {
		players := withThrow("p1", 100, 22.75, 0)
		players["p1"].LastThrow.PlayerVelocity = model.Vec3{3, 0, 0}
		players["p1"].LastThrow.ReleaseWindow = window
		events := NewThrow001(nil).Evaluate(testCtx(), players, 100)
		if len(events) != 1 {
			t.Fatalf("%s: events=%d", name, len(events))
		}
		text := events[0].ObservedValue
		if !strings.Contains(text, "player-relative unavailable") || !strings.Contains(text, "position-difference estimate 19.75") {
			t.Fatalf("%s: unlabelled estimate: %q", name, text)
		}
		if evidence := events[0].Evidence.(model.ThrowEvidence); !near(evidence.PlayerRelativeSpeed, 19.75, 1e-9) {
			t.Fatalf("%s: fallback evidence changed: %+v", name, evidence)
		}
	}
}

func idleShotState(pid string, frame int) *model.PlayerState {
	ps := mechanicsThrowState(frame, 12, 0)
	ps.PlayerID = pid
	ps.LastThrow = nil
	return ps
}

// The pipeline does not dispatch detectors outside active play, so a goal or
// pause is a frame-index and timestamp gap between Evaluate calls. The
// supporting window must span it. Mutation: restoring the
// `frame > h.lastFrame+1 || dt > .2` reset makes the third count 1.
func TestThrow005WindowSurvivesInactivePhaseDispatchGaps(t *testing.T) {
	d, records := shotRecorder()
	evaluate := func(frame int, release bool) {
		ps := idleShotState("p1", frame)
		if release {
			ps = mechanicsThrowState(frame, 12, float64(frame))
		}
		d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
	}
	for frame := 1; frame <= 10; frame++ {
		evaluate(frame, frame == 2 || frame == 5)
	}
	// Frames 11-20 (a goal celebration, 0.67 s) are never dispatched.
	for frame := 21; frame <= 23; frame++ {
		evaluate(frame, frame == 22)
	}
	if len(*records) != 3 {
		t.Fatalf("records=%d, want 3", len(*records))
	}
	for i, want := range []float64{1, 2, 3} {
		if got := (*records)[i].Metrics["observed_release_count"]; got != want {
			t.Fatalf("release %d: observed_release_count=%v, want %v (window wiped at the dispatch gap)", i, got, want)
		}
	}
	if last := (*records)[2].Metrics; last["support_start_frame"] != 2 || last["support_end_frame"] != 22 {
		t.Fatalf("support bounds wrong: %+v", last)
	}
}

// A player who misses one sample (stale LastFrameIdx, still on the roster)
// keeps the window; a rewind still starts over and a player who left the
// roster is released. Mutations: deleting the history of every non-active
// player again makes the second count 1; removing the rewind reset makes the
// third count 3.
func TestThrow005WindowSurvivesMissedSampleButNotRewind(t *testing.T) {
	d, records := shotRecorder()
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": mechanicsThrowState(5, 12, 1)}, 5)
	stale, other := idleShotState("p1", 5), idleShotState("p2", 6)
	stale.FrameCount = 10 // detect.IsStale only applies to a player that has been seen
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": stale, "p2": other}, 6)
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": mechanicsThrowState(7, 12, 2)}, 7)
	if got := (*records)[1].Metrics["observed_release_count"]; got != 2 {
		t.Fatalf("missed sample wiped the window: %v", got)
	}
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": mechanicsThrowState(3, 12, 2)}, 3)
	if got := (*records)[2].Metrics["observed_release_count"]; got != 1 {
		t.Fatalf("rewind kept stale support: %v", got)
	}
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p2": idleShotState("p2", 8)}, 8)
	if _, kept := d.histories["p1"]; kept {
		t.Fatal("history of a player no longer on the roster was retained")
	}
}
