package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const inspectorRadius = 8

// physicsInspection is a raw-vs-derived, frame-level explanation of an
// incident. It deliberately records limitations alongside the numbers so the
// UI cannot imply that interpolated replay telemetry is server truth.
type physicsInspection struct {
	Version        int                     `json:"version"`
	MatchID        string                  `json:"match_id"`
	PlayerID       string                  `json:"player_id"`
	PlayerName     string                  `json:"player_name,omitempty"`
	FocusFrame     int                     `json:"focus_frame"`
	DiscSpeedCap   float64                 `json:"disc_speed_cap"`
	Event          *physicsInspectionEvent `json:"event,omitempty"`
	Frames         []physicsInspectorFrame `json:"frames"`
	RawFocusTick   json.RawMessage         `json:"raw_focus_tick,omitempty"`
	RawTickPresent bool                    `json:"raw_tick_present"`
	Telemetry      *telemetryHealthView    `json:"telemetry_health,omitempty"`
	Limitations    []string                `json:"limitations"`
}

type physicsInspectionEvent struct {
	EventID         string          `json:"event_id"`
	DetectorID      string          `json:"detector_id"`
	DetectorVersion string          `json:"detector_version"`
	ObservedValue   string          `json:"observed_value"`
	ExpectedRange   string          `json:"expected_range"`
	Severity        float64         `json:"severity"`
	Confidence      float64         `json:"confidence"`
	Evidence        json.RawMessage `json:"evidence,omitempty"`
}

type derivedPhysicsFrame struct {
	Velocity               model.Vec3 `json:"velocity"`
	Speed                  float64    `json:"speed"`
	Acceleration           model.Vec3 `json:"acceleration"`
	AccelerationMagnitude  float64    `json:"acceleration_magnitude"`
	ReportedVelocity       model.Vec3 `json:"reported_velocity"`
	ReportedSpeed          float64    `json:"reported_speed"`
	HasReportedVelocity    bool       `json:"has_reported_velocity"`
	PlayspaceOffset        model.Vec3 `json:"playspace_offset"`
	PlayspaceDistance      float64    `json:"playspace_distance"`
	PlayspaceVelocity      model.Vec3 `json:"playspace_velocity"`
	PlayspaceSpeed         float64    `json:"playspace_speed"`
	PlayspaceRigCoherence  float64    `json:"playspace_rig_coherence"`
	PlayspaceTrackedHands  int        `json:"playspace_tracked_hands"`
	PlayspaceValid         bool       `json:"playspace_valid"`
	MovementOrigin         string     `json:"movement_origin,omitempty"`
	LeftHandVelocity       model.Vec3 `json:"left_hand_velocity"`
	LeftHandSpeed          float64    `json:"left_hand_speed"`
	LeftHandRelativeSpeed  float64    `json:"left_hand_relative_speed"`
	RightHandVelocity      model.Vec3 `json:"right_hand_velocity"`
	RightHandSpeed         float64    `json:"right_hand_speed"`
	RightHandRelativeSpeed float64    `json:"right_hand_relative_speed"`
	LeftWristAngularRate   float64    `json:"left_wrist_angular_rate"`
	RightWristAngularRate  float64    `json:"right_wrist_angular_rate"`
	FrameDt                float64    `json:"frame_dt"`
	IsHighPing             bool       `json:"is_high_ping"`
}

type physicsInspectorFrame struct {
	FrameIndex            int                        `json:"frame_index"`
	Timestamp             float64                    `json:"timestamp"`
	Focus                 bool                       `json:"focus"`
	Source                model.PlayerTelemetryFrame `json:"source"`
	Derived               derivedPhysicsFrame        `json:"derived"`
	IndependentAudit      independentPhysicsAudit    `json:"independent_audit"`
	ContactClassification string                     `json:"contact_classification"`
	Signals               []string                   `json:"signals"`
	Throw                 *model.ThrowEvent          `json:"throw,omitempty"`
}

