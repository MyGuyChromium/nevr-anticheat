package adapter

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	spatial "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/spatial/v1"
	capture "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"github.com/echotools/tape/v4/pkg/codec"
	"github.com/klauspost/compress/zstd"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func tapeTestHeader() *capture.CaptureHeader {
	return &capture.CaptureHeader{CaptureId: testutil.NativeTapeCaptureID, CreatedAt: timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)),
		FormatVersion: 2, FrameEncoding: capture.FrameEncoding_FRAME_ENCODING_SPARSE, GameType: "echo_arena", Producer: "synthetic-tests/untrusted",
		GameHeader: &capture.CaptureHeader_EchoArena{EchoArena: &capture.EchoArenaHeader{SessionId: testutil.NativeTapeMatchID, MapName: "mpl_arena_a", ClientName: "Local",
			InitialRoster: []*capture.PlayerInfo{{Slot: 1, AccountNumber: 1001, DisplayName: "Local", Role: capture.Role_ROLE_BLUE_TEAM}}}}}
}
func tapeTestPose(x float32) *spatial.Pose {
	return &spatial.Pose{Position: &spatial.Vec3{X: x, Y: 2, Z: 3}, Orientation: &spatial.Quat{W: 1}}
}
func tapeTestFrame(index uint32, events ...*capture.EchoEvent) *capture.Frame {
	return &capture.Frame{FrameIndex: index, TimestampOffsetMs: index * 33, Payload: &capture.Frame_EchoArena{EchoArena: &capture.EchoArenaFrame{GameStatus: capture.GameStatus_GAME_STATUS_PLAYING,
		Disc:    &capture.DiscState{Pose: tapeTestPose(4), Velocity: &spatial.Vec3{X: 1}},
		Players: []*capture.PlayerState{{Slot: 1, Body: tapeTestPose(1), Head: tapeTestPose(1), LeftHand: tapeTestPose(.7), RightHand: tapeTestPose(1.3), Velocity: &spatial.Vec3{}}}, Events: events}}}
}
func tapeTestGrab(slot int32, left, right string) *capture.EchoEvent {
	return &capture.EchoEvent{Event: &capture.EchoEvent_GrabChanged{GrabChanged: &capture.GrabChanged{PlayerSlot: slot, LeftHolding: left, RightHolding: right}}}
}
func tapeTestThrow(slot int32, speed float32) *capture.EchoEvent {
	return &capture.EchoEvent{Event: &capture.EchoEvent_DiscThrown{DiscThrown: &capture.DiscThrown{PlayerSlot: slot, ThrowDetails: &capture.ThrowDetails{
		ArmSpeed: 1, TotalSpeed: speed, OffAxisSpinDeg: 3, WristThrowPenalty: 4, RotPerSec: 5, PotSpeedFromRot: 6, SpeedFromArm: 7, SpeedFromMovement: 8, SpeedFromWrist: 9, WristAlignToThrowDeg: 10, ThrowAlignToMovementDeg: 11, OffAxisPenalty: 12, ThrowMovePenalty: 13}}}}
}
func tapeTestRaw(t *testing.T, h *capture.CaptureHeader, f *capture.Frame) string {
	t.Helper()
	hb, err := proto.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := proto.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"_nevr_tape": TapeRawRecord{Version: 1, Header: hb, Frame: fb}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func tapeTestWrite(t *testing.T, h *capture.CaptureHeader, frames ...*capture.Frame) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.tape")
	w, err := codec.NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	for _, f := range frames {
		if err = w.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTapeActualCodecImportAndRawReanalysis(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic.tape")
	if err := testutil.WriteNativeTapeFixture(path); err != nil {
		t.Fatal(err)
	}
	p := NewEchoReplayParser()
	var ticks []*ParsedTick
	mc, diag, err := p.ParseFileStream(path, func(tick *ParsedTick) error { ticks = append(ticks, tick); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if mc.MatchID != testutil.NativeTapeMatchID || mc.Source != "tape" || mc.NativeCapture == nil || mc.NativeCapture.ContainerIntegrity != "verified_footer_and_checksum" {
		t.Fatalf("wrong native metadata: %+v", mc)
	}
	if len(ticks) != testutil.NativeTapeFrames || diag.FramesMapped != testutil.NativeTapeFrames {
		t.Fatalf("ticks=%d mapped=%d", len(ticks), diag.FramesMapped)
	}
	replay := NewTapeRawDecoder()
	for i, tick := range ticks {
		if tick.FrameIndex != i || len(tick.Frames) != 1 || tick.Frames[0].PlayerID != testutil.NativeTapePlayerID {
			t.Fatalf("tick %d invalid: %+v", i, tick)
		}
		f := tick.Frames[0]
		if f.Observation.Source != "tape" || f.Observation.Authority != "client_reported" || f.Observation.TimeBasis != "capture_offset_ms" || f.Disc.Attachment.State != "free" {
			t.Fatalf("unsafe source/attachment: %+v", f)
		}
		if f.HasScore || f.Disc.BounceCount != nil {
			t.Fatal("zero nonoptional scalars manufactured known observations")
		}
		again, err := replay.Decode(tick.RawJSON)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(tick.Frames, again.Frames) || !tick.SampleTime.Equal(again.SampleTime) {
			t.Fatalf("raw native reanalysis changed frame %d", i)
		}
	}
	if replay.MatchContext().NativeCapture.ContainerIntegrity != "not_verified_from_records" {
		t.Fatal("stored JSON claimed compressor verification")
	}
	clone := mc.NativeCapture.Clone()
	clone.Limitations[0] = "changed"
	if mc.NativeCapture.Limitations[0] == "changed" {
		t.Fatal("metadata clone aliases limitations")
	}
}

func TestTapeProjectionDiagnosticsDoNotClaimOriginalFieldPresence(t *testing.T) {
	d := NewTapeRawDecoder()
	tick, err := d.Decode(tapeTestRaw(t, tapeTestHeader(), tapeTestFrame(0)))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(tick.RawJSON), &fields); err != nil {
		t.Fatal(err)
	}
	fields["future_unrelated_key"] = json.RawMessage(`true`)
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	diag := NewDiagnosticReport()
	if _, err := diag.RecordSessionJSON(data); err != nil {
		t.Fatal(err)
	}
	if diag.NativeProjectionSnapshots != 1 || diag.PresenceTracked || diag.UnknownFields["top._nevr_tape"] != 0 || diag.UnknownFields["top.future_unrelated_key"] != 1 {
		t.Fatalf("incorrect native projection classification: %+v", diag)
	}
	var note bool
	for _, warning := range diag.HealthWarnings() {
		if warning.Code == "native_projection" {
			note = true
		}
	}
	if !note {
		t.Fatal("native projection limitations were omitted")
	}
	for _, invalid := range []string{`null`, `{"version":1,"header":"AA==","frame":"AA=="}`, `{"version":2,"header":"AQ==","frame":"AQ=="}`} {
		fields["_nevr_tape"] = json.RawMessage(invalid)
		bad, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		badDiag := NewDiagnosticReport()
		if _, err := badDiag.RecordSessionJSON(bad); err != nil {
			t.Fatal(err)
		}
		if badDiag.NativeProjectionSnapshots != 0 || badDiag.UnknownFields["top._nevr_tape"] != 1 {
			t.Fatal("malformed native wrapper was trusted by schema diagnostics")
		}
	}
}

func TestTapeSignedAccountConversionRejectsOutOfRangeRoster(t *testing.T) {
	d := NewTapeRawDecoder()
	if err := d.initialize(tapeTestHeader()); err != nil {
		t.Fatal(err)
	}
	// Exercise the conversion boundary even if earlier roster validation were
	// accidentally bypassed by a future sparse update implementation.
	d.roster[1].AccountNumber = uint64(1) << 63
	if _, _, err := d.session(tapeTestFrame(0).GetEchoArena()); err == nil {
		t.Fatal("out-of-range account became a negative signed player identity")
	}
	d.roster[1].AccountNumber = (uint64(1) << 63) - 1
	s, _, err := d.session(tapeTestFrame(0).GetEchoArena())
	if err != nil || s.Teams[0].Players[0].UserID != 9223372036854775807 {
		t.Fatalf("valid signed account upper boundary was changed: %v", err)
	}
}

func TestTapeRejectsMalformedAndUnsupportedCaptures(t *testing.T) {
	cases := map[string]func(*capture.CaptureHeader){
		"future version":      func(h *capture.CaptureHeader) { h.FormatVersion = 3 },
		"legacy unknown type": func(h *capture.CaptureHeader) { h.GameType = "" },
		"combat":              func(h *capture.CaptureHeader) { h.GameType = "echo_combat" },
		"dense":               func(h *capture.CaptureHeader) { h.FrameEncoding = capture.FrameEncoding_FRAME_ENCODING_DENSE },
		"dictionary":          func(h *capture.CaptureHeader) { h.DictionarySha256 = []byte{1} },
		"timestamp":           func(h *capture.CaptureHeader) { h.CreatedAt = nil },
		"capture identity":    func(h *capture.CaptureHeader) { h.CaptureId = "" },
		"session identity":    func(h *capture.CaptureHeader) { h.GetEchoArena().SessionId = "" },
		"duplicate slot": func(h *capture.CaptureHeader) {
			h.GetEchoArena().InitialRoster = append(h.GetEchoArena().InitialRoster, proto.Clone(h.GetEchoArena().InitialRoster[0]).(*capture.PlayerInfo))
		},
		"zero account": func(h *capture.CaptureHeader) { h.GetEchoArena().InitialRoster[0].AccountNumber = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := tapeTestHeader()
			mutate(h)
			if _, err := NewTapeRawDecoder().Decode(tapeTestRaw(t, h, tapeTestFrame(0))); err == nil {
				t.Fatal("unsupported header accepted")
			}
		})
	}
	for _, raw := range []string{`{}`, `{"_nevr_tape":null}`, `{"_nevr_tape":{"version":7,"header":"AQ==","frame":"AQ=="}}`, `{"_nevr_tape":{"version":1,"header":"!","frame":"AQ=="}}`} {
		if _, err := NewTapeRawDecoder().Decode(raw); err == nil {
			t.Fatalf("malformed wrapper accepted: %s", raw)
		}
	}
	d := NewTapeRawDecoder()
	d.maxRecordBytes = 32
	if _, err := d.Decode(tapeTestRaw(t, tapeTestHeader(), tapeTestFrame(0))); err == nil {
		t.Fatal("record size limit ignored")
	}
}

func TestTapeOrderGapsAndCaptureBoundaries(t *testing.T) {
	h := tapeTestHeader()
	for _, kind := range []string{"duplicate index", "backward index", "duplicate time", "backward time", "changed header", "duplicate player", "unknown player", "missing payload"} {
		t.Run(kind, func(t *testing.T) {
			d := NewTapeRawDecoder()
			first := tapeTestFrame(5, tapeTestGrab(1, "disc", "disc"))
			if _, err := d.Decode(tapeTestRaw(t, h, first)); err != nil {
				t.Fatal(err)
			}
			second := tapeTestFrame(6)
			hh := proto.Clone(h).(*capture.CaptureHeader)
			switch kind {
			case "duplicate index":
				second.FrameIndex = 5
			case "backward index":
				second.FrameIndex = 4
			case "duplicate time":
				second.TimestampOffsetMs = 165
			case "backward time":
				second.TimestampOffsetMs = 164
			case "changed header":
				hh.CaptureId = "another"
			case "duplicate player":
				second.GetEchoArena().Players = append(second.GetEchoArena().Players, second.GetEchoArena().Players[0])
			case "unknown player":
				second.GetEchoArena().Players[0].Slot = 2
			case "missing payload":
				second.Payload = nil
			}
			if _, err := d.Decode(tapeTestRaw(t, hh, second)); err == nil {
				t.Fatal("invalid sequence accepted")
			}
			if _, err := d.Decode(tapeTestRaw(t, h, tapeTestFrame(7))); err == nil {
				t.Fatal("failed decoder resumed without new source boundary")
			}
		})
	}
	d := NewTapeRawDecoder()
	if _, err := d.Decode(tapeTestRaw(t, h, tapeTestFrame(0, tapeTestGrab(1, "disc", "disc")))); err != nil {
		t.Fatal(err)
	}
	tick, err := d.Decode(tapeTestRaw(t, h, tapeTestFrame(2)))
	if err != nil {
		t.Fatal(err)
	}
	f := tick.Frames[0]
	if tick.FrameIndex != 1 || f.Observation.SourceEpoch != 1 || f.DeltaTime != 0 || f.Disc.Attachment.State != "unknown" || f.HasPossession {
		t.Fatalf("gap reused stale observations: %+v", f)
	}
}

func TestTapeSlotReuseDoesNotInheritGrabState(t *testing.T) {
	h := tapeTestHeader()
	d := NewTapeRawDecoder()
	if _, err := d.Decode(tapeTestRaw(t, h, tapeTestFrame(0, tapeTestGrab(1, "disc", "disc")))); err != nil {
		t.Fatal(err)
	}
	join := &capture.EchoEvent{Event: &capture.EchoEvent_PlayerJoined{PlayerJoined: &capture.PlayerJoined{Slot: 1, AccountNumber: 2002, DisplayName: "Replacement", Role: capture.Role_ROLE_ORANGE_TEAM}}}
	f := tapeTestFrame(1, join)
	f.GetEchoArena().Players[0].Flags = 8
	tick, err := d.Decode(tapeTestRaw(t, h, f))
	if err != nil {
		t.Fatal(err)
	}
	p := tick.Frames[0]
	if p.PlayerID != "echovr:2002" || p.Team != "orange" || p.HasPossession || p.Disc.Attachment.State != "unknown" || p.HeldItems.Left != nil {
		t.Fatalf("replacement inherited identity/grab: %+v", p)
	}
}

func TestTapeLocalThrowNeedsObservedReleaseAndSourceBinding(t *testing.T) {
	h := tapeTestHeader()
	d := NewTapeRawDecoder()
	baseline, err := d.Decode(tapeTestRaw(t, h, tapeTestFrame(0, tapeTestGrab(1, "disc", "disc"), tapeTestThrow(1, 15))))
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Frames[0].GameLastThrow != nil {
		t.Fatal("first sensor baseline treated as a throw")
	}
	released, err := d.Decode(tapeTestRaw(t, h, tapeTestFrame(1, tapeTestGrab(1, "none", "none"), tapeTestThrow(1, 18))))
	if err != nil {
		t.Fatal(err)
	}
	f := released.Frames[0]
	if f.GameLastThrow == nil || f.GameLastThrow.TotalSpeed != 18 || f.GameLastThrow.ThrowMovePenalty != 13 || !f.GameLastThrowProvenance.BoundLocalThrow(f.PlayerID, f.FrameIndex, f.Timestamp) {
		t.Fatalf("bound local components lost: %+v", f)
	}
	stale, err := d.Decode(tapeTestRaw(t, h, tapeTestFrame(2, tapeTestThrow(1, 18))))
	if err != nil {
		t.Fatal(err)
	}
	if stale.Frames[0].GameLastThrow != nil {
		t.Fatal("stale sensor record emitted again")
	}
	for _, kind := range []string{"remote client", "no held baseline", "no explicit release", "gap"} {
		t.Run(kind, func(t *testing.T) {
			h := tapeTestHeader()
			if kind == "remote client" {
				h.GetEchoArena().ClientName = "Spectator"
			}
			d := NewTapeRawDecoder()
			first := tapeTestFrame(0, tapeTestGrab(1, "disc", "disc"))
			if kind == "no held baseline" {
				first.GetEchoArena().Events = nil
			}
			if _, err := d.Decode(tapeTestRaw(t, h, first)); err != nil {
				t.Fatal(err)
			}
			second := tapeTestFrame(1, tapeTestGrab(1, "none", "none"), tapeTestThrow(1, 18))
			if kind == "no explicit release" {
				second.GetEchoArena().Events = []*capture.EchoEvent{tapeTestThrow(1, 18)}
			}
			if kind == "gap" {
				second.FrameIndex = 3
				second.TimestampOffsetMs = 99
			}
			tick, err := d.Decode(tapeTestRaw(t, h, second))
			if err != nil {
				t.Fatal(err)
			}
			if tick.Frames[0].GameLastThrow != nil {
				t.Fatal("unbound sensor throw became local evidence")
			}
		})
	}
}

func TestTapeMissingTrackingAndEmptyTicksRemainUnknown(t *testing.T) {
	h := tapeTestHeader()
	d := NewTapeRawDecoder()
	empty := tapeTestFrame(0, tapeTestGrab(1, "disc", "disc"))
	empty.GetEchoArena().Players = nil
	tick, err := d.Decode(tapeTestRaw(t, h, empty))
	if err != nil {
		t.Fatal(err)
	}
	if len(tick.Frames) != 0 || tick.RawJSON == "" || !tick.NewMatch {
		t.Fatal("empty sparse-event tick was lost")
	}
	f := tapeTestFrame(1)
	np := f.GetEchoArena().Players[0]
	np.LeftHand.Orientation = nil
	np.RightHand.Orientation = &spatial.Quat{W: 2}
	np.Velocity = nil
	f.GetEchoArena().Disc.Velocity = nil
	tick, err = d.Decode(tapeTestRaw(t, h, f))
	if err != nil {
		t.Fatal(err)
	}
	p := tick.Frames[0]
	if p.Disc != nil || p.ReportedVelocity != nil || *p.LeftHandRotationValid || *p.RightHandRotationValid {
		t.Fatalf("missing tracking manufactured observations: %+v", p)
	}
	if p.HeldItems.Left == nil || *p.HeldItems.Left != "disc" {
		t.Fatal("empty tick's sparse event was not reconstructed")
	}
}

func TestTapeTruncationCorruptionAndByteLimits(t *testing.T) {
	path := tapeTestWrite(t, tapeTestHeader(), tapeTestFrame(0), tapeTestFrame(1))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{0, 1, len(b) / 2, len(b) - 30, len(b) - 1} {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "cut.tape")
			if err := os.WriteFile(file, b[:cut], 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := NewEchoReplayParser().ParseFileStream(file, func(*ParsedTick) error { return nil }); err == nil {
				t.Fatal("truncated capture reported success")
			}
		})
	}
	p := NewEchoReplayParser()
	p.SetMaxReplayBytes(16)
	if _, _, err := p.ParseFileStream(path, func(*ParsedTick) error { return nil }); err == nil {
		t.Fatal("file size limit ignored")
	}
	p = NewEchoReplayParser()
	p.SetMaxLineBytes(32)
	if _, _, err := p.ParseFileStream(path, func(*ParsedTick) error { return nil }); err == nil {
		t.Fatal("native record size limit ignored")
	}
}

