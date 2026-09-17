// Package replay handles reading and converting replay/telemetry data.
package replay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// DefaultMaxLegacyReplayBytes caps the older single-document JSON format.
// Modern .echoreplay files use adapter.EchoReplayParser and are streamed. The
// legacy document is decoded as a stream too, but encoding/json buffers one
// whole value (one frame) at a time, so a document that is a single giant
// frame costs about twice its size in transient memory before it is refused.
// 128 MiB holds roughly 300,000 per-player frames of this format (eight
// players at 30 Hz for 20 minutes); it was 512 MiB, read into memory whole.
// What bounds the decoded result are the structural limits below.
const DefaultMaxLegacyReplayBytes int64 = 128 * 1024 * 1024

const (
	// MaxLegacyReplayPlayersPerFrame bounds the "Players" array of one frame
	// (and the header roster). It matches the native tape slot limit; an Echo
	// Arena match never comes close.
	MaxLegacyReplayPlayersPerFrame = 64
	// MaxLegacyReplayFrames bounds both the number of frames (ticks) and the
	// number of per-player frames a legacy document may produce. Every
	// per-player frame costs about 600 bytes of memory however few bytes the
	// file spends on it ("{}," is three), so without this limit a 16 MiB file
	// expanded to 5.6 million frames and 9 GB of heap.
	MaxLegacyReplayFrames = 2_000_000
)

var (
	ErrLegacyReplayTooLarge = errors.New("legacy JSON replay exceeds size limit")
	// ErrLegacyReplayTooManyPlayers rejects a frame (or header roster) that
	// lists more than MaxLegacyReplayPlayersPerFrame players.
	ErrLegacyReplayTooManyPlayers = errors.New("legacy JSON replay lists too many players in one frame")
	// ErrLegacyReplayTooManyFrames rejects a document holding more than
	// MaxLegacyReplayFrames frames or per-player frames.
	ErrLegacyReplayTooManyFrames = errors.New("legacy JSON replay holds too many frames")
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
	HasScore      bool       `json:"-"`
	Goals         int        `json:"goals"`
	Stuns         int        `json:"stuns"`
}

// legacyField decodes one JSON value and remembers whether its key was present
// and whether the value was null, so a single pass over a player object can
// tell "key absent" from "explicit zero" without a second map decode. With a
// repeated key the last occurrence decides null, like encoding/json itself.
type legacyField[T any] struct {
	value   T
	present bool
	null    bool
}

func (f *legacyField[T]) UnmarshalJSON(data []byte) error {
	f.present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.null = true // like encoding/json: null leaves the value alone
		return nil
	}
	f.null = false
	return json.Unmarshal(data, &f.value)
}

// rawPlayerFrameWire is the single-pass wire form of RawPlayerFrame: the
// canonical keys (presence-tracked where an alias or the score rule needs
// it) plus the contract aliases.
type rawPlayerFrameWire struct {
	PlayerID      string                  `json:"player_id"`
	Position      [3]float64              `json:"position"`
	Rotation      [4]float64              `json:"rotation"`
	LeftHand      legacyField[[3]float64] `json:"left_hand"`
	RightHand     legacyField[[3]float64] `json:"right_hand"`
	LeftHandRot   legacyField[[4]float64] `json:"left_hand_rot"`
	RightHandRot  legacyField[[4]float64] `json:"right_hand_rot"`
	IsStunned     bool                    `json:"is_stunned"`
	IsBoosting    bool                    `json:"is_boosting"`
	ShieldActive  bool                    `json:"shield_active"`
	IsImmune      bool                    `json:"is_immune"`
	HasPossession bool                    `json:"has_possession"`
	PingMs        legacyField[float64]    `json:"ping_ms"`
	BlueScore     legacyField[int]        `json:"blue_score"`
	OrangeScore   legacyField[int]        `json:"orange_score"`
	Goals         int                     `json:"goals"`
	Stuns         int                     `json:"stuns"`

	LeftHandAlias     *[3]float64 `json:"left_hand_position"`
	RightHandAlias    *[3]float64 `json:"right_hand_position"`
	LeftHandRotAlias  *[4]float64 `json:"left_hand_rotation"`
	RightHandRotAlias *[4]float64 `json:"right_hand_rotation"`
	PingMsAlias       *float64    `json:"estimated_ping_ms"`
}

