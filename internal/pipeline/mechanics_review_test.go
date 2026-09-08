package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/throw"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func mechanicsFramesForTest() []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 6; i++ {
		f := cleanFrame("P", i)
		f.Observation = &model.ObservationContext{Source: "echo_http_session", SourceID: "recorder-A", Authority: "client_reported", TimeBasis: "recorder_prefix", SessionID: "M-TEST", FrameIndex: i, Timestamp: f.Timestamp}
		attachment := &model.DiscAttachment{State: "free"}
		if i < 3 {
			attachment = &model.DiscAttachment{State: "held", HolderID: "P", HandCandidates: []string{"right"}}
		}
		f.HasPossession = i < 3
		f.Disc = &model.DiscState{Position: f.RightHandPosition, Velocity: model.Vec3{0, 0, 18}, Attachment: attachment, PossessionKnown: true, IsHeld: i < 3}
		if i < 3 {
			f.Disc.PossessorID = "P"
			f.Disc.Velocity = model.Vec3{}
		}
		frames = append(frames, f)
	}
	return frames
}

func TestMechanicsReleaseAssessmentClonesNullableSamplesAndHidesPrivateSourceID(t *testing.T) {
	velocity := model.Vec3{4.7, 0, 0}
	release := &model.ReleaseObservation{PlayerID: "P", FirstFreeFrame: 3, StartTime: .1, EndTime: .2,
		Source: &model.ObservationContext{Source: "echo_http_session", SourceID: "http://private-user:secret@localhost/session", Authority: "claimed_engine", TimeBasis: "receive_time", SessionID: "S", FrameIndex: 3, Timestamp: .2},
		PlayerMovement: []model.MovementObservation{{FrameIndex: 2, Timestamp: .1, Position: model.Vec3{1, 2, 3}, ReportedVelocity: nil},
			{FrameIndex: 3, Timestamp: .2, Position: model.Vec3{2, 2, 3}, ReportedVelocity: &velocity}}}
	record := releaseAssessment(release, model.DefaultProjectRules())
	if record.RawSamples[0].ReportedVelocity != nil || record.RawSamples[1].ReportedVelocity == nil {
		t.Fatal("missing observed motion became zero")
	}
	velocity[0] = 0
	if record.RawSamples[1].ReportedVelocity[0] != 4.7 {
		t.Fatal("retained release input aliases mutable caller")
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "localhost") {
		t.Fatal("raw private recorder identifier leaked into evidence")
	}
	if record.EventID != release.EventID() || record.Result != model.MechanicsInconclusive || record.VerifiedEngineBuild != "" || record.ValidationReference != "" {
		t.Fatal("wire source claim became verified knowledge or changed event identity")
	}
}

func TestMechanicsReleaseDiagnosticsCannotCreateWrongPlayerOrExtraIncidents(t *testing.T) {
	p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{throw.NewThrow001(nil), throw.NewThrow003(nil)})
	c := newCoverageTracker(p.detectors, p.cfg, []string{"P"})
	detach := p.attachDecisionCoverage(c)
	defer detach()
	r := model.ReleaseObservation{PlayerID: "OTHER", FirstFreeFrame: 3, StartTime: .1, EndTime: .2}
	p.reviewCancelledRelease("P", r, "release_confirmation_unavailable")
	ps := &model.PlayerState{PlayerID: "P", LastThrow: &model.ThrowEvent{ThrowerID: "OTHER", FrameIndex: 3, Timestamp: .2, ReleaseWindow: &r}}
	p.reviewRelease(ps, 3)
	for _, d := range c.players["P"].Detectors {
		if d.MechanicsReview != nil && d.MechanicsReview.Total != 0 {
			t.Fatal("mismatched event was reassigned to enclosing player")
		}
	}
	if c.players["OTHER"] != nil {
		t.Fatal("diagnostic manufactured roster entry")
	}
	// A real low-coverage release produces two related diagnostics but zero
	// detector incidents, scores, or verified physics/settings conclusions.
	p2, _ := newPipeline(config.DefaultConfig(), []detect.Detector{throw.NewThrow001(nil), throw.NewThrow003(nil)})
	result, err := p2.ProcessMatch(context.Background(), matchCtx("P"), mechanicsFramesForTest()[:5])
	if err != nil {
		t.Fatal(err)
	}
	physics, settings := mechanicsLogFromResult(t, result, "THROW_001"), mechanicsLogFromResult(t, result, "THROW_003")
	if physics.Records[0].EventID != settings.Records[0].EventID || len(result.DetectionEvents) != 0 || result.PlayerCoverage["P"].Status != model.ReviewStatusInsufficientData {
		t.Fatalf("diagnostics inflated incidents or adequate coverage: %+v", result)
	}
}

