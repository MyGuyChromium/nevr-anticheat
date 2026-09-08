package pipeline

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func qualityFrame(i int) model.PlayerTelemetryFrame {
	leftObserved, rightObserved := true, true
	velocity := model.Vec3{0, 0, 1}
	return model.PlayerTelemetryFrame{PlayerID: "P1", FrameIndex: i, Timestamp: float64(i) / 15,
		DeltaTime: 1.0 / 15, Position: model.Vec3{0, 1.7, float64(i) / 15},
		Rotation: model.QuatIdentity(), LeftHandPosition: model.Vec3{-0.3, 1.4, float64(i) / 15},
		RightHandPosition: model.Vec3{0.3, 1.4, float64(i) / 15}, LeftHandRotation: model.QuatIdentity(),
		RightHandRotation: model.QuatIdentity(), ReportedVelocity: &velocity,
		LeftHandRotationValid: &leftObserved, RightHandRotationValid: &rightObserved,
		IsBoostingKnown: &leftObserved,
		Disc:            &model.DiscState{Position: model.Vec3{0, 1.5, 0}}, GamePhase: "playing"}
}

func TestAssessTelemetryQualityHealthyAndCorrupt(t *testing.T) {
	cfg := config.DefaultConfig()
	frames := make([]model.PlayerTelemetryFrame, 30)
	for i := range frames {
		frames[i] = qualityFrame(i)
	}
	healthy := AssessTelemetryQuality(frames, cfg)
	if healthy.Gated || healthy.Score < 99 || healthy.Grade != "excellent" {
		t.Fatalf("healthy telemetry = %+v", healthy)
	}
	frames[10].Timestamp = math.NaN()
	for i := 11; i < len(frames); i++ {
		frames[i].Timestamp = frames[i-1].Timestamp - .1
	}
	corrupt := AssessTelemetryQuality(frames, cfg)
	if !corrupt.Gated || corrupt.TimestampProblems == 0 || corrupt.ConfidenceMultiplier >= 1 {
		t.Fatalf("corrupt telemetry not gated = %+v", corrupt)
	}
}

func TestLegalMotionContextDistinguishesGameMotionAndStep(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := &model.MatchContext{MatchID: "M", PlayerIDs: []string{"P1"}, TeamAssignments: map[string]string{"P1": "blue"}, Physics: model.DefaultPhysics()}
	state := &model.PlayerState{PlayerID: "P1"}
	for i := 0; i < 8; i++ {
		frame := qualityFrame(i)
		fe.UpdatePlayerState(state, &frame, mc)
	}
	if !state.LegalContext.GameLocomotion || state.LegalContext.PlayspaceStep {
		t.Fatalf("game motion context = %+v", state.LegalContext)
	}
	zero := model.Vec3{}
	for i := 8; i < 14; i++ {
		frame := qualityFrame(i)
		frame.ReportedVelocity = &zero
		// The tracked rig advances together faster than the game velocity.
		frame.Position[2] += float64(i-7) * .04
		frame.LeftHandPosition[2] += float64(i-7) * .04
		frame.RightHandPosition[2] += float64(i-7) * .04
		fe.UpdatePlayerState(state, &frame, mc)
	}
	if !state.LegalContext.PlayspaceStep || state.LegalContext.Leaning {
		t.Fatalf("playspace step context = %+v", state.LegalContext)
	}
}