func TestTapeNativeClipDispatchPrecisionAndMixedSources(t *testing.T) {
	h := tapeTestHeader()
	f := tapeTestFrame(0, tapeTestGrab(1, "none", "none"))
	raw := tapeTestRaw(t, h, f)
	line := h.CreatedAt.AsTime().Format(time.RFC3339Nano) + "\t" + raw + "\n"
	p := NewEchoReplayParser()
	count := 0
	mc, _, err := p.parseReader(strings.NewReader(line), "native.echoreplay", func(tick *ParsedTick) error {
		count++
		if tick.Frames[0].Observation.Source != "tape" {
			t.Fatal("native clip relabeled legacy")
		}
		return nil
	})
	if err != nil || mc == nil || count != 1 {
		t.Fatalf("native clip: count=%d err=%v", count, err)
	}
	badTime := "2026/01/02 03:04:05.123\t" + raw + "\n"
	if _, _, err := NewEchoReplayParser().parseReader(strings.NewReader(badTime), "bad.echoreplay", func(*ParsedTick) error { return nil }); err == nil {
		t.Fatal("lossy timestamp projection accepted")
	}
	legacy := "2026/01/02 03:04:05.200\t{}\n"
	if _, _, err := NewEchoReplayParser().parseReader(strings.NewReader(line+legacy), "mixed.echoreplay", func(*ParsedTick) error { return nil }); err == nil {
		t.Fatal("mixed native and legacy records accepted")
	}
	if _, _, err := NewEchoReplayParser().parseReader(strings.NewReader(legacy+line), "mixed.echoreplay", func(*ParsedTick) error { return nil }); err == nil {
		t.Fatal("mixed legacy and native records accepted")
	}
}

