package testutil

import (
	"errors"
	"time"

	spatialpb "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/spatial/v1"
	capturepb "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"github.com/echotools/tape/v4/pkg/codec"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	NativeTapeMatchID   = "8a0c0b6e-0000-4000-8000-000000000001"
	NativeTapeCaptureID = "8a0c0b6e-0000-4000-8000-000000000002"
	NativeTapePlayerID  = "echovr:1001"
	NativeTapeFrames    = 12
)

// WriteNativeTapeFixture creates an explicitly synthetic stationary review
// recording using the real pinned codec. It is an integration fixture, not a
// fair-play label, independent mechanics evidence, or detector-accuracy test.
func WriteNativeTapeFixture(path string) error {
	w, err := codec.NewWriter(path)
	if err != nil {
		return err
	}
	header := &capturepb.CaptureHeader{
		CaptureId:     NativeTapeCaptureID,
		CreatedAt:     timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		FormatVersion: 2,
		FrameEncoding: capturepb.FrameEncoding_FRAME_ENCODING_SPARSE,
		GameType:      "echo_arena",
		GameHeader: &capturepb.CaptureHeader_EchoArena{EchoArena: &capturepb.EchoArenaHeader{
			SessionId:     NativeTapeMatchID,
			MapName:       "mpl_arena_a",
			ClientName:    "Synthetic Native Player",
			TeamNames:     []string{"BLUE TEAM", "ORANGE TEAM", "SPECTATORS"},
			InitialRoster: []*capturepb.PlayerInfo{{Slot: 1, AccountNumber: 1001, DisplayName: "Synthetic Native Player", Role: capturepb.Role_ROLE_BLUE_TEAM}},
		}},
	}
	if err := w.WriteHeader(header); err != nil {
		return errors.Join(err, w.Close())
	}
	pose := func(x, y, z float32) *spatialpb.Pose {
		return &spatialpb.Pose{Position: &spatialpb.Vec3{X: x, Y: y, Z: z}, Orientation: &spatialpb.Quat{W: 1}}
	}
	for i := uint32(0); i < NativeTapeFrames; i++ {
		ea := &capturepb.EchoArenaFrame{
			GameStatus: capturepb.GameStatus_GAME_STATUS_PLAYING,
			GameClock:  120 - float32(i)/30,
			Disc:       &capturepb.DiscState{Pose: pose(7, 2, 3), Velocity: &spatialpb.Vec3{}},
			Players:    []*capturepb.PlayerState{{Slot: 1, Head: pose(1, 2.2, 3), Body: pose(1, 2, 3), LeftHand: pose(.7, 1.8, 3), RightHand: pose(1.3, 1.8, 3), Velocity: &spatialpb.Vec3{}, Ping: 20}},
		}
		if i == 0 {
			ea.Events = []*capturepb.EchoEvent{{Event: &capturepb.EchoEvent_GrabChanged{GrabChanged: &capturepb.GrabChanged{PlayerSlot: 1, LeftHolding: "none", RightHolding: "none"}}}}
		}
		if err := w.WriteFrame(&capturepb.Frame{FrameIndex: i, TimestampOffsetMs: i * 33, Payload: &capturepb.Frame_EchoArena{EchoArena: ea}}); err != nil {
			return errors.Join(err, w.Close())
		}
	}
	return w.Close()
}
