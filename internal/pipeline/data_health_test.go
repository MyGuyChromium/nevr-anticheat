package pipeline

import (
	"context"
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func healthFrame(id string, i int) model.PlayerTelemetryFrame {
	f := cleanFrame(id, i)
	f.Observation = &model.ObservationContext{Source: "test", SourceID: "a", Authority: "client_reported", SessionID: "health", TimeBasis: "fixture", FrameIndex: i, Timestamp: f.Timestamp}
	v := model.Vec3{0, 0, 0}
	f.ReportedVelocity = &v
	f.Disc = &model.DiscState{Position: model.Vec3{0, 1, 0}}
	return f
}

func TestHealthLiveRecoverySourceScopeAndDetachedSnapshots(t *testing.T) {
	cfg := testConfig("enforce")
	p, _ := newPipeline(cfg, []detect.Detector{movement.NewMov001(nil), bio.NewBio001(nil)})
	p.SetSkipReset(true)
	mc := matchCtx("a", "b")
	var initial *model.DataHealth
	for i := 0; i <= healthRecoverySamples; i++ {
		a, b := healthFrame("a", i), healthFrame("b", i)
		b.Observation.SourceID = "b"
		if i == 0 {
			a.Observation = nil
		}
		res, err := p.ProcessMatch(context.Background(), mc, []model.PlayerTelemetryFrame{a, b})
		if err != nil {
			t.Fatal(err)
		}
		ha, hb := res.PlayerCoverage["a"].DataHealth, res.PlayerCoverage["b"].DataHealth
		if hb.State != model.HealthHealthy {
			t.Fatalf("unrelated source degraded: %+v", hb)
		}
		if i == 0 {
			initial = ha
			if ha.State != model.HealthBlind {
				t.Fatalf("missing source not blind: %+v", ha)
			}
		}
		// The first restored source also starts a fresh recovery window.
		if i > 0 && i <= healthRecoverySamples && ha.State == model.HealthHealthy {
			t.Fatalf("early recovery at %d", i)
		}
	}
	f := healthFrame("a", healthRecoverySamples+1)
	res, err := p.ProcessMatch(context.Background(), mc, []model.PlayerTelemetryFrame{f})
	if err != nil {
		t.Fatal(err)
	}
	if h := res.PlayerCoverage["a"].DataHealth; h.State != model.HealthHealthy || h.BlindSamples == 0 {
		t.Fatalf("recovery/evidence lost: %+v", h)
	}
	if initial.State != model.HealthBlind || initial.RecoverySamplesRemaining != healthRecoverySamples {
		t.Fatal("returned snapshot aliased mutable live health")
	}
}

func TestHealthSharedJumpIsReviewOnlyAndQuaternionSignInvariant(t *testing.T) {
	cfg := testConfig("enforce")
	p, _ := newPipeline(cfg, []detect.Detector{bio.NewBio001(nil), movement.NewMov001(nil)})
	p.SetSkipReset(true)
	mc := matchCtx("a", "b", "c", "other")
	var result *MatchResult
	for i := 0; i < 2; i++ {
		var frames []model.PlayerTelemetryFrame
		for _, id := range []string{"a", "b", "c", "other"} {
			f := healthFrame(id, i)
			if id == "other" {
				f.Observation.SourceID = "other"
			}
			if i == 1 {
				f.Rotation = model.Quat{0, math.Sin(.49 * math.Pi), 0, math.Cos(.49 * math.Pi)}
			}
			frames = append(frames, f)
		}
		var err error
		result, err = p.ProcessMatch(context.Background(), mc, frames)
		if err != nil {
			t.Fatal(err)
		}
	}
	h := result.PlayerCoverage["a"].DataHealth
	if h.State != model.HealthDegraded || len(h.AffectedDetectors) != 1 || h.AffectedDetectors[0] != "BIO_001" {
		t.Fatalf("wrongly scoped shared jump: %+v", h)
	}
	if result.PlayerCoverage["other"].DataHealth.State != model.HealthHealthy {
		t.Fatal("jump on another feed corroborated shared fault")
	}
	ev := model.DetectionEvent{DetectorID: "BIO_001", PlayerID: "a", AutoEnforce: true, EnforcementWeight: 1}
	p.applyEvidenceSafety(&ev)
	if !ev.IsShadow || ev.EnforcementWeight != 0 || ev.AutoEnforce {
		t.Fatalf("unsafe event %+v", ev)
	}
	f := healthFrame("a", 2)
	f.Rotation = p.players["a"].Rotation
	for i := range f.Rotation {
		f.Rotation[i] = -f.Rotation[i]
	}
	if len(p.sharedOrientationJumps([]model.PlayerTelemetryFrame{f})) != 0 {
		t.Fatal("q/-q manufactured a discontinuity")
	}
}

func TestHealthMissingDiscDoesNotSuspendMovementReview(t *testing.T) {
	cfg := testConfig("enforce")
	p, _ := newPipeline(cfg, []detect.Detector{movement.NewMov001(nil), bio.NewBio001(nil)})
	frames := []model.PlayerTelemetryFrame{healthFrame("a", 0), healthFrame("a", 1)}
	frames[0].Disc, frames[1].Disc = nil, nil
	res, err := p.ProcessMatch(context.Background(), matchCtx("a"), frames)
	if err != nil {
		t.Fatal(err)
	}
	h := res.PlayerCoverage["a"].DataHealth
	if h.State != model.HealthDegraded || len(h.AffectedDetectors) != 0 {
		t.Fatalf("missing disc leaked to unrelated checks: %+v", h)
	}
}

func TestHealthMissingSourceKeepsFindingsButCannotScore(t *testing.T) {
	cfg := testConfig("enforce")
	p, _ := newPipeline(cfg, []detect.Detector{movement.NewMov001(nil)})
	res, err := p.ProcessMatch(context.Background(), matchCtx("P1"), speedHackFrames("P1", 90))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DetectionEvents) == 0 {
		t.Fatal("lost review evidence")
	}
	for _, ev := range res.DetectionEvents {
		if !ev.IsShadow || ev.AutoEnforce || ev.EnforcementWeight != 0 {
			t.Fatalf("unsafe blind-feed event: %+v", ev)
		}
	}
	if res.PlayerCoverage["P1"].Status != model.ReviewStatusInsufficientData {
		t.Fatal("blind feed presented evaluated")
	}
}

