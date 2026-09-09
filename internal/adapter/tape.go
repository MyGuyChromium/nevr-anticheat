package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	spatial "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/spatial/v1"
	capture "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"github.com/echotools/tape/v4/pkg/codec"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const TapeSchemaRevision = "638a4669f605.2"
const maxTapePlayers = 64
const maxTapeFrames = 1_000_000
const maxTapeNormalizedFrames = 1_000_000

// This is a conservative observation-continuity bound, not a claimed capture
// rate or detector threshold. The original timing is retained without repair.
const maxTapeContinuityMs = 500

// TapeRawRecord retains the native schema, including unknown protobuf fields.
// It is embedded as _nevr_tape alongside a compatibility session projection.
// The projection is not an original Echo HTTP response and must not be used
// to re-analyze native records. Sparse records must be replayed in order.
type TapeRawRecord struct {
	Version int    `json:"version"`
	Header  []byte `json:"header"`
	Frame   []byte `json:"frame"`
}

// IsTapeRawJSON detects the reserved record marker, including null/invalid
// markers which Decode must reject rather than silently treating as legacy.
func IsTapeRawJSON(raw string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return false
	}
	_, present := fields["_nevr_tape"]
	return present
}

// NativeTapeSampleTime reads timing without replaying sparse player state.
// Useful for clip export: a late player may not exist in the initial roster.
// No timestamp is inferred from an ordinal, receive time or assumed cadence.
func NativeTapeSampleTime(raw string) (time.Time, error) {
	if len(raw) > DefaultMaxLineBytes {
		return time.Time{}, fmt.Errorf("native tape record exceeds size limit")
	}
	var wrapper struct {
		Native *TapeRawRecord `json:"_nevr_tape"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return time.Time{}, err
	}
	if wrapper.Native == nil || wrapper.Native.Version != 1 || len(wrapper.Native.Header) == 0 || len(wrapper.Native.Frame) == 0 {
		return time.Time{}, fmt.Errorf("invalid native tape record")
	}
	h, f := new(capture.CaptureHeader), new(capture.Frame)
	if err := proto.Unmarshal(wrapper.Native.Header, h); err != nil {
		return time.Time{}, err
	}
	if err := proto.Unmarshal(wrapper.Native.Frame, f); err != nil {
		return time.Time{}, err
	}
	if err := NewTapeRawDecoder().initialize(h); err != nil {
		return time.Time{}, err
	}
	if f.GetEchoArena() == nil {
		return time.Time{}, fmt.Errorf("native tape frame has no Echo Arena payload")
	}
	return nativeSampleTime(h, f)
}

func nativeSampleTime(h *capture.CaptureHeader, f *capture.Frame) (time.Time, error) {
	sample := h.CreatedAt.AsTime().Add(time.Duration(f.TimestampOffsetMs) * time.Millisecond)
	if err := timestamppb.New(sample).CheckValid(); err != nil {
		return time.Time{}, fmt.Errorf("native tape frame timestamp exceeds supported range: %w", err)
	}
	return sample, nil
}

// TapeRawDecoder reconstructs a single sparse capture. It is not concurrency
// safe. Missing hand baselines remain unknown, not free; metadata is untrusted.
type TapeRawDecoder struct {
	mapper              *Mapper
	diag                *DiagnosticReport
	header              *capture.CaptureHeader
	headerBytes         []byte
	ctx                 *model.MatchContext
	roster              map[int32]*capture.PlayerInfo
	held                map[int32]*capture.GrabChanged
	stats               map[int32]*capture.PlayerStatsUpdated
	lastScore           *EchoVRLastScore
	previousThrow       *capture.ThrowDetails
	previousIndex       uint32
	previousTime        uint32
	ordinal             int
	epoch               uint64
	maxRecordBytes      int
	totalRecordBytes    int64
	failed              bool
	normalizedFrames    int
	maxNormalizedFrames int
}

func NewTapeRawDecoder() *TapeRawDecoder {
	return &TapeRawDecoder{mapper: NewMapper(), diag: NewDiagnosticReport(), maxRecordBytes: DefaultMaxLineBytes, maxNormalizedFrames: maxTapeNormalizedFrames,
		roster: make(map[int32]*capture.PlayerInfo), held: make(map[int32]*capture.GrabChanged), stats: make(map[int32]*capture.PlayerStatsUpdated)}
}

func (d *TapeRawDecoder) SetPhysics(p model.PhysicsConstants) { d.mapper.SetPhysics(p) }
func (d *TapeRawDecoder) MatchContext() *model.MatchContext   { return d.ctx }

func (d *TapeRawDecoder) Decode(raw string) (tick *ParsedTick, err error) {
	if d.failed {
		return nil, fmt.Errorf("native tape decoder cannot continue after a rejected record")
	}
	defer func() {
		if err != nil {
			d.failed = true
		}
	}()
	if int64(len(raw)) > DefaultMaxReplayBytes-d.totalRecordBytes {
		return nil, fmt.Errorf("native tape stored records exceed decoded byte budget")
	}
	if len(raw) > d.maxRecordBytes {
		return nil, fmt.Errorf("native tape record exceeds %d-byte limit", d.maxRecordBytes)
	}
	var envelope struct {
		Native *TapeRawRecord `json:"_nevr_tape"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, fmt.Errorf("invalid native tape record: %w", err)
	}
	if envelope.Native == nil || envelope.Native.Version != 1 || len(envelope.Native.Header) == 0 || len(envelope.Native.Frame) == 0 {
		return nil, fmt.Errorf("missing or unsupported native tape record")
	}
	if d.header == nil {
		h := new(capture.CaptureHeader)
		if err := proto.Unmarshal(envelope.Native.Header, h); err != nil {
			return nil, fmt.Errorf("invalid native tape header: %w", err)
		}
		if err := d.initialize(h); err != nil {
			return nil, err
		}
		d.headerBytes = bytes.Clone(envelope.Native.Header)
	} else if !bytes.Equal(d.headerBytes, envelope.Native.Header) {
		return nil, fmt.Errorf("native tape header/capture changed within stored replay")
	}
	f := new(capture.Frame)
	if err := proto.Unmarshal(envelope.Native.Frame, f); err != nil {
		return nil, fmt.Errorf("invalid native tape frame: %w", err)
	}
	tick, err = d.mapFrame(f, envelope.Native)
	return tick, err
}

