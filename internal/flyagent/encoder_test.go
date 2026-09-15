package flyagent

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func testTick(hasDisc bool, goal AttackGoal) (*Encoder, *adapter.ParsedTick) {
	selfVelocity := model.Vec3{0, 0, 1}
	attachment := &model.DiscAttachment{State: "free"}
	possessorID := ""
	if hasDisc {
		attachment = &model.DiscAttachment{State: "held", HolderID: "self", HandCandidates: []string{"right"}}
		possessorID = "self"
	}
	disc := &model.DiscState{
		Attachment: attachment, Position: model.Vec3{2, 1, 8}, Velocity: model.Vec3{0, 0, -9},
		IsHeld: hasDisc, PossessionKnown: true, PossessorID: possessorID,
	}
	ctx := &model.MatchContext{
		MatchID: "match", IsPrivate: true, Physics: model.DefaultPhysics(),
		PlayerNames: map[string]string{"self": "Fly", "friend": "Friend", "enemy": "Enemy"},
	}
	observation := &model.ObservationContext{
		Source: "echoreplay", Authority: "client_reported", TimeBasis: "recorder_prefix",
		SessionID: "match", FrameIndex: 7, Timestamp: 1.2, Freshness: "sampled_snapshot",
	}
	frames := []model.PlayerTelemetryFrame{
		{Observation: observation, PlayerID: "self", Team: "blue", FrameIndex: 7, Timestamp: 1.2, DeltaTime: 1.0 / 15, Position: model.Vec3{}, Rotation: model.QuatIdentity(), ReportedVelocity: &selfVelocity, HasPossession: hasDisc, GamePhase: "playing", Disc: disc},
		{PlayerID: "friend", Team: "blue", FrameIndex: 7, Position: model.Vec3{1, 0, 4}, Rotation: model.QuatIdentity(), Disc: disc},
		{PlayerID: "enemy", Team: "orange", FrameIndex: 7, Position: model.Vec3{-1, 0, 3}, Rotation: model.QuatIdentity(), Disc: disc},
	}
	encoder, err := NewEncoder("Fly", goal)
	if err != nil {
		panic(err)
	}
	return encoder, &adapter.ParsedTick{
		MatchID: "match", MatchCtx: ctx, FrameIndex: 7, Frames: frames,
		Session: &adapter.EchoVRSessionResponse{GameStatus: "playing"},
	}
}

func TestEncoderTargetsDiscThenExplicitGoal(t *testing.T) {
	encoder, tick := testTick(false, GoalPositiveZ)
	observation, err := encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if observation.TargetKind != "disc" || observation.Currents["target_left"] <= 0 || observation.Currents["target_ahead"] <= 0 {
		t.Fatalf("disc target = %+v currents=%v", observation.Target, observation.Currents)
	}
	if observation.Opponent.PlayerID != "enemy" || observation.Teammate.PlayerID != "friend" {
		t.Fatalf("nearest entities: opponent=%+v teammate=%+v", observation.Opponent, observation.Teammate)
	}
	for name, value := range observation.Currents {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			t.Errorf("current %s = %v", name, value)
		}
	}

	encoder, tick = testTick(true, GoalPositiveZ)
	observation, err = encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if observation.TargetKind != "goal" || observation.Target.Relative[2] <= 0 || observation.Currents["target_ahead"] == 0 {
		t.Fatalf("positive goal target = %+v", observation.Target)
	}
	encoder, tick = testTick(true, GoalNegativeZ)
	observation, err = encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Target.Relative[2] >= 0 || observation.Currents["target_behind"] == 0 {
		t.Fatalf("negative goal target = %+v", observation.Target)
	}
}

func TestEncoderIsRotationRelative(t *testing.T) {
	encoder, tick := testTick(false, GoalPositiveZ)
	baseline, err := encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	// A 180-degree yaw plus a reflected world scene represents the same
	// egocentric target.
	tick.Frames[0].Rotation = model.Quat{0, 1, 0, 0}
	tick.Frames[0].Disc.Position[2] *= -1
	tick.Frames[0].Disc.Velocity[2] *= -1
	rotated, err := encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(baseline.Target.Relative[2]-rotated.Target.Relative[2]) > 1e-9 || math.Abs(baseline.Target.Relative[0]+rotated.Target.Relative[0]) > 1e-9 {
		// X was not reflected in the world fixture, so a 180 yaw reverses it.
		t.Fatalf("unexpected ego transform: baseline=%v rotated=%v", baseline.Target.Relative, rotated.Target.Relative)
	}
}

