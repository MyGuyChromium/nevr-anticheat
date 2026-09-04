package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

type auditPrevious struct {
	position  model.Vec3
	timestamp float64
	seen      bool
}

var physicsAuditHeader = []string{
	"match_id", "frame_index", "timestamp_s", "player_id", "team", "derivation_status", "dt_s",
	"pose_x", "pose_y", "pose_z", "reported_vx", "reported_vy", "reported_vz", "reported_speed_mps",
	"independent_vx", "independent_vy", "independent_vz", "independent_speed_mps",
	"extractor_vx", "extractor_vy", "extractor_vz", "extractor_speed_mps", "extractor_delta_error_mps",
	"playspace_vx", "playspace_vy", "playspace_vz", "playspace_speed_mps", "playspace_distance_m",
	"playspace_rig_coherence", "playspace_valid", "movement_origin", "leaning", "playspace_step",
	"game_locomotion", "boosting", "possible_slap_or_push", "possible_head_contact", "tracking_limited",
	"left_hand_world_speed_mps", "right_hand_world_speed_mps", "left_hand_relative_speed_mps",
	"right_hand_relative_speed_mps", "left_wrist_rad_s", "right_wrist_rad_s", "has_possession",
	"disc_speed_mps", "disc_held", "disc_possessor", "release_detected", "release_hand", "release_speed_mps",
	"engine_last_throw_total_mps", "ping_ms", "game_phase",
}

func ff(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
func fi(v int) string     { return strconv.Itoa(v) }
func fb(v bool) string    { return strconv.FormatBool(v) }

// writePhysicsAudit exports detector inputs and a separately computed pose
// derivative. It intentionally does not issue a cheating verdict: the file is
// evidence for lining NEVR's reconstruction up with Spark or another trusted
// viewer before thresholds are calibrated.
func writePhysicsAudit(replayPath, outputPath string) (rows int, err error) {
	out, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("creating physics audit: %w", err)
	}
	cw := csv.NewWriter(out)
	keep, closed := false, false
	defer func() {
		if !closed {
			_ = out.Close()
		}
		if !keep {
			// #nosec G703 -- outputPath is the explicit CLI destination created above.
			_ = os.Remove(outputPath)
		}
	}()
	if err := cw.Write(physicsAuditHeader); err != nil {
		return 0, err
	}

	parser := adapter.NewEchoReplayParser()
	states := make(map[string]*model.PlayerState)
	previous := make(map[string]auditPrevious)
	extractor := pipeline.NewFeatureExtractor(model.DefaultHistoryWindow)
	_, _, parseErr := parser.ParseFileStream(replayPath, func(tick *adapter.ParsedTick) error {
		if tick.NewMatch {
			states = make(map[string]*model.PlayerState)
			previous = make(map[string]auditPrevious)
			extractor = pipeline.NewFeatureExtractor(model.DefaultHistoryWindow)
		}
		for i := range tick.Frames {
			frame := &tick.Frames[i]
			ps := states[frame.PlayerID]
			if ps == nil {
				ps = &model.PlayerState{PlayerID: frame.PlayerID, Team: frame.Team}
				states[frame.PlayerID] = ps
			}
			beforeThrows := ps.ThrowCount
			prev := previous[frame.PlayerID]
			status := "first_sample"
			dt := 0.0
			independentVelocity := model.Vec3{}
			if prev.seen {
				dt = frame.Timestamp - prev.timestamp
				switch {
				case math.IsNaN(dt) || math.IsInf(dt, 0) || dt <= 0:
					status = "non_monotonic_time"
				case dt > extractor.MaxFrameDtSeconds():
					status = "sampling_gap"
				case prev.position.IsZero():
					status = "missing_previous_pose"
				default:
					status = "derived"
					independentVelocity = frame.Position.Sub(prev.position).Scale(1 / model.Clamp(dt, pipeline.MinFrameDt, extractor.MaxFrameDtSeconds()))
				}
			}

			extractor.UpdatePlayerState(ps, frame, tick.MatchCtx)
			releaseDetected := ps.ThrowCount > beforeThrows && ps.LastThrow != nil
			releaseHand, releaseSpeed := "", 0.0
			if releaseDetected {
				releaseHand = ps.LastThrow.ThrowingHand
				releaseSpeed = ps.LastThrow.ReleaseSpeed
			}
			reported := model.Vec3{}
			if frame.ReportedVelocity != nil {
				reported = *frame.ReportedVelocity
			}
			discSpeed, discHeld, discPossessor := 0.0, false, ""
			if frame.Disc != nil {
				discSpeed, discHeld, discPossessor = frame.Disc.Speed, frame.Disc.IsHeld, frame.Disc.PossessorID
			}
			engineThrowSpeed := 0.0
			if frame.GameLastThrow != nil {
				engineThrowSpeed = frame.GameLastThrow.TotalSpeed
			}
			deltaError := ps.Velocity.Sub(independentVelocity).Magnitude()
			lc := ps.LegalContext
			record := []string{
				tick.MatchID, fi(frame.FrameIndex), ff(frame.Timestamp), frame.PlayerID, frame.Team, status, ff(dt),
				ff(frame.Position[0]), ff(frame.Position[1]), ff(frame.Position[2]), ff(reported[0]), ff(reported[1]), ff(reported[2]), ff(reported.Magnitude()),
				ff(independentVelocity[0]), ff(independentVelocity[1]), ff(independentVelocity[2]), ff(independentVelocity.Magnitude()),
				ff(ps.Velocity[0]), ff(ps.Velocity[1]), ff(ps.Velocity[2]), ff(ps.Speed), ff(deltaError),
				ff(ps.PlayspaceVelocity[0]), ff(ps.PlayspaceVelocity[1]), ff(ps.PlayspaceVelocity[2]), ff(ps.PlayspaceSpeed), ff(ps.PlayspaceDistance),
				ff(ps.PlayspaceRigCoherence), fb(ps.PlayspaceValid), ps.MovementOrigin, fb(lc.Leaning), fb(lc.PlayspaceStep),
				fb(lc.GameLocomotion), fb(lc.Boosting), fb(lc.PossibleSlapOrPush), fb(lc.PossibleHeadContact), fb(lc.TrackingLimited),
				ff(ps.LeftHandSpeed), ff(ps.RightHandSpeed), ff(ps.LeftHandRelativeSpeed), ff(ps.RightHandRelativeSpeed),
				ff(ps.LeftWristAngularRate), ff(ps.RightWristAngularRate), fb(frame.HasPossession), ff(discSpeed), fb(discHeld), discPossessor,
				fb(releaseDetected), releaseHand, ff(releaseSpeed), ff(engineThrowSpeed), ff(frame.EstimatedPingMs), frame.GamePhase,
			}
			if err := cw.Write(record); err != nil {
				return err
			}
			rows++
			previous[frame.PlayerID] = auditPrevious{position: frame.Position, timestamp: frame.Timestamp, seen: true}
		}
		return nil
	})
	if parseErr != nil {
		return rows, fmt.Errorf("parsing replay: %w", parseErr)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return rows, fmt.Errorf("writing physics audit: %w", err)
	}
	if err := out.Close(); err != nil {
		return rows, fmt.Errorf("closing physics audit: %w", err)
	}
	closed = true
	keep = true
	return rows, nil
}
