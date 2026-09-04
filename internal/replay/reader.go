// Package replay handles reading and converting replay/telemetry data.
package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// DefaultMaxLegacyReplayBytes caps the older single-document JSON format.
// Modern .echoreplay files use adapter.EchoReplayParser and are streamed, but
// accepting an unbounded io.ReadAll here would let a corrupt or hostile file
// exhaust the desktop process before JSON decoding even begins.
const DefaultMaxLegacyReplayBytes int64 = 512 * 1024 * 1024

var ErrLegacyReplayTooLarge = errors.New("legacy JSON replay exceeds size limit")

// FrameParser is the interface for parsing raw replay data.
// The actual protobuf parser depends on the nevr-common/v4 library.
// This decouples the anticheat from the replay format.
type FrameParser interface {
	Open(path string) error
	Close() error
	Header() (*ReplayHeader, error)
	NextFrame() (*RawFrame, bool, error)
}

// ReplayHeader contains match metadata from the replay file header.
type ReplayHeader struct {
	MatchID      string
	Map          string
	GameMode     string
	IsRanked     bool
	IsPrivate    bool
	StartTime    time.Time
	PlayerIDs    []string
	Teams        map[string]string
	ServerRegion string
}

// RawFrame contains a single frame of raw telemetry data.
type RawFrame struct {
	Index     int
	Timestamp float64
	GamePhase string
	Players   []RawPlayerFrame
	Disc      *RawDiscFrame
}

// RawPlayerFrame contains raw per-player data for one frame.
//
// Legacy JSON replay layout ({"header": ReplayHeader, "frames": [RawFrame]}):
// the canonical keys are the ones tagged below; the wire-contract spellings
// from docs/telemetry_contract.md (left_hand_position, right_hand_position,
// left_hand_rotation, right_hand_rotation, estimated_ping_ms) are accepted as
// aliases so a file crafted from the contract does not silently lose hands
// and ping.
type RawPlayerFrame struct {
	PlayerID      string     `json:"player_id"`
	Position      [3]float64 `json:"position"`
	Rotation      [4]float64 `json:"rotation"`
	LeftHand      [3]float64 `json:"left_hand"`
	RightHand     [3]float64 `json:"right_hand"`
	LeftHandRot   [4]float64 `json:"left_hand_rot"`
	RightHandRot  [4]float64 `json:"right_hand_rot"`
	IsStunned     bool       `json:"is_stunned"`
	IsBoosting    bool       `json:"is_boosting"`
	ShieldActive  bool       `json:"shield_active"`
	IsImmune      bool       `json:"is_immune"`
	HasPossession bool       `json:"has_possession"`
	PingMs        float64    `json:"ping_ms"`
	BlueScore     int        `json:"blue_score"`
	OrangeScore   int        `json:"orange_score"`
	Goals         int        `json:"goals"`
	Stuns         int        `json:"stuns"`
}

// rawPlayerFrameJSON avoids recursion in RawPlayerFrame.UnmarshalJSON.
type rawPlayerFrameJSON RawPlayerFrame

// UnmarshalJSON decodes the canonical keys and then applies the contract
// aliases for any field the canonical key left at its zero value.
func (rp *RawPlayerFrame) UnmarshalJSON(data []byte) error {
	var base rawPlayerFrameJSON
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}
	var alias struct {
		LeftHand     *[3]float64 `json:"left_hand_position"`
		RightHand    *[3]float64 `json:"right_hand_position"`
		LeftHandRot  *[4]float64 `json:"left_hand_rotation"`
		RightHandRot *[4]float64 `json:"right_hand_rotation"`
		PingMs       *float64    `json:"estimated_ping_ms"`
	}
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	if base.LeftHand == ([3]float64{}) && alias.LeftHand != nil {
		base.LeftHand = *alias.LeftHand
	}
	if base.RightHand == ([3]float64{}) && alias.RightHand != nil {
		base.RightHand = *alias.RightHand
	}
	if base.LeftHandRot == ([4]float64{}) && alias.LeftHandRot != nil {
		base.LeftHandRot = *alias.LeftHandRot
	}
	if base.RightHandRot == ([4]float64{}) && alias.RightHandRot != nil {
		base.RightHandRot = *alias.RightHandRot
	}
	if base.PingMs == 0 && alias.PingMs != nil {
		base.PingMs = *alias.PingMs
	}
	*rp = RawPlayerFrame(base)
	return nil
}

// RawDiscFrame contains raw disc data for one frame. "possessor_id" is
// accepted as an alias for "holder_id".
type RawDiscFrame struct {
	Position [3]float64 `json:"position"`
	Velocity [3]float64 `json:"velocity"`
	HolderID string     `json:"holder_id"`
}

type rawDiscFrameJSON RawDiscFrame

func (rd *RawDiscFrame) UnmarshalJSON(data []byte) error {
	var base rawDiscFrameJSON
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}
	var alias struct {
		PossessorID string `json:"possessor_id"`
	}
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	if base.HolderID == "" {
		base.HolderID = alias.PossessorID
	}
	*rd = RawDiscFrame(base)
	return nil
}

// ReplayReader reads replay files and produces normalized telemetry frames.
type ReplayReader struct {
	path    string
	parser  FrameParser
	physics model.PhysicsConstants
}

// NewReplayReader creates a new reader with the given parser.
func NewReplayReader(path string, parser FrameParser) *ReplayReader {
	return &ReplayReader{path: path, parser: parser, physics: model.DefaultPhysics()}
}

// SetPhysics sets the physics constants copied into the MatchContext this
// reader produces (from the [physics] config block). Default: model.DefaultPhysics.
func (r *ReplayReader) SetPhysics(phys model.PhysicsConstants) {
	if phys != (model.PhysicsConstants{}) {
		r.physics = phys
	}
}

