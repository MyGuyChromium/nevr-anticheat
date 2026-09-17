package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// Until these tests every call to POST /api/lab/promotions/{id} expected 422,
// so the code that WRITES a promotion (one detector to review, every other
// detector forced to shadow / weight 0 / auto_enforce=false) never ran.
//
// Nothing is mocked: the library below is stored through the same store calls
// the app uses, the evidence is a real hash-bound artifact with two immutable
// ballots per window, and the real handler evaluates the real gate.

const (
	teethPromoted         = "THROW_001"
	teethMatchesPerSplit  = 10
	teethPositivesInMatch = 8  // 10 matches x 8  =  80 per evaluation split
	teethNegativesInMatch = 40 // 10 matches x 40 = 400 per evaluation split
)

// teethSeedPromotableLibrary stores the smallest library that satisfies every
// promotion requirement for THROW_001: 20 single-player matches (10 holdout, 10
// validation; each its own connected group), 160 detected positive and 800
// quiet legitimate windows, all three ping and capture-rate bands, and every
// THROW legal-context control.
func teethSeedPromotableLibrary(t *testing.T, s *server) {
	t.Helper()
	ctx := context.Background()
	store := s.engine.Store()
	// Test database only: ~4000 tiny transactions are slow with fsync.
	if _, err := store.DB().Exec(`PRAGMA synchronous=OFF`); err != nil {
		t.Fatal(err)
	}

	// The split is a pure function of the player roster, so rosters are chosen
	// to land where the policy needs them.
	var players []string
	perSplit := map[string]int{}
	for i := 0; len(players) < 2*teethMatchesPerSplit; i++ {
		if i > 10000 {
			t.Fatal("could not find player ids for both evaluation splits")
		}
		id := fmt.Sprintf("teeth:%04d", i)
		split := splitForGroup("players:" + id)
		if (split == "holdout" || split == "validation") && perSplit[split] < teethMatchesPerSplit {
			perSplit[split]++
			players = append(players, id)
		}
	}

	artifact, err := store.StoreBlindArtifact(ctx, "teeth-evidence.bin", []byte("synthetic evidence bytes for the promotion write-path test"))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := s.blindCandidateFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	pings := []float64{40, 100, 200}
	tickRates := []float64{10, 20, 30}
	legalControls := []string{"normal", "stack", "block_push", "slap", "headbutt"}

	for i, playerID := range players {
		matchID := fmt.Sprintf("TEETH-PROMOTION-%02d", i)
		if err := store.StoreMatchContext(ctx, &model.MatchContext{MatchID: matchID, PlayerIDs: []string{playerID}, TickRate: tickRates[i%3]}, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := store.StoreAnalysisRun(ctx, sqlite.AnalysisRun{MatchID: matchID, Source: "teeth", AppVersion: appVersion,
			BuildCommit: analysisBuildRevision(), CalibrationFingerprint: calibrationFingerprint(s.engine.Config()),
			TelemetryQuality: 95, QualityGrade: "good"}); err != nil {
			t.Fatal(err)
		}
		summary, err := json.Marshal(replay.MatchSummary{MatchID: matchID, Players: []*replay.PlayerSummary{{PlayerID: playerID, PingAvg: pings[i%3]}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.StoreMatchSummaryJSON(ctx, sqlite.MatchSummaryMeta{MatchID: matchID}, summary); err != nil {
			t.Fatal(err)
		}

		var frames []model.PlayerTelemetryFrame
		var events []model.DetectionEvent
		type window struct {
			start int
			truth string
			legal string
		}
		var windows []window
		for k := 0; k < teethPositivesInMatch+teethNegativesInMatch; k++ {
			w := window{start: 10 * (k + 1), truth: sqlite.GroundTruthNegative, legal: legalControls[k%len(legalControls)]}
			if k >= teethNegativesInMatch {
				w.truth, w.legal = sqlite.GroundTruthPositive, "normal"
				events = append(events, model.DetectionEvent{EventID: uuid.NewString(), DetectorID: teethPromoted, DetectorVersion: "teeth",
					MatchID: matchID, PlayerID: playerID, FrameIndex: w.start, FrameRangeStart: w.start, FrameRangeEnd: w.start + 1,
					Severity: .8, Confidence: .9, IsShadow: true,
					CausalKey: model.CausalKey{PlayerID: playerID, FrameStart: w.start, FrameEnd: w.start + 1, AnomalyType: teethPromoted}})
			}
			windows = append(windows, w)
			frames = append(frames, model.PlayerTelemetryFrame{PlayerID: playerID, FrameIndex: w.start, Timestamp: float64(w.start) / 30})
		}
		if n, err := store.StoreTelemetryFrames(ctx, matchID, frames); err != nil || n != len(frames) {
			t.Fatalf("stored %d of %d frames: %v", n, len(frames), err)
		}
		if n, err := store.StoreDetectionEvents(ctx, events, "initial"); err != nil || n != len(events) {
			t.Fatalf("stored %d of %d events: %v", n, len(events), err)
		}
		for _, w := range windows {
			session, err := store.CreateBlindReviewSession(ctx, sqlite.BlindReviewBinding{MatchID: matchID, PlayerID: playerID,
				DetectorID: teethPromoted, Kind: sqlite.OpportunityThrow, BehaviorType: "teeth fixture", FrameStart: w.start, FrameEnd: w.start + 1,
				EvidenceMethod: "controlled_reproduction", ArtifactSHA256: artifact.SHA256, LegalContext: w.legal}, candidate)
			if err != nil {
				t.Fatal(err)
			}
			for _, reviewer := range []string{"Reviewer A", "Reviewer B"} {
				if _, err := store.SubmitBlindReviewBallot(ctx, session.SessionID, candidate, sqlite.BlindReviewBallot{
					ReviewerID: reviewer, GroundTruth: w.truth, PreRevealAttestation: true}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.RevealBlindReview(ctx, session.SessionID, candidate); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// teethStandInForTrustedReleaseBuild is the one thing this test cannot be for
// real. Production freezes the holdout to a candidate identified by the VCS
// revision stamped into a clean release build; a `go test` binary has no such
// stamp, so production (correctly) quarantines every holdout match the first
// time the dashboard is evaluated - see
// TestTeethUnidentifiableBuildCannotPromoteEvenWithPerfectEvidence. This trigger
// undoes that quarantine in THIS test database only. It changes no production
// code path and no threshold; it stands in for "this is a trusted build".
func teethStandInForTrustedReleaseBuild(t *testing.T, s *server) {
	t.Helper()
	if _, err := s.engine.Store().DB().Exec(`CREATE TRIGGER teeth_trusted_build AFTER UPDATE OF quarantined ON calibration_split_assignments
		WHEN NEW.quarantined = 1 BEGIN
			UPDATE calibration_split_assignments SET quarantined = 0 WHERE match_id = NEW.match_id;
		END`); err != nil {
		t.Fatal(err)
	}
}

type teethPromotionReply struct {
	Error           string                    `json:"error"`
	AutoEnforce     *bool                     `json:"auto_enforce"`
	RestartRequired bool                      `json:"restart_required"`
	Profile         sqlite.ConfigProfile      `json:"profile"`
	Promotion       sqlite.DetectorPromotion  `json:"promotion"`
	Metric          detectorCalibrationMetric `json:"metric"`
}

func teethPromote(t *testing.T, base, detectorID string) (int, teethPromotionReply) {
	t.Helper()
	var reply teethPromotionReply
	resp := postJSONTest(t, base+"/api/lab/promotions/"+detectorID, map[string]any{}, &reply)
	return resp.StatusCode, reply
}

func teethActiveProfile(t *testing.T, s *server) (sqlite.ConfigProfile, bool) {
	t.Helper()
	profile, active, err := s.engine.Store().GetActiveConfigProfile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return profile, active
}

func teethActivateProfile(t *testing.T, s *server, name string, detectors map[string]config.DetectorConfig) {
	t.Helper()
	doc, err := json.Marshal(detectors)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.engine.Store().StoreConfigProfile(context.Background(), sqlite.ConfigProfile{Name: name, DetectorsJSON: string(doc)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.engine.Store().ActivateConfigProfile(context.Background(), profile.Name); err != nil {
		t.Fatal(err)
	}
}

// teethLaunchWithSelectedProfile puts the running detectors in the state a
// launch with a selected profile leaves them in (the profile's JSON applied to
// the engine) and returns those detectors. posture lets a test make the running
// configuration deliberately unsafe.
func teethLaunchWithSelectedProfile(t *testing.T, s *server, posture map[string]config.DetectorConfig) map[string]config.DetectorConfig {
	t.Helper()
	doc, err := json.Marshal(cloneConfig(s.engine.Config()).Detectors)
	if err != nil {
		t.Fatal(err)
	}
	var detectors map[string]config.DetectorConfig
	if err := json.Unmarshal(doc, &detectors); err != nil {
		t.Fatal(err)
	}
	for id, want := range posture {
		detector := detectors[id]
		detector.Enabled = true
		detector.Mode, detector.EnforcementWeight, detector.AutoEnforce = want.Mode, want.EnforcementWeight, want.AutoEnforce
		detectors[id] = detector
	}
	s.engine.Config().Detectors = detectors
	return cloneConfig(s.engine.Config()).Detectors
}

func TestTeethSuccessfulPromotionForcesEveryOtherDetectorToShadow(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	ctx := context.Background()

	// A running configuration that is NOT already safe: if the promotion code
	// merely copied it, these postures would leak into the stored profile.
	selected := teethLaunchWithSelectedProfile(t, s, map[string]config.DetectorConfig{
		"MOV_001":     {Mode: "review", EnforcementWeight: 0.9, AutoEnforce: true},
		"THROW_002":   {Mode: "enforce", EnforcementWeight: 1, AutoEnforce: true},
		teethPromoted: {Mode: "shadow", EnforcementWeight: 0, AutoEnforce: true},
	})
	teethSeedPromotableLibrary(t, s)
	teethStandInForTrustedReleaseBuild(t, s)

	// 1. A selected profile whose thresholds ARE the running thresholds does
	// not stand in the way (the changed-threshold case has its own test).
	teethActivateProfile(t, s, "teeth selected thresholds", selected)

	// 2. Only one detector may hold a promotion.
	if _, err := s.engine.Store().StoreDetectorPromotion(ctx, sqlite.DetectorPromotion{DetectorID: "MOV_001",
		ProfileName: "Calibration review MOV_001", Status: sqlite.PromotionActive, ConfigFingerprint: "teeth"}); err != nil {
		t.Fatal(err)
	}
	if status, reply := teethPromote(t, base, teethPromoted); status != http.StatusConflict || !strings.Contains(reply.Error, "roll back MOV_001 first") {
		t.Fatalf("second concurrent promotion: status=%d error=%q, want 409 naming MOV_001", status, reply.Error)
	}
	if profile, _ := teethActiveProfile(t, s); strings.HasPrefix(profile.Name, "Calibration review") {
		t.Fatalf("a refused promotion activated profile %q", profile.Name)
	}
	if changed, err := s.engine.Store().RollBackDetectorPromotion(ctx, "MOV_001"); err != nil || !changed {
		t.Fatalf("rolling back MOV_001: %t, %v", changed, err)
	}

	// 3. The promotion itself.
	status, reply := teethPromote(t, base, teethPromoted)
	if status != http.StatusOK {
		t.Fatalf("a fully evidenced detector was refused: status=%d error=%q block=%q reasons=%q", status, reply.Error, reply.Metric.PromotionBlock, reply.Metric.Reasons)
	}
	if reply.AutoEnforce == nil || *reply.AutoEnforce || !reply.RestartRequired || reply.Promotion.DetectorID != teethPromoted || reply.Promotion.Status != sqlite.PromotionActive {
		t.Fatalf("promotion reply = %+v", reply)
	}

	profile, active := teethActiveProfile(t, s)
	if !active || profile.Name != "Calibration review "+teethPromoted {
		t.Fatalf("active profile = %q (active=%t)", profile.Name, active)
	}
	var stored map[string]config.DetectorConfig
	if err := json.Unmarshal([]byte(profile.DetectorsJSON), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != len(config.DetectorSpecs()) {
		t.Fatalf("stored profile covers %d detectors, want all %d", len(stored), len(config.DetectorSpecs()))
	}
	for id, detector := range stored {
		if detector.AutoEnforce {
			t.Errorf("%s: a promotion profile must never auto-enforce", id)
		}
		if id == teethPromoted {
			if detector.Mode != "review" || !detector.Enabled || detector.EnforcementWeight <= 0 {
				t.Errorf("%s was not moved to scored human review: %+v", id, detector)
			}
			continue
		}
		if detector.Mode != "shadow" || detector.EnforcementWeight != 0 {
			t.Errorf("%s rode along with the promotion: mode=%q weight=%v (want shadow / 0)", id, detector.Mode, detector.EnforcementWeight)
		}
	}

	// The stored approval must be the one the next launch accepts, and what
	// that launch applies must be the same single-detector posture.
	promotion, ok, err := s.engine.Store().GetDetectorPromotion(ctx, teethPromoted)
	if err != nil || !ok || promotion.Status != sqlite.PromotionActive || promotion.ConfigFingerprint != shortHash([]byte(profile.DetectorsJSON)) {
		t.Fatalf("stored approval = %+v, %t, %v", promotion, ok, err)
	}
	nextLaunch := cloneConfig(config.DefaultConfig())
	nextLaunch.General.LogLevel = "error"
	engine := replay.NewEngine(nextLaunch, s.engine.Store())
	applyActiveProfile(engine)
	for id := range engine.Config().Detectors {
		got := engine.Config().GetDetectorConfig(id)
		if got.AutoEnforce || (id == teethPromoted) != (got.Mode == "review") || (id != teethPromoted && got.EnforcementWeight != 0) {
			t.Errorf("next launch would run %s as %+v", id, got)
		}
	}
}

// A selected profile whose thresholds differ from the running detectors means
// the stored evidence was not produced by what would be promoted. Everything
// else about this library would pass, so only that check can refuse it.
func TestTeethPromotionIsRefusedWhenTheSelectedProfileChangedThresholds(t *testing.T) {
	s, ts := newTestServer(t)
	selected := teethLaunchWithSelectedProfile(t, s, nil)
	teethSeedPromotableLibrary(t, s)
	teethStandInForTrustedReleaseBuild(t, s)

	changed := selected[teethPromoted]
	changed.Params["base_tolerance"] = changed.Params["base_tolerance"].(float64) + 0.25
	selected[teethPromoted] = changed
	teethActivateProfile(t, s, "teeth thresholds changed after analysis", selected)

	status, reply := teethPromote(t, ts.URL+"/"+testToken, teethPromoted)
	if status != http.StatusConflict || !strings.Contains(reply.Error, "re-analyze the library") {
		t.Fatalf("promotion under a changed threshold profile: status=%d error=%q, want 409", status, reply.Error)
	}
	if promotions, _ := s.engine.Store().ListDetectorPromotions(context.Background()); len(promotions) != 0 {
		t.Fatalf("a refused promotion was recorded: %+v", promotions)
	}
	if profile, _ := teethActiveProfile(t, s); strings.HasPrefix(profile.Name, "Calibration review") {
		t.Fatalf("a refused promotion activated profile %q", profile.Name)
	}
}

// The counterpart of teethStandInForTrustedReleaseBuild: the same perfect
// library, evaluated by this unidentifiable test binary WITHOUT the stand-in,
// must be refused because its holdout cannot be tied to a candidate.
func TestTeethUnidentifiableBuildCannotPromoteEvenWithPerfectEvidence(t *testing.T) {
	s, ts := newTestServer(t)
	teethSeedPromotableLibrary(t, s)
	status, reply := teethPromote(t, ts.URL+"/"+testToken, teethPromoted)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d, want 422", status)
	}
	quarantined := false
	for _, reason := range reply.Metric.Reasons {
		quarantined = quarantined || strings.Contains(reason, "quarantined cross-split exposure")
	}
	if !quarantined || reply.Metric.Holdout.PositiveOpportunities != 0 {
		t.Fatalf("holdout evidence from an unidentifiable build was accepted: holdout=%+v reasons=%q", reply.Metric.Holdout, reply.Metric.Reasons)
	}
	if promotions, _ := s.engine.Store().ListDetectorPromotions(context.Background()); len(promotions) != 0 {
		t.Fatalf("a refused promotion was recorded: %+v", promotions)
	}
	if _, active := teethActiveProfile(t, s); active {
		t.Fatal("a refused promotion activated a profile")
	}
}