// UnmarshalJSON decodes the canonical keys and applies the contract aliases
// only when the canonical key is absent. Explicit zero/null canonical
// measurements never become fresh nonzero values from a conflicting alias.
// The player object is decoded once; it used to be decoded three times.
func (rp *RawPlayerFrame) UnmarshalJSON(data []byte) error {
	var w rawPlayerFrameWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	out := RawPlayerFrame{
		PlayerID: w.PlayerID, Position: w.Position, Rotation: w.Rotation,
		LeftHand: w.LeftHand.value, RightHand: w.RightHand.value,
		LeftHandRot: w.LeftHandRot.value, RightHandRot: w.RightHandRot.value,
		IsStunned: w.IsStunned, IsBoosting: w.IsBoosting, ShieldActive: w.ShieldActive,
		IsImmune: w.IsImmune, HasPossession: w.HasPossession,
		PingMs: w.PingMs.value, BlueScore: w.BlueScore.value, OrangeScore: w.OrangeScore.value,
		Goals: w.Goals, Stuns: w.Stuns,
	}
	if !w.LeftHand.present && w.LeftHandAlias != nil {
		out.LeftHand = *w.LeftHandAlias
	}
	if !w.RightHand.present && w.RightHandAlias != nil {
		out.RightHand = *w.RightHandAlias
	}
	if !w.LeftHandRot.present && w.LeftHandRotAlias != nil {
		out.LeftHandRot = *w.LeftHandRotAlias
	}
	if !w.RightHandRot.present && w.RightHandRotAlias != nil {
		out.RightHandRot = *w.RightHandRotAlias
	}
	if !w.PingMs.present && w.PingMsAlias != nil {
		out.PingMs = *w.PingMsAlias
	}
	out.HasScore = w.BlueScore.present && !w.BlueScore.null && w.OrangeScore.present && !w.OrangeScore.null
	*rp = out
	return nil
}

// RawDiscFrame contains raw disc data for one frame. "possessor_id" is
// accepted as an alias for "holder_id".
type RawDiscFrame struct {
	Position [3]float64 `json:"position"`
	Velocity [3]float64 `json:"velocity"`
	HolderID string     `json:"holder_id"`
}