// ReadMatch reads an entire match, returning context and frames.
func (r *ReplayReader) ReadMatch() (*model.MatchContext, []model.PlayerTelemetryFrame, error) {
	if err := r.parser.Open(r.path); err != nil {
		return nil, nil, fmt.Errorf("opening replay: %w", err)
	}
	defer r.parser.Close()

	header, err := r.parser.Header()
	if err != nil {
		return nil, nil, fmt.Errorf("reading header: %w", err)
	}

	matchCtx := convertHeader(header, r.path)
	matchCtx.Physics = r.physics

	var allFrames []model.PlayerTelemetryFrame
	prevTimestamp := 0.0
	first := true

	for {
		raw, ok, err := r.parser.NextFrame()
		if err != nil {
			return nil, nil, fmt.Errorf("reading frame: %w", err)
		}
		if !ok {
			break
		}
		// The first tick has no predecessor: dt is unknown (0), not
		// timestamp - 0, which would reject excerpts that start mid-match.
		converted := convertFrame(raw, prevTimestamp, first)
		first = false
		allFrames = append(allFrames, converted...)
		prevTimestamp = raw.Timestamp
	}

	return matchCtx, allFrames, nil
}

func convertHeader(h *ReplayHeader, path string) *model.MatchContext {
	return &model.MatchContext{
		MatchID:         h.MatchID,
		Map:             h.Map,
		GameMode:        h.GameMode,
		IsRanked:        h.IsRanked,
		IsPrivate:       h.IsPrivate,
		StartTime:       h.StartTime,
		PlayerIDs:       h.PlayerIDs,
		TeamAssignments: h.Teams,
		ServerRegion:    h.ServerRegion,
		Source:          "replay",
		ReplayFile:      path,
		Physics:         model.DefaultPhysics(),
	}
}

func convertFrame(raw *RawFrame, prevTimestamp float64, first bool) []model.PlayerTelemetryFrame {
	dt := raw.Timestamp - prevTimestamp
	if first || dt < 0 {
		dt = 0
	}

	var disc *model.DiscState
	if raw.Disc != nil {
		vel := model.Vec3(raw.Disc.Velocity)
		disc = &model.DiscState{
			Position:    model.Vec3(raw.Disc.Position),
			Velocity:    vel,
			Speed:       vel.Magnitude(),
			PossessorID: raw.Disc.HolderID,
			IsHeld:      raw.Disc.HolderID != "",
		}
	}

	var frames []model.PlayerTelemetryFrame
	for _, rp := range raw.Players {
		frames = append(frames, model.PlayerTelemetryFrame{
			PlayerID:          rp.PlayerID,
			FrameIndex:        raw.Index,
			Timestamp:         raw.Timestamp,
			DeltaTime:         dt,
			Position:          model.Vec3(rp.Position),
			Rotation:          model.Quat(rp.Rotation),
			LeftHandPosition:  model.Vec3(rp.LeftHand),
			RightHandPosition: model.Vec3(rp.RightHand),
			LeftHandRotation:  model.Quat(rp.LeftHandRot),
			RightHandRotation: model.Quat(rp.RightHandRot),
			IsStunned:         rp.IsStunned,
			IsBoosting:        rp.IsBoosting,
			ShieldActive:      rp.ShieldActive,
			IsImmune:          rp.IsImmune,
			HasPossession:     rp.HasPossession,
			Disc:              disc,
			EstimatedPingMs:   rp.PingMs,
			GamePhase:         raw.GamePhase,
			BlueScore:         rp.BlueScore,
			OrangeScore:       rp.OrangeScore,
			Goals:             rp.Goals,
			Stuns:             rp.Stuns,
		})
	}
	return frames
}

// JSONFrameParser reads JSON-encoded frame files (for testing).
type JSONFrameParser struct {
	frames   []RawFrame
	header   *ReplayHeader
	idx      int
	maxBytes int64
}

func NewJSONFrameParser() *JSONFrameParser {
	return &JSONFrameParser{maxBytes: DefaultMaxLegacyReplayBytes}
}

// SetMaxBytes overrides the legacy JSON document limit. It is primarily useful
// to give importers and tests a stricter bound; non-positive values are ignored.
func (p *JSONFrameParser) SetMaxBytes(n int64) {
	if n > 0 {
		p.maxBytes = n
	}
}

type jsonReplay struct {
	Header ReplayHeader `json:"header"`
	Frames []RawFrame   `json:"frames"`
}

func (p *JSONFrameParser) Open(path string) error {
	p.frames = nil
	p.header = nil
	p.idx = 0
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	maxBytes := p.maxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxLegacyReplayBytes
	}
	if info, statErr := f.Stat(); statErr == nil && info.Size() > maxBytes {
		return fmt.Errorf("%w: %d bytes (max %d)", ErrLegacyReplayTooLarge, info.Size(), maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("%w: more than %d bytes", ErrLegacyReplayTooLarge, maxBytes)
	}
	var replay jsonReplay
	if err := json.Unmarshal(data, &replay); err != nil {
		return err
	}
	p.header = &replay.Header
	p.frames = replay.Frames
	p.idx = 0
	return nil
}

func (p *JSONFrameParser) Close() error { return nil }

func (p *JSONFrameParser) Header() (*ReplayHeader, error) {
	if p.header == nil {
		return nil, fmt.Errorf("no header available")
	}
	return p.header, nil
}

func (p *JSONFrameParser) NextFrame() (*RawFrame, bool, error) {
	if p.idx >= len(p.frames) {
		return nil, false, nil
	}
	frame := &p.frames[p.idx]
	p.idx++
	return frame, true, nil
}
