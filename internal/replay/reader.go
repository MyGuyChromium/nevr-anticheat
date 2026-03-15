// Package replay handles reading and converting replay/telemetry data.
package replay

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

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

// RawDiscFrame contains raw disc data for one frame.
type RawDiscFrame struct {
	Position [3]float64 `json:"position"`
	Velocity [3]float64 `json:"velocity"`
	HolderID string     `json:"holder_id"`
}

// ReplayReader reads replay files and produces normalized telemetry frames.
type ReplayReader struct {
	path   string
	parser FrameParser
}

// NewReplayReader creates a new reader with the given parser.
func NewReplayReader(path string, parser FrameParser) *ReplayReader {
	return &ReplayReader{path: path, parser: parser}
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

	var allFrames []model.PlayerTelemetryFrame
	prevTimestamp := 0.0

	for {
		raw, ok, err := r.parser.NextFrame()
		if err != nil {
			return nil, nil, fmt.Errorf("reading frame: %w", err)
		}
		if !ok {
			break
		}
		converted := convertFrame(raw, prevTimestamp)
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

func convertFrame(raw *RawFrame, prevTimestamp float64) []model.PlayerTelemetryFrame {
	dt := raw.Timestamp - prevTimestamp
	if dt < 0 {
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
	frames []RawFrame
	header *ReplayHeader
	idx    int
}

func NewJSONFrameParser() *JSONFrameParser {
	return &JSONFrameParser{}
}

type jsonReplay struct {
	Header ReplayHeader `json:"header"`
	Frames []RawFrame   `json:"frames"`
}

func (p *JSONFrameParser) Open(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return err
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
