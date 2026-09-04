package replay

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	storesqlite "github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func remapSession(t *testing.T, frame int, holding string) string {
	t.Helper()
	hand := adapter.EchoVRHand{
		Position: [3]float64{1.3, 1.2, 0}, Forward: [3]float64{0, 0, 1},
		Left: [3]float64{1, 0, 0}, Up: [3]float64{0, 1, 0},
	}
	p := adapter.EchoVRPlayer{
		Name: "Alice", UserID: 101, Possession: true, // deliberately stale after release
		HoldingLeft: "none", HoldingRight: holding,
		Velocity: [3]float64{0, 0, 2},
		Body: adapter.EchoVRBodyHead{
			Position: [3]float64{1, 1, float64(frame) * 0.1}, Forward: [3]float64{0, 0, 1},
			Left: [3]float64{1, 0, 0}, Up: [3]float64{0, 1, 0},
		},
		Head:  adapter.EchoVRBodyHead{Position: [3]float64{1, 1, float64(frame) * 0.1}},
		LHand: hand, RHand: hand,
	}
	disc := &adapter.EchoVRDisc{Position: hand.Position}
	if holding == "none" {
		disc.Velocity = [3]float64{0, 0, 19.91}
	}
	s := adapter.EchoVRSessionResponse{
		SessionID: "M1", MatchType: "Echo_Arena", MapName: "mpl_arena_a", GameStatus: "playing",
		Disc: disc, Teams: []adapter.EchoVRTeam{{TeamName: "BLUE TEAM", Players: []adapter.EchoVRPlayer{p}}},
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRemapStoredTelemetryUsesRawHoldingAndCurrentPhysics(t *testing.T) {
	ctx := context.Background()
	store, err := storesqlite.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := []model.PlayerTelemetryFrame{
		{PlayerID: "echovr:101", FrameIndex: 0, Timestamp: 0, Position: model.Vec3{9, 9, 9}, HasPossession: true},
		{PlayerID: "echovr:101", FrameIndex: 1, Timestamp: 0.067, Position: model.Vec3{9, 9, 9}, HasPossession: true},
		{PlayerID: "echovr:101", FrameIndex: 2, Timestamp: 0.134, Position: model.Vec3{9, 9, 9}, HasPossession: true},
	}
	raw := map[int]string{0: remapSession(t, 0, "disc"), 1: remapSession(t, 1, "none"), 2: remapSession(t, 2, "none")}
	if _, err := store.StoreTelemetryFramesWithRaw(ctx, "M1", old, raw); err != nil {
		t.Fatal(err)
	}
	stored := &model.MatchContext{
		MatchID: "M1", StartTime: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
		PlayerIDs: []string{"echovr:101"}, TeamAssignments: map[string]string{"echovr:101": "blue"},
		Physics: model.PhysicsConstants{DiscSpeedCap: 18.7},
	}
	physics := model.DefaultPhysics()
	got, err := RemapStoredTelemetry(ctx, store, stored, physics)
	if err != nil {
		t.Fatal(err)
	}
	if got.RawTicks != 3 || len(got.Frames) != 3 || got.Context.Physics.DiscSpeedCap != 18.9 {
		t.Fatalf("remap metadata = %+v", got)
	}
	if !got.Frames[0].HasPossession || got.Frames[1].HasPossession || got.Frames[2].HasPossession {
		t.Fatalf("explicit holding state not remapped: %v %v %v", got.Frames[0].HasPossession, got.Frames[1].HasPossession, got.Frames[2].HasPossession)
	}
	if got.Frames[1].Disc == nil || got.Frames[1].Disc.Speed < 19.9 || got.Frames[1].Position == old[1].Position {
		t.Fatalf("raw disc/pose not restored: %+v", got.Frames[1])
	}
	if stored.Physics.DiscSpeedCap != 18.7 {
		t.Fatal("caller-owned match context was mutated")
	}
}

func TestRemapStoredTelemetryRejectsDiscontinuousRawTicks(t *testing.T) {
	ctx := context.Background()
	store, err := storesqlite.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	frame := model.PlayerTelemetryFrame{PlayerID: "echovr:101", FrameIndex: 1, Timestamp: 0.067, Position: model.Vec3{1, 1, 1}}
	if _, err := store.StoreTelemetryFramesWithRaw(ctx, "M1", []model.PlayerTelemetryFrame{frame}, map[int]string{1: remapSession(t, 1, "none")}); err != nil {
		t.Fatal(err)
	}
	_, err = RemapStoredTelemetry(ctx, store, &model.MatchContext{MatchID: "M1"}, model.DefaultPhysics())
	if err == nil || !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("discontinuous raw ticks error = %v", err)
	}
}
