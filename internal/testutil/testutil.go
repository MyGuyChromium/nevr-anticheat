// Package testutil provides test helpers for the anticheat system.
package testutil

import (
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// NewMatchContext creates a test MatchContext.
func NewMatchContext() *model.MatchContext {
	return &model.MatchContext{
		MatchID:   "test-match-001",
		Map:       "mpl_arena_a",
		GameMode:  "Echo_Arena",
		IsRanked:  true,
		StartTime: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		Duration:  10 * time.Minute,
		PlayerIDs: []string{"player1", "player2"},
		TeamAssignments: map[string]string{
			"player1": "blue",
			"player2": "orange",
		},
		TickRate: 15.0,
		Source:   "test",
		Physics:  model.DefaultPhysics(),
	}
}

// NewPlayerState creates a test PlayerState.
func NewPlayerState(playerID string) *model.PlayerState {
	return &model.PlayerState{
		PlayerID:     playerID,
		Team:         "blue",
		Position:     model.Vec3{5, 0, 0},
		Rotation:     model.QuatIdentity(),
		LeftHand:     model.Vec3{4.7, 0.3, 0.2},
		RightHand:    model.Vec3{5.3, 0.3, -0.2},
		LeftHandRot:  model.QuatIdentity(),
		RightHandRot: model.QuatIdentity(),
		FrameDt:      0.067,
	}
}

// MakeThrowEvent creates a ThrowEvent with given parameters.
func MakeThrowEvent(throwerID string, frameIdx int, releaseSpeed, releaseAngle float64) model.ThrowEvent {
	return model.ThrowEvent{
		ThrowerID:          throwerID,
		Attribution:        model.ThrowAttribution{PlayerID: throwerID, Confidence: 0.95, Method: "possession_track"},
		FrameIndex:         frameIdx,
		Timestamp:          float64(frameIdx) * 0.067,
		ReleasePosition:    model.Vec3{5, 0, 0},
		ReleaseVelocity:    model.Vec3{releaseSpeed, 0, 0},
		ReleaseSpeed:       releaseSpeed,
		ThrowingHand:       "right",
		HandPosition:       model.Vec3{5.3, 0.3, 0},
		HandVelocity:       model.Vec3{releaseSpeed * 0.5, 0, 0},
		HandSpeed:          releaseSpeed * 0.5,
		WristOrientation:   model.QuatIdentity(),
		PlayerPosition:     model.Vec3{5, 0, 0},
		PlayerVelocity:     model.Vec3{0, 0, 0},
		HandToDiscDistance: 0.3,
		ReleaseAngle:       releaseAngle,
		PossessionDuration: 1.0,
	}
}

// MakeDetectionEvent creates a test DetectionEvent.
func MakeDetectionEvent(detectorID, playerID string, severity, confidence float64) model.DetectionEvent {
	return model.DetectionEvent{
		EventID:           model.NewEventID(),
		DetectorID:        detectorID,
		DetectorVersion:   "1.0.0",
		MatchID:           "test-match-001",
		PlayerID:          playerID,
		FrameIndex:        100,
		FrameRangeStart:   98,
		FrameRangeEnd:     102,
		Timestamp:         6.7,
		Severity:          severity,
		Confidence:        confidence,
		ObservedValue:     "test_value: 25.0",
		ExpectedRange:     "test_value: < 20.0",
		CausalKey:         model.CausalKey{PlayerID: playerID, FrameStart: 98, FrameEnd: 102, AnomalyType: "test"},
		EnforcementWeight: 0.8,
	}
}

// GenerateCleanFrames creates N frames of normal gameplay.
func GenerateCleanFrames(playerID string, count int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, count)
	for i := 0; i < count; i++ {
		ts := float64(i) * 0.067
		frames[i] = model.PlayerTelemetryFrame{
			PlayerID:          playerID,
			FrameIndex:        i,
			Timestamp:         ts,
			DeltaTime:         0.067,
			Position:          model.Vec3{float64(i) * 0.1, 0, 0},
			Rotation:          model.QuatIdentity(),
			LeftHandPosition:  model.Vec3{float64(i)*0.1 - 0.3, 0.3, 0.2},
			RightHandPosition: model.Vec3{float64(i)*0.1 + 0.3, 0.3, -0.2},
			LeftHandRotation:  model.QuatIdentity(),
			RightHandRotation: model.QuatIdentity(),
			GamePhase:         "playing",
			Disc: &model.DiscState{
				Position: model.Vec3{0, 0, 0},
				Velocity: model.Vec3{0, 0, 0},
			},
		}
	}
	return frames
}
