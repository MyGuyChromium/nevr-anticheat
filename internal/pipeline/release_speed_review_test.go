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

// A synthetic authored release after warmup. All measurements are constructed,
// not derived from a private recording or a claimed legal/cheat population.
func speedReviewFrames(speeds ...float64) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, 0, 8+len(speeds))
	discPosition := model.Vec3{1.3, 1.3, 5.6}
	for i := 0; i < 8+len(speeds); i++ {
		f := cleanFrame("P", i)
		f.Observation = &model.ObservationContext{Source: "echo_http_session", SourceID: "https://private-token@example.invalid/session",
			Authority: "client_reported", TimeBasis: "recorder_prefix", SessionID: "M-TEST", FrameIndex: i, Timestamp: f.Timestamp}
		bounce := 0
		f.Disc = &model.DiscState{Position: f.RightHandPosition, PossessionKnown: true, SampledPlayerCount: 1, BounceCount: &bounce,
			Attachment: &model.DiscAttachment{State: "held", HolderID: "P", HandCandidates: []string{"right"}}, IsHeld: true, PossessorID: "P"}
		f.HasPossession = true
		if i >= 8 {
			if i > 8 {
				discPosition[2] += speeds[i-9] * .067
			}
			f.HasPossession = false
			f.Disc.Attachment = &model.DiscAttachment{State: "free"}
			f.Disc.IsHeld, f.Disc.PossessorID = false, ""
			f.Disc.Position = discPosition
			f.Disc.Velocity, f.Disc.Speed = model.Vec3{0, 0, speeds[i-8]}, speeds[i-8]
		}
		frames = append(frames, f)
	}
	return frames
}

func runSpeedReview(t *testing.T, frames []model.PlayerTelemetryFrame) (*MatchResult, model.ThrowEvidence) {
	t.Helper()
	cfg := config.DefaultConfig()
	if cfg.Physics.Constants().DiscSpeedCap != 18.9 || model.DefaultPhysics().DiscSpeedCap != 18.9 {
		t.Fatal("evidence fix changed the owner's configured threshold")
	}
	p, _ := newPipeline(cfg, []detect.Detector{throw.NewThrow001(nil)})
	result, err := p.ProcessMatch(context.Background(), matchCtx("P"), frames)
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsInvalid != 0 || len(result.DetectionEvents) != 1 {
		t.Fatalf("want one valid retained review; invalid=%d events=%+v", result.EventsInvalid, result.DetectionEvents)
	}
	event := result.DetectionEvents[0]
	if event.PlayerID != "P" || event.Timestamp != frames[8].Timestamp || event.AutoEnforce || !event.IsShadow {
		t.Fatalf("changed actor/release time/review-only policy: %+v", event)
	}
	evidence, ok := event.Evidence.(model.ThrowEvidence)
	if !ok || evidence.SpeedReview == nil || evidence.SpeedReview.ReleaseFrame != 8 || evidence.EffectiveCap != 18.9 {
		t.Fatalf("missing or incorrectly anchored evidence: %+v", event.Evidence)
	}
	if evidence.ReleaseSpeed != frames[8].Disc.Speed || evidence.SampledDiscSpeed != frames[8].Disc.Speed || evidence.ReleaseVelocity != frames[8].Disc.Velocity {
		t.Fatalf("raw release input was overwritten: %+v", evidence)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil || strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), "example.invalid") {
		t.Fatalf("evidence leaked private source or failed encoding: %s (%v)", encoded, err)
	}
	var roundtrip model.ThrowEvidence
	if err := json.Unmarshal(encoded, &roundtrip); err != nil || roundtrip.SpeedReview.Status != evidence.SpeedReview.Status || len(roundtrip.SpeedReview.Samples) != len(evidence.SpeedReview.Samples) {
		t.Fatalf("structured review did not survive persistence encoding: %+v (%v)", roundtrip, err)
	}
	return result, evidence
}

func TestReleaseSpeedReviewPipelineTransientAndRepeatedSamples(t *testing.T) {
	for _, tc := range []struct {
		name   string
		speeds []float64
		status string
		above  int
	}{
		{"first-free spike", []float64{30, 18, 18}, "uncorroborated", 1},
		{"three above policy", []float64{20, 20, 20}, "corroborated", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, ev := runSpeedReview(t, speedReviewFrames(tc.speeds...))
			r := ev.SpeedReview
			if r.Status != tc.status || r.AboveCapSamples != tc.above || len(r.Samples) != 3 || r.ResolvedFrame != 10 || r.RequiredSamples != 3 {
				t.Fatalf("wrong sampled comparison: %+v", r)
			}
			for i, sample := range r.Samples {
				if sample.FrameIndex != 8+i || sample.Speed != tc.speeds[i] {
					t.Fatalf("sample lost/replaced: %+v", r.Samples)
				}
			}
			if tc.status == "uncorroborated" && result.DetectionEvents[0].EnforcementWeight != 0 {
				t.Fatal("uncorroborated spike acquired scoring weight")
			}
		})
	}
}