func TestTapeTimeGapsAndNormalizedResourceLimit(t *testing.T) {
	h := tapeTestHeader()
	d := NewTapeRawDecoder()
	if _, err := d.Decode(tapeTestRaw(t, h, tapeTestFrame(0, tapeTestGrab(1, "disc", "disc")))); err != nil {
		t.Fatal(err)
	}
	f := tapeTestFrame(1, tapeTestGrab(1, "none", "none"), tapeTestThrow(1, 18))
	f.TimestampOffsetMs = maxTapeContinuityMs + 1
	tick, err := d.Decode(tapeTestRaw(t, h, f))
	if err != nil {
		t.Fatal(err)
	}
	p := tick.Frames[0]
	if p.Observation.SourceEpoch != 1 || p.DeltaTime != 0 || p.GameLastThrow != nil {
		t.Fatal("continuous indices bridged a declared time gap")
	}
	d = NewTapeRawDecoder()
	d.maxNormalizedFrames = 1
	if _, err := d.Decode(tapeTestRaw(t, h, tapeTestFrame(0))); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(tapeTestRaw(t, h, tapeTestFrame(1))); err == nil || !strings.Contains(err.Error(), "player-frame limit") {
		t.Fatalf("normalized resource budget not enforced: %v", err)
	}
}

