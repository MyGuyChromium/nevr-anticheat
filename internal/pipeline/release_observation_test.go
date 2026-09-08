package pipeline

import (
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// These fixtures carry explicit attachment and source observations. Tests that
// remove them call the production extractor directly, without filling defaults.
func releaseFrame(index int, attachment string) model.PlayerTelemetryFrame {
	f := feFrame("p1", index, float64(index)*0.05, model.Vec3{1 + float64(index)*0.25, 1.6, 1})
	f.Observation = &model.ObservationContext{Source: "synthetic", SourceID: "source-a", Authority: "client_reported", TimeBasis: "fixture_time", SessionID: "m", SourcePlayerID: "p1", FrameIndex: index, Timestamp: f.Timestamp, Freshness: "sampled_snapshot"}
	f.ReportedVelocity = vecPtr(model.Vec3{5, 0, 0})
	f.Disc = &model.DiscState{Position: f.RightHandPosition, Velocity: model.Vec3{19, 0, 0}, Speed: 19, PossessionKnown: true, SampledPlayerCount: 1, Attachment: &model.DiscAttachment{State: "free"}}
	if attachment != "free" {
		f.HasPossession = true
		f.Disc.IsHeld, f.Disc.PossessorID = true, "p1"
		f.Disc.Attachment = &model.DiscAttachment{State: "held", HolderID: "p1", HandCandidates: []string{attachment}}
	}
	return f
}

func releaseStart(t *testing.T) (*FeatureExtractor, *model.PlayerState) {
	t.Helper()
	fe, ps := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}
	for i := 0; i < 3; i++ {
		f := releaseFrame(i, "right")
		fe.UpdatePlayerState(ps, &f, feTestCtx())
	}
	return fe, ps
}

func TestReleaseConfirmationPreservesOriginalInterval(t *testing.T) {
	fe, ps := releaseStart(t)
	f := releaseFrame(3, "free")
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	if ps.LastThrow != nil || ps.ThrowCount != 0 {
		t.Fatal("first free sample published a throw")
	}
	// Confirmation movement must not replace evidence about the release interval.
	f = releaseFrame(4, "free")
	f.ReportedVelocity = vecPtr(model.Vec3{})
	f.Disc.Speed = 2
	f.Disc.Velocity = model.Vec3{2, 0, 0}
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	e := ps.LastThrow
	if e == nil || e.FrameIndex != 3 || !approx(e.Timestamp, 0.15, 1e-12) || !e.ObservedAt(4) || e.ObservedAt(3) || ps.ThrowCount != 1 {
		t.Fatalf("wrong publication: %+v", e)
	}
	if e.ReleaseSpeed != 19 || e.ThrowingHand != "right" || e.ReleaseWindow.StartFrame != 2 || e.ReleaseWindow.EndFrame != 3 || len(e.ReleaseWindow.PlayerMovement) != 2 {
		t.Fatalf("release measurements replaced: %+v", e)
	}
	for _, s := range e.ReleaseWindow.PlayerMovement {
		if s.ReportedVelocity == nil || *s.ReportedVelocity != (model.Vec3{5, 0, 0}) {
			t.Fatal("confirmation replaced interval velocity")
		}
	}
	prior := e
	f = releaseFrame(5, "right")
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	f = releaseFrame(6, "right")
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	f = releaseFrame(7, "free")
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	if ps.LastThrow != prior || prior.FrameIndex != 3 {
		t.Fatal("pending release mutated returned throw pointer")
	}
}