// independentPhysicsAudit recomputes first-order kinematics directly from two
// normalized source frames without using FeatureExtractor state. A non-zero
// delta is a feature-extractor/debugging signal, not a cheat signal.
type independentPhysicsAudit struct {
	Valid                  bool       `json:"valid"`
	RawDt                  float64    `json:"raw_dt"`
	PoseVelocity           model.Vec3 `json:"pose_velocity"`
	PoseSpeed              float64    `json:"pose_speed"`
	LeftHandVelocity       model.Vec3 `json:"left_hand_velocity"`
	LeftHandRelativeSpeed  float64    `json:"left_hand_relative_speed"`
	RightHandVelocity      model.Vec3 `json:"right_hand_velocity"`
	RightHandRelativeSpeed float64    `json:"right_hand_relative_speed"`
	ExtractorVelocityDelta float64    `json:"extractor_velocity_delta"`
	Note                   string     `json:"note,omitempty"`
}

func independentAudit(previous, current *model.PlayerTelemetryFrame, derived derivedPhysicsFrame, maxDt float64) independentPhysicsAudit {
	if previous == nil {
		return independentPhysicsAudit{Note: "no prior player frame in this replay"}
	}
	dt := current.Timestamp - previous.Timestamp
	if dt < pipeline.MinFrameDt || dt > maxDt {
		return independentPhysicsAudit{RawDt: dt, Note: "timestamp gap is not valid for finite differences"}
	}
	velocity := current.Position.Sub(previous.Position).Scale(1 / dt)
	out := independentPhysicsAudit{Valid: true, RawDt: dt, PoseVelocity: velocity, PoseSpeed: velocity.Magnitude()}
	if !previous.LeftHandPosition.IsZero() && !current.LeftHandPosition.IsZero() {
		out.LeftHandVelocity = current.LeftHandPosition.Sub(previous.LeftHandPosition).Scale(1 / dt)
		out.LeftHandRelativeSpeed = out.LeftHandVelocity.Sub(velocity).Magnitude()
	}
	if !previous.RightHandPosition.IsZero() && !current.RightHandPosition.IsZero() {
		out.RightHandVelocity = current.RightHandPosition.Sub(previous.RightHandPosition).Scale(1 / dt)
		out.RightHandRelativeSpeed = out.RightHandVelocity.Sub(velocity).Magnitude()
	}
	out.ExtractorVelocityDelta = velocity.Sub(derived.Velocity).Magnitude()
	if out.ExtractorVelocityDelta > 1e-6 {
		out.Note = "independent pose velocity does not match the feature extractor"
	}
	return out
}

func derivedFrame(ps *model.PlayerState) derivedPhysicsFrame {
	return derivedPhysicsFrame{
		Velocity: ps.Velocity, Speed: ps.Speed, Acceleration: ps.Acceleration,
		AccelerationMagnitude: ps.AccelerationMagnitude,
		ReportedVelocity:      ps.ReportedVelocity, ReportedSpeed: ps.ReportedVelocity.Magnitude(),
		HasReportedVelocity: ps.HasReportedVelocity, PlayspaceOffset: ps.PlayspaceOffset,
		PlayspaceDistance: ps.PlayspaceDistance, PlayspaceVelocity: ps.PlayspaceVelocity,
		PlayspaceSpeed: ps.PlayspaceSpeed, PlayspaceRigCoherence: ps.PlayspaceRigCoherence,
		PlayspaceTrackedHands: ps.PlayspaceTrackedHands, PlayspaceValid: ps.PlayspaceValid,
		MovementOrigin: ps.MovementOrigin, LeftHandVelocity: ps.LeftHandVelocity,
		LeftHandSpeed: ps.LeftHandSpeed, LeftHandRelativeSpeed: ps.LeftHandRelativeSpeed,
		RightHandVelocity: ps.RightHandVelocity, RightHandSpeed: ps.RightHandSpeed,
		RightHandRelativeSpeed: ps.RightHandRelativeSpeed,
		LeftWristAngularRate:   ps.LeftWristAngularRate, RightWristAngularRate: ps.RightWristAngularRate,
		FrameDt: ps.FrameDt, IsHighPing: ps.IsHighPing,
	}
}