func TestEncoderNormalizesToleratedRotationAndRejectsInvalid(t *testing.T) {
	rootHalf := math.Sqrt(0.5)
	unit := model.Quat{0, rootHalf, 0, rootHalf}
	scaled := model.Quat{0, rootHalf * 1.005, 0, rootHalf * 1.005}
	var expected model.Vec3
	for i, rotation := range []model.Quat{unit, scaled, {-unit[0], -unit[1], -unit[2], -unit[3]}} {
		encoder, tick := testTick(false, GoalPositiveZ)
		tick.Frames[0].Rotation = rotation
		observation, err := encoder.Encode(tick)
		if err != nil {
			t.Fatal(err)
		}
		if !observation.OrientationKnown {
			t.Fatalf("rotation %v was not accepted", rotation)
		}
		if i == 0 {
			expected = observation.Target.Relative
		} else if observation.Target.Relative.Sub(expected).Magnitude() > 1e-9 {
			t.Fatalf("rotation %v produced %v, want %v", rotation, observation.Target.Relative, expected)
		}
	}

	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	network, err := NewNetwork(topology, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	invalid := []model.Quat{{}, {math.NaN(), 0, 0, 1}, {math.Inf(1), 0, 0, 1}, {0, 0.4, 0, 0.4}}
	for _, rotation := range invalid {
		encoder, tick := testTick(false, GoalPositiveZ)
		tick.Frames[0].Rotation = rotation
		observation, err := encoder.Encode(tick)
		if err != nil {
			t.Fatal(err)
		}
		if observation.OrientationKnown || observation.Currents["orientation_missing"] != 1 {
			t.Fatalf("invalid rotation %v was treated as known", rotation)
		}
		if observation.Target.Relative != (model.Vec3{}) {
			t.Fatalf("invalid rotation fell back to world axes: %v", observation.Target.Relative)
		}
		if intent := Decode(observation, network); intent.NeutralReason != "orientation_unavailable" {
			t.Fatalf("invalid rotation intent = %+v", intent)
		}
	}
}

func TestEncoderPhaseAndSourceFailClosed(t *testing.T) {
	encoder, tick := testTick(false, GoalPositiveZ)
	tick.Session.GameStatus = ""
	observation, err := encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if observation.PhaseKnown || observation.GameActive || ControlBlockReason(observation) != "phase_unavailable" {
		t.Fatalf("missing phase observation = %+v", observation)
	}

	encoder, tick = testTick(false, GoalPositiveZ)
	tick.Session.GameStatus = "pre_match"
	tick.Frames[0].GamePhase = "pre_match"
	observation, err = encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.PhaseKnown || observation.GameActive || ControlBlockReason(observation) != "inactive_game_phase" {
		t.Fatalf("pre-match observation = %+v", observation)
	}

	encoder, tick = testTick(false, GoalPositiveZ)
	tick.Frames[0].Observation.SessionID = "another-match"
	observation, err = encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if observation.SourceEpochKnown || observation.SourceKind != "" || observation.SourceAuthority != "" ||
		observation.SourceTimeBasis != "" || ControlBlockReason(observation) != "source_provenance_unavailable" {
		t.Fatalf("unbound source observation = %+v", observation)
	}
}

func TestEncoderPossessionAndTargetAreConfirmed(t *testing.T) {
	encoder, tick := testTick(false, GoalPositiveZ)
	tick.Frames[0].Disc.Attachment = &model.DiscAttachment{State: "unknown"}
	observation, err := encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if observation.PossessionKnown || observation.TargetKind != "none" || observation.Currents["grab_opportunity"] != 0 || ControlBlockReason(observation) != "disc_possession_unknown" {
		t.Fatalf("unknown possession observation = %+v", observation)
	}

	encoder, tick = testTick(false, GoalPositiveZ)
	tick.Frames[0].Disc.PossessionConflict = true
	observation, err = encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.PossessionConflict || observation.TargetKind != "none" || observation.Currents["grab_opportunity"] != 0 || observation.Currents["throw_opportunity"] != 0 || ControlBlockReason(observation) != "disc_possession_conflict" {
		t.Fatalf("conflicting possession observation = %+v", observation)
	}

	encoder, tick = testTick(false, GoalPositiveZ)
	tick.Frames[0].Disc.Position = model.Vec3{math.MaxFloat64, 0, math.MaxFloat64}
	observation, err = encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if observation.TargetKind != "none" || observation.Target.Available || ControlBlockReason(observation) != "target_unavailable" {
		t.Fatalf("missing disc target observation = %+v", observation)
	}
}

func TestUnknownTeamsAreNotOpponents(t *testing.T) {
	encoder, tick := testTick(false, GoalPositiveZ)
	tick.Frames[0].Team = ""
	observation, err := encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if observation.TeamKnown || observation.Opponent.Available || observation.Teammate.Available || ControlBlockReason(observation) != "team_unavailable" {
		t.Fatalf("unknown team observation = %+v", observation)
	}
}

func TestEncoderPlayerSelectionAndReset(t *testing.T) {
	encoder, tick := testTick(false, GoalPositiveZ)
	first, err := encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	second, err := encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 0 || second.Sequence != 1 {
		t.Fatalf("sequences = %d, %d", first.Sequence, second.Sequence)
	}
	encoder.Reset()
	reset, err := encoder.Encode(tick)
	if err != nil || reset.Sequence != 0 {
		t.Fatalf("reset sequence=%d err=%v", reset.Sequence, err)
	}

	missing, _ := NewEncoder("nobody", GoalPositiveZ)
	if _, err := missing.Encode(tick); !errors.Is(err, ErrPlayerAbsent) {
		t.Fatalf("missing error = %v", err)
	}
	if _, err := NewEncoder("Fly", AttackGoal("guess")); err == nil {
		t.Fatal("invalid goal accepted")
	}
}

func TestEncoderBindsIdentityAndTeam(t *testing.T) {
	encoder, tick := testTick(false, GoalPositiveZ)
	if _, err := encoder.Encode(tick); err != nil {
		t.Fatal(err)
	}
	encoder.Reset()
	tick.Frames[0].PlayerID = "replacement"
	tick.MatchCtx.PlayerNames = map[string]string{"replacement": "Fly", "friend": "Friend", "enemy": "Enemy"}
	if _, err := encoder.Encode(tick); !errors.Is(err, ErrPlayerAbsent) {
		t.Fatalf("display-name selector switched identity: %v", err)
	}

	encoder, tick = testTick(false, GoalPositiveZ)
	if _, err := encoder.Encode(tick); err != nil {
		t.Fatal(err)
	}
	encoder.Reset()
	tick.Frames[0].Team = "orange"
	if _, err := encoder.Encode(tick); err == nil || !strings.Contains(err.Error(), "changed team") {
		t.Fatalf("team change error = %v", err)
	}
}

func TestSelectFramePrefersUniqueIDAndRejectsDuplicateID(t *testing.T) {
	_, tick := testTick(false, GoalPositiveZ)
	tick.MatchCtx.PlayerNames["enemy"] = "self"
	frame, err := selectFrame(tick, "self")
	if err != nil || frame.PlayerID != "self" {
		t.Fatalf("exact ID selection frame=%+v err=%v", frame, err)
	}
	tick.Frames = append(tick.Frames, tick.Frames[0])
	if _, err := selectFrame(tick, "self"); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate ID error = %v", err)
	}
}

