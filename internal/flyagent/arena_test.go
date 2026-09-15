package flyagent

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func arenaAction(translation [3]float64, yaw float64) ActionIntent {
	return ActionIntent{Schema: ActionSchema, Translation: translation, Yaw: yaw, Confidence: 0.5}
}

func TestArenaSeedDeterminismActionCausalityAndCheckpoint(t *testing.T) {
	config := DefaultArenaConfig()
	config.MaxSteps = 40
	first, err := NewArena(config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewArena(config)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := first.Reset(42)
	if err != nil {
		t.Fatal(err)
	}
	same, err := second.Reset(42)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(initial, same) || !reflect.DeepEqual(first.Checkpoint(), second.Checkpoint()) {
		t.Fatal("same seed did not produce identical initial state")
	}
	if reason := ControlBlockReason(initial); reason != "" {
		t.Fatalf("synthetic observation was blocked: %s", reason)
	}
	if initial.SourceKind != ArenaSourceKind || initial.SourceAuthority != "simulated" || initial.SourceTimeBasis != "fixed_step" || !initial.SourceEpochKnown {
		t.Fatalf("synthetic provenance = %+v", initial)
	}
	other, err := second.Reset(43)
	if err != nil {
		t.Fatal(err)
	}
	initialDigest, err := DigestObservation(initial)
	if err != nil {
		t.Fatal(err)
	}
	otherDigest, err := DigestObservation(other)
	if err != nil {
		t.Fatal(err)
	}
	if initialDigest == otherDigest {
		t.Fatal("different seeds produced the same observation digest")
	}

	actions := []ActionIntent{
		arenaAction([3]float64{0, 0, 1}, 0),
		arenaAction([3]float64{0.2, 0.1, 0.8}, 0.25),
		arenaAction([3]float64{-0.1, 0, 0.7}, -0.1),
	}
	for _, action := range actions {
		if _, _, err := first.Step(action); err != nil {
			t.Fatal(err)
		}
	}
	if first.Checkpoint().PlayerPosition == initial.SelfPosition {
		t.Fatal("translation action did not affect the next player observation")
	}
	checkpoint := first.Checkpoint()
	blob, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip ArenaCheckpoint
	if err := json.Unmarshal(blob, &roundTrip); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		Observation *Observation
		Transition  ArenaTransition
	}
	suffix := []ActionIntent{
		arenaAction([3]float64{0.4, -0.2, 0.3}, 0.5),
		arenaAction([3]float64{0, 0.5, 0.4}, -0.4),
		arenaAction([3]float64{-0.3, 0, 0.6}, 0),
	}
	runSuffix := func() []outcome {
		var outcomes []outcome
		for _, action := range suffix {
			observation, transition, err := first.Step(action)
			if err != nil {
				t.Fatal(err)
			}
			outcomes = append(outcomes, outcome{observation, transition})
		}
		return outcomes
	}
	want := runSuffix()
	if err := first.Restore(roundTrip); err != nil {
		t.Fatal(err)
	}
	if got := runSuffix(); !reflect.DeepEqual(got, want) {
		t.Fatalf("arena diverged after checkpoint restore\n got: %#v\nwant: %#v", got, want)
	}
}