func TestReleaseSpeedReviewPipelineCannotBridgeMissingEvidence(t *testing.T) {
	for _, kind := range []string{"eof", "inactive", "gap", "source", "bounce", "catch"} {
		t.Run(kind, func(t *testing.T) {
			frames := speedReviewFrames(20, 20, 20)
			switch kind {
			case "eof":
				frames = frames[:10]
			case "inactive":
				frames[10].GamePhase = "round_over"
			case "gap":
				frames[10].FrameIndex = 11
				frames[10].Timestamp += .067
				frames[10].Observation.FrameIndex = 11
				frames[10].Observation.Timestamp = frames[10].Timestamp
			case "source":
				frames[10].Observation.SourceEpoch++
			case "bounce":
				*frames[10].Disc.BounceCount = 1
			case "catch":
				frames[10].HasPossession = true
				frames[10].Disc.IsHeld, frames[10].Disc.PossessorID = true, "P"
				frames[10].Disc.Attachment = &model.DiscAttachment{State: "held", HolderID: "P", HandCandidates: []string{"right"}}
			}
			result, ev := runSpeedReview(t, frames)
			if ev.SpeedReview.Status != "uncorroborated" || ev.SpeedReview.Reason == "" || result.DetectionEvents[0].EnforcementWeight != 0 {
				t.Fatalf("%s became corroboration/scoring: %+v", kind, ev)
			}
		})
	}
}

func TestReleaseSpeedReviewPipelineLocalReportCannotReplaceSample(t *testing.T) {
	frames := speedReviewFrames(18, 18, 18)
	frames[8].GameLastThrow = &model.GameThrowDetails{TotalSpeed: 25, SpeedFromArm: 12, SpeedFromMovement: 4, SpeedFromWrist: 9}
	frames[8].GameLastThrowProvenance = frames[8].Observation.Clone()
	frames[8].GameLastThrowProvenance.SourcePlayerID, frames[8].GameLastThrowProvenance.Freshness = "P", "value_change"
	// Recorder identity is part of source continuity, not merely the report.
	for i := range frames {
		frames[i].Observation.SourcePlayerID = "P"
	}
	_, evidence := runSpeedReview(t, frames)
	if evidence.ReleaseSpeed != 18 || evidence.GameLastThrow == nil || evidence.GameLastThrow.TotalSpeed != 25 || evidence.SpeedReview.Status != "uncorroborated" {
		t.Fatalf("independent scalar replaced the measured vector or established authority: %+v", evidence)
	}
}

func TestReleaseSpeedReviewSourceBoundaryClosesIndependentIncidents(t *testing.T) {
	frames := speedReviewFrames(20, 20)
	second := speedReviewFrames(20, 20, 20)
	for _, frame := range second[6:] {
		frame.FrameIndex += 4
		frame.Timestamp += .067 * 4
		frame.Observation.FrameIndex, frame.Observation.Timestamp = frame.FrameIndex, frame.Timestamp
		frame.Observation.SourceEpoch = 1
		frames = append(frames, frame)
	}
	p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{throw.NewThrow001(nil)})
	r, err := p.ProcessMatch(context.Background(), matchCtx("P"), frames)
	if err != nil {
		t.Fatal(err)
	}
	if r.EventsInvalid != 0 || len(r.DetectionEvents) != 2 {
		t.Fatalf("recording boundary merged or lost evidence: invalid=%d events=%+v", r.EventsInvalid, r.DetectionEvents)
	}
	seen := map[int]bool{}
	for _, event := range r.DetectionEvents {
		evidence, ok := event.Evidence.(model.ThrowEvidence)
		if !ok || evidence.SpeedReview == nil {
			t.Fatalf("missing structured source evidence: %+v", event)
		}
		review := evidence.SpeedReview
		seen[review.ReleaseFrame] = true
		if review.ReleaseFrame == 8 && (review.Status != "uncorroborated" || len(review.Samples) != 2) {
			t.Fatalf("old source borrowed new-source samples: %+v", review)
		}
		if review.ReleaseFrame == 12 && (review.Status != "corroborated" || len(review.Samples) != 3) {
			t.Fatalf("new source lost its independent release: %+v", review)
		}
		if event.MergedCount > 1 || !event.IsShadow || event.AutoEnforce {
			t.Fatalf("source incidents merged or review safety changed: %+v", event)
		}
	}
	if !seen[8] || !seen[12] {
		t.Fatalf("wrong original release identities: %v", seen)
	}
}