func TestTapeUnknownNativeFieldsAndQuaternionsRetained(t *testing.T) {
	h := tapeTestHeader()
	f := tapeTestFrame(0)
	unknown := protowire.AppendVarint(protowire.AppendTag(nil, 200, protowire.VarintType), 12345)
	f.ProtoReflect().SetUnknown(unknown)
	f.GetEchoArena().Players[0].LeftHand.Orientation = &spatial.Quat{X: .5, Y: .5, Z: .5, W: .501}
	d := NewTapeRawDecoder()
	tick, err := d.Decode(tapeTestRaw(t, h, f))
	if err != nil {
		t.Fatal(err)
	}
	if q := tick.Frames[0].LeftHandRotation; q.Magnitude() < .99999999 || q.Magnitude() > 1.00000001 {
		t.Fatalf("accepted quaternion not normalized: %v", q)
	}
	var envelope struct {
		Native TapeRawRecord `json:"_nevr_tape"`
	}
	if err = json.Unmarshal([]byte(tick.RawJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	retained := new(capture.Frame)
	if err = proto.Unmarshal(envelope.Native.Frame, retained); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retained.ProtoReflect().GetUnknown(), unknown) || !proto.Equal(retained, f) {
		t.Fatal("native source fields were normalized/discarded in retained protobuf")
	}
}

func TestTapeReimportEncodingDeterministicWithMetadataAndEventMaps(t *testing.T) {
	h := tapeTestHeader()
	h.Metadata = make(map[string]string)
	event := &capture.GenericEvent{Data: make(map[string]string)}
	for i := range 24 {
		key := fmt.Sprintf("synthetic_%02d", i)
		h.Metadata[key] = fmt.Sprint(i)
		event.Data[key] = fmt.Sprint(i + 100)
	}
	f := tapeTestFrame(0, &capture.EchoEvent{Event: &capture.EchoEvent_GenericEvent{GenericEvent: event}})
	unknown := protowire.AppendVarint(protowire.AppendTag(nil, 200, protowire.VarintType), 12345)
	h.ProtoReflect().SetUnknown(unknown)
	f.ProtoReflect().SetUnknown(unknown)
	path := tapeTestWrite(t, h, f)
	var first string
	for attempt := range 16 {
		_, _, err := NewEchoReplayParser().ParseFileStream(path, func(tick *ParsedTick) error {
			if attempt == 0 {
				first = tick.RawJSON
			} else if tick.RawJSON != first {
				t.Fatalf("identical native file changed retained record bytes on import %d", attempt)
			}
			var wrapper struct {
				Native TapeRawRecord `json:"_nevr_tape"`
			}
			if err := json.Unmarshal([]byte(tick.RawJSON), &wrapper); err != nil {
				t.Fatal(err)
			}
			retainedHeader, retainedFrame := new(capture.CaptureHeader), new(capture.Frame)
			if proto.Unmarshal(wrapper.Native.Header, retainedHeader) != nil || proto.Unmarshal(wrapper.Native.Frame, retainedFrame) != nil || !proto.Equal(h, retainedHeader) || !proto.Equal(f, retainedFrame) {
				t.Fatal("deterministic encoding changed native map/unknown fields")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTapeSampleTimeDoesNotRequireLatePlayerRoster(t *testing.T) {
	h := tapeTestHeader()
	f := tapeTestFrame(100)
	f.GetEchoArena().Players[0].Slot = 17
	raw := tapeTestRaw(t, h, f)
	got, err := NativeTapeSampleTime(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := h.CreatedAt.AsTime().Add(3300 * time.Millisecond)
	if !got.Equal(want) {
		t.Fatalf("got=%s want=%s", got, want)
	}
	if _, err = NewTapeRawDecoder().Decode(raw); err == nil {
		t.Fatal("state decoder invented missing late-player roster")
	}
	for _, bad := range []string{"", `{"_nevr_tape":null}`, `{"_nevr_tape":{"version":1,"header":"AA==","frame":"AA=="}}`} {
		if _, err := NativeTapeSampleTime(bad); err == nil {
			t.Fatal("bad timestamp source accepted")
		}
	}
}

func TestTapeNativeClipRejectsEveryDamagedSparseRecord(t *testing.T) {
	h := tapeTestHeader()
	first := h.CreatedAt.AsTime().Format(time.RFC3339Nano) + "\t" + tapeTestRaw(t, h, tapeTestFrame(0)) + "\n"
	last := h.CreatedAt.AsTime().Add(66*time.Millisecond).Format(time.RFC3339Nano) + "\t" + tapeTestRaw(t, h, tapeTestFrame(2)) + "\n"
	for _, bad := range []string{"no separator\n", "bad time\t{}\n", h.CreatedAt.AsTime().Format(time.RFC3339Nano) + "\t\n", h.CreatedAt.AsTime().Format(time.RFC3339Nano) + "\t{bad json\n"} {
		for _, input := range []string{first + bad + last, bad + first} {
			if _, _, err := NewEchoReplayParser().parseReader(strings.NewReader(input), "damaged.echoreplay", func(*ParsedTick) error { return nil }); err == nil {
				t.Fatalf("sparse record loss accepted: %q", bad)
			}
		}
	}
}

func TestTapeCompressionWindowDictionaryAndChecksumPreflight(t *testing.T) {
	// Header-only hostile inputs fail before a Zstd decoder can allocate the
	// advertised window. They are synthetic protocol declarations, not captures.
	for name, b := range map[string][]byte{
		"oversized window": {0x28, 0xb5, 0x2f, 0xfd, 0x04, 0x88, 1, 0, 0, 0, 0, 0, 0},
		"missing checksum": {0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x00, 1, 0, 0},
		"dictionary":       {0x28, 0xb5, 0x2f, 0xfd, 0x05, 0x00, 1, 1, 0, 0, 0, 0, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "unsafe.tape")
			if err := os.WriteFile(file, b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := checkTapeContainer(file); err == nil {
				t.Fatal("unsafe compressor declaration accepted")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "whole.tape")
	w, err := codec.NewWriterWithOptions(path, codec.WithWholeStreamCompression())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.WriteHeader(tapeTestHeader()); err != nil {
		t.Fatal(err)
	}
	if err = w.WriteFrame(tapeTestFrame(0)); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = NewEchoReplayParser().ParseFileStream(path, func(*ParsedTick) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 0xff
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = NewEchoReplayParser().ParseFileStream(path, func(*ParsedTick) error { return nil }); err == nil {
		t.Fatal("corrupted compressor checksum reported success")
	}
}

func TestTapeUncompressedIntegrityAndUnknownEnvelopes(t *testing.T) {
	envelopes := []*capture.Envelope{{Message: &capture.Envelope_Header{Header: tapeTestHeader()}}, {Message: &capture.Envelope_Frame{Frame: tapeTestFrame(0)}}, {Message: &capture.Envelope_Footer{Footer: &capture.CaptureFooter{FrameCount: 1}}}}
	encode := func(envelopes []*capture.Envelope) []byte {
		var out []byte
		for _, e := range envelopes {
			b, err := proto.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			out = binary.AppendUvarint(out, uint64(len(b)))
			out = append(out, b...)
		}
		return out
	}
	path := filepath.Join(t.TempDir(), "plain.tape")
	if err := os.WriteFile(path, encode(envelopes), 0600); err != nil {
		t.Fatal(err)
	}
	mc, _, err := NewEchoReplayParser().ParseFileStream(path, func(*ParsedTick) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if mc.NativeCapture.ContainerIntegrity != "verified_footer_only_uncompressed" {
		t.Fatal("uncompressed bytes claimed compressor checksum")
	}
	unknown := new(capture.Envelope)
	unknown.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 99, protowire.BytesType), []byte{1}))
	withUnknown := []*capture.Envelope{envelopes[0], unknown, envelopes[1], envelopes[2]}
	if err = os.WriteFile(path, encode(withUnknown), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = NewEchoReplayParser().ParseFileStream(path, func(*ParsedTick) error { return nil }); err == nil {
		t.Fatal("skipped unknown native state envelope reported success")
	}
	// Confirm compressed-envelope CRC policy applies independently of the tape
	// footer: a valid footer is not a replacement for a compressor checksum.
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(false))
	if err != nil {
		t.Fatal(err)
	}
	b := encoder.EncodeAll(encode(envelopes), nil)
	encoder.Close()
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = NewEchoReplayParser().ParseFileStream(path, func(*ParsedTick) error { return nil }); err == nil {
		t.Fatal("checksum-free compressed capture silently accepted")
	}
}