func TestReleaseAmbiguitiesAreInconclusive(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		mutate       func(*model.PlayerTelemetryFrame)
	}{
		{"same_player_regrab", "release_reattachment_or_transfer", func(f *model.PlayerTelemetryFrame) { *f = releaseFrame(4, "left") }},
		{"other_player_transfer", "release_reattachment_or_transfer", func(f *model.PlayerTelemetryFrame) {
			f.Disc.IsHeld = true
			f.Disc.PossessorID = "p2"
			f.Disc.Attachment = &model.DiscAttachment{State: "held", HolderID: "p2", HandCandidates: []string{"right"}}
		}},
		{"unknown", "release_attachment_unknown", func(f *model.PlayerTelemetryFrame) { f.Disc.Attachment = nil }},
		{"conflict", "release_attachment_unknown", func(f *model.PlayerTelemetryFrame) { f.Disc.PossessionConflict = true }},
		{"gap", "release_observation_gap", func(f *model.PlayerTelemetryFrame) { f.FrameIndex++; f.Timestamp += 0.05 }},
		{"phase", "release_inactive_phase", func(f *model.PlayerTelemetryFrame) { f.GamePhase = "round_over" }},
		{"source", "release_source_changed", func(f *model.PlayerTelemetryFrame) { f.Observation.SourceID = "source-b" }},
		{"session", "release_source_changed", func(f *model.PlayerTelemetryFrame) { f.Observation.SessionID = "other-session" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fe, ps := releaseStart(t)
			var reasons []string
			fe.SetReleaseObserver(func(pid string, o model.ReleaseObservation, reason string) {
				reasons = append(reasons, reason)
				if pid != "p1" || o.FirstFreeFrame != 3 {
					t.Error("wrong diagnostic identity")
				}
				o.HandCandidates[0] = "mutated"
			})
			f := releaseFrame(3, "free")
			fe.UpdatePlayerState(ps, &f, feTestCtx())
			f = releaseFrame(4, "free")
			tc.mutate(&f)
			fe.UpdatePlayerState(ps, &f, feTestCtx())
			fe.DrainPendingReleases("eof")
			if ps.LastThrow != nil || ps.ThrowCount != 0 || !reflect.DeepEqual(reasons, []string{tc.reason}) {
				t.Fatalf("throw=%+v reasons=%v", ps.LastThrow, reasons)
			}
		})
	}
}

func TestReleaseUnknownLegacyAndHandTransfers(t *testing.T) {
	fe, ps := releaseStart(t)
	f := releaseFrame(3, "left")
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	if len(fe.pendingReleases) != 0 || ps.LastThrow != nil || ps.PossessionStartFrame != 0 {
		t.Fatal("same-player hand transfer became release/reacquisition")
	}
	f = releaseFrame(4, "free")
	f.Disc.Attachment = nil
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	f = releaseFrame(5, "free")
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	if ps.LastThrow != nil || len(fe.pendingReleases) != 0 {
		t.Fatal("legacy boolean fabricated release")
	}
}

func TestReleaseEOFAndUnavailableMotion(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(map[bool]string{false: "eof", true: "confirmed_no_motion"}[confirm], func(t *testing.T) {
			fe, ps := releaseStart(t)
			var reasons []string
			fe.SetReleaseObserver(func(_ string, _ model.ReleaseObservation, r string) { reasons = append(reasons, r) })
			f := releaseFrame(3, "free")
			f.Disc.Speed = 0
			f.Disc.Velocity = model.Vec3{}
			fe.UpdatePlayerState(ps, &f, feTestCtx())
			want := "release_confirmation_unavailable"
			if confirm {
				f = releaseFrame(4, "free")
				fe.UpdatePlayerState(ps, &f, feTestCtx())
				want = "release_motion_unavailable"
			}
			fe.DrainPendingReleases("release_confirmation_unavailable")
			fe.DrainPendingReleases("release_confirmation_unavailable")
			if ps.LastThrow != nil || !reflect.DeepEqual(reasons, []string{want}) {
				t.Fatalf("event=%+v reasons=%v", ps.LastThrow, reasons)
			}
		})
	}
}