func (d *TapeRawDecoder) initialize(h *capture.CaptureHeader) error {
	if h == nil || h.GetEchoArena() == nil {
		return fmt.Errorf("native tape has no Echo Arena header")
	}
	if h.FormatVersion != 2 {
		return fmt.Errorf("unsupported native tape format version %d; version 2 required", h.FormatVersion)
	}
	if h.FrameEncoding != capture.FrameEncoding_FRAME_ENCODING_UNSPECIFIED && h.FrameEncoding != capture.FrameEncoding_FRAME_ENCODING_SPARSE {
		return fmt.Errorf("unsupported native tape frame encoding %s; sparse captures required", h.FrameEncoding)
	}
	switch strings.ToLower(h.GameType) {
	case "echo_arena", "echo_arena_private", "echo_arena_tournament":
	default:
		return fmt.Errorf("unsupported native tape game type %q; an explicit Echo Arena game_type is required", h.GameType)
	}
	if len(h.DictionarySha256) > 0 {
		return fmt.Errorf("dictionary-compressed native tape is unsupported; export without a compression dictionary")
	}
	if h.CreatedAt == nil || h.CreatedAt.CheckValid() != nil {
		return fmt.Errorf("native tape creation timestamp is missing or invalid")
	}
	if strings.TrimSpace(h.CaptureId) == "" || len(h.CaptureId) > 256 || strings.TrimSpace(h.GetEchoArena().SessionId) == "" || len(h.GetEchoArena().SessionId) > 256 {
		return fmt.Errorf("native tape capture/session identity is missing or oversized")
	}
	if len(h.Producer) > 1024 || len(h.GetEchoArena().InitialRoster) > maxTapePlayers {
		return fmt.Errorf("native tape header exceeds identity limits")
	}
	for _, p := range h.GetEchoArena().InitialRoster {
		if p == nil {
			return fmt.Errorf("nil native tape roster entry")
		}
		if _, ok := d.roster[p.Slot]; ok {
			return fmt.Errorf("duplicate native tape roster slot %d", p.Slot)
		}
		if err := d.putPlayer(p); err != nil {
			return err
		}
	}
	d.header = h
	d.mapper.SetObservationSource("tape", "capture_offset_ms", h.CaptureId)
	d.mapper.haveFirstSample = true
	d.mapper.firstSampleTime = h.CreatedAt.AsTime()
	d.ctx = &model.MatchContext{MatchID: h.GetEchoArena().SessionId, Map: h.GetEchoArena().MapName, GameMode: h.GameType,
		IsPrivate: strings.Contains(strings.ToLower(h.GameType), "private"), StartTime: h.CreatedAt.AsTime(), Source: "tape",
		Physics: d.mapper.physics, PlayerNames: make(map[string]string), TeamAssignments: make(map[string]string),
		NativeCapture: &model.NativeCapture{CaptureID: h.CaptureId, Producer: h.Producer, GameType: h.GameType, FormatVersion: h.FormatVersion,
			FormatMinor: h.FormatMinor, FormatPatch: h.FormatPatch, FrameEncoding: "sparse", SchemaRevision: TapeSchemaRevision, ContainerIntegrity: "not_verified_from_records",
			Limitations: []string{"Recorder-reported telemetry is not authenticated engine authority.", "Frame time is a millisecond capture offset, not an exact physics tick.", "Zero-valued non-optional scalar fields cannot prove observation presence.", "Sparse hand state is unknown until an explicit grab baseline; gaps invalidate carried state.", "Throw/catch sensor events are observations, not authoritative contact decisions."}}}
	return nil
}

