package pipeline

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func gameEvidenceFrame(player string, frame int) model.PlayerTelemetryFrame {
	f := cleanFrame(player, frame)
	f.Observation = &model.ObservationContext{Source: "replay", SourceID: "https://synthetic.invalid/?token=private", Authority: "client_reported", TimeBasis: "capture", SessionID: "M-TEST", FrameIndex: frame, Timestamp: f.Timestamp}
	known := true
	f.IsStunnedKnown, f.StunsKnown = &known, &known
	head := model.Vec3{1, 1.8, 5}
	f.HeadPosition = &head
	return f
}

func gameEvidenceGrabFrames(count int, phase string) []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < count; i++ {
		f := gameEvidenceFrame("P", i)
		f.GamePhase = phase
		a := &model.DiscAttachment{State: "free"}
		if i >= 1 {
			a = &model.DiscAttachment{State: "held", HolderID: "P", HandCandidates: []string{"right"}}
		}
		f.HasPossession = i >= 1
		f.Disc = &model.DiscState{Position: model.Vec3{4, 2, 3}, Attachment: a, PossessionKnown: true, IsHeld: i >= 1}
		if i >= 1 {
			f.Disc.PossessorID = "P"
		}
		frames = append(frames, f)
	}
	return frames
}

func gameEvidenceLog(t *testing.T, result *MatchResult, player, detector string) *model.MechanicsReviewLog {
	t.Helper()
	coverage := result.PlayerCoverage[player]
	if coverage == nil {
		t.Fatalf("missing coverage for %s", player)
	}
	for _, entry := range coverage.Detectors {
		if entry.DetectorID == detector {
			if entry.MechanicsReview == nil {
				t.Fatalf("missing %s mechanics log", detector)
			}
			return entry.MechanicsReview
		}
	}
	t.Fatalf("missing detector coverage %s", detector)
	return nil
}

func assertGameEvidenceUnscored(t *testing.T, r *MatchResult) {
	t.Helper()
	if len(r.DetectionEvents) != 0 || len(r.ReviewCases) != 0 || r.EventsInvalid != 0 {
		t.Fatalf("review-only evidence affected incident flow: %+v", r)
	}
	for _, score := range r.PlayerScores {
		if score.TotalScore != 0 || score.EventCount != 0 || score.ExceedsReview || score.ExceedsAutoEnforce {
			t.Fatalf("mechanics evidence changed score: %+v", score)
		}
	}
	for _, coverage := range r.PlayerCoverage {
		if coverage.GameRuleProfile == nil || coverage.GameRuleProfile.Reference.Applicability != model.RuleReferenceOnly {
			t.Fatal("missing reference-only default profile")
		}
		for _, entry := range coverage.Detectors {
			if entry.MechanicsReview == nil {
				continue
			}
			for _, record := range entry.MechanicsReview.Records {
				if err := record.Validate(); err != nil {
					t.Fatal(err)
				}
				if record.Result != model.MechanicsInconclusive || record.RuleReference == nil || record.RuleReference.Applicability != model.RuleReferenceOnly || record.VerifiedEngineBuild != "" || record.ValidationReference != "" {
					t.Fatalf("reference or observation became a verdict: %+v", record)
				}
				data, _ := json.Marshal(record)
				if strings.Contains(string(data), "token=private") || strings.Contains(string(data), "synthetic.invalid") {
					t.Fatal("source credential leaked into mechanics evidence")
				}
			}
		}
	}
}

func TestGameEvidenceMagsActiveAndNonPlayConfirmation(t *testing.T) {
	// Compile-time guard: a return-signature mismatch must not silently send
	// STATE001 through FlushPhase instead of its non-play observation path.
	var _ PhaseReviewObserver = state.NewState001(nil)
	for _, phase := range []string{"playing", "pre_match", "post_match"} {
		t.Run(phase, func(t *testing.T) {
			p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{state.NewState001(nil)})
			r, err := p.ProcessMatch(context.Background(), matchCtx("P"), gameEvidenceGrabFrames(3, phase))
			if err != nil {
				t.Fatal(err)
			}
			assertGameEvidenceUnscored(t, r)
			log := gameEvidenceLog(t, r, "P", "STATE_001")
			if log.Total != 1 || len(log.Records) != 1 || log.Inconclusive != 1 {
				t.Fatalf("non-play observer not dispatched continuously: %+v", log)
			}
			a := log.Records[0]
			want := "grab_geometry_unverified"
			if phase != "playing" {
				want = "grab_non_play_observation"
			}
			if a.Reason != want || a.FrameIndex != 1 || a.Metrics["sampled_possession_confirmed"] != 1 || a.Metrics["confirmation_frame"] != 2 || len(a.RawSamples) != 3 {
				t.Fatalf("original transition/confirmation context lost: %+v", a)
			}
		})
	}
}

