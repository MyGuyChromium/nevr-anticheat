package sqlite

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestTelemetryOptionalObservationsPersistWithoutSchemaChange(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	head := model.Vec3{1, 1.8, 2}
	bounce := 0
	present := model.PlayerTelemetryFrame{PlayerID: "P1", Position: model.Vec3{1, 1.6, 2}, HeadPosition: &head,
		Disc: &model.DiscState{Position: model.Vec3{1, 1.7, 2}, Velocity: model.Vec3{1, 0, 0}, Speed: 1,
			BounceCount: &bounce, PossessionKnown: true, PossessionConflict: true, SampledPlayerCount: 2}}
	var legacy model.PlayerTelemetryFrame
	if err := json.Unmarshal([]byte(`{"player_id":"P2","position":[2,1.6,3],"disc":{"position":[2,1.7,3],"velocity":[1,0,0],"holder_id":"P2"}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	n, err := s.StoreTelemetryFrames(ctx, "observations", []model.PlayerTelemetryFrame{present, legacy})
	if err != nil || n != 2 {
		t.Fatalf("store=%d, %v", n, err)
	}
	frames, err := s.GetMatchFrames(ctx, "observations")
	if err != nil || len(frames) != 2 {
		t.Fatalf("load=%d, %v", len(frames), err)
	}
	got, old := frames[0], frames[1]
	if got.HeadPosition == nil || *got.HeadPosition != head || got.Disc.BounceCount == nil || *got.Disc.BounceCount != 0 ||
		!got.Disc.PossessionKnown || !got.Disc.PossessionConflict || got.Disc.SampledPlayerCount != 2 {
		t.Fatalf("persisted optional observations lost: %+v, %+v", got.HeadPosition, got.Disc)
	}
	if old.HeadPosition != nil || old.Disc.BounceCount != nil || old.Disc.PossessionKnown || old.Disc.PossessionConflict ||
		old.Disc.SampledPlayerCount != 0 || old.Disc.PossessorID != "P2" || !old.Disc.IsHeld || old.Disc.Speed != 1 {
		t.Fatalf("legacy unknown fields invented observations or lost holder alias: %+v, %+v", old.HeadPosition, old.Disc)
	}
	var storedJSON string
	if err := s.DB().QueryRowContext(ctx, `SELECT frame_json FROM telemetry_frames WHERE match_id='observations' AND player_id='P2'`).Scan(&storedJSON); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"head_position", "bounce_count", "possession_known", "possession_conflict", "sampled_player_count"} {
		if strings.Contains(storedJSON, `"`+key+`"`) {
			t.Fatalf("legacy absence not preserved: %s", storedJSON)
		}
	}
}
