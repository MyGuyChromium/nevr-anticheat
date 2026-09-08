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
	// ReasonInvalidTimestamp rejects a NaN, +/-Inf or negative Timestamp. It
	// is the same code and the same rule the ingest guard applies
	// (validateIngestFrame), so the live and offline paths agree.
	ReasonInvalidTimestamp = "invalid_timestamp"
	ReasonDtOutOfRange     = "dt_out_of_range"
	ReasonOutOfBounds      = "out_of_arena_bounds"
)

// Sanitization reason codes, reported alongside a valid frame when a field
// was repaired in place (or, for SanitizedZeroRotation, carried the "no
// data" sentinel) rather than causing rejection.
const (
	// SanitizedHandPosition: a NaN/Inf hand position was replaced by the
	// zero-vector tracking-loss sentinel.
	SanitizedHandPosition = "nan_hand_position"
	// SanitizedFarHand: a hand further than MaxHandBodyDistance from the
	// body was replaced by the zero-vector tracking-loss sentinel. A hand
	// that far away is a tracking glitch, not a reach; left in place the
	// extractor would derive a hand speed of thousands of m/s from it.
	SanitizedFarHand = "far_hand_position"
	// SanitizedRotation: a NaN/Inf or near-zero (|q| <= 0.1, unusable)
	// quaternion was replaced by the zero-quaternion sentinel.
	SanitizedRotation = "invalid_rotation"
	// SanitizedZeroRotation: a rotation field carried the zero-quaternion
	// "no rotation data" sentinel (the contract value for tracking loss).
	// Nothing is repaired; the code makes the volume of missing rotation
	// data visible per match.
	SanitizedZeroRotation = "zero_rotation"
	// SanitizedNegativeDt: a negative DeltaTime was set to 0 (unknown
	// spacing), the same meaning the extractor already gives dt <= 0.
	SanitizedNegativeDt = "negative_delta_time"
	// SanitizedDisc: a disc with a NaN/Inf field was dropped from the frame.
	SanitizedDisc = "invalid_disc"
	// SanitizedDiscOutOfRange: a disc outside the arena (plus tolerance) or
	// moving faster than DiscSpeedSanityFactor x DiscSpeedCap was dropped
	// from the frame.
	SanitizedDiscOutOfRange = "disc_out_of_range"
	// SanitizedPing: a non-finite/negative ping was replaced with 0 (unknown),
	// or a value above MaxEstimatedPingMs was clamped. This prevents malformed
	// latency from creating NaN evidence or unbounded detector tolerances.
	SanitizedPing = "invalid_ping"
	// SanitizedGameLastThrow: an incomplete or non-finite engine throw record
	// was removed so it cannot override a valid sampled disc speed.
	SanitizedGameLastThrow = "invalid_game_last_throw"
	// SanitizedReportedVelocity: a non-finite game-authored velocity was
	// removed. Nil means unavailable, so playspace reconstruction safely
	// disables itself for that frame.
	SanitizedReportedVelocity = "invalid_reported_velocity"
	// Optional observations become unknown independently of the legacy body,
	// hand and disc motion fields when their metadata is unusable.
	SanitizedHeadPosition    = "invalid_head_position"
	SanitizedDiscObservation = "invalid_disc_observation"
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

// MaxProducerDt is the longest producer-reported DeltaTime (seconds) accepted
// as a real sample interval. Anything longer is a clock jump, not a stall.
//
// This is the validator's only upper bound and it is a fixed constant, not
// the pipeline.max_frame_dt config key: that key is the FEATURE EXTRACTOR's
// gap threshold (a known dt above it updates raw state but derives no
// kinematics, and it is the clamp ceiling for finite differences). A stall
// between max_frame_dt and MaxProducerDt is therefore a valid frame that the
// extractor treats as a gap, never a rejection.
const MaxProducerDt = 60.0

// MaxHandBodyDistance is the largest |hand - body| (metres) accepted as a
// tracked hand. PAT_005 already treats > 1.6 m hand-to-head as an extended
// reach; 3 m is beyond any arm plus controller offset, so a hand further out
// is a tracking glitch and is sanitized to the zero-vector sentinel.
const MaxHandBodyDistance = 3.0

// DiscSpeedSanityFactor bounds the disc speed the validator accepts, as a
// multiple of the match's DiscSpeedCap. THROW_001 measures over-cap throws
// well below this; a disc reported at more than 4x the cap is corrupt data
// and is dropped from the frame rather than fed to the throw detectors.
const DiscSpeedSanityFactor = 4.0

// MaxEstimatedPingMs is the largest latency accepted as detector context.
// It matches the strict compatibility validator's public wire-contract cap.
const MaxEstimatedPingMs = 1000.0

// FrameValidator checks telemetry frames for validity.
type FrameValidator struct {
	minDt float64
}

// NewFrameValidator creates a validator from config. Only
// pipeline.min_frame_dt is consulted here (its half is the duplicated-tick
// bound); pipeline.max_frame_dt belongs to the feature extractor, see
// MaxProducerDt.
func NewFrameValidator(cfg *config.Config) *FrameValidator {
	return &FrameValidator{
		minDt: cfg.Pipeline.MinFrameDt,
	}
}

// Validate checks a single telemetry frame. It rejects frames whose body
// position, timestamp or delta time is unusable (returning a
// *ValidationError) and sanitizes the rest in place: non-unit rotations are
// normalized, NaN/Inf or near-zero rotations become the zero quaternion,
// NaN/Inf or far-away (> MaxHandBodyDistance) hand positions become the
// zero-vector tracking-loss sentinel (which the feature extractor already
// treats as "no data"), a negative DeltaTime becomes 0 (unknown), and a disc
// with NaN/Inf fields, outside the arena or faster than
// DiscSpeedSanityFactor x DiscSpeedCap is dropped from the frame so no
// downstream distance, angle or speed is nonsense. The returned slice lists
// the sanitization codes applied, each at most once.
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
	// Timestamp: the same rule as the ingest guard (non-finite or negative).
	if math.IsNaN(frame.Timestamp) || math.IsInf(frame.Timestamp, 0) || frame.Timestamp < 0 {
		return nil, &ValidationError{Reason: ReasonInvalidTimestamp, PlayerID: frame.PlayerID,
			Detail: fmt.Sprintf("timestamp %v", frame.Timestamp)}
	}
	if math.IsNaN(frame.DeltaTime) || math.IsInf(frame.DeltaTime, 0) {
		return nil, &ValidationError{Reason: ReasonDtOutOfRange, PlayerID: frame.PlayerID, Detail: "non-finite dt"}
	}
	// Check DeltaTime. dt <= 0 means "unknown" (first frame, restarted clock).
	// Producers report REAL sample spacing (contract 1), so a long gap (a
	// broadcaster stall, a reconnect) is a legitimate sample, not a corrupt
	// frame: it is accepted and the feature extractor treats it as a gap
	// (raw state updated, no kinematics derived, see MaxFrameDt). Only a
	// spacing that cannot be a sample interval is rejected: shorter than half
	// the configured minimum (a duplicated tick) or longer than
	// MaxProducerDt (a clock jump).
	if frame.DeltaTime > 0 && (frame.DeltaTime < v.minDt*0.5 || frame.DeltaTime > MaxProducerDt) {
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
	// A negative dt is "unknown spacing" (the extractor's dt <= 0 rule), not
	// a rejection; normalize it so every consumer sees the same value.
	if frame.DeltaTime < 0 {
		frame.DeltaTime = 0
		sanitized = append(sanitized, SanitizedNegativeDt)
	}
	// Quaternions: normalize near-unit, zero NaN/Inf/near-zero, report the
	// zero sentinel.
	var leftRotationFixed, rightRotationFixed bool
	frame.LeftHandRotation, frame.LeftHandRotationValid, leftRotationFixed =
		sanitizeObservedHandRotation(frame.LeftHandRotation, frame.LeftHandRotationValid)
	frame.RightHandRotation, frame.RightHandRotationValid, rightRotationFixed =
		sanitizeObservedHandRotation(frame.RightHandRotation, frame.RightHandRotationValid)
	rotFixed, rotZero := leftRotationFixed || rightRotationFixed, false
	for i, q := range []*model.Quat{&frame.Rotation, &frame.LeftHandRotation, &frame.RightHandRotation} {
		var fixed bool
		*q, fixed = sanitizeQuat(*q)
		fixed = fixed || (i == 1 && leftRotationFixed) || (i == 2 && rightRotationFixed)
		rotFixed = rotFixed || fixed
		rotZero = rotZero || (!fixed && quatIsZero(*q))
	}
	if rotFixed {
		sanitized = append(sanitized, SanitizedRotation)
	}
	if rotZero {
		sanitized = append(sanitized, SanitizedZeroRotation)
	}
	// Hand positions: tracking loss is encoded as the zero vector. A
	// non-finite hand and a hand impossibly far from the body both become
	// that sentinel (the zero vector itself is never distance-checked).
	var handNaN, handFar bool
	for _, h := range []*model.Vec3{&frame.LeftHandPosition, &frame.RightHandPosition} {
		switch {
		case h.HasNaN() || h.HasInf():
			*h = model.Vec3{}
			handNaN = true
		case !h.IsZero() && h.Distance(frame.Position) > MaxHandBodyDistance:
			*h = model.Vec3{}
			handFar = true
		}
	}
	if handNaN {
		sanitized = append(sanitized, SanitizedHandPosition)
	}
	if handFar {
		sanitized = append(sanitized, SanitizedFarHand)
	}
	// Ping adjusts detector tolerances and confidence, so it must be finite
	// and bounded even when a producer bypasses nevr-compat --strict.
	if math.IsNaN(frame.EstimatedPingMs) || math.IsInf(frame.EstimatedPingMs, 0) || frame.EstimatedPingMs < 0 {
		frame.EstimatedPingMs = 0
		sanitized = append(sanitized, SanitizedPing)
	} else if frame.EstimatedPingMs > MaxEstimatedPingMs {
		frame.EstimatedPingMs = MaxEstimatedPingMs
		sanitized = append(sanitized, SanitizedPing)
	}
	if frame.GameLastThrow != nil && !frame.GameLastThrow.Valid() {
		frame.GameLastThrow = nil
		sanitized = append(sanitized, SanitizedGameLastThrow)
	}
	if frame.ReportedVelocity != nil && (frame.ReportedVelocity.HasNaN() || frame.ReportedVelocity.HasInf()) {
		frame.ReportedVelocity = nil
		sanitized = append(sanitized, SanitizedReportedVelocity)
	}
	if head := frame.HeadPosition; head != nil && (head.IsZero() || head.HasNaN() || head.HasInf() ||
		math.Abs(head[0]) > halfX || math.Abs(head[1]) > halfY || math.Abs(head[2]) > halfZ) {
		frame.HeadPosition = nil
		sanitized = append(sanitized, SanitizedHeadPosition)
	}
	// Disc state: drop it rather than the whole frame when it is not finite
	// or not physically plausible.
	if frame.Disc != nil {
		switch {
		case !discIsFinite(frame.Disc):
			frame.Disc = nil
			sanitized = append(sanitized, SanitizedDisc)
		case !discInRange(frame.Disc, matchCtx.Physics, halfX, halfY, halfZ):
			frame.Disc = nil
			sanitized = append(sanitized, SanitizedDiscOutOfRange)
		}
	}
	if disc := frame.Disc; disc != nil {
		invalidBounce := disc.BounceCount != nil && *disc.BounceCount < 0
		invalidCount := disc.SampledPlayerCount < 0 || (disc.PossessionKnown && disc.SampledPlayerCount == 0)
		if invalidBounce || invalidCount {
			// Frame copies can share a disc pointer. Repair only this frame's
			// optional metadata, preserving the observed motion/holder fields.
			repaired := *disc
			if invalidBounce {
				repaired.BounceCount = nil
			}
			if invalidCount {
				repaired.SampledPlayerCount = 0
				repaired.PossessionKnown = false
			}
			frame.Disc = &repaired
			sanitized = append(sanitized, SanitizedDiscObservation)
		}
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

// sanitizeQuat normalizes near-unit quaternions and zeroes NaN/Inf or
// near-zero (|q| <= 0.1, not normalizable) ones. The zero quaternion is the
// "no rotation data" sentinel that IsUnit() rejects, so consumers skip it.
// The bool reports whether the quaternion was replaced by the sentinel; an
// input that already is the sentinel is returned unchanged with false.
func sanitizeQuat(q model.Quat) (model.Quat, bool) {
	for _, c := range q {
		if math.IsNaN(c) || math.IsInf(c, 0) {
			return model.Quat{}, true
		}
	}
	if normalized, ok := q.NormalizedRotation(); ok {
		return normalized, false
	}
	if magnitude := q.Magnitude(); magnitude > 0.1 && !math.IsInf(magnitude, 0) {
		return q.Normalize(), false
	}
	if quatIsZero(q) {
		return q, false
	}
	return model.Quat{}, true
}

// quatIsZero reports whether q is the zero-quaternion sentinel.
func quatIsZero(q model.Quat) bool {
	return q == model.Quat{}
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

// discInRange reports whether the disc is inside the arena bounds used for
// players and no faster than DiscSpeedSanityFactor x DiscSpeedCap (both the
// velocity magnitude and the reported Speed are checked; a zero cap falls
// back to DefaultPhysics).
func discInRange(d *model.DiscState, ph model.PhysicsConstants, halfX, halfY, halfZ float64) bool {
	if math.Abs(d.Position[0]) > halfX || math.Abs(d.Position[1]) > halfY || math.Abs(d.Position[2]) > halfZ {
		return false
	}
	speedCap := ph.DiscSpeedCap
	if speedCap <= 0 {
		speedCap = model.DefaultPhysics().DiscSpeedCap
	}
	limit := DiscSpeedSanityFactor * speedCap
	return d.Velocity.Magnitude() <= limit && math.Abs(d.Speed) <= limit
}