func classifyInspectorFrame(frame *model.PlayerTelemetryFrame, ps *model.PlayerState, throw *model.ThrowEvent) (string, []string) {
	classification := "no contact observed"
	var signals []string
	if throw != nil {
		switch {
		case throw.PossibleHeadContact:
			classification = "possible head contact"
			signals = append(signals, "disc was closer to the tracked head than either controller")
		case throw.HandTracked:
			classification = "tracked " + throw.ThrowingHand + "-hand release"
			signals = append(signals, "release hand selected from prior held-disc geometry")
		default:
			classification = "unclassified disc release"
			signals = append(signals, "controller attribution was not reliable")
		}
	}
	if frame.IsBoosting {
		signals = append(signals, "source reports boosting")
	}
	if ps.HasReportedVelocity && ps.ReportedVelocity.Magnitude() >= 0.25 {
		signals = append(signals, "game-authored locomotion is present (may include boost, grab, slap, or stacking)")
	}
	if ps.PlayspaceValid && ps.PlayspaceSpeed >= 0.35 {
		signals = append(signals, "tracked rig moved beyond game-authored velocity")
	}
	if frame.LeftHandPosition.IsZero() || frame.RightHandPosition.IsZero() {
		signals = append(signals, "one or more controllers were not tracked")
	}
	if frame.IsStunned {
		signals = append(signals, "player is stunned")
	}
	if frame.ShieldActive {
		signals = append(signals, "shield/block is active")
	}
	if ps.LegalContext.PrimaryExplanation != "" && ps.LegalContext.PrimaryExplanation != "no special legal-motion context identified" {
		signals = append(signals, ps.LegalContext.PrimaryExplanation)
	}
	if len(signals) == 0 {
		signals = []string{"no special state transition identified"}
	}
	return classification, signals
}

