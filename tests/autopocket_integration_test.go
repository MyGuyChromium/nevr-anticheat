package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/catalog"
	"github.com/nevr-anticheat/nevr-anticheat/internal/ingest"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// This coherent, synthetic curve exercises software contracts only. It is not
// a verified autopocket signature or independent evidence of real-play accuracy.
// It mirrors state.catchTestFlight, but passes normalized telemetry through the
// real validator/extractor (including exact heads and explicit observations).
func autopocketIntegrationFrames() []model.PlayerTelemetryFrame {
	const free, dt = 10, 1.0 / 15
	positions, velocities := make([]model.Vec3, free), make([]model.Vec3, free)
	positions[0] = model.Vec3{3, 2, 1}
	for i := 0; i < free; i++ {
		angle := 0.0
		if i >= 4 {
			angle = float64(i-3) * 8 * math.Pi / 180
		}
		velocities[i] = model.Vec3{10 * math.Cos(angle), 10 * math.Sin(angle), 0}
		if i > 0 {
			positions[i] = positions[i-1].Add(velocities[i-1].Add(velocities[i]).Scale(dt / 2))
		}
	}
	hand := positions[free-1].Add(velocities[free-1].Normalized().Scale(.9))
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < free+2; i++ {
		position, velocity, holder := hand, model.Vec3{}, "receiver"
		if i < free {
			position, velocity, holder = positions[i], velocities[i], ""
		}
		for _, id := range []string{"receiver", "other"} {
			left, team := hand, "blue"
			if id == "other" {
				left, team = model.Vec3{3, 2, 20}, "orange"
			}
			head, bounce := left.Add(model.Vec3{0, 1.2, 0}), 0
			attachment := &model.DiscAttachment{State: "free"}
			if holder != "" {
				attachment.State, attachment.HolderID, attachment.HandCandidates = "held", holder, []string{"left", "right"}
			}
			frames = append(frames, model.PlayerTelemetryFrame{
				PlayerID: id, Team: team, FrameIndex: i + 1, Timestamp: 10 + float64(i)*dt, DeltaTime: dt,
				Observation: &model.ObservationContext{Source: "synthetic", Authority: "client_reported", TimeBasis: "capture", SessionID: "catch-integration", FrameIndex: i + 1, Timestamp: 10 + float64(i)*dt},
				Position:    left.Add(model.Vec3{0, 1, 0}), Rotation: model.QuatIdentity(), HeadPosition: &head,
				LeftHandPosition: left, RightHandPosition: left.Add(model.Vec3{0, 0, .5}),
				LeftHandRotation: model.QuatIdentity(), RightHandRotation: model.QuatIdentity(),
				GamePhase: "playing", EstimatedPingMs: 30, HasPossession: holder == id,
				Disc: &model.DiscState{Position: position, Velocity: velocity, Speed: velocity.Magnitude(), Attachment: attachment,
					PossessorID: holder, IsHeld: holder != "", PossessionKnown: true, SampledPlayerCount: 2, BounceCount: &bounce},
			})
		}
	}
	return frames
}

func requireAutopocketObservation(t *testing.T, events []model.DetectionEvent) model.DetectionEvent {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("expected one synthetic catch observation, got %d: %+v", len(events), events)
	}
	event := events[0]
	if event.DetectorID != "STATE_008" || event.PlayerID != "receiver" || !event.IsShadow || event.AutoEnforce || event.EnforcementWeight != 0 {
		t.Fatalf("catch review must remain attributed observation-only: %+v", event)
	}
	evidence, ok := event.Evidence.(model.StateEvidence)
	if !ok || len(evidence.CatchTrajectory) != 10 || evidence.CatchTrajectory[9].FrameIndex != 10 ||
		evidence.Metrics["confirmed_catch_frame"] != 12 || len(evidence.Limitations) < 3 ||
		!strings.Contains(evidence.Attribution, "cause and actor unverified") {
		t.Fatalf("catch review lost its free-flight evidence or attribution limits: %#v", event.Evidence)
	}
	return event
}

func requireAutopocketSameEvidence(t *testing.T, want, got model.DetectionEvent) {
	t.Helper()
	// Persistence does not retain the in-memory dedup count; compare every
	// other signature field, including time range, severity and confidence.
	a, b := sortedSignatures([]model.DetectionEvent{want}), sortedSignatures([]model.DetectionEvent{got})
	a[0].Merged, b[0].Merged = 0, 0
	if !reflect.DeepEqual(a, b) || want.Timestamp != got.Timestamp || want.DetectorVersion != got.DetectorVersion {
		t.Fatalf("catch observation signature changed: want=%+v got=%+v", a, b)
	}
	wantJSON, err := json.Marshal(want.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("catch evidence changed:\nwant %s\ngot  %s", wantJSON, gotJSON)
	}
}

