package ingest

import (
	"context"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestRosterRejectedPeerCannotSetTickClock(t *testing.T) {
	mm, store, _ := newTestManager(t)
	mm.SetLimits(0, 1)
	if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{goodFrame("P1", 0)}); got.Accepted != 1 {
		t.Fatal(got)
	}
	// A sorts before P1 but is already outside the admitted roster. Its clock
	// cannot establish the timestamp against which P1's real tick is tested.
	rejected := goodFrame("A", 1)
	rejected.Timestamp = 100
	if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{rejected, goodFrame("P1", 1)}); got.Accepted != 1 || got.Rejected != 1 || got.Ignored != 0 {
		t.Fatalf("roster-rejected peer suppressed an admitted observation: %+v", got)
	}
	if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{goodFrame("P1", 2)}); got.Accepted != 1 || got.Rejected != 0 {
		t.Fatalf("valid stream did not recover: %+v", got)
	}
	mm.EndMatch("m")
	frames, err := store.GetMatchFrames(context.Background(), "m")
	if err != nil || len(frames) != 3 {
		t.Fatalf("stored frames=%d err=%v", len(frames), err)
	}
	for i, frame := range frames {
		if frame.PlayerID != "P1" || frame.FrameIndex != i || frame.Timestamp != goodFrame("P1", i).Timestamp {
			t.Fatalf("unexpected persisted observation: %+v", frame)
		}
	}
}

func TestClosedStoredTickCannotAcquireLateRosterAfterResume(t *testing.T) {
	for _, finalize := range []bool{false, true} {
		name := "manager_restart"
		if finalize {
			name = "after_match_end"
		}
		t.Run(name, func(t *testing.T) {
			mm, store, _ := newTestManager(t)
			original := goodFrame("P1", 0)
			if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{original}); got.Accepted != 1 {
				t.Fatal(got)
			}
			if finalize {
				mm.EndMatch("m")
			} else {
				// The old process's in-memory partial tick is unavailable after
				// restart. Resuming does not recreate it from a late peer alone.
				mm = NewMatchManager(mm.cfg, store, mm.detectorFn, quietLogger())
			}
			if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{original}); got.Ignored != 1 || got.Accepted != 0 || got.Rejected != 0 {
				t.Fatalf("identical stored retry lost idempotence: %+v", got)
			}
			if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{goodFrame("P2", 0)}); got.Rejected != 1 || got.Accepted != 0 || got.Ignored != 0 {
				t.Fatalf("late peer reopened a closed stored tick: %+v", got)
			}
			// The next actual tick may still arrive in per-player slices.
			for _, pid := range []string{"P1", "P2"} {
				if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{goodFrame(pid, 1)}); got.Accepted != 1 || got.Rejected != 0 {
					t.Fatalf("fresh partial roster rejected: %+v", got)
				}
			}
			if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{goodFrame("P1", 2)}); got.Accepted != 1 {
				t.Fatal(got)
			}
			if ps := mm.matches["m"].Players["P2"]; ps == nil || ps.FrameCount != 1 {
				t.Fatalf("new player must have only the fresh accepted tick: %+v", ps)
			}
			mm.EndMatch("m")
			if n := countRows(t, store, `SELECT COUNT(*) FROM telemetry_frames WHERE match_id = 'm' AND player_id = 'P2' AND frame_index = 0`); n != 0 {
				t.Fatal("closed tick was modified")
			}
		})
	}
}

func TestRestartRecoversRosterFromDurableTelemetryBeforeAdmission(t *testing.T) {
	mm, store, _ := newTestManager(t)
	mm.SetLimits(0, 1)
	if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{goodFrame("P1", 0)}); got.Accepted != 1 {
		t.Fatal(got)
	}
	// Creation persisted the empty context; the first tick is already durable
	// while the periodic roster/context refresh has not run. Simulate restart
	// without orderly finalization updating that context.
	stored, err := store.GetMatchContext(context.Background(), "m")
	if err != nil || len(stored.PlayerIDs) != 0 {
		t.Fatalf("fixture did not retain the pre-roster durable context: %+v, %v", stored, err)
	}
	resumed := NewMatchManager(mm.cfg, store, mm.detectorFn, quietLogger())
	resumed.SetLimits(0, 1)
	if got := resumed.HandleFrames("m", "", []model.PlayerTelemetryFrame{goodFrame("A", 1), goodFrame("P1", 1)}); got.Accepted != 1 || got.Rejected != 1 {
		t.Fatalf("restart forgot the admitted durable roster: %+v", got)
	}
	context := resumed.GetMatchContext("m")
	if len(context.PlayerIDs) != 1 || context.PlayerIDs[0] != "P1" {
		t.Fatalf("durable player replaced after restart: %+v", context.PlayerIDs)
	}
	if n := countRows(t, store, `SELECT COUNT(*) FROM telemetry_frames WHERE match_id = 'm' AND player_id = 'A'`); n != 0 {
		t.Fatal("unexpected player admitted after restart")
	}
	if got := resumed.HandleFrames("m", "", []model.PlayerTelemetryFrame{goodFrame("P1", 2)}); got.Accepted != 1 || got.Rejected != 0 {
		t.Fatalf("original stream failed to continue: %+v", got)
	}
	resumed.EndMatch("m")
}

func TestFailedTelemetryWriteCannotAdvanceRosterOrTeams(t *testing.T) {
	mm, store, _ := newTestManager(t)
	mm.SetLimits(0, 2)
	first := goodFrame("P1", 0)
	first.Team = "blue"
	if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{first}); got.Accepted != 1 {
		t.Fatal(got)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_raw BEFORE INSERT ON telemetry_frames WHEN NEW.frame_index = 1 BEGIN SELECT RAISE(ABORT, 'synthetic raw write failure'); END`); err != nil {
		t.Fatal(err)
	}
	changed, joined := goodFrame("P1", 1), goodFrame("P2", 1)
	changed.Team, joined.Team = "orange", "blue"
	batch := []model.PlayerTelemetryFrame{changed, joined}
	if got := mm.HandleFrames("m", "", batch); got.Rejected != 2 || got.Accepted != 0 {
		t.Fatalf("failed raw write must reject the whole batch: %+v", got)
	}
	beforeRetry := mm.GetMatchContext("m")
	if len(beforeRetry.PlayerIDs) != 1 || beforeRetry.PlayerIDs[0] != "P1" || beforeRetry.TeamAssignments["P1"] != "blue" || len(beforeRetry.TeamAssignments) != 1 {
		t.Fatalf("uncommitted raw rows changed context: %+v", beforeRetry)
	}
	if _, err := store.DB().Exec(`DROP TRIGGER fail_raw`); err != nil {
		t.Fatal(err)
	}
	if got := mm.HandleFrames("m", "", batch); got.Accepted != 2 || got.Ignored != 0 || got.Rejected != 0 {
		t.Fatalf("unchanged retry did not recover: %+v", got)
	}
	afterRetry := mm.GetMatchContext("m")
	if len(afterRetry.PlayerIDs) != 2 || afterRetry.TeamAssignments["P1"] != "orange" || afterRetry.TeamAssignments["P2"] != "blue" {
		t.Fatalf("committed context not updated: %+v", afterRetry)
	}
	mm.EndMatch("m")
}