func (d *TapeRawDecoder) putPlayer(p *capture.PlayerInfo) error {
	if p.Slot < 0 || p.Slot >= maxTapePlayers || p.AccountNumber == 0 || p.AccountNumber > math.MaxInt64 || len(p.DisplayName) > 512 {
		return fmt.Errorf("invalid native tape player identity at slot %d", p.GetSlot())
	}
	for slot, other := range d.roster {
		if slot != p.Slot && other.AccountNumber == p.AccountNumber {
			return fmt.Errorf("native tape account occupies multiple slots")
		}
	}
	if old := d.roster[p.Slot]; old == nil || old.AccountNumber != p.AccountNumber {
		delete(d.held, p.Slot)
		delete(d.stats, p.Slot)
	}
	d.roster[p.Slot] = proto.Clone(p).(*capture.PlayerInfo)
	return nil
}

func (d *TapeRawDecoder) applyEvents(ea *capture.EchoArenaFrame) error {
	if len(ea.Events) > 4096 {
		return fmt.Errorf("native tape frame has too many events")
	}
	for _, event := range ea.Events {
		if event == nil {
			return fmt.Errorf("nil native tape event")
		}
		switch e := event.Event.(type) {
		case *capture.EchoEvent_PlayerJoined:
			p := e.PlayerJoined
			if p == nil {
				return fmt.Errorf("nil native tape join event")
			}
			if err := d.putPlayer(&capture.PlayerInfo{Slot: p.Slot, AccountNumber: p.AccountNumber, DisplayName: p.DisplayName, Role: p.Role, JerseyNumber: p.JerseyNumber, Level: p.Level}); err != nil {
				return err
			}
		case *capture.EchoEvent_PlayerLeft:
			slot := e.PlayerLeft.GetPlayerSlot()
			delete(d.roster, slot)
			delete(d.held, slot)
			delete(d.stats, slot)
		case *capture.EchoEvent_PlayerSwitchedTeam:
			if p := d.roster[e.PlayerSwitchedTeam.GetPlayerSlot()]; p != nil {
				p.Role = e.PlayerSwitchedTeam.GetNewRole()
			}
		case *capture.EchoEvent_PlayerInfoUpdated:
			if p := d.roster[e.PlayerInfoUpdated.GetPlayerSlot()]; p != nil {
				p.Level = e.PlayerInfoUpdated.GetLevel()
				p.JerseyNumber = e.PlayerInfoUpdated.GetJerseyNumber()
			}
		case *capture.EchoEvent_GrabChanged:
			g := e.GrabChanged
			if g == nil || d.roster[g.PlayerSlot] == nil || len(g.LeftHolding) > 64 || len(g.RightHolding) > 64 {
				return fmt.Errorf("invalid native tape grab event")
			}
			d.held[g.PlayerSlot] = proto.Clone(g).(*capture.GrabChanged)
		case *capture.EchoEvent_PlayerStatsUpdated:
			if s := e.PlayerStatsUpdated; s != nil && d.roster[s.PlayerSlot] != nil {
				d.stats[s.PlayerSlot] = proto.Clone(s).(*capture.PlayerStatsUpdated)
			}
		case *capture.EchoEvent_GoalScored:
			if g := e.GoalScored; g != nil {
				team := ""
				switch g.Team {
				case capture.Role_ROLE_BLUE_TEAM:
					team = "blue"
				case capture.Role_ROLE_ORANGE_TEAM:
					team = "orange"
				}
				d.lastScore = &EchoVRLastScore{DiscSpeed: float64(g.DiscSpeed), Team: team, GoalType: strings.ReplaceAll(strings.TrimPrefix(g.GoalType.String(), "GOAL_TYPE_"), "_", " "), PointAmount: int(g.PointAmount), DistanceThrown: float64(g.DistanceThrown), PersonScored: g.PersonScored, AssistScored: g.AssistScored}
			}
		}
	}
	return nil
}

