package pipeline

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Validation reason codes. They are stable identifiers used for logging,
// MatchResult.InvalidFrameReasons and metrics, so telemetry drift (a schema
// change that zeroes positions, a physics mismatch that rejects every frame)
// is attributable instead of being folded into one opaque counter.
const (
	ReasonMissingPlayerID = "missing_player_id"
	ReasonZeroPosition    = "zero_position"
	ReasonInvalidPosition = "invalid_position"
	ReasonDtOutOfRange    = "dt_out_of_range"
	ReasonOutOfBounds     = "out_of_arena_bounds"
)

// Sanitization reason codes, reported alongside a valid frame when a field
// was repaired in place rather than causing rejection.
const (
	SanitizedHandPosition = "nan_hand_position"
	SanitizedRotation     = "invalid_rotation"
	SanitizedDisc         = "invalid_disc"
)

// ValidationError describes why a frame was rejected.
type ValidationError struct {
	Reason   string
	PlayerID string
	Detail   string
}

func (e *ValidationError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s for player %s: %s", e.Reason, e.PlayerID, e.Detail)
	}
	return fmt.Sprintf("%s for player %s", e.Reason, e.PlayerID)
}

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

// Validate checks a single telemetry frame. It rejects frames whose body
// position or delta time is unusable (returning a *ValidationError) and
// sanitizes the rest in place: non-unit rotations are normalized, NaN/Inf
// hand positions and rotations are replaced with the zero-vector tracking-loss
// sentinel (which the feature extractor already treats as "no data"), and a
// disc with NaN/Inf fields is dropped from the frame so no downstream distance
// or angle becomes NaN. The returned slice lists the sanitizations applied.
func (v *FrameValidator) Validate(frame *model.PlayerTelemetryFrame, matchCtx *model.MatchContext) ([]string, error) {
	if frame.PlayerID == "" {
		return nil, &ValidationError{Reason: ReasonMissingPlayerID}
	}
	// Reject zero-vector positions
	if frame.Position.IsZero() {
		return nil, &ValidationError{Reason: ReasonZeroPosition, PlayerID: frame.PlayerID}
	}
	// Reject NaN/Inf
	if frame.Position.HasNaN() || frame.Position.HasInf() {
		return nil, &ValidationError{Reason: ReasonInvalidPosition, PlayerID: frame.PlayerID}
	}
	if math.IsNaN(frame.Timestamp) || math.IsInf(frame.Timestamp, 0) ||
		math.IsNaN(frame.DeltaTime) || math.IsInf(frame.DeltaTime, 0) {
		return nil, &ValidationError{Reason: ReasonDtOutOfRange, PlayerID: frame.PlayerID, Detail: "non-finite time"}
	}
	// Check DeltaTime. dt <= 0 means "unknown" (first frame, restarted clock).
	if frame.DeltaTime > 0 && (frame.DeltaTime < v.minDt*0.5 || frame.DeltaTime > v.maxDt*2.5) {
		return nil, &ValidationError{Reason: ReasonDtOutOfRange, PlayerID: frame.PlayerID,
			Detail: fmt.Sprintf("dt %.4f", frame.DeltaTime)}
	}
	// Arena bounds check (generous tolerance) derived from the match physics.
	// CONFIRMED from real replay: X is narrow (±5m), Y is vertical (±7m), Z is long (±77m).
	// ArenaLength maps to Z, ArenaWidth maps to X, ArenaHeight maps to Y.
	halfX, halfY, halfZ := arenaHalfExtents(matchCtx.Physics)
	if math.Abs(frame.Position[0]) > halfX ||
		math.Abs(frame.Position[1]) > halfY ||
		math.Abs(frame.Position[2]) > halfZ {
		return nil, &ValidationError{Reason: ReasonOutOfBounds, PlayerID: frame.PlayerID,
			Detail: fmt.Sprintf("position %v", frame.Position)}
	}

	var sanitized []string
	// Quaternions: normalize near-unit, zero NaN/Inf.
	var rotFixed, lfix, rfix bool
	frame.Rotation, rotFixed = sanitizeQuat(frame.Rotation)
	frame.LeftHandRotation, lfix = sanitizeQuat(frame.LeftHandRotation)
	frame.RightHandRotation, rfix = sanitizeQuat(frame.RightHandRotation)
	if rotFixed || lfix || rfix {
		sanitized = append(sanitized, SanitizedRotation)
	}
	// Hand positions: tracking loss is encoded as the zero vector.
	handFixed := false
	if frame.LeftHandPosition.HasNaN() || frame.LeftHandPosition.HasInf() {
		frame.LeftHandPosition = model.Vec3{}
		handFixed = true
	}
	if frame.RightHandPosition.HasNaN() || frame.RightHandPosition.HasInf() {
		frame.RightHandPosition = model.Vec3{}
		handFixed = true
	}
	if handFixed {
		sanitized = append(sanitized, SanitizedHandPosition)
	}
	// Disc state: drop it rather than the whole frame when it is not finite.
	if frame.Disc != nil && !discIsFinite(frame.Disc) {
		frame.Disc = nil
		sanitized = append(sanitized, SanitizedDisc)
	}
	return sanitized, nil
}

// arenaHalfExtents returns the accepted |X|,|Y|,|Z| bounds (half extent plus a
// 5 m tolerance). A zero-valued physics block (a MatchContext source that
// omitted it) falls back to DefaultPhysics instead of rejecting everything.
func arenaHalfExtents(ph model.PhysicsConstants) (halfX, halfY, halfZ float64) {
	def := model.DefaultPhysics()
	if ph.ArenaWidth <= 0 {
		ph.ArenaWidth = def.ArenaWidth
	}
	if ph.ArenaHeight <= 0 {
		ph.ArenaHeight = def.ArenaHeight
	}
	if ph.ArenaLength <= 0 {
		ph.ArenaLength = def.ArenaLength
	}
	const tolerance = 5.0
	return ph.ArenaWidth/2 + tolerance, ph.ArenaHeight/2 + tolerance, ph.ArenaLength/2 + tolerance
}

// sanitizeQuat normalizes near-unit quaternions and zeroes NaN/Inf ones (a
// zero quaternion is the "no rotation data" sentinel that IsUnit() rejects, so
// consumers skip it). The bool reports whether a NaN/Inf quaternion was zeroed.
func sanitizeQuat(q model.Quat) (model.Quat, bool) {
	for _, c := range q {
		if math.IsNaN(c) || math.IsInf(c, 0) {
			return model.Quat{}, true
		}
	}
	if q.IsUnit() {
		return q, false
	}
	if q.Magnitude() > 0.1 {
		return q.Normalize(), false
	}
	return q, false
}

// discIsFinite reports whether every numeric disc field is finite.
func discIsFinite(d *model.DiscState) bool {
	if d.Position.HasNaN() || d.Position.HasInf() || d.Velocity.HasNaN() || d.Velocity.HasInf() {
		return false
	}
	if math.IsNaN(d.Speed) || math.IsInf(d.Speed, 0) {
		return false
	}
	return true
}
