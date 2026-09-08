package ingest

import (
	"context"
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func TestLiveFrameIdentityEquivalenceIsConservative(t *testing.T) {
	base := goodFrame("exact-id-9007199254740993", 10)
	base.Observation = &model.ObservationContext{Source: "fixture", Authority: "client_reported", TimeBasis: "received", SessionID: "m", FrameIndex: 10, Timestamp: base.Timestamp}
	for _, tc := range []struct {
		name   string
		mutate func(*model.PlayerTelemetryFrame)
		same   bool
	}{
		{"identical", func(*model.PlayerTelemetryFrame) {}, true},
		{"float_ulp", func(f *model.PlayerTelemetryFrame) { f.Position[0] = math.Nextafter(f.Position[0], math.Inf(1)) }, true},
		{"quaternion_sign", func(f *model.PlayerTelemetryFrame) {
			for i := range f.Rotation {
				f.Rotation[i] = -f.Rotation[i]
			}
		}, true},
		{"different_pose", func(f *model.PlayerTelemetryFrame) { f.Position[0] += .01 }, false},
		{"different_identity", func(f *model.PlayerTelemetryFrame) { f.PlayerID = "exact-id-9007199254740992" }, false},
		{"different_clock_epoch", func(f *model.PlayerTelemetryFrame) {
			f.Observation = f.Observation.Clone()
			f.Observation.SourceEpoch++
		}, false},
		{"unknown_became_zero", func(f *model.PlayerTelemetryFrame) { f.ReportedVelocity = &model.Vec3{} }, false},
		{"different_time", func(f *model.PlayerTelemetryFrame) { f.Timestamp += 1e-6 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := base
			tc.mutate(&other)
			same, err := sameLiveFrame(base, other)
			if err != nil || same != tc.same {
				t.Fatalf("same=%v err=%v", same, err)
			}
		})
	}
}

func TestMatchManagerConflictingIdentitySuspendsWithoutRewritingRaw(t *testing.T) {
	for _, sameBatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "persisted_retry", true: "within_batch"}[sameBatch], func(t *testing.T) {
			mm, store, _ := newTestManager(t)
			original := goodFrame("P1", 0)
			conflicting := original
			conflicting.Position[0] += 10
			if sameBatch {
				got := mm.HandleFramesWithRaw("m", "", []model.PlayerTelemetryFrame{original, conflicting}, `{"original":true}`)
				if got.Accepted != 1 || got.Rejected != 1 {
					t.Fatal(got)
				}
			} else {
				mm.HandleFramesWithRaw("m", "", []model.PlayerTelemetryFrame{original}, `{"original":true}`)
				got := mm.HandleFramesWithRaw("m", "", []model.PlayerTelemetryFrame{conflicting}, `{"changed":true}`)
				if got.Accepted != 0 || got.Rejected != 1 {
					t.Fatal(got)
				}
			}
			match := mm.matches["m"]
			if !match.AnalysisIncomplete || match.AnalysisSuspensionReason != sqlite.LiveSourceIdentityConflictReason || match.PersistenceError != "" {
				t.Fatal("conflicting identity was not separately suspended")
			}
			// A reset producer catching up to the old counter cannot resume
			// live analysis. Its genuinely new raw rows remain investigable.
			for i := 1; i < 60; i++ {
				if got := mm.HandleFrames("m", "", []model.PlayerTelemetryFrame{speedHack("P1", i)}); got.Accepted != 1 {
					t.Fatal(got)
				}
			}
			mm.EndMatch("m")
			frames, err := store.GetMatchFrames(context.Background(), "m")
			if err != nil || len(frames) != 60 || frames[0].Position != original.Position {
				t.Fatal("prior raw row changed or later raw lost")
			}
			raw, err := store.GetMatchRawTicks(context.Background(), "m", 0, 0)
			if err != nil || raw[0] != `{"original":true}` {
				t.Fatal("original payload replaced")
			}
			if got := countRows(t, store, `SELECT COUNT(*) FROM detection_events WHERE match_id = ?`, "m"); got != 0 {
				t.Fatal("suspended stream generated live events")
			}
			resumed := NewMatchManager(mm.cfg, store, mm.detectorFn, quietLogger())
			if got := resumed.HandleFrames("m", "", []model.PlayerTelemetryFrame{goodFrame("P1", 60)}); got.Accepted != 1 {
				t.Fatal(got)
			}
			if !resumed.matches["m"].AnalysisIncomplete || resumed.matches["m"].AnalysisSuspensionReason != sqlite.LiveSourceIdentityConflictReason {
				t.Fatal("manager restart erased identity conflict")
			}
		})
	}
}