func (d *TapeRawDecoder) mapFrame(f *capture.Frame, native *TapeRawRecord) (*ParsedTick, error) {
	if d.ordinal >= maxTapeFrames {
		return nil, fmt.Errorf("native tape exceeds %d-frame limit", maxTapeFrames)
	}
	ea := f.GetEchoArena()
	if ea == nil || len(ea.Players) > maxTapePlayers {
		return nil, fmt.Errorf("native tape frame missing Echo Arena payload or exceeding player limit")
	}
	gap := false
	if d.ordinal > 0 {
		if f.FrameIndex <= d.previousIndex || f.TimestampOffsetMs <= d.previousTime {
			return nil, fmt.Errorf("native tape has duplicate/backward frame index or capture timestamp")
		}
		gap = f.FrameIndex != d.previousIndex+1 || f.TimestampOffsetMs-d.previousTime > maxTapeContinuityMs
	}
	var previousHeld map[int32]*capture.GrabChanged
	previousAccounts := make(map[int32]uint64, len(d.roster))
	for slot, p := range d.roster {
		previousAccounts[slot] = p.AccountNumber
	}
	if gap {
		d.epoch++
		d.held = make(map[int32]*capture.GrabChanged)
		d.previousThrow = nil
		clear(d.mapper.prevTimestamp)
		previousHeld = nil
	} else {
		// Event replacement must not mutate the pre-frame transition baseline.
		copyHeld := make(map[int32]*capture.GrabChanged, len(d.held))
		for k, v := range d.held {
			copyHeld[k] = v
		}
		previousHeld = copyHeld
	}
	if err := d.applyEvents(ea); err != nil {
		return nil, err
	}
	for slot, account := range previousAccounts {
		if current := d.roster[slot]; current == nil || current.AccountNumber != account {
			delete(previousHeld, slot)
			d.previousThrow = nil
		}
	}
	session, nativePlayers, err := d.session(ea)
	if err != nil {
		return nil, err
	}
	sample, err := nativeSampleTime(d.header, f)
	if err != nil {
		return nil, err
	}
	d.mapper.frameIndex = d.ordinal
	result := d.mapper.MapSessionAt(session, sample)
	if len(result.Frames) > d.maxNormalizedFrames-d.normalizedFrames {
		return nil, fmt.Errorf("native tape exceeds %d normalized player-frame limit", d.maxNormalizedFrames)
	}
	MergeMatchContext(d.ctx, result.MatchCtx)
	d.ctx.Source = "tape"
	d.ctx.StartTime = d.header.CreatedAt.AsTime()
	d.ctx.Duration = time.Duration(f.TimestampOffsetMs) * time.Millisecond
	clientID := uniqueClientPlayer(session)
	localThrow := d.localThrow(ea, previousHeld, clientID, gap)
	if localThrow != nil {
		session.LastThrow = (*EchoVRLastThrow)(localThrow)
	}
	for i := range result.Frames {
		p := &result.Frames[i]
		np := nativePlayers[p.PlayerID]
		p.Rotation, _ = nativeRotation(np.GetBody())
		p.LeftHandRotation, *p.LeftHandRotationValid = nativeRotation(np.GetLeftHand())
		p.RightHandRotation, *p.RightHandRotationValid = nativeRotation(np.GetRightHand())
		p.HasScore = ea.BluePoints != 0 || ea.OrangePoints != 0
		p.Observation.Source = "tape"
		p.Observation.TimeBasis = "capture_offset_ms"
		p.Observation.SourceEpoch = d.epoch
		p.Observation.SourceID = d.header.CaptureId
		p.GameLastThrow = nil
		p.GameLastThrowProvenance = nil
		if p.PlayerID == clientID && localThrow != nil {
			p.GameLastThrow = localThrow
			p.GameLastThrowProvenance = p.Observation.Clone()
			p.GameLastThrowProvenance.Freshness = "value_change"
		}
	}
	projection, err := tapeProjection(session, native)
	if err != nil {
		return nil, err
	}
	if len(projection) > d.maxRecordBytes {
		return nil, fmt.Errorf("native tape record projection exceeds %d-byte limit", d.maxRecordBytes)
	}
	if int64(len(projection)) > DefaultMaxReplayBytes-d.totalRecordBytes {
		return nil, fmt.Errorf("native tape retained records exceed decoded-byte budget")
	}
	d.diag.RecordSessionWithJSON(session, projection)
	d.diag.RecordMappingResult(result)
	d.diag.RecordMapperStats(d.mapper.Stats())
	tick := &ParsedTick{MatchID: d.ctx.MatchID, MatchCtx: d.ctx, NewMatch: d.ordinal == 0, FrameIndex: d.ordinal, SampleTime: sample, Frames: result.Frames, RawJSON: string(projection), Session: session}
	d.previousIndex = f.FrameIndex
	d.previousTime = f.TimestampOffsetMs
	d.ordinal++
	d.normalizedFrames += len(result.Frames)
	d.totalRecordBytes += int64(len(projection))
	return tick, nil
}