func (rd *RawDiscFrame) UnmarshalJSON(data []byte) error {
	var w struct {
		Position    [3]float64          `json:"position"`
		Velocity    [3]float64          `json:"velocity"`
		HolderID    legacyField[string] `json:"holder_id"`
		PossessorID string              `json:"possessor_id"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	out := RawDiscFrame{Position: w.Position, Velocity: w.Velocity, HolderID: w.HolderID.value}
	if !w.HolderID.present {
		out.HolderID = w.PossessorID
	}
	*rd = out
	return nil
}

// boundedArray walks a JSON array one element at a time so a limit applies
// while decoding, before an oversized array has been materialised. data is
// the complete JSON value; null is accepted as an empty array.
func boundedArray(data []byte, each func(dec *json.Decoder, n int) error) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("expected a JSON array, found %v", tok)
	}
	for n := 0; dec.More(); n++ {
		if err := each(dec, n); err != nil {
			return err
		}
	}
	_, err = dec.Token() // closing bracket
	return err
}

const legacyDamagedHint = "the file is damaged or was not written by a recorder, so nothing was imported"

// legacyPlayers is RawFrame.Players on the wire, limited while decoding.
type legacyPlayers []RawPlayerFrame

func (lp *legacyPlayers) UnmarshalJSON(data []byte) error {
	*lp = nil
	return boundedArray(data, func(dec *json.Decoder, n int) error {
		if n >= MaxLegacyReplayPlayersPerFrame {
			return fmt.Errorf("%w: more than %d players (an Echo Arena match has about ten); %s",
				ErrLegacyReplayTooManyPlayers, MaxLegacyReplayPlayersPerFrame, legacyDamagedHint)
		}
		var p RawPlayerFrame
		if err := dec.Decode(&p); err != nil {
			return err
		}
		*lp = append(*lp, p)
		return nil
	})
}

// legacyPlayerIDs is ReplayHeader.PlayerIDs on the wire, limited while decoding.
type legacyPlayerIDs []string

func (ids *legacyPlayerIDs) UnmarshalJSON(data []byte) error {
	*ids = nil
	return boundedArray(data, func(dec *json.Decoder, n int) error {
		if n >= MaxLegacyReplayPlayersPerFrame {
			return fmt.Errorf("%w: the header roster names more than %d players; %s",
				ErrLegacyReplayTooManyPlayers, MaxLegacyReplayPlayersPerFrame, legacyDamagedHint)
		}
		var id string
		if err := dec.Decode(&id); err != nil {
			return err
		}
		*ids = append(*ids, id)
		return nil
	})
}

// UnmarshalJSON decodes one frame with its player list bounded (see
// MaxLegacyReplayPlayersPerFrame). Keys are the Go field names, as before.
func (rf *RawFrame) UnmarshalJSON(data []byte) error {
	var w struct {
		Index     int
		Timestamp float64
		GamePhase string
		Players   legacyPlayers
		Disc      *RawDiscFrame
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*rf = RawFrame{Index: w.Index, Timestamp: w.Timestamp, GamePhase: w.GamePhase, Players: w.Players, Disc: w.Disc}
	return nil
}

// UnmarshalJSON decodes the header with its roster bounded.
func (h *ReplayHeader) UnmarshalJSON(data []byte) error {
	var w struct {
		MatchID      string
		Map          string
		GameMode     string
		IsRanked     bool
		IsPrivate    bool
		StartTime    time.Time
		PlayerIDs    legacyPlayerIDs
		Teams        map[string]string
		ServerRegion string
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if len(w.Teams) > MaxLegacyReplayPlayersPerFrame {
		return fmt.Errorf("%w: the header assigns teams to %d players (limit %d); %s",
			ErrLegacyReplayTooManyPlayers, len(w.Teams), MaxLegacyReplayPlayersPerFrame, legacyDamagedHint)
	}
	*h = ReplayHeader{MatchID: w.MatchID, Map: w.Map, GameMode: w.GameMode, IsRanked: w.IsRanked, IsPrivate: w.IsPrivate,
		StartTime: w.StartTime, PlayerIDs: w.PlayerIDs, Teams: w.Teams, ServerRegion: w.ServerRegion}
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
	return r.readOpened()
}

// readOpened converts an already opened parser's header and frames. ReadMatch
// is the production caller; the fuzz target feeds the parser from memory.
func (r *ReplayReader) readOpened() (*model.MatchContext, []model.PlayerTelemetryFrame, error) {
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
		// The file name only, like every other source: the full path names the
		// uploader's user profile and would be persisted and exported as evidence.
		ReplayFile: filepath.Base(path),
		Physics:    model.DefaultPhysics(),
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
			HasScore:          rp.HasScore || rp.BlueScore != 0 || rp.OrangeScore != 0,
			BlueScore:         rp.BlueScore,
			OrangeScore:       rp.OrangeScore,
			Goals:             rp.Goals,
			Stuns:             rp.Stuns,
		})
	}
	return frames
}

// JSONFrameParser reads the legacy single-document JSON replay format
// ({"header": ..., "frames": [...]}). The desktop app accepts .json uploads,
// so the document is untrusted: it is decoded as a stream under a byte limit
// and the structural limits MaxLegacyReplayPlayersPerFrame and
// MaxLegacyReplayFrames, all enforced while decoding.
type JSONFrameParser struct {
	frames    []RawFrame
	header    *ReplayHeader
	idx       int
	maxBytes  int64
	maxFrames int
}

func NewJSONFrameParser() *JSONFrameParser {
	return &JSONFrameParser{maxBytes: DefaultMaxLegacyReplayBytes, maxFrames: MaxLegacyReplayFrames}
}

// SetMaxBytes overrides the legacy JSON document limit. It is primarily useful
// to give importers and tests a stricter bound; non-positive values are ignored.
func (p *JSONFrameParser) SetMaxBytes(n int64) {
	if n > 0 {
		p.maxBytes = n
	}
}

func (p *JSONFrameParser) limit() int64 {
	if p.maxBytes <= 0 {
		return DefaultMaxLegacyReplayBytes
	}
	return p.maxBytes
}

func (p *JSONFrameParser) frameLimit() int {
	if p.maxFrames <= 0 {
		return MaxLegacyReplayFrames
	}
	return p.maxFrames
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
	if info, statErr := f.Stat(); statErr == nil && info.Size() > p.limit() {
		return fmt.Errorf("%w: %d bytes (max %d)", ErrLegacyReplayTooLarge, info.Size(), p.limit())
	}
	return p.load(f)
}

// legacyLimitReader fails with ErrLegacyReplayTooLarge once more than limit
// bytes have been read (the file size is only advisory: a file can grow, and
// load also serves readers that have no size).
type legacyLimitReader struct {
	r     io.Reader
	limit int64
	read  int64
}

func (l *legacyLimitReader) Read(buf []byte) (int, error) {
	n, err := l.r.Read(buf)
	l.read += int64(n)
	if l.read > l.limit {
		return n, fmt.Errorf("%w: more than %d bytes", ErrLegacyReplayTooLarge, l.limit)
	}
	return n, err
}

// load decodes one legacy JSON document from r. Open is the production
// caller; the fuzz target calls load directly so the fuzzing engine never
// waits on the filesystem. Nothing is kept when load fails.
func (p *JSONFrameParser) load(r io.Reader) error {
	p.frames = nil
	p.header = nil
	p.idx = 0
	header, frames, err := decodeLegacyReplay(json.NewDecoder(&legacyLimitReader{r: r, limit: p.limit()}), p.frameLimit())
	if err != nil {
		return err
	}
	p.header = header
	p.frames = frames
	return nil
}

// decodeLegacyReplay walks the top-level object token by token so the frame
// limits apply while the "frames" array is being decoded. Key matching is
// case-insensitive like encoding/json; unknown keys are skipped.
func decodeLegacyReplay(dec *json.Decoder, maxFrames int) (*ReplayHeader, []RawFrame, error) {
	header := &ReplayHeader{}
	var frames []RawFrame
	tok, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, errors.New("legacy JSON replay is empty")
		}
		return nil, nil, err
	}
	if tok != nil { // a bare null document is an empty replay, as before
		if d, ok := tok.(json.Delim); !ok || d != '{' {
			return nil, nil, fmt.Errorf("legacy JSON replay must be a JSON object with \"header\" and \"frames\", found %v", tok)
		}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, nil, err
			}
			key, _ := keyTok.(string)
			switch {
			case strings.EqualFold(key, "header"):
				if err := dec.Decode(header); err != nil {
					return nil, nil, fmt.Errorf("header: %w", err)
				}
			case strings.EqualFold(key, "frames"):
				if frames, err = decodeLegacyFrames(dec, maxFrames); err != nil {
					return nil, nil, err
				}
			default:
				if err := skipJSONValue(dec); err != nil {
					return nil, nil, err
				}
			}
		}
		if _, err := dec.Token(); err != nil { // closing brace
			return nil, nil, err
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected data after the replay document")
		}
		return nil, nil, err
	}
	return header, frames, nil
}

// decodeLegacyFrames decodes the "frames" array, refusing the document as
// soon as it holds more than maxFrames frames or per-player frames.
func decodeLegacyFrames(dec *json.Decoder, maxFrames int) ([]RawFrame, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok == nil {
		return nil, nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, fmt.Errorf("legacy JSON replay: \"frames\" must be an array, found %v", tok)
	}
	var frames []RawFrame
	playerFrames := 0
	for dec.More() {
		if len(frames) >= maxFrames {
			return nil, fmt.Errorf("%w: more than %d frames; %s", ErrLegacyReplayTooManyFrames, maxFrames, legacyDamagedHint)
		}
		var frame RawFrame
		if err := dec.Decode(&frame); err != nil {
			return nil, fmt.Errorf("frame entry %d: %w", len(frames)+1, err)
		}
		playerFrames += len(frame.Players)
		if playerFrames > maxFrames {
			return nil, fmt.Errorf("%w: more than %d per-player frames by frame entry %d; %s",
				ErrLegacyReplayTooManyFrames, maxFrames, len(frames)+1, legacyDamagedHint)
		}
		frames = append(frames, frame)
	}
	if _, err := dec.Token(); err != nil { // closing bracket
		return nil, err
	}
	return frames, nil
}

// skipJSONValue consumes one complete JSON value without building it.
func skipJSONValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
		if depth <= 0 {
			return nil
		}
	}
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