func TestArenaPossessionReleaseGoalAndTerminalState(t *testing.T) {
	config := DefaultArenaConfig()
	config.MaxSteps = 20
	arena, err := NewArena(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := arena.Reset(9); err != nil {
		t.Fatal(err)
	}
	checkpoint := arena.Checkpoint()
	checkpoint.PlayerPosition = model.Vec3{0, 0, config.HalfExtents[2] - 1.3}
	checkpoint.PlayerVelocity = model.Vec3{}
	checkpoint.PlayerYaw = 0
	checkpoint.DiscPosition = model.Vec3{0, 0, config.HalfExtents[2] - 0.8}
	checkpoint.DiscVelocity = model.Vec3{}
	if err := arena.Restore(checkpoint); err != nil {
		t.Fatal(err)
	}

	grip := ActionIntent{Schema: ActionSchema, LeftGrip: true, Confidence: 1}
	observation, transition, err := arena.Step(grip)
	if err != nil {
		t.Fatal(err)
	}
	if !transition.AcquiredDisc || !observation.HasDisc || observation.TargetKind != "goal" {
		t.Fatalf("disc was not acquired: observation=%+v transition=%+v", observation, transition)
	}
	release := ActionIntent{Schema: ActionSchema, Release: true, Confidence: 1}
	observation, transition, err = arena.Step(release)
	if err != nil {
		t.Fatal(err)
	}
	if !transition.ReleasedDisc || !transition.ScoredFor || !transition.Done || transition.TerminationReason != "attack_goal" || observation.GameActive {
		t.Fatalf("release did not close the goal episode: observation=%+v transition=%+v", observation, transition)
	}
	if transition.SyntheticObjective < config.GoalBonus {
		t.Fatalf("goal objective=%v, want at least goal bonus %v", transition.SyntheticObjective, config.GoalBonus)
	}
	terminal := arena.Checkpoint()
	restored, err := NewArena(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(terminal); err != nil {
		t.Fatalf("restore terminal checkpoint: %v", err)
	}
	if !reflect.DeepEqual(restored.Checkpoint(), terminal) {
		t.Fatal("terminal checkpoint changed during restore")
	}
	if _, _, err := arena.Step(ActionIntent{Schema: ActionSchema}); err == nil {
		t.Fatal("terminal arena accepted another action")
	}
}

func TestArenaRejectsInvalidInputsWithoutAdvancing(t *testing.T) {
	config := DefaultArenaConfig()
	invalid := config
	invalid.StepSeconds = math.NaN()
	if _, err := NewArena(invalid); err == nil {
		t.Fatal("non-finite config was accepted")
	}
	arena, err := NewArena(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := arena.Step(ActionIntent{Schema: ActionSchema}); err == nil {
		t.Fatal("uninitialized arena accepted an action")
	}
	if _, err := arena.Reset(1); err != nil {
		t.Fatal(err)
	}
	before := arena.Checkpoint()
	bad := arenaAction([3]float64{0, 0, 1}, 0)
	bad.Translation[1] = math.Inf(1)
	if _, _, err := arena.Step(bad); err == nil {
		t.Fatal("non-finite action was accepted")
	}
	if after := arena.Checkpoint(); !reflect.DeepEqual(after, before) {
		t.Fatal("invalid action advanced arena state")
	}
	wrongConfig := config
	wrongConfig.MaxSteps++
	other, err := NewArena(wrongConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Restore(before); err == nil {
		t.Fatal("checkpoint restored into a different configuration")
	}
	badCheckpoint := before
	badCheckpoint.PlayerPosition[0] = config.HalfExtents[0] + 1
	if err := arena.Restore(badCheckpoint); err == nil {
		t.Fatal("out-of-bounds checkpoint was accepted")
	}
	if after := arena.Checkpoint(); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected checkpoint partially changed arena state")
	}
}

func TestArenaHasNoGravityAndReflectsAtBoundaries(t *testing.T) {
	config := DefaultArenaConfig()
	config.MaxSteps = 5
	config.PlayerDrag = 0
	config.DiscDrag = 0
	config.WallRestitution = 0.5
	arena, err := NewArena(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := arena.Reset(5); err != nil {
		t.Fatal(err)
	}
	checkpoint := arena.Checkpoint()
	checkpoint.PlayerPosition = model.Vec3{config.HalfExtents[0] - 0.01, 0, 0}
	checkpoint.PlayerVelocity = model.Vec3{5, 1, 0}
	checkpoint.DiscPosition = model.Vec3{-config.HalfExtents[0] + 0.01, 0, 0}
	checkpoint.DiscVelocity = model.Vec3{-5, 1, 0}
	checkpoint.PlayerYaw = 0
	if err := arena.Restore(checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, _, err := arena.Step(ActionIntent{Schema: ActionSchema}); err != nil {
		t.Fatal(err)
	}
	after := arena.Checkpoint()
	if !insideBox(after.PlayerPosition, config.HalfExtents) || !insideBox(after.DiscPosition, config.HalfExtents) || after.PlayerVelocity[0] >= 0 || after.DiscVelocity[0] <= 0 {
		t.Fatalf("wall reflection failed: %+v", after)
	}
	if after.PlayerVelocity[1] != 1 || after.DiscVelocity[1] != 1 {
		t.Fatalf("vertical velocity changed without force (gravity leaked in): player=%v disc=%v", after.PlayerVelocity[1], after.DiscVelocity[1])
	}
}
