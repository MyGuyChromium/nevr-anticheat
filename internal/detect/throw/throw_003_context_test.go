package throw

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func angleObservedFixture() map[string]*model.PlayerState {
	players := withThrow("p1", 10, 12, 179)
	ps := players["p1"]
	te := ps.LastThrow
	observed := 11
	te.ObservedFrameIndex = &observed
	te.HandVelocity = model.Vec3{-5 * math.Cos(math.Pi/180), 5 * math.Sin(math.Pi/180), 0}
	te.HandSpeed = 5
	te.PlayerVelocity = model.Vec3{1, 0, 0}
	te.HandRelativeVelocity = te.HandVelocity.Sub(te.PlayerVelocity)
	te.HandRelativeSpeed = te.HandRelativeVelocity.Magnitude()
	te.HandTracked = true
	te.HandAttributionConfidence = .8
	te.HandAttributionAnchor = "last_held_disc"
	source := &model.ObservationContext{Source: "echo_http_session", SourceID: "https://private.example/source?token=secret",
		Authority: "client_reported", TimeBasis: "replay_timestamp", SessionID: "throw-test", SourcePlayerID: "recorder",
		FrameIndex: te.FrameIndex, Timestamp: te.Timestamp}
	reported := model.Vec3{1, 0, 0}
	te.ReleaseWindow = &model.ReleaseObservation{PlayerID: "p1", FirstFreeFrame: 10, StartFrame: 9, EndFrame: 10,
		StartTime: 9 * .067, EndTime: te.Timestamp, Source: source, HandCandidates: []string{"right"},
		PlayerMovement: []model.MovementObservation{{FrameIndex: 9, Timestamp: 9 * .067, Position: model.Vec3{1, 2, 3}, ReportedVelocity: &reported}}}
	ps.LastFrameIdx, ps.LastTimestamp = observed, 11*.067
	ps.Observation = source.Clone()
	ps.Observation.FrameIndex, ps.Observation.Timestamp = observed, ps.LastTimestamp
	return players
}

func TestThrow003RetainsRawMotionAndReferenceNotWristSetting(t *testing.T) {
	players := angleObservedFixture()
	te := players["p1"].LastThrow
	before, err := json.Marshal(te)
	if err != nil {
		t.Fatal(err)
	}
	d := NewThrow003(nil)
	events := d.Evaluate(testCtx(), players, 11)
	if len(events) != 1 {
		t.Fatalf("expected supported comparison, got %d", len(events))
	}
	ev := events[0]
	e := ev.Evidence.(model.ReleaseAngleEvidence)
	if ev.FrameIndex != 10 || ev.Timestamp != te.Timestamp || e.ReleaseFrame != 10 || e.ObservedFrame != 11 || ev.FrameRangeEnd != 11 {
		t.Fatalf("release and confirmation identities conflated: %+v", ev)
	}
	if e.ReleaseAngle != te.ReleaseAngle || e.DiscVelocity != te.ReleaseVelocity || e.HandVelocity != te.HandVelocity ||
		e.HandRelativeVelocity != te.HandRelativeVelocity || e.PlayerVelocity != te.PlayerVelocity || e.BodyTranslationFactor != .8 ||
		e.HandAttributionConfidence != .8 || e.HandAttributionAnchor != te.HandAttributionAnchor || !e.HandTracked {
		t.Fatalf("raw motion or attribution lost: %+v", e)
	}
	wantConfidence := model.SigmoidConfidence(te.ReleaseAngle, 177, .1) * .8 * .9 * .8
	if math.Abs(ev.Confidence-wantConfidence) > 1e-12 || d.maxAngleDev != 177 || ev.EnforcementWeight != .5 {
		t.Fatalf("evidence work changed threshold or scoring: %+v", ev)
	}
	if e.Behavior != model.BehaviorReleaseDirection || e.Measurement != "world_hand_velocity_vs_first_free_disc_velocity" ||
		e.SourceStatus != "same_source_sampled_transition_not_authoritative" || e.NativeAssistanceReference == nil ||
		e.NativeAssistanceReference.ProfileID != model.DefaultGameRuleProfileID ||
		e.NativeAssistanceReference.Applicability != "reference_only_recording_configuration_unverified" ||
		e.NativeAssistanceStatus != "enabled_in_reference_defaults_recording_setting_unobserved" || len(e.Limitations) < 4 ||
		!strings.Contains(e.ContactStatus, "unresolved") {
		t.Fatalf("measurement limits or source reference missing: %+v", e)
	}
	raw, err := json.Marshal(ev)
	if err != nil || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "private.example") {
		t.Fatalf("private source path leaked or invalid persistent evidence: %v", err)
	}
	var decoded model.DetectionEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, ok := decoded.Evidence.(model.ReleaseAngleEvidence); !ok || !reflect.DeepEqual(got, e) {
		t.Fatalf("typed persisted evidence changed: %T", decoded.Evidence)
	}
	after, _ := json.Marshal(te)
	if string(before) != string(after) {
		t.Fatal("evidence preparation mutated source throw")
	}
	e.ReleaseWindow.Source.SourceEpoch = 90
	e.ReleaseWindow.HandCandidates[0] = "left"
	(*e.ReleaseWindow.PlayerMovement[0].ReportedVelocity)[0] = 90
	ev.Attribution.PlayerID = "different"
	if te.ReleaseWindow.Source.SourceEpoch != 0 || te.ReleaseWindow.HandCandidates[0] != "right" ||
		(*te.ReleaseWindow.PlayerMovement[0].ReportedVelocity)[0] != 1 || te.Attribution.PlayerID != "p1" {
		t.Fatal("mutable evidence aliases source metadata")
	}
}

