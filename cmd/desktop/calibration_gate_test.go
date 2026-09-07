package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strings"
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
	metric := representativeGateMetric()
	applyPromotionGate(&metric)
	if !metric.Eligible || len(metric.Reasons) != 0 {
		t.Fatalf("passing metric was rejected: %+v", metric)
	}
}

func representativeGateMetric() detectorCalibrationMetric {
	full := confusionMetric{TruePositive: 200, TrueNegative: 1000, PositiveOpportunities: 200,
		NegativeOpportunities: 1000, Players: 20, Matches: 20, Precision: 1, Recall: 1,
		IndependentEvidence: 1200, CurrentProvenance: 1200}
	half := confusionMetric{TruePositive: 100, TrueNegative: 500, PositiveOpportunities: 100,
		NegativeOpportunities: 500, Players: 10, Matches: 10, Precision: 1, Recall: 1,
		IndependentEvidence: 600, CurrentProvenance: 600}
	return detectorCalibrationMetric{DetectorID: "THROW_001", Overall: full, Validation: half, Holdout: half}
}

func TestPromotionGateRejectsWeakOrUnobservableEvidence(t *testing.T) {
	for name, modify := range map[string]func(*detectorCalibrationMetric){
		"old50percentRecall": func(m *detectorCalibrationMetric) { m.Overall.Recall = .5 },
		"uncertainPrecision": func(m *detectorCalibrationMetric) {
			m.Holdout.TruePositive = 60
			m.Holdout.TrueNegative = 10000
			m.Holdout.PositiveOpportunities = 80
			m.Holdout.NegativeOpportunities = 10000
		},
		"uncertainFalseRate": func(m *detectorCalibrationMetric) {
			m.Holdout.FalsePositive = 1
			m.Holdout.TrueNegative = 399
			m.Holdout.FalsePositiveRate = .0025
			m.Holdout.NegativeOpportunities = 400
		},
		"oneReviewer":         func(m *detectorCalibrationMetric) { m.Overall.IndependentEvidence-- },
		"stale":               func(m *detectorCalibrationMetric) { m.Overall.StaleProvenance = 1 },
		"badTelemetry":        func(m *detectorCalibrationMetric) { m.Overall.UnusableTelemetry = 1 },
		"crossSplitExposure":  func(m *detectorCalibrationMetric) { m.Overall.IsolationConflicts = 1 },
		"oneHoldoutPlayer":    func(m *detectorCalibrationMetric) { m.Holdout.Players = 1 },
		"walkingUnobservable": func(m *detectorCalibrationMetric) { m.DetectorID = "MOV_006" },
		"wristAliased":        func(m *detectorCalibrationMetric) { m.DetectorID = "BIO_001" },
	} {
		t.Run(name, func(t *testing.T) {
			m := representativeGateMetric()
			modify(&m)
			applyPromotionGate(&m)
			if m.Eligible {
				t.Fatalf("unsafe promotion: %+v", m)
			}
		})
	}
}

func TestPromotionEvidenceNeedsIndependentWindowNotEventLabel(t *testing.T) {
	meta := map[string]matchCalibrationMeta{"M": {CurrentProvenance: true, QualityBand: "excellent"}}
	window := sqlite.CalibrationOpportunity{MatchID: "M", PlayerID: "P", DetectorID: "THROW_001", FrameStart: 10, FrameEnd: 12,
		GroundTruth: sqlite.GroundTruthPositive, ReviewerID: "reviewer-a", VerifierID: "reviewer-b", BlindReview: true,
		VerifiedGroundTruth: sqlite.GroundTruthPositive, EvidenceMethod: "controlled_reproduction", EvidenceReference: "trial-42/frame-10-12"}
	samples := samplesFromLabels(nil, []sqlite.CalibrationOpportunity{window}, meta)
	if len(samples) != 1 || !samples[0].IndependentEvidence || samples[0].UnusableTelemetry {
		t.Fatalf("verified window: %+v", samples)
	}
	reviews := []sqlite.EventReview{{MatchID: "M", PlayerID: "P", DetectorID: "THROW_001", FrameIndex: 50, Verdict: "yes", BlindReview: true}}
	samples = samplesFromLabels(reviews, []sqlite.CalibrationOpportunity{window}, meta)
	if samples[0].IndependentEvidence {
		t.Fatal("event-only review became independent release evidence")
	}
	metrics := metricsForSamples(samples, nil, map[string]string{"M": "quarantined"})["THROW_001"]
	if metrics.Overall.IsolationConflicts != 2 || metrics.Training.PositiveOpportunities != 0 || metrics.Holdout.PositiveOpportunities != 0 {
		t.Fatalf("quarantine leaked into metrics: %+v", metrics)
	}
}