func mechanicsLogFromResult(t *testing.T, r *MatchResult, id string) *model.MechanicsReviewLog {
	t.Helper()
	for _, d := range r.PlayerCoverage["P"].Detectors {
		if d.DetectorID == id {
			if d.MechanicsReview == nil {
				t.Fatal("missing log")
			}
			return d.MechanicsReview
		}
	}
	t.Fatal("missing check")
	return nil
}

func TestMechanicsReleaseEvidenceSameIdentityAndNoAuthorityEscalation(t *testing.T) {
	cfg := config.DefaultConfig()
	p, _ := newPipeline(cfg, []detect.Detector{throw.NewThrow001(nil), throw.NewThrow003(nil)})
	frames := mechanicsFramesForTest()
	// Same observed first-free input polled twice is one release, not two.
	frames = append(frames[:4], append([]model.PlayerTelemetryFrame{frames[3]}, frames[4:]...)...)
	r, err := p.ProcessMatch(context.Background(), matchCtx("P"), frames)
	if err != nil {
		t.Fatal(err)
	}
	physics, settings := mechanicsLogFromResult(t, r, "THROW_001"), mechanicsLogFromResult(t, r, "THROW_003")
	if physics.Total != 1 || settings.Total != 1 || physics.Inconclusive != 1 || settings.Inconclusive != 1 {
		t.Fatalf("physics=%+v settings=%+v", physics, settings)
	}
	a, b := physics.Records[0], settings.Records[0]
	if a.EventID != b.EventID || a.FrameIndex != 3 || a.IntervalStart != frames[2].Timestamp || a.IntervalEnd != frames[3].Timestamp {
		t.Fatalf("bad release identity: %+v %+v", a, b)
	}
	if a.Metrics["fast_throw_rule_mps"] != 19 || a.Metrics["required_movement_rule_mps"] != 4.7 || len(a.RawSamples) < 3 {
		t.Fatal(a)
	}
	if r.InvalidFrameReasons["stale_player_sample"] != 1 || p.Players()["P"].ThrowCount != 1 {
		t.Fatalf("duplicate counted: %+v", r)
	}
	if a.Validate() != nil || b.Validate() != nil {
		t.Fatal("invalid reproducible evidence")
	}
}

func TestMechanicsUnconfirmedReleaseOfflineAndLiveFinalizeExactlyOnce(t *testing.T) {
	for _, live := range []bool{false, true} {
		p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{throw.NewThrow001(nil)})
		p.SetSkipReset(live)
		mc := matchCtx("P")
		frames := mechanicsFramesForTest()[:4]
		r, err := p.ProcessMatch(context.Background(), mc, frames)
		if err != nil {
			t.Fatal(err)
		}
		if live {
			if mechanicsLogFromResult(t, r, "THROW_001").Total != 0 {
				t.Fatal("release prematurely finalized")
			}
			r = p.Finalize(mc)
		}
		log := mechanicsLogFromResult(t, r, "THROW_001")
		if log.Total != 1 || log.Records[0].Reason != "release_confirmation_unavailable" || log.ValidatedViolation != 0 || p.Players()["P"].ThrowCount != 0 {
			t.Fatalf("%+v", log)
		}
		if live && mechanicsLogFromResult(t, p.Finalize(mc), "THROW_001").Total != 0 {
			t.Fatal("EOF counted twice")
		}
	}
}

func TestPipelineStaleSamplesCannotRewindReleaseOrDerivatives(t *testing.T) {
	p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{throw.NewThrow001(nil)})
	p.SetSkipReset(true)
	mc := matchCtx("P")
	frames := mechanicsFramesForTest()
	if _, err := p.ProcessMatch(context.Background(), mc, frames); err != nil {
		t.Fatal(err)
	}
	before := *p.Players()["P"]
	r, err := p.ProcessMatch(context.Background(), mc, frames[2:4])
	if err != nil {
		t.Fatal(err)
	}
	after := p.Players()["P"]
	if r.FramesProcessed != 0 || after.LastFrameIdx != before.LastFrameIdx || after.FrameCount != before.FrameCount || after.Velocity != before.Velocity || after.ThrowCount != before.ThrowCount {
		t.Fatal("stale sample rewound state")
	}
	if mechanicsLogFromResult(t, r, "THROW_001").Total != 0 {
		t.Fatal("duplicate evidence")
	}
}