func TestGameEvidenceMagsOfflineAndLiveEOF(t *testing.T) {
	for _, live := range []bool{false, true} {
		p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{state.NewState001(nil)})
		p.SetSkipReset(live)
		mc := matchCtx("P")
		r, err := p.ProcessMatch(context.Background(), mc, gameEvidenceGrabFrames(2, "playing"))
		if err != nil {
			t.Fatal(err)
		}
		if live {
			if gameEvidenceLog(t, r, "P", "STATE_001").Total != 0 {
				t.Fatal("pending holder was published before confirmation/EOF")
			}
			r = p.Finalize(mc)
		}
		assertGameEvidenceUnscored(t, r)
		log := gameEvidenceLog(t, r, "P", "STATE_001")
		if log.Total != 1 || log.Records[0].Reason != "grab_possession_unconfirmed_end_of_stream" || log.Records[0].Metrics["sampled_possession_confirmed"] != 0 {
			t.Fatalf("EOF lost unconfirmed transition: %+v", log)
		}
		if live && gameEvidenceLog(t, p.Finalize(mc), "P", "STATE_001").Total != 0 {
			t.Fatal("EOF duplicated mechanics record")
		}
	}
}

func TestGameEvidenceMagsSourceAndPhaseLifecycle(t *testing.T) {
	for _, boundary := range []string{"source", "phase"} {
		t.Run(boundary, func(t *testing.T) {
			frames := gameEvidenceGrabFrames(2, "playing")
			for i, attachment := range []string{"free", "held", "held"} {
				f := gameEvidenceGrabFrames(3, "playing")[i]
				f.FrameIndex = i + 2
				f.Timestamp = float64(i+2) * .067
				f.Observation.FrameIndex, f.Observation.Timestamp = f.FrameIndex, f.Timestamp
				if boundary == "source" {
					f.Observation.SourceID = "source-B"
				} else {
					f.GamePhase = "pre_match"
				}
				if f.Disc.Attachment.State != attachment {
					t.Fatal("broken synthetic fixture")
				}
				frames = append(frames, f)
			}
			p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{state.NewState001(nil)})
			r, err := p.ProcessMatch(context.Background(), matchCtx("P"), frames)
			if err != nil {
				t.Fatal(err)
			}
			assertGameEvidenceUnscored(t, r)
			log := gameEvidenceLog(t, r, "P", "STATE_001")
			if log.Total != 2 || len(log.Records) != 2 {
				t.Fatalf("boundary dropped or merged independent transitions: %+v", log)
			}
			first, second := log.Records[0], log.Records[1]
			if first.FrameIndex != 1 || first.Reason != "grab_possession_unconfirmed_"+boundary+"_changed" || second.FrameIndex != 3 || second.Metrics["sampled_possession_confirmed"] != 1 || first.EventID == second.EventID {
				t.Fatalf("boundary context lost: %+v", log.Records)
			}
		})
	}
}

func gameEvidenceStunFrames(count int) []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < count; i++ {
		victim, candidate := gameEvidenceFrame("P", i), gameEvidenceFrame("Q", i)
		victim.Team, candidate.Team = "blue", "orange"
		victim.IsStunned = i >= 1
		if i >= 1 {
			candidate.Stuns = 1
		}
		frames = append(frames, victim, candidate)
	}
	return frames
}