func TestHealthRejectedOldSampleCannotRewindSnapshot(t *testing.T) {
	p, _ := newPipeline(testConfig("enforce"), []detect.Detector{movement.NewMov001(nil)})
	p.SetSkipReset(true)
	mc := matchCtx("a")
	frames := []model.PlayerTelemetryFrame{healthFrame("a", 10), healthFrame("a", 11)}
	first, err := p.ProcessMatch(context.Background(), mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	before := first.PlayerCoverage["a"].DataHealth
	old := healthFrame("a", 2)
	next, err := p.ProcessMatch(context.Background(), mc, []model.PlayerTelemetryFrame{old})
	if err != nil {
		t.Fatal(err)
	}
	after := next.PlayerCoverage["a"].DataHealth
	if after.State != model.HealthBlind || after.LastFrame != before.LastFrame || after.Revision <= before.Revision {
		t.Fatalf("fault lost/rewound: before=%+v after=%+v", before, after)
	}
}

func TestHealthRecoveredFeedDoesNotPromoteDelayedFaultEvidence(t *testing.T) {
	p, _ := newPipeline(testConfig("enforce"), []detect.Detector{movement.NewMov001(nil)})
	p.SetSkipReset(true)
	mc := matchCtx("a")
	for i := 0; i < 12; i++ {
		f := healthFrame("a", i)
		if i == 0 {
			f.Observation = nil
		}
		if _, err := p.ProcessMatch(context.Background(), mc, []model.PlayerTelemetryFrame{f}); err != nil {
			t.Fatal(err)
		}
	}
	if p.health["a"].summary.State != model.HealthHealthy {
		t.Fatal("fixture not recovered")
	}
	ev := model.DetectionEvent{PlayerID: "a", DetectorID: "MOV_001", FrameIndex: 11, FrameRangeStart: 0, FrameRangeEnd: 11, EnforcementWeight: 1}
	p.applyEvidenceSafety(&ev)
	if !ev.IsShadow || ev.EnforcementWeight != 0 {
		t.Fatal("delayed old fault became scored evidence after recovery")
	}
	ev = model.DetectionEvent{PlayerID: "a", DetectorID: "MOV_001", FrameIndex: 11, FrameRangeStart: 10, FrameRangeEnd: 11, CausalKey: model.CausalKey{FrameStart: 10, FrameEnd: 11}, EnforcementWeight: 1}
	p.applyEvidenceSafety(&ev)
	if ev.IsShadow || ev.EnforcementWeight != 1 {
		t.Fatal("genuinely post-recovery evidence suppressed")
	}
}
