package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func TestGroupedDatasetSplitsKeepSharedPlayersTogether(t *testing.T) {
	matches := []sqlite.StoredMatch{
		{Context: &model.MatchContext{MatchID: "M1", PlayerIDs: []string{"P1", "P2"}}},
		{Context: &model.MatchContext{MatchID: "M2", PlayerIDs: []string{"P3", "P1"}}},
		{Context: &model.MatchContext{MatchID: "M3", PlayerIDs: []string{"P4"}}},
	}
	splits, assignments := groupedDatasetSplits(matches)
	if len(assignments) != 3 || splits["M1"] == "" || splits["M1"] != splits["M2"] {
		t.Fatalf("shared-player splits = %+v, assignments=%+v", splits, assignments)
	}
}

func TestCalibrationMetricsCountMissesAndWilsonIntervals(t *testing.T) {
	samples := []calibrationSample{
		{MatchID: "M1", PlayerID: "P1", DetectorID: "THROW_001", FrameStart: 10, FrameEnd: 12, Truth: sqlite.GroundTruthPositive, Source: "ground_truth_window", PingBand: "low", CaptureBand: "standard", QualityBand: "good"},
		{MatchID: "M1", PlayerID: "P1", DetectorID: "THROW_001", FrameStart: 20, FrameEnd: 22, Truth: sqlite.GroundTruthNegative, Source: "ground_truth_window", PingBand: "low", CaptureBand: "standard", QualityBand: "good"},
	}
	events := map[string][]model.DetectionEvent{"M1": {{DetectorID: "THROW_001", PlayerID: "P1", FrameIndex: 21}}}
	metric := metricsForSamples(samples, events, map[string]string{"M1": "holdout"})["THROW_001"]
	if metric.Overall.FalseNegative != 1 || metric.Overall.FalsePositive != 1 || metric.Holdout.FalseNegative != 1 || metric.Holdout.FalsePositive != 1 {
		t.Fatalf("confusion metric = %+v", metric)
	}
	ci := wilson(0, 300)
	if ci.Upper <= 0 || ci.Upper >= .02 {
		t.Fatalf("wilson 0/300 = %+v", ci)
	}
}

func TestGroundTruthWindowSupersedesOverlappingEventReview(t *testing.T) {
	reviews := []sqlite.EventReview{{MatchID: "M1", PlayerID: "P1", DetectorID: "THROW_001", FrameIndex: 20, Verdict: "yes"}}
	opportunities := []sqlite.CalibrationOpportunity{{MatchID: "M1", PlayerID: "P1", DetectorID: "THROW_001", FrameStart: 18, FrameEnd: 22, GroundTruth: sqlite.GroundTruthPositive}}
	samples := samplesFromLabels(reviews, opportunities, map[string]matchCalibrationMeta{})
	if len(samples) != 1 || samples[0].Source != "ground_truth_window" {
		t.Fatalf("overlapping labels were double-counted: %+v", samples)
	}
}

func TestPromotionGateAcceptsRepresentativePassingEvidence(t *testing.T) {
	metric := detectorCalibrationMetric{
		DetectorID: "THROW_001",
		Overall: confusionMetric{TruePositive: 30, TrueNegative: 300, PositiveOpportunities: 30,
			NegativeOpportunities: 300, Players: 5, Matches: 10, Precision: 1, Recall: 1},
		Validation: confusionMetric{TruePositive: 5, TrueNegative: 50, PositiveOpportunities: 5,
			NegativeOpportunities: 50, Precision: 1, Recall: 1},
		Holdout: confusionMetric{TruePositive: 5, TrueNegative: 50, PositiveOpportunities: 5,
			NegativeOpportunities: 50, Precision: 1, Recall: 1},
	}
	applyPromotionGate(&metric)
	if !metric.Eligible || len(metric.Reasons) != 0 {
		t.Fatalf("passing metric was rejected: %+v", metric)
	}
}

func TestCalibrationFingerprintIgnoresReviewPostureButNotThresholds(t *testing.T) {
	base := config.DefaultConfig()
	posture := cloneConfig(base)
	detector := posture.Detectors["THROW_001"]
	detector.Mode, detector.EnforcementWeight, detector.AutoEnforce = "review", 0.9, true
	posture.Detectors["THROW_001"] = detector
	if calibrationFingerprint(base) != calibrationFingerprint(posture) {
		t.Fatal("review posture changed the detector-behavior fingerprint")
	}
	threshold := cloneConfig(base)
	detector = threshold.Detectors["THROW_001"]
	detector.Params["base_tolerance"] = detector.Params["base_tolerance"].(float64) + 0.1
	threshold.Detectors["THROW_001"] = detector
	if calibrationFingerprint(base) == calibrationFingerprint(threshold) {
		t.Fatal("threshold change did not change the detector-behavior fingerprint")
	}
}