func (d *TapeRawDecoder) session(ea *capture.EchoArenaFrame) (*EchoVRSessionResponse, map[string]*capture.PlayerState, error) {
	s := &EchoVRSessionResponse{SessionID: d.ctx.MatchID, MapName: d.ctx.Map, MatchType: d.ctx.GameMode, ClientName: d.header.GetEchoArena().ClientName,
		PrivateMatch: d.ctx.IsPrivate, GameStatus: nativeGameStatus(ea.GameStatus), GameClock: float64(ea.GameClock), BluePoints: int(ea.BluePoints), OrangePoints: int(ea.OrangePoints),
		LastScore: d.lastScore,
		Teams:     []EchoVRTeam{{TeamName: "BLUE TEAM"}, {TeamName: "ORANGE TEAM"}, {TeamName: "SPECTATORS"}}}
	natives := make(map[string]*capture.PlayerState)
	seen := make(map[int32]bool)
	for _, np := range ea.Players {
		if np == nil || seen[np.Slot] {
			return nil, nil, fmt.Errorf("native tape frame contains nil/duplicate player slot")
		}
		seen[np.Slot] = true
		info := d.roster[np.Slot]
		if info == nil {
			return nil, nil, fmt.Errorf("native tape player slot %d has no roster identity", np.Slot)
		}
		// Repeat the identity bound at the signed adapter conversion boundary.
		// Do not depend solely on validation when sparse roster state was added.
		if info.AccountNumber == 0 || info.AccountNumber > math.MaxInt64 {
			return nil, nil, fmt.Errorf("native tape account exceeds supported identity range")
		}
		team := 2
		switch info.Role {
		case capture.Role_ROLE_BLUE_TEAM:
			team = 0
		case capture.Role_ROLE_ORANGE_TEAM:
			team = 1
		}
		p := EchoVRPlayer{Name: info.DisplayName, UserID: int64(info.AccountNumber), PlayerID: int(np.Slot), Level: int(info.Level), Body: nativeBody(np.Body), Head: nativeBody(np.Head),
			LHand: nativeHand(np.LeftHand), RHand: nativeHand(np.RightHand), Stunned: np.Flags&1 != 0, Invulnerable: np.Flags&2 != 0, Blocking: np.Flags&4 != 0, Ping: int(np.Ping)}
		velocity := nativeVector(np.Velocity)
		known := velocity != nil
		p.velocityObserved = &known
		if known {
			p.Velocity = *velocity
		}
		if g := d.held[np.Slot]; g != nil {
			p.HoldingLeft = g.LeftHolding
			p.HoldingRight = g.RightHolding
		}
		// Never use the stale possession bit or disc-holder sensor as a fallback.
		p.Possession = p.HoldsDisc()
		if stats := d.stats[np.Slot]; stats != nil {
			p.Stats = EchoVRPlayerStats{Points: int(stats.Points), Goals: int(stats.Goals), Stuns: int(stats.Stuns), Saves: int(stats.Saves), Assists: int(stats.Assists), Steals: int(stats.Steals), Passes: int(stats.Passes), Catches: int(stats.Catches), Blocks: int(stats.Blocks), Interceptions: int(stats.Interceptions), ShotsOnGoal: int(stats.ShotsTaken)}
		}
		p.Stats.Possession = float64(np.PossessionTime)
		s.Teams[team].Players = append(s.Teams[team].Players, p)
		natives[playerID(p)] = np
	}
	if disc := ea.Disc; disc != nil {
		pos, vel := nativeVector(disc.GetPose().GetPosition()), nativeVector(disc.Velocity)
		pk, vk := pos != nil, vel != nil
		s.Disc = &EchoVRDisc{positionObserved: &pk, velocityObserved: &vk}
		if pk {
			s.Disc.Position = *pos
		}
		if vk {
			s.Disc.Velocity = *vel
		}
		if disc.BounceCount > 0 {
			n := int(disc.BounceCount)
			s.Disc.BounceCount = &n
		}
	}
	return s, natives, nil
}

