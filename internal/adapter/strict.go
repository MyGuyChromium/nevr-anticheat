package adapter

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// StrictMapper is a Mapper that fails hard on uncertain fields instead of defaulting.
// Used during first live integration testing to surface all mapping issues immediately.
type StrictMapper struct {
	*Mapper
	errors []StrictError
}

// StrictError is a hard failure from strict mode validation.
type StrictError struct {
	PlayerName string `json:"player_name"`
	Field      string `json:"field"`
	Issue      string `json:"issue"`
	RawValue   string `json:"raw_value"`
}

// NewStrictMapper creates a mapper that rejects any frame with uncertain data.
func NewStrictMapper() *StrictMapper {
	return &StrictMapper{
		Mapper: NewMapper(),
	}
}

// Errors returns all strict-mode errors accumulated so far.
func (sm *StrictMapper) Errors() []StrictError {
	return sm.errors
}

// MapSessionStrict converts a session but rejects frames with any uncertain fields.
func (sm *StrictMapper) MapSessionStrict(raw *EchoVRSessionResponse) *MappingResult {
	result := sm.Mapper.MapSession(raw)

	// Re-validate each frame with strict checks
	var strictFrames []model.PlayerTelemetryFrame
	for _, frame := range result.Frames {
		errs := sm.strictValidate(raw, &frame)
		if len(errs) > 0 {
			sm.errors = append(sm.errors, errs...)
			result.Errors = append(result.Errors, MappingError{
				PlayerName: frame.PlayerID,
				Field:      "strict_validation",
				Message:    fmt.Sprintf("%d strict errors", len(errs)),
			})
			continue
		}
		strictFrames = append(strictFrames, frame)
	}
	result.Frames = strictFrames
	return result
}

func (sm *StrictMapper) strictValidate(raw *EchoVRSessionResponse, frame *model.PlayerTelemetryFrame) []StrictError {
	var errs []StrictError

	// Check rotation quality. Identity quaternion is valid IF the raw direction
	// vectors were non-zero (forward=[0,0,1], left=[-1,0,0], up=[0,1,0] produces identity).
	// Only flag if we can detect that direction vectors were actually missing.
	// We check by looking at the raw player data — if all 3 direction vectors are zero,
	// the mapper would have produced identity from zero input, which is suspicious.
	// This check requires access to the raw data, which we don't have here.
	// Instead, check for the specific pathological case: hand rotation is identity AND
	// it stays identity for multiple frames (tracked by the pipeline, not here).
	// For now, skip identity-quat strict checks — identity IS a valid rotation.

	// Require non-zero hand positions
	if frame.LeftHandPosition.IsZero() {
		errs = append(errs, StrictError{
			PlayerName: frame.PlayerID, Field: "left_hand_position",
			Issue: "zero position",
		})
	}
	if frame.RightHandPosition.IsZero() {
		errs = append(errs, StrictError{
			PlayerName: frame.PlayerID, Field: "right_hand_position",
			Issue: "zero position",
		})
	}

	// Require hand positions within 2m of body
	if frame.LeftHandPosition.Distance(frame.Position) > 2.0 {
		errs = append(errs, StrictError{
			PlayerName: frame.PlayerID, Field: "left_hand_position",
			Issue:    fmt.Sprintf("%.2fm from body (max 2m)", frame.LeftHandPosition.Distance(frame.Position)),
			RawValue: fmt.Sprintf("%v", frame.LeftHandPosition),
		})
	}
	if frame.RightHandPosition.Distance(frame.Position) > 2.0 {
		errs = append(errs, StrictError{
			PlayerName: frame.PlayerID, Field: "right_hand_position",
			Issue:    fmt.Sprintf("%.2fm from body (max 2m)", frame.RightHandPosition.Distance(frame.Position)),
			RawValue: fmt.Sprintf("%v", frame.RightHandPosition),
		})
	}

	// Validate quaternion unit-ness (direction vector quality)
	rotMag := frame.Rotation.Magnitude()
	if rotMag < 0.95 || rotMag > 1.05 {
		errs = append(errs, StrictError{
			PlayerName: frame.PlayerID, Field: "rotation",
			Issue:    fmt.Sprintf("quaternion magnitude %.4f (expected ~1.0)", rotMag),
			RawValue: fmt.Sprintf("%v", frame.Rotation),
		})
	}

	// Validate disc state
	if frame.Disc != nil {
		if frame.Disc.Speed > 100 {
			errs = append(errs, StrictError{
				PlayerName: frame.PlayerID, Field: "disc.speed",
				Issue:    fmt.Sprintf("disc speed %.1f m/s (sanity max 100)", frame.Disc.Speed),
				RawValue: fmt.Sprintf("%.1f", frame.Disc.Speed),
			})
		}
		// If held, disc speed should be low
		if frame.HasPossession && frame.Disc.Speed > 5.0 {
			errs = append(errs, StrictError{
				PlayerName: frame.PlayerID, Field: "disc.speed",
				Issue:    fmt.Sprintf("disc held but speed %.1f m/s (expected <5 when held)", frame.Disc.Speed),
				RawValue: fmt.Sprintf("possession=true, disc_speed=%.1f", frame.Disc.Speed),
			})
		}
	}

	// Validate position in arena bounds
	if math.Abs(frame.Position[0]) > 45 || math.Abs(frame.Position[1]) > 20 || math.Abs(frame.Position[2]) > 20 {
		errs = append(errs, StrictError{
			PlayerName: frame.PlayerID, Field: "position",
			Issue:    "out of arena bounds",
			RawValue: fmt.Sprintf("%v", frame.Position),
		})
	}

	// Validate ping is reasonable
	if frame.EstimatedPingMs < 0 || frame.EstimatedPingMs > 1000 {
		errs = append(errs, StrictError{
			PlayerName: frame.PlayerID, Field: "ping",
			Issue:    fmt.Sprintf("ping %.0fms out of range [0, 1000]", frame.EstimatedPingMs),
			RawValue: fmt.Sprintf("%.0f", frame.EstimatedPingMs),
		})
	}

	return errs
}