func TestEncoderDoesNotFabricateStationaryVelocity(t *testing.T) {
	encoder, tick := testTick(false, GoalPositiveZ)
	tick.Frames[0].ReportedVelocity = nil
	observation, err := encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	if observation.SelfVelocityKnown || observation.Currents["self_velocity_missing"] != 1 ||
		observation.Currents["disc_approaching"] != 0 || ControlBlockReason(observation) != "self_velocity_unavailable" {
		t.Fatalf("missing velocity observation = %+v", observation)
	}
}

func TestDecodeNeutralAndBounded(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	network, err := NewNetwork(topology, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	observation := &Observation{
		Schema: ObservationSchema, SourceEpochKnown: true, PhaseKnown: true, GameActive: true,
		OrientationKnown: true, SelfVelocityKnown: true, TeamKnown: true, PossessionKnown: true,
		TargetKind: "disc", Target: RelativeEntity{Available: true, Distance: 1},
		Currents: map[string]float64{"target_left": 1, "target_ahead": 1, "boost_opportunity": 1},
	}
	for i := 0; i < 5; i++ {
		if err := network.Step(1.0/15.0, observation.Currents); err != nil {
			t.Fatal(err)
		}
	}
	intent := Decode(observation, network)
	if intent.Yaw <= 0 || intent.Translation[0] <= 0 || intent.Translation[2] <= 0 || intent.Confidence <= 0 || intent.Confidence > 1 {
		t.Fatalf("intent = %+v", intent)
	}
	observation.Stunned = true
	if neutral := Decode(observation, network); neutral.NeutralReason != "player_stunned" || neutral.Confidence != 0 {
		t.Fatalf("stunned intent = %+v", neutral)
	}
}

func TestExecutionPolicyIsReplayOnly(t *testing.T) {
	if err := AuthorizeExecution(ModeReplay, false); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode      ExecutionMode
		isPrivate bool
	}{
		{ModePrivateLive, true},
		{ModePrivateLive, false},
		{ModePublicLive, false},
	} {
		if err := AuthorizeExecution(tc.mode, tc.isPrivate); err == nil {
			t.Errorf("mode %q private=%t was authorized", tc.mode, tc.isPrivate)
		}
	}
}