func (d *TapeRawDecoder) localThrow(ea *capture.EchoArenaFrame, previous map[int32]*capture.GrabChanged, client string, gap bool) *model.GameThrowDetails {
	if client == "" {
		return nil
	}
	var out *model.GameThrowDetails
	for _, evt := range ea.Events {
		e := evt.GetDiscThrown()
		if e == nil || e.ThrowDetails == nil {
			continue
		}
		p := d.roster[e.PlayerSlot]
		if p == nil || "echovr:"+strconv.FormatUint(p.AccountNumber, 10) != client {
			continue
		}
		changed := !proto.Equal(d.previousThrow, e.ThrowDetails)
		d.previousThrow = proto.Clone(e.ThrowDetails).(*capture.ThrowDetails)
		old, now := previous[e.PlayerSlot], d.held[e.PlayerSlot]
		if !changed || gap || d.ordinal == 0 || old == nil || now == nil || (old.LeftHolding != "disc" && old.RightHolding != "disc") || now.LeftHolding == "disc" || now.RightHolding == "disc" || !heldItemKnown(now.LeftHolding) || !heldItemKnown(now.RightHolding) {
			continue
		}
		t := e.ThrowDetails
		v := &model.GameThrowDetails{ArmSpeed: float64(t.ArmSpeed), TotalSpeed: float64(t.TotalSpeed), OffAxisSpinDeg: float64(t.OffAxisSpinDeg), WristThrowPenalty: float64(t.WristThrowPenalty), RotPerSec: float64(t.RotPerSec), PotentialSpeedFromRot: float64(t.PotSpeedFromRot), SpeedFromArm: float64(t.SpeedFromArm), SpeedFromMovement: float64(t.SpeedFromMovement), SpeedFromWrist: float64(t.SpeedFromWrist), WristAlignToThrowDeg: float64(t.WristAlignToThrowDeg), ThrowAlignToMovementDeg: float64(t.ThrowAlignToMovementDeg), OffAxisPenalty: float64(t.OffAxisPenalty), ThrowMovePenalty: float64(t.ThrowMovePenalty)}
		if v.Valid() {
			out = v
		}
	}
	return out
}