func TestThrow003LegalAssistanceAndContactExamplesRemainNonVerdicts(t *testing.T) {
	// These are synthetic direction examples, not an implementation or
	// independent calibration of the engine's native assistance transformation.
	for _, degrees := range []float64{0, 10, 22.5, 45, 90, 135, 177} {
		players := angleObservedFixture()
		te := players["p1"].LastThrow
		te.ReleaseAngle = degrees
		te.HandVelocity = model.Vec3{5, 0, 0}
		te.ReleaseVelocity = model.Vec3{12 * math.Cos(degrees*math.Pi/180), 12 * math.Sin(degrees*math.Pi/180), 0}
		if got := NewThrow003(nil).Evaluate(testCtx(), players, 11); len(got) != 0 {
			t.Fatalf("below-threshold sampled direction %.1f generated a wrist claim", degrees)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*model.ThrowEvent)
	}{
		{"body translation", func(te *model.ThrowEvent) { te.PlayerVelocity = model.Vec3{te.HandSpeed, 0, 0} }},
		{"unknown slap hand", func(te *model.ThrowEvent) { te.ThrowingHand = "unknown" }},
		{"ambiguous slap hand", func(te *model.ThrowEvent) { te.HandAttributionConfidence = 0 }},
		{"headbutt candidate", func(te *model.ThrowEvent) { te.PossibleHeadContact = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			players := angleObservedFixture()
			tc.mutate(players["p1"].LastThrow)
			if got := NewThrow003(nil).Evaluate(testCtx(), players, 11); len(got) != 0 {
				t.Fatalf("uncertain contact/translation generated release direction finding: %+v", got)
			}
		})
	}
}

func TestThrow003RejectsContradictoryFrameAndSourceContext(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		mutate       func(*model.PlayerState)
	}{
		{"stale state", "stale_player_context", func(ps *model.PlayerState) { ps.LastFrameIdx-- }},
		{"other thrower", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.ThrowerID = "other" }},
		{"other attribution", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.Attribution.PlayerID = "other" }},
		{"future release", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.FrameIndex = 12 }},
		{"delayed stale release", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.FrameIndex = 8 }},
		{"nonfinite release", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.Timestamp = math.NaN() }},
		{"different interval player", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.PlayerID = "other" }},
		{"interval gap", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.StartFrame = 7 }},
		{"interval rewind", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.StartTime = 1 }},
		{"bad interval time", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.EndTime++ }},
		{"bad source frame", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.Source.FrameIndex-- }},
		{"bad confirmation frame", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.Observation.FrameIndex-- }},
		{"unbound confirmation time", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastTimestamp++ }},
		{"repeated confirmation time", "release_angle_context_invalid", func(ps *model.PlayerState) {
			ps.Observation.Timestamp = ps.LastThrow.Timestamp
			ps.LastTimestamp = ps.LastThrow.Timestamp
		}},
		{"bad source payload", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.Source.Authority = "" }},
		{"nonfinite movement", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.PlayerMovement[0].Position[0] = math.Inf(1) }},
		{"unknown hand label", "release_angle_context_invalid", func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.HandCandidates[0] = "unexpected" }},
		{"changed recorder", "release_angle_source_changed", func(ps *model.PlayerState) { ps.Observation.SourceID = "different-recorder" }},
		{"changed epoch", "release_angle_source_changed", func(ps *model.PlayerState) { ps.Observation.SourceEpoch++ }},
		{"changed session", "release_angle_source_changed", func(ps *model.PlayerState) { ps.Observation.SessionID = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			players := angleObservedFixture()
			tc.mutate(players["p1"])
			d := NewThrow003(nil)
			reason := ""
			d.SetDecisionObserver(func(_, _ string, _ int, r string) { reason = r })
			if got := d.Evaluate(testCtx(), players, 11); len(got) != 0 || reason != tc.reason {
				t.Fatalf("inconsistent context accepted or wrong reason: events=%d reason=%s", len(got), reason)
			}
		})
	}
}

func TestThrow003MissingSourceContextIsExplicitlyUnknown(t *testing.T) {
	for _, tc := range []struct {
		status string
		mutate func(*model.PlayerState)
	}{
		{"release_window_unavailable", func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow = nil }},
		{"release_source_unavailable", func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.Source = nil }},
		{"release_source_recorded_confirmation_source_unavailable", func(ps *model.PlayerState) { ps.Observation = nil }},
	} {
		players := angleObservedFixture()
		tc.mutate(players["p1"])
		got := NewThrow003(nil).Evaluate(testCtx(), players, 11)
		if len(got) != 1 || got[0].Evidence.(model.ReleaseAngleEvidence).SourceStatus != tc.status {
			t.Fatalf("legacy/missing context was silently certified: %+v", got)
		}
	}
}