func requireAutopocketNoStoredConsequences(t *testing.T, store *sqlite.Store) {
	t.Helper()
	// These are fresh test-only databases. Global zero counts also catch an
	// incorrectly attributed score, case, or enforcement action for another ID.
	for _, table := range []string{"suspicion_scores", "review_cases", "cross_match_review_cases", "enforcement_actions"} {
		var count int
		if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("observation-only catch created %d rows in %s", count, table)
		}
	}
}

func TestAutopocketPipelineStorageAndLiveChunksStayObservationOnly(t *testing.T) {
	for _, mode := range []string{"shadow", "adversarial_enforce"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			h := testutil.NewHarness(t).WithDetectors("STATE_008")
			cfg := h.Config()
			cfg.Shadow.ShadowDetectors = nil
			dc := cfg.Detectors["STATE_008"]
			dc.Mode = "shadow"
			if mode == "adversarial_enforce" {
				// Deliberately bypass config.Validate, as a caller embedding the
				// library could. Constructor and emissions must still fail closed.
				dc.Mode, dc.EnforcementWeight, dc.AutoEnforce = "enforce", 1, true
			}
			cfg.Detectors["STATE_008"] = dc
			mc := &model.MatchContext{
				MatchID: "synthetic-catch-" + mode, GameMode: "Echo_Arena", TickRate: 15,
				PlayerIDs: []string{"receiver", "other"}, TeamAssignments: map[string]string{"receiver": "blue", "other": "orange"},
				Physics: cfg.Physics.Constants(),
			}
			h.WithMatchContext(mc)
			run := func(frames []model.PlayerTelemetryFrame) *pipeline.MatchResult {
				t.Helper()
				p, _ := h.NewPipeline()
				result, err := p.ProcessMatch(ctx, mc, frames)
				if err != nil {
					t.Fatal(err)
				}
				if result.InvalidFrames != 0 || result.EventsInvalid != 0 {
					t.Fatalf("invalid fixture: invalid frames=%d (%v) invalid events=%d",
						result.InvalidFrames, result.InvalidFrameReasons, result.EventsInvalid)
				}
				if result.TelemetryQuality.Gated {
					t.Fatalf("fixture quality gating would mask the detector's own observation-only safeguard: %+v", result.TelemetryQuality)
				}
				// The scorer may expose an empty in-memory entry for an observed
				// player; it must have no contribution, event, or threshold flag.
				for pid, score := range result.PlayerScores {
					if score.TotalScore != 0 || score.BaseScore != 0 || score.CorrelationBonus != 0 || score.EventCount != 0 ||
						score.ExceedsReview || score.ExceedsAutoEnforce || len(score.ScoreByDetector) != 0 || len(score.ScoreByCategory) != 0 {
						t.Fatalf("catch observation scored %s: %+v", pid, score)
					}
				}
				return result
			}
			frames := autopocketIntegrationFrames()
			result := run(frames)
			want := requireAutopocketObservation(t, result.DetectionEvents)
			store := newTestStore(t)
			if n, err := store.StoreTelemetryFrames(ctx, mc.MatchID, frames); err != nil || n != len(frames) {
				t.Fatalf("stored telemetry=%d/%d error=%v", n, len(frames), err)
			}
			stored, err := replay.StoreMatchAnalysis(ctx, store, mc, result, "initial", replay.AnalysisOptions{Logger: quietLogger()})
			if err != nil || stored.EventsStored != 1 || stored.ScoresStored != 0 || stored.CasesStored != 0 || len(result.ReviewCases) != 0 {
				t.Fatalf("stored catch analysis=%+v cases=%+v error=%v", stored, result.ReviewCases, err)
			}
			events, err := store.GetMatchEvents(ctx, mc.MatchID)
			if err != nil {
				t.Fatal(err)
			}
			requireAutopocketSameEvidence(t, want, requireAutopocketObservation(t, events))
			requireAutopocketNoStoredConsequences(t, store)
			loaded, err := store.GetMatchFrames(ctx, mc.MatchID)
			if err != nil || len(loaded) != len(frames) {
				t.Fatalf("loaded frames=%d error=%v", len(loaded), err)
			}
			reprocessed := run(loaded)
			requireAutopocketSameEvidence(t, want, requireAutopocketObservation(t, reprocessed.DetectionEvents))
			replaced, err := replay.ReplaceMatchAnalysis(ctx, store, mc, reprocessed, "reprocess", replay.AnalysisOptions{Logger: quietLogger()})
			if err != nil || replaced.EventsStored != 1 || replaced.ScoresStored != 0 || replaced.CasesStored != 0 || len(reprocessed.ReviewCases) != 0 {
				t.Fatalf("reprocessed catch analysis=%+v cases=%+v error=%v", replaced, reprocessed.ReviewCases, err)
			}
			events, err = store.GetMatchEvents(ctx, mc.MatchID)
			if err != nil {
				t.Fatal(err)
			}
			requireAutopocketSameEvidence(t, want, requireAutopocketObservation(t, events))
			requireAutopocketNoStoredConsequences(t, store)

			for _, chunkSize := range []int{1, 3, len(frames)} {
				t.Run(fmt.Sprintf("live_chunk_%d", chunkSize), func(t *testing.T) {
					liveStore := newTestStore(t)
					mm := ingest.NewMatchManager(cfg, liveStore, func() []detect.Detector { return catalog.Build(cfg, nil) }, quietLogger())
					t.Cleanup(mm.Close)
					// Odd chunk sizes deliberately split the two-player snapshot;
					// MatchManager must close ticks before evaluating possession.
					liveFrames := autopocketIntegrationFrames()
					for start := 0; start < len(liveFrames); start += chunkSize {
						end := min(start+chunkSize, len(liveFrames))
						batch := liveFrames[start:end]
						accepted := mm.HandleFrames(mc.MatchID, "", batch)
						if accepted.Accepted != len(batch) || accepted.Rejected != 0 || accepted.Ignored != 0 {
							t.Fatalf("live batch starting at %d: %+v", start, accepted)
						}
					}
					mm.EndMatch(mc.MatchID)
					liveEvents, err := liveStore.GetMatchEvents(ctx, mc.MatchID)
					if err != nil {
						t.Fatal(err)
					}
					requireAutopocketSameEvidence(t, want, requireAutopocketObservation(t, liveEvents))
					requireAutopocketNoStoredConsequences(t, liveStore)
				})
			}
		})
	}
}