func nativeVector(v *spatial.Vec3) *[3]float64 {
	if v == nil {
		return nil
	}
	out := [3]float64{float64(v.X), float64(v.Y), float64(v.Z)}
	for _, x := range out {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
	}
	return &out
}
func nativeRotation(p *spatial.Pose) (model.Quat, bool) {
	if p == nil || p.Orientation == nil {
		return model.Quat{}, false
	}
	q := model.Quat{float64(p.Orientation.X), float64(p.Orientation.Y), float64(p.Orientation.Z), float64(p.Orientation.W)}
	if !q.IsUnit() {
		return model.Quat{}, false
	}
	return q.Normalize(), true
}
func nativeBody(p *spatial.Pose) EchoVRBodyHead {
	b := EchoVRBodyHead{}
	if v := nativeVector(p.GetPosition()); v != nil {
		b.Position = *v
	}
	if q, ok := nativeRotation(p); ok {
		q = q.Normalize()
		b.Forward = q.Rotate(model.Vec3{0, 0, 1})
		b.Left = q.Rotate(model.Vec3{1, 0, 0})
		b.Up = q.Rotate(model.Vec3{0, 1, 0})
	}
	return b
}
func nativeHand(p *spatial.Pose) EchoVRHand {
	return EchoVRHand(nativeBody(p))
}
func nativeGameStatus(s capture.GameStatus) string {
	if s == capture.GameStatus_GAME_STATUS_UNSPECIFIED {
		return "unknown"
	}
	return strings.ToLower(strings.TrimPrefix(s.String(), "GAME_STATUS_"))
}

func tapeProjection(s *EchoVRSessionResponse, native *TapeRawRecord) ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("invalid native tape values: %w", err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	fields["_nevr_tape"], err = json.Marshal(native)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func (p *EchoReplayParser) parseTapeFile(path string, fn func(*ParsedTick) error) (*model.MatchContext, *DiagnosticReport, error) {
	d := NewTapeRawDecoder()
	defer func() { p.mapper.stats = d.mapper.stats }()
	d.SetPhysics(p.mapper.physics)
	d.maxRecordBytes = p.maxLineBytes
	info, err := os.Stat(path)
	if err != nil {
		return nil, d.diag, err
	}
	if !info.Mode().IsRegular() || info.Size() > p.maxReplayBytes {
		return nil, d.diag, fmt.Errorf("native tape file exceeds size/type limits")
	}
	compressed, err := checkTapeContainer(path)
	if err != nil {
		return nil, d.diag, err
	}
	r, err := codec.NewReader(path, codec.WithMaxDecodedBytes(p.maxReplayBytes), codec.WithMaxFrameCount(maxTapeFrames))
	if err != nil {
		return nil, d.diag, fmt.Errorf("opening native tape: %w", err)
	}
	defer r.Close()
	h, err := r.ReadHeader()
	if err != nil {
		return nil, d.diag, fmt.Errorf("reading native tape header: %w", err)
	}
	if err = d.initialize(h); err != nil {
		return nil, d.diag, err
	}
	// Header metadata and generic-event payloads contain protobuf maps. Stable
	// encoding makes identical file reimports compare as the same retained
	// evidence; it is not a cross-version canonical protobuf representation.
	marshal := proto.MarshalOptions{Deterministic: true}
	d.headerBytes, err = marshal.Marshal(h)
	if err != nil {
		return nil, d.diag, err
	}
	d.ctx.ReplayFile = filepath.Base(path)
	p.matches = append(p.matches, d.ctx)
	for {
		f, err := r.ReadFrame()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return d.ctx, d.diag, fmt.Errorf("reading native tape: %w", err)
		}
		if proto.Size(f) > p.maxLineBytes || len(d.headerBytes) > p.maxLineBytes {
			return d.ctx, d.diag, fmt.Errorf("native tape protobuf record exceeds size limit")
		}
		b, err := marshal.Marshal(f)
		if err != nil {
			return d.ctx, d.diag, err
		}
		tick, err := d.mapFrame(f, &TapeRawRecord{Version: 1, Header: d.headerBytes, Frame: b})
		if err != nil {
			return d.ctx, d.diag, err
		}
		if err = fn(tick); err != nil {
			return d.ctx, d.diag, err
		}
	}
	if r.SkippedEnvelopes() > 0 {
		return d.ctx, d.diag, fmt.Errorf("native tape contains unsupported envelope variants")
	}
	if d.ordinal == 0 {
		return d.ctx, d.diag, fmt.Errorf("native tape contains no frames")
	}
	d.ctx.NativeCapture.ContainerIntegrity = "verified_footer_only_uncompressed"
	if compressed {
		d.ctx.NativeCapture.ContainerIntegrity = "verified_footer_and_checksum"
	}
	return d.ctx, d.diag, nil
}