func TestExperimentEndpointsRejectHoldoutAndQuarantine(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	for _, split := range []string{"holdout", "quarantined"} {
		t.Run(split, func(t *testing.T) {
			id := "EXPERIMENT-" + split
			match := sqlite.StoredMatch{Context: &model.MatchContext{MatchID: id, PlayerIDs: []string{id + "-player"}}}
			if err := s.engine.Store().StoreMatchContext(t.Context(), match.Context, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := s.engine.Store().ReconcileCalibrationSplits(t.Context(), []sqlite.StoredMatch{match}, map[string]string{id: "holdout"}); err != nil {
				t.Fatal(err)
			}
			if split == "quarantined" {
				if err := s.engine.Store().RecordCalibrationExposure(t.Context(), id); err != nil {
					t.Fatal(err)
				}
			}
			for _, endpoint := range []string{"/api/lab/experiments", "/api/lab/thresholds/preview"} {
				resp := postJSONTest(t, base+endpoint, map[string]any{"scope": "match", "match_id": id, "detector_id": "THROW_001", "parameter": "base_tolerance", "value": 1, "values": []float64{1}}, nil)
				if resp.StatusCode != http.StatusConflict {
					t.Fatalf("%s accepted %s: %d", endpoint, split, resp.StatusCode)
				}
			}
		})
	}
}

func TestCalibrationExposureRejectsDirtyUnknownOrStaleCandidates(t *testing.T) {
	revision := strings.Repeat("a", 40)
	info := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "false"}}}
	if !trustedCalibrationBuild(info, revision) {
		t.Fatal("clean identifiable build rejected")
	}
	if trustedCalibrationBuild(nil, revision) || trustedCalibrationBuild(info, "development") || trustedCalibrationBuild(info, strings.Repeat("b", 40)) {
		t.Fatal("unknown revision trusted")
	}
	info.Settings[1].Value = "true"
	if trustedCalibrationBuild(info, revision) {
		t.Fatal("dirty build trusted")
	}
	run := sqlite.AnalysisRun{AppVersion: "1", BuildCommit: revision, CalibrationFingerprint: "behavior-A"}
	fp := exposureCandidateFingerprint(run, "1", revision, "behavior-A", true)
	if fp == "" {
		t.Fatal("identifiable candidate lost")
	}
	for _, got := range []string{
		exposureCandidateFingerprint(run, "2", revision, "behavior-A", true),
		exposureCandidateFingerprint(run, "1", strings.Repeat("b", 40), "behavior-A", true),
		exposureCandidateFingerprint(run, "1", revision, "behavior-B", true),
		exposureCandidateFingerprint(run, "1", revision, "behavior-A", false),
	} {
		if got != "" {
			t.Fatal("stale/untrusted provenance created a candidate lock")
		}
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

func TestImportedEmptyPlayerWindowsAreUnobservableNotConfusionEvidence(t *testing.T) {
	s, ts := newTestServer(t)
	if resp, out := upload(t, ts, true, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != http.StatusOK || !out.Results[0].OK {
		t.Fatalf("upload status=%d out=%+v", resp.StatusCode, out)
	}
	for i, truth := range []string{sqlite.GroundTruthPositive, sqlite.GroundTruthNegative} {
		if err := s.engine.Store().ImportCalibrationOpportunity(t.Context(), sqlite.CalibrationOpportunity{
			MatchID: "SYN-FIXTURE-001", PlayerID: "echovr:1001", DetectorID: "THROW_001",
			Kind: sqlite.OpportunityThrow, FrameStart: 100000 + i*100, FrameEnd: 100001 + i*100, GroundTruth: truth,
			ReviewerID: "Alice", VerifierID: "Bob", VerifiedGroundTruth: truth, BlindReview: true,
			EvidenceMethod: "controlled_reproduction", EvidenceReference: "independent-trial",
		}); err != nil {
			t.Fatal(err)
		}
	}
	dashboard, err := s.buildCalibrationDashboard(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	metric, ok := detectorMetric(dashboard, "THROW_001")
	x := metric.Overall
	if !ok || dashboard.Samples != 2 || x.Uncertain != 2 || x.UnusableTelemetry != 2 || x.IndependentEvidence != 0 ||
		x.PositiveOpportunities != 0 || x.NegativeOpportunities != 0 || x.TruePositive+x.FalsePositive+x.TrueNegative+x.FalseNegative != 0 || metric.Eligible {
		t.Fatalf("missing telemetry became measured evidence: %+v", metric)
	}
	items, err := s.engine.Store().ListCalibrationOpportunities(t.Context(), "SYN-FIXTURE-001", "THROW_001")
	if err != nil || len(items) != 2 {
		t.Fatalf("annotations were not preserved: %+v, %v", items, err)
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
	if report.Payload.Dashboard.SealedHoldout || report.Payload.Dashboard.ProductionValidated || report.Payload.Dashboard.ReleaseEligible || len(report.Payload.Dashboard.ValidationLimitations) == 0 {
		t.Fatal("human-review metrics must not claim sealed external validation or release approval")
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