func (s *server) buildPhysicsInspection(ctx context.Context, matchID, playerID string, focusFrame int, event *model.DetectionEvent) (*physicsInspection, error) {
	if focusFrame < 0 {
		return nil, fmt.Errorf("invalid focus frame %d", focusFrame)
	}
	store := s.engine.Store()
	sm, err := store.GetStoredMatch(ctx, matchID)
	if err != nil {
		return nil, err
	}
	if event != nil && playerID == "" {
		playerID = event.PlayerID
	}
	if playerID == "" {
		return nil, fmt.Errorf("player id is required")
	}
	frames, err := store.GetMatchFrames(ctx, matchID)
	if err != nil {
		return nil, fmt.Errorf("loading telemetry: %w", err)
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("match %s has no normalized telemetry: %w", matchID, sqlite.ErrNotFound)
	}
	sort.SliceStable(frames, func(i, j int) bool {
		if frames[i].FrameIndex != frames[j].FrameIndex {
			return frames[i].FrameIndex < frames[j].FrameIndex
		}
		return frames[i].PlayerID < frames[j].PlayerID
	})

	p := s.engine.NewPipeline()
	extractor := p.Extractor()
	states := make(map[string]*model.PlayerState)
	for _, pid := range sm.Context.PlayerIDs {
		states[pid] = &model.PlayerState{PlayerID: pid, Team: sm.Context.TeamAssignments[pid]}
	}
	from, to := focusFrame-inspectorRadius, focusFrame+inspectorRadius
	if from < 0 {
		from = 0
	}
	out := &physicsInspection{
		Version: 1, MatchID: matchID, PlayerID: playerID, PlayerName: nameOf(sm.Context, playerID),
		FocusFrame: focusFrame, DiscSpeedCap: sm.Context.Physics.DiscSpeedCap, Frames: []physicsInspectorFrame{},
		Limitations: []string{
			"Replay snapshots may be client-side and interpolated; they are not authoritative server simulation ticks.",
			"Echo telemetry exposes no feet, guardian origin, or arena-object contact ID, so a legal lean, wall/block slap, boost, and stack cannot always be separated from pose data alone.",
			"Contact labels are conservative review aids. They do not create scores or enforcement actions.",
		},
	}
	if event != nil {
		ev := &physicsInspectionEvent{
			EventID: event.EventID, DetectorID: event.DetectorID, DetectorVersion: event.DetectorVersion,
			ObservedValue: event.ObservedValue, ExpectedRange: event.ExpectedRange,
			Severity: event.Severity, Confidence: event.Confidence,
		}
		if event.Evidence != nil {
			if raw, marshalErr := json.Marshal(event.Evidence); marshalErr == nil {
				ev.Evidence = raw
			}
		}
		out.Event = ev
	}
	found := false
	var previousTarget *model.PlayerTelemetryFrame
	for i := range frames {
		frame := &frames[i]
		if frame.FrameIndex > to {
			break
		}
		ps := states[frame.PlayerID]
		if ps == nil {
			ps = &model.PlayerState{PlayerID: frame.PlayerID, Team: sm.Context.TeamAssignments[frame.PlayerID]}
			states[frame.PlayerID] = ps
		}
		extractor.UpdatePlayerState(ps, frame, sm.Context)
		if frame.PlayerID != playerID {
			continue
		}
		derived := derivedFrame(ps)
		audit := independentAudit(previousTarget, frame, derived, extractor.MaxFrameDtSeconds())
		copyFrame := *frame
		previousTarget = &copyFrame
		if frame.FrameIndex < from {
			continue
		}
		found = true
		var throw *model.ThrowEvent
		if ps.LastThrow != nil && ps.LastThrow.FrameIndex == frame.FrameIndex {
			copyThrow := *ps.LastThrow
			throw = &copyThrow
		}
		contact, signals := classifyInspectorFrame(frame, ps, throw)
		out.Frames = append(out.Frames, physicsInspectorFrame{
			FrameIndex: frame.FrameIndex, Timestamp: frame.Timestamp, Focus: frame.FrameIndex == focusFrame,
			Source: *frame, Derived: derived, IndependentAudit: audit, ContactClassification: contact, Signals: signals, Throw: throw,
		})
	}
	if !found {
		return nil, fmt.Errorf("player %s has no telemetry near frame %d: %w", playerID, focusFrame, sqlite.ErrNotFound)
	}
	rawTicks, err := store.GetMatchRawTicks(ctx, matchID, focusFrame, focusFrame)
	if err != nil {
		return nil, err
	}
	if raw := rawTicks[focusFrame]; raw != "" && json.Valid([]byte(raw)) {
		out.RawTickPresent = true
		out.RawFocusTick = json.RawMessage(raw)
	}
	return out, nil
}

func findMatchEvent(ctx context.Context, store *sqlite.Store, matchID, eventID string) (*model.DetectionEvent, error) {
	events, err := store.GetMatchEvents(ctx, matchID)
	if err != nil {
		return nil, err
	}
	for i := range events {
		if events[i].EventID == eventID {
			return &events[i], nil
		}
	}
	return nil, fmt.Errorf("event %s in match %s: %w", eventID, matchID, sqlite.ErrNotFound)
}

func storedTelemetryDiagnostics(ctx context.Context, store *sqlite.Store, matchID string) (*adapter.DiagnosticReport, error) {
	ticks, err := store.GetAllMatchRawTicks(ctx, matchID)
	if err != nil {
		return nil, err
	}
	if len(ticks) == 0 {
		return nil, nil
	}
	indices := make([]int, 0, len(ticks))
	for idx := range ticks {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	diag := adapter.NewDiagnosticReport()
	for _, idx := range indices {
		if _, err := diag.RecordSessionJSON([]byte(ticks[idx])); err != nil {
			diag.FramesRejected++
		}
	}
	return diag, nil
}

func isInspectorNotFound(err error) bool {
	return errors.Is(err, sqlite.ErrNotFound)
}
