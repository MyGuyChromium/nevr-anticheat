package pipeline

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// FrameValidator checks telemetry frames for validity.
type FrameValidator struct {
	minDt float64
	maxDt float64
}

// NewFrameValidator creates a validator from config.
func NewFrameValidator(cfg *config.Config) *FrameValidator {
	return &FrameValidator{
		minDt: cfg.Pipeline.MinFrameDt,
		maxDt: cfg.Pipeline.MaxFrameDt,
	}
}

// Validate checks a single telemetry frame.
func (v *FrameValidator) Validate(frame *model.PlayerTelemetryFrame, matchCtx *model.MatchContext) error {
	if frame.PlayerID == "" {
		return fmt.Errorf("missing player_id")
	}
	// Reject zero-vector positions
	if frame.Position.IsZero() {
		return fmt.Errorf("zero position for player %s", frame.PlayerID)
	}
	// Reject NaN/Inf
	if frame.Position.HasNaN() || frame.Position.HasInf() {
		return fmt.Errorf("NaN/Inf position for player %s", frame.PlayerID)
	}
	// Check quaternion unit-ness
	if !frame.Rotation.IsUnit() && frame.Rotation.Magnitude() > 0.1 {
		frame.Rotation = frame.Rotation.Normalize()
	}
	// Check DeltaTime
	if frame.DeltaTime > 0 && (frame.DeltaTime < v.minDt*0.5 || frame.DeltaTime > v.maxDt*2.5) {
		return fmt.Errorf("dt %.4f out of range for player %s", frame.DeltaTime, frame.PlayerID)
	}
	// Arena bounds check (generous tolerance)
	halfLen := matchCtx.Physics.ArenaLength/2 + 5
	halfW := matchCtx.Physics.ArenaWidth/2 + 5
	halfH := matchCtx.Physics.ArenaHeight/2 + 5
	if math.Abs(frame.Position[0]) > halfLen ||
		math.Abs(frame.Position[1]) > halfH ||
		math.Abs(frame.Position[2]) > halfW {
		return fmt.Errorf("position out of arena bounds for player %s", frame.PlayerID)
	}
	return nil
}
