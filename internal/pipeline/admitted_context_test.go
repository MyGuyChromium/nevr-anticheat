package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"slices"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestSharedOrientationPrevalidationPreservesInputsAndCountsOnce(t *testing.T) {
	p, _ := newPipeline(testConfig("enforce"), []detect.Detector{bio.NewBio001(nil)})
	p.SetSkipReset(true)
	mc := matchCtx("a", "b", "c")
	var initial, next []model.PlayerTelemetryFrame
	unobserved, invalidBounce := false, -1
	for _, id := range mc.PlayerIDs {
		initial = append(initial, healthFrame(id, 0))
		f := healthFrame(id, 1)
		f.LeftHandRotationValid = &unobserved
		f.LeftHandPosition = model.Vec3{99, 99, 99}
		f.Disc.BounceCount = &invalidBounce
		next = append(next, f)
	}
	if _, err := p.ProcessMatch(context.Background(), mc, initial); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.sharedOrientationJumps(next, mc)
	result, err := p.ProcessMatch(context.Background(), mc, next)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || unobserved || invalidBounce != -1 {
		t.Fatal("validation mutated caller-owned frames or shared optional pointers")
	}
	for _, reason := range []string{SanitizedFarHand, SanitizedDiscObservation, SanitizedRotation} {
		if result.SanitizedFrames[reason] != 3 {
			t.Fatalf("%s counted %d times, want once per row", reason, result.SanitizedFrames[reason])
		}
	}
}

func TestRejectedPhaseCannotSuppressAdmittedPlayers(t *testing.T) {
	for _, mode := range []string{"invalid_position", "stale_sample", "valid_inactive"} {
		t.Run(mode, func(t *testing.T) {
			d := newRecorder("ADMISSION_TEST", "test", 0)
			p, _ := newPipeline(testConfig("enforce"), []detect.Detector{d})
			p.SetSkipReset(true)
			mc := matchCtx("a", "b")
			if _, err := p.ProcessMatch(context.Background(), mc, []model.PlayerTelemetryFrame{healthFrame("a", 0), healthFrame("b", 0)}); err != nil {
				t.Fatal(err)
			}
			d.Reset()
			a, b := healthFrame("a", 1), healthFrame("b", 1)
			b.GamePhase = "round_over"
			switch mode {
			case "invalid_position":
				b.Position = model.Vec3{}
			case "stale_sample":
				b.Timestamp = 0
				b.Observation.Timestamp = 0
			}
			result, err := p.ProcessMatch(context.Background(), mc, []model.PlayerTelemetryFrame{a, b})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "valid_inactive" {
				if len(d.seen) != 0 || result.InvalidFrames != 0 {
					t.Fatalf("valid non-play context ignored: calls=%v rejected=%d", d.seen, result.InvalidFrames)
				}
			} else if len(d.seen) == 0 || result.InvalidFrames != 1 {
				t.Fatalf("rejected peer phase suppressed valid observations: calls=%v rejected=%d", d.seen, result.InvalidFrames)
			}
		})
	}
}

func TestSharedOrientationFaultRequiresAdmittedCorroboration(t *testing.T) {
	for _, mode := range []string{"invalid_position", "stale_frame", "unbound_source", "rejected_duplicate", "invalid_duplicate_first", "valid"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := newPipeline(testConfig("enforce"), []detect.Detector{bio.NewBio001(nil)})
			p.SetSkipReset(true)
			mc := matchCtx("a", "b", "c")
			var initial, next []model.PlayerTelemetryFrame
			for _, id := range mc.PlayerIDs {
				initial = append(initial, healthFrame(id, 0))
				f := healthFrame(id, 1)
				f.Rotation = model.Quat{0, math.Sin(.49 * math.Pi), 0, math.Cos(.49 * math.Pi)}
				next = append(next, f)
			}
			if _, err := p.ProcessMatch(context.Background(), mc, initial); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "invalid_position":
				next[2].Position = model.Vec3{}
			case "stale_frame":
				// The current source timestamp alone does not make a replayed
				// frame identity an independent contemporaneous observation.
				next[2].FrameIndex = 0
				next[2].Observation.FrameIndex = 0
			case "unbound_source":
				next[2].Observation.FrameIndex = 99
			case "rejected_duplicate":
				next = append(next, next[2])
				next[2].Rotation = model.QuatIdentity()
			case "invalid_duplicate_first":
				next = append(next, next[2])
				next[2].Position = model.Vec3{}
			}
			shared := p.sharedOrientationJumps(next, mc)
			wantShared := mode == "valid" || mode == "invalid_duplicate_first"
			if got, want := shared["a"], wantShared; got != want {
				t.Fatalf("shared fault=%v want %v; mode=%s", got, want, mode)
			}
			if mode == "invalid_position" || mode == "valid" || mode == "rejected_duplicate" || mode == "invalid_duplicate_first" {
				result, err := p.ProcessMatch(context.Background(), mc, next)
				if err != nil {
					t.Fatal(err)
				}
				reasons := result.PlayerCoverage["a"].DataHealth.Reasons
				if slices.Contains(reasons, "shared_orientation_jump") != wantShared {
					t.Fatalf("false shared fault reached persisted health: %v", reasons)
				}
			}
		})
	}
}

func TestNegativeFrameIdentityNeverSeedsHistory(t *testing.T) {
	d := newRecorder("ADMISSION_TEST", "test", 0)
	p, _ := newPipeline(testConfig("enforce"), []detect.Detector{d})
	bad := healthFrame("a", 0)
	bad.FrameIndex = -1
	bad.Observation.FrameIndex = -1
	good := healthFrame("a", 1)
	result, err := p.ProcessMatch(context.Background(), matchCtx("a"), []model.PlayerTelemetryFrame{bad, good})
	if err != nil {
		t.Fatal(err)
	}
	if result.InvalidFrameReasons["invalid_frame_index"] != 1 || p.Players()["a"].FrameCount != 1 || result.FramesProcessed != 1 {
		t.Fatalf("invalid frame seeded detector history: rejected=%v count=%d frames=%d", result.InvalidFrameReasons, p.Players()["a"].FrameCount, result.FramesProcessed)
	}
}