func TestGameEvidenceDefaultStunDiagnosticsIndependentOfDisabledHeuristic(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.Detectors["STATE_007"].Enabled {
		t.Fatal("test requires disabled legacy heuristic")
	}
	p, _ := newPipeline(cfg, nil) // no detector instances: this is independent diagnostics
	r, err := p.ProcessMatch(context.Background(), matchCtx("P", "Q"), gameEvidenceStunFrames(7))
	if err != nil {
		t.Fatal(err)
	}
	assertGameEvidenceUnscored(t, r)
	log := gameEvidenceLog(t, r, "P", "STATE_007")
	if log.Total != 1 || len(log.Records) != 1 || log.Inconclusive != 1 {
		t.Fatalf("default onset diagnostics missing: %+v", log)
	}
	a := log.Records[0]
	if a.Reason != "stun_contact_geometry_unverified" || a.FrameIndex != 1 || len(a.StunCandidates) != 1 || a.StunCandidates[0].PlayerID != "Q" || a.StunCandidates[0].CounterBefore != 0 || a.StunCandidates[0].CounterAfter != 1 {
		t.Fatalf("onset/counter evidence lost: %+v", a)
	}
	for _, entry := range r.PlayerCoverage["P"].Detectors {
		if entry.DetectorID == "STATE_007" && (entry.Enabled || !entry.ReviewOnlyDiagnostics) {
			t.Fatal("legacy scoring enabled by diagnostics")
		}
	}
	if gameEvidenceLog(t, r, "Q", "STATE_007").Total != 0 {
		t.Fatal("candidate incorrectly became a victim finding")
	}
}

func TestGameEvidenceStunInterruptionsAndUnknownPresence(t *testing.T) {
	for _, mode := range []string{"gap", "source", "inactive", "missing_baseline", "missing_counter", "eof"} {
		t.Run(mode, func(t *testing.T) {
			frames := gameEvidenceStunFrames(7)
			want := ""
			switch mode {
			case "gap":
				frames = append(frames[:4], frames[6:]...)
				want = "stun_sample_gap"
			case "source":
				for i := 4; i < len(frames); i++ {
					frames[i].Observation.SourceID = "changed-source"
				}
				want = "stun_source_changed"
			case "inactive":
				for i := 4; i < len(frames); i++ {
					frames[i].GamePhase = "score"
				}
				want = "stun_inactive_phase"
			case "missing_baseline":
				frames[0].IsStunnedKnown = nil
			case "missing_counter":
				for i := range frames {
					if frames[i].PlayerID == "Q" {
						frames[i].StunsKnown = nil
					}
				}
				want = "stun_counter_evidence_unavailable"
			case "eof":
				frames = frames[:4]
				want = "stun_end_of_stream"
			}
			p, _ := newPipeline(config.DefaultConfig(), nil)
			r, err := p.ProcessMatch(context.Background(), matchCtx("P", "Q"), frames)
			if err != nil {
				t.Fatal(err)
			}
			assertGameEvidenceUnscored(t, r)
			log := gameEvidenceLog(t, r, "P", "STATE_007")
			if mode == "missing_baseline" {
				if log.Total != 0 {
					t.Fatal("unknown baseline became observed false→true onset")
				}
				return
			}
			if log.Total != 1 || len(log.Records) != 1 || log.Records[0].Reason != want || log.Records[0].FrameIndex != 1 {
				t.Fatalf("interrupted onset mismatch: %+v", log)
			}
			if mode == "missing_counter" && len(log.Records[0].StunCandidates) != 0 {
				t.Fatal("unknown counter generated attribution candidate")
			}
		})
	}
}

func TestGameEvidenceStunLiveChunksEqualOfflineAndFinalizeOnce(t *testing.T) {
	frames := gameEvidenceStunFrames(2)
	offline, _ := newPipeline(config.DefaultConfig(), nil)
	want, err := offline.ProcessMatch(context.Background(), matchCtx("P", "Q"), frames)
	if err != nil {
		t.Fatal(err)
	}
	live, _ := newPipeline(config.DefaultConfig(), nil)
	live.SetSkipReset(true)
	mc := matchCtx("P", "Q")
	for i := 0; i < len(frames); i += 2 {
		r, err := live.ProcessMatch(context.Background(), mc, frames[i:i+2])
		if err != nil {
			t.Fatal(err)
		}
		assertGameEvidenceUnscored(t, r)
		if gameEvidenceLog(t, r, "P", "STATE_007").Total != 0 {
			t.Fatal("association ended before EOF")
		}
	}
	got := live.Finalize(mc)
	assertGameEvidenceUnscored(t, got)
	if !reflect.DeepEqual(gameEvidenceLog(t, want, "P", "STATE_007").Records, gameEvidenceLog(t, got, "P", "STATE_007").Records) {
		t.Fatal("live chunking changed onset evidence")
	}
	if gameEvidenceLog(t, live.Finalize(mc), "P", "STATE_007").Total != 0 {
		t.Fatal("live EOF retained duplicate onset")
	}
}