func TestAutopocketLegacyNormalizedInputsRemainUnavailableAfterStorage(t *testing.T) {
	for _, missing := range []string{"head", "bounce_count", "possession_observation", "attachment", "source"} {
		t.Run(missing, func(t *testing.T) {
			frames := autopocketIntegrationFrames()
			for i := range frames {
				switch missing {
				case "head":
					frames[i].HeadPosition = nil
				case "bounce_count":
					frames[i].Disc.BounceCount = nil
				case "possession_observation":
					frames[i].Disc.PossessionKnown = false
					frames[i].Disc.SampledPlayerCount = 0
				case "attachment":
					frames[i].Disc.Attachment = nil // old booleans stay true but cannot grant knowledge
				case "source":
					frames[i].Observation = nil
				}
			}
			h := testutil.NewHarness(t).WithDetectors("STATE_008")
			mc := &model.MatchContext{
				MatchID: "legacy-catch-" + missing, TickRate: 15, Physics: h.Config().Physics.Constants(),
				PlayerIDs: []string{"receiver", "other"}, TeamAssignments: map[string]string{"receiver": "blue", "other": "orange"},
			}
			store, ctx := newTestStore(t), context.Background()
			if n, err := store.StoreTelemetryFrames(ctx, mc.MatchID, frames); err != nil || n != len(frames) {
				t.Fatalf("stored telemetry=%d/%d error=%v", n, len(frames), err)
			}
			loaded, err := store.GetMatchFrames(ctx, mc.MatchID)
			if err != nil || len(loaded) != len(frames) {
				t.Fatalf("loaded frames=%d error=%v", len(loaded), err)
			}
			for path, input := range map[string][]model.PlayerTelemetryFrame{"direct": frames, "stored": loaded} {
				p, _ := h.NewPipeline()
				result, err := p.ProcessMatch(ctx, mc, input)
				if err != nil {
					t.Fatal(err)
				}
				if result.InvalidFrames != 0 || result.FramesProcessed != len(frames)/2 || len(result.DetectionEvents) != 0 {
					t.Fatalf("%s: missing %s was treated as observation or rejected legacy frames: %+v", path, missing, result)
				}
			}
		})
	}
}