func TestActiveProfileRequiresExactPromotionApproval(t *testing.T) {
	s, _ := newTestServer(t)
	cfg := cloneConfig(s.engine.Config())
	detector := cfg.Detectors["THROW_001"]
	detector.Enabled, detector.Mode, detector.EnforcementWeight, detector.AutoEnforce = true, "review", 0.9, true
	cfg.Detectors["THROW_001"] = detector
	doc, err := json.Marshal(cfg.Detectors)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.engine.Store().StoreConfigProfile(t.Context(), sqlite.ConfigProfile{
		Name: "gated-profile", DetectorsJSON: string(doc),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.engine.Store().ActivateConfigProfile(t.Context(), profile.Name); err != nil {
		t.Fatal(err)
	}

	applyActiveProfile(s.engine)
	got := s.engine.Config().GetDetectorConfig("THROW_001")
	if got.Mode != "shadow" || got.EnforcementWeight != 0 || got.AutoEnforce {
		t.Fatalf("unapproved profile did not fail closed: %+v", got)
	}

	if _, err := s.engine.Store().StoreDetectorPromotion(t.Context(), sqlite.DetectorPromotion{
		DetectorID: "THROW_001", ProfileName: profile.Name, Status: sqlite.PromotionActive,
		ConfigFingerprint: "stale-profile-fingerprint",
	}); err != nil {
		t.Fatal(err)
	}
	fresh := cloneConfig(config.DefaultConfig())
	fresh.General.LogLevel = "error"
	staleEngine := replay.NewEngine(fresh, s.engine.Store())
	applyActiveProfile(staleEngine)
	got = staleEngine.Config().GetDetectorConfig("THROW_001")
	if got.Mode != "shadow" || got.EnforcementWeight != 0 {
		t.Fatalf("stale profile approval did not fail closed: %+v", got)
	}

	if _, err := s.engine.Store().StoreDetectorPromotion(t.Context(), sqlite.DetectorPromotion{
		DetectorID: "THROW_001", ProfileName: profile.Name, Status: sqlite.PromotionActive,
		ConfigFingerprint: shortHash(doc),
	}); err != nil {
		t.Fatal(err)
	}
	approvedConfig := cloneConfig(config.DefaultConfig())
	approvedConfig.General.LogLevel = "error"
	approvedEngine := replay.NewEngine(approvedConfig, s.engine.Store())
	applyActiveProfile(approvedEngine)
	got = approvedEngine.Config().GetDetectorConfig("THROW_001")
	if got.Mode != "review" || got.EnforcementWeight != 0.9 || got.AutoEnforce {
		t.Fatalf("approved review profile was not applied safely: %+v", got)
	}
}

func TestDesktopStartupRevokesPromotionWhenGateNoLongerPasses(t *testing.T) {
	store, err := sqlite.NewStore(filepath.Join(t.TempDir(), "startup-gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.General.LogLevel = "error"
	detector := cfg.Detectors["THROW_001"]
	detector.Enabled, detector.Mode, detector.EnforcementWeight = true, "review", 0.9
	cfg.Detectors["THROW_001"] = detector
	doc, err := json.Marshal(cfg.Detectors)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.StoreConfigProfile(t.Context(), sqlite.ConfigProfile{Name: "startup-review", DetectorsJSON: string(doc)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateConfigProfile(t.Context(), profile.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreDetectorPromotion(t.Context(), sqlite.DetectorPromotion{
		DetectorID: "THROW_001", ProfileName: profile.Name, Status: sqlite.PromotionActive,
		ConfigFingerprint: shortHash(doc),
	}); err != nil {
		t.Fatal(err)
	}
	engine := replay.NewEngine(config.DefaultConfig(), store)
	s := newServer(engine, testToken)
	t.Cleanup(func() {
		s.quitOnce.Do(func() { close(s.quit) })
		select {
		case <-s.runtime.stopped:
		case <-time.After(5 * time.Second):
			t.Error("desktop runtime did not stop")
		}
		store.Close()
	})
	if got := engine.Config().GetDetectorConfig("THROW_001"); got.Mode != "shadow" || got.EnforcementWeight != 0 {
		t.Fatalf("startup did not fail closed: %+v", got)
	}
	promotion, ok, err := store.GetDetectorPromotion(t.Context(), "THROW_001")
	if err != nil || !ok || promotion.Status != sqlite.PromotionRolledBack {
		t.Fatalf("promotion after startup = %+v, %t, %v", promotion, ok, err)
	}
}

func TestCalibrationOpportunityAndBlindReviewAPI(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	if resp, out := upload(t, ts, true, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != http.StatusOK || !out.Results[0].OK {
		t.Fatalf("upload status=%d out=%+v", resp.StatusCode, out)
	}
	const matchID, playerID = "SYN-FIXTURE-001", "echovr:1001"
	var opportunity sqlite.CalibrationOpportunity
	resp := postJSONTest(t, base+"/api/calibration/opportunities", map[string]any{
		"match_id": matchID, "player_id": playerID, "detector_id": "THROW_001",
		"opportunity_kind": "throw", "frame_start": 10, "frame_end": 12,
		"ground_truth": "positive", "behavior_type": "verified missing signal", "blind_review": true,
	}, &opportunity)
	if resp.StatusCode != http.StatusOK || opportunity.OpportunityID == "" || !opportunity.BlindReview {
		t.Fatalf("opportunity status=%d body=%+v", resp.StatusCode, opportunity)
	}
	var dashboard calibrationDashboard
	if resp := getJSON(t, base+"/api/lab/validation", &dashboard); resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard status=%d", resp.StatusCode)
	}
	metric, ok := detectorMetric(dashboard, "THROW_001")
	if !ok || metric.Overall.PositiveOpportunities != 1 || metric.Overall.FalseNegative+metric.Overall.TruePositive != 1 || metric.Overall.CurrentProvenance != 1 || metric.Eligible {
		t.Fatalf("THROW_001 metric = %+v", metric)
	}
	if resp := postJSONTest(t, base+"/api/lab/promotions/THROW_001", map[string]any{}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("under-calibrated promotion status=%d", resp.StatusCode)
	}

	event := model.DetectionEvent{EventID: uuid.NewString(), DetectorID: "THROW_003", DetectorVersion: "test",
		MatchID: matchID, PlayerID: playerID, FrameIndex: 15, FrameRangeStart: 14, FrameRangeEnd: 16,
		Severity: .5, Confidence: .7, ObservedValue: "angle", ExpectedRange: "human range",
		CausalKey: model.CausalKey{PlayerID: playerID, FrameStart: 14, FrameEnd: 16, AnomalyType: "test"}, IsShadow: true}
	if err := s.engine.Store().StoreDetectionEvent(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	var review sqlite.EventReview
	resp = postJSONTest(t, base+"/api/event/"+event.EventID+"/review", map[string]any{"verdict": "no", "blind_review": true}, &review)
	if resp.StatusCode != http.StatusOK || !review.BlindReview {
		t.Fatalf("blind review status=%d body=%+v", resp.StatusCode, review)
	}
	if resp := deleteJSONTest(t, base+"/api/calibration/opportunities/"+opportunity.OpportunityID); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}
}

func TestImportedOpportunityIsNotMeasuredUntilReplayExists(t *testing.T) {
	s, ts := newTestServer(t)
	if err := s.engine.Store().ImportCalibrationOpportunity(t.Context(), sqlite.CalibrationOpportunity{
		MatchID: "NOT-YET-IMPORTED", PlayerID: "P1", DetectorID: "THROW_001",
		Kind: sqlite.OpportunityThrow, GroundTruth: sqlite.GroundTruthPositive,
	}); err != nil {
		t.Fatal(err)
	}
	var dashboard calibrationDashboard
	if resp := getJSON(t, ts.URL+"/"+testToken+"/api/lab/validation", &dashboard); resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard status=%d", resp.StatusCode)
	}
	if dashboard.Samples != 0 {
		t.Fatalf("unavailable imported replay contributed %d samples", dashboard.Samples)
	}
}

func TestCalibrationReportCarriesVerifiableProvenance(t *testing.T) {
	_, ts := newTestServer(t)
	var report calibrationReport
	resp := getJSON(t, ts.URL+"/"+testToken+"/api/lab/calibration-report", &report)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report status = %d", resp.StatusCode)
	}
	if report.Payload.SchemaVersion != "nevr-calibration-report/v1" || report.Payload.ConfigFingerprint == "" || report.Payload.CalibrationFingerprint == "" {
		t.Fatalf("report provenance = %+v", report.Payload)
	}
	raw, err := json.Marshal(report.Payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); report.SHA256 != got || resp.Header.Get("X-NEVR-Payload-SHA256") != got {
		t.Fatalf("report hash = %q header=%q want %q", report.SHA256, resp.Header.Get("X-NEVR-Payload-SHA256"), got)
	}
	if disposition := resp.Header.Get("Content-Disposition"); disposition == "" {
		t.Fatal("calibration report is missing download disposition")
	}
}

func deleteJSONTest(t *testing.T, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}