func TestReleaseLocalReportRequiresBoundProvenance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		accepted bool
		mutate   func(*model.PlayerTelemetryFrame)
	}{
		{"bound", true, func(f *model.PlayerTelemetryFrame) {}},
		{"naked", false, func(f *model.PlayerTelemetryFrame) { f.GameLastThrowProvenance = nil }},
		{"remote", false, func(f *model.PlayerTelemetryFrame) { f.GameLastThrowProvenance.SourcePlayerID = "p2" }},
		{"stale_frame", false, func(f *model.PlayerTelemetryFrame) { f.GameLastThrowProvenance.FrameIndex-- }},
		{"stale_time", false, func(f *model.PlayerTelemetryFrame) { f.GameLastThrowProvenance.Timestamp -= 0.05 }},
		{"stale_frame_context", false, func(f *model.PlayerTelemetryFrame) { f.Observation.FrameIndex-- }},
		{"source", false, func(f *model.PlayerTelemetryFrame) { f.GameLastThrowProvenance.SourceID = "other" }},
		{"unknown_freshness", false, func(f *model.PlayerTelemetryFrame) { f.GameLastThrowProvenance.Freshness = "" }},
		{"self_claimed_engine", false, func(f *model.PlayerTelemetryFrame) { f.GameLastThrowProvenance.Authority = "engine" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fe, ps := releaseStart(t)
			f := releaseFrame(3, "free")
			f.GameLastThrow = &model.GameThrowDetails{TotalSpeed: 23, SpeedFromMovement: 6, SpeedFromArm: 17}
			f.GameLastThrowProvenance = f.Observation.Clone()
			f.GameLastThrowProvenance.Freshness = "value_change"
			tc.mutate(&f)
			fe.UpdatePlayerState(ps, &f, feTestCtx())
			f = releaseFrame(4, "free")
			fe.UpdatePlayerState(ps, &f, feTestCtx())
			if ps.LastThrow == nil || (ps.LastThrow.GameLastThrow != nil) != tc.accepted || ps.LastThrow.ReleaseSpeed != 19 || ps.LastThrow.Attribution.Confidence >= 1 {
				t.Fatalf("invalid trust boundary: %+v", ps.LastThrow)
			}
		})
	}
}

func TestReleaseStaleSampleCannotRewindAndSourceSwitchClearsHistory(t *testing.T) {
	fe, ps := releaseStart(t)
	before := *ps
	f := releaseFrame(1, "free")
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	if !reflect.DeepEqual(before, *ps) {
		t.Fatal("stale input changed state")
	}
	f = releaseFrame(3, "free")
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	f = releaseFrame(4, "free")
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	if ps.LastThrow == nil {
		t.Fatal("fixture no throw")
	}
	f = releaseFrame(5, "free")
	f.Observation.SourceID = "b"
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	if ps.LastThrow != nil || len(ps.ThrowHistory) != 0 || len(ps.PositionHistory) != 1 || ps.FrameDt != 0 {
		t.Fatal("source switch retained stale evidence")
	}
}

func TestReleaseFirstFreeAcrossGapOrInactiveIsOnlyAnAssessment(t *testing.T) {
	for _, inactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "gap", true: "inactive"}[inactive], func(t *testing.T) {
			fe, ps := releaseStart(t)
			var observations []model.ReleaseObservation
			var reasons []string
			fe.SetReleaseObserver(func(_ string, o model.ReleaseObservation, r string) {
				observations = append(observations, o)
				reasons = append(reasons, r)
			})
			f := releaseFrame(3, "free")
			want := "release_observation_gap"
			if inactive {
				f.GamePhase = "round_over"
				want = "release_inactive_phase"
			} else {
				f.FrameIndex = 8
				f.Timestamp = 1
			}
			fe.UpdatePlayerState(ps, &f, feTestCtx())
			if ps.LastThrow != nil || len(fe.pendingReleases) != 0 || len(observations) != 1 || reasons[0] != want || observations[0].StartFrame != 2 || observations[0].EndFrame != f.FrameIndex || observations[0].EndTime != f.Timestamp {
				t.Fatalf("bad state-change assessment: %v %+v", reasons, observations)
			}
			f.FrameIndex++
			f.Timestamp += 0.05
			fe.UpdatePlayerState(ps, &f, feTestCtx())
			fe.DrainPendingReleases("eof")
			if len(observations) != 1 {
				t.Fatal("state-change assessment duplicated on later free sample")
			}
		})
	}
}
