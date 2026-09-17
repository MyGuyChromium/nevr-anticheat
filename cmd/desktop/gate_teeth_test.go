package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
)

// The promotion gate is the desktop's enforcement of "never move a detector out
// of shadow without evidence". Every expectation below is written out by hand
// on purpose: a test that read promotionBlocked or the promotionMin* constants
// back from production code would keep passing when they are weakened.

// teethBlockedDetectors must never become promotable, whatever the statistics say.
var teethBlockedDetectors = []string{
	"BIO_001",
	"MOV_003", "MOV_004", "MOV_005", "MOV_006",
	"PAT_001", "PAT_002", "PAT_003", "PAT_004", "PAT_005",
	"STATE_001", "STATE_003", "STATE_004", "STATE_005", "STATE_006", "STATE_007", "STATE_008",
	"THROW_004", "THROW_005", "THROW_006", "THROW_007",
}

// teethPromotableDetectors is the complete set the gate may ever pass. Adding a
// detector here is a deliberate safety decision, not a test fix.
var teethPromotableDetectors = []string{
	"BIO_002", "BIO_003", "BIO_004", "MOV_001", "MOV_002", "STATE_002",
	"THROW_001", "THROW_002", "THROW_003", "THROW_008",
}

// teethPassingMetric passes every statistical requirement for detectorID's
// family, so the only thing left that can refuse it is the block list.
func teethPassingMetric(detectorID string) detectorCalibrationMetric {
	metric := representativeGateMetric()
	metric.DetectorID = detectorID
	for _, key := range []string{"normal", "transition", "lean", "stack", "block_push", "slap", "headbutt"} {
		metric.ByLegalContext[key] = confusionMetric{NegativeOpportunities: 20, Clusters: clusterDescription{NegativeGroups: 3}}
	}
	return metric
}

func TestTeethPassingMetricIsEligibleForEveryPromotableDetector(t *testing.T) {
	for _, id := range teethPromotableDetectors {
		metric := teethPassingMetric(id)
		applyPromotionGate(&metric)
		if !metric.Eligible || len(metric.Reasons) != 0 || metric.PromotionBlock != "" {
			t.Errorf("%s: the reference passing metric was refused: block=%q reasons=%q", id, metric.PromotionBlock, metric.Reasons)
		}
	}
}

func TestTeethBlockedDetectorsStayBlockedWithPerfectEvidence(t *testing.T) {
	for _, id := range teethBlockedDetectors {
		t.Run(id, func(t *testing.T) {
			if _, ok := config.DetectorSpecFor(id); !ok {
				t.Fatalf("%s is no longer a known detector; update teethBlockedDetectors deliberately", id)
			}
			metric := teethPassingMetric(id)
			applyPromotionGate(&metric)
			if metric.Eligible {
				t.Fatalf("%s became promotable on statistics alone", id)
			}
			if strings.TrimSpace(metric.PromotionBlock) == "" {
				t.Fatalf("%s has no promotion block; reasons=%q", id, metric.Reasons)
			}
			// Exactly one reason, and it is the block: the refusal must not be
			// an accident of the statistics supplied by this test.
			if !reflect.DeepEqual(metric.Reasons, []string{metric.PromotionBlock}) {
				t.Fatalf("%s was refused for something other than its block: %q", id, metric.Reasons)
			}
		})
	}
}

func TestTeethBlockAndPromotableListsCoverEveryDetector(t *testing.T) {
	listed := make(map[string]string)
	for _, id := range teethBlockedDetectors {
		listed[id] = "blocked"
	}
	for _, id := range teethPromotableDetectors {
		if listed[id] != "" {
			t.Fatalf("%s is listed as both blocked and promotable", id)
		}
		listed[id] = "promotable"
	}
	for _, spec := range config.DetectorSpecs() {
		switch listed[spec.ID] {
		case "":
			t.Errorf("%s is a new detector: decide deliberately whether it is blocked or promotable and list it here", spec.ID)
		case "promotable":
			if reason := config.DetectorPauseReason(spec.ID); reason != "" {
				t.Errorf("%s is paused (%s) but listed as promotable", spec.ID, reason)
			}
		}
		delete(listed, spec.ID)
	}
	for id := range listed {
		t.Errorf("%s is listed here but is not a known detector", id)
	}
	for id := range promotionBlocked {
		known := false
		for _, blocked := range teethBlockedDetectors {
			known = known || blocked == id
		}
		if !known {
			t.Errorf("%s is blocked in production but missing from teethBlockedDetectors", id)
		}
	}
}

// teethGateCase changes one thing in a passing metric and names the exact
// reasons the gate must give. A case that is refused for any other reason fails.
type teethGateCase struct {
	name    string
	modify  func(*detectorCalibrationMetric)
	reasons []string
}

func teethRunGateCases(t *testing.T, detectorID string, cases []teethGateCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metric := teethPassingMetric(detectorID)
			tc.modify(&metric)
			applyPromotionGate(&metric)
			got := append([]string(nil), metric.Reasons...)
			want := append([]string(nil), tc.reasons...)
			sort.Strings(got)
			sort.Strings(want)
			if metric.Eligible || !reflect.DeepEqual(got, want) {
				t.Fatalf("eligible=%t\n got reasons: %q\nwant reasons: %q", metric.Eligible, got, want)
			}
		})
	}
}

func TestTeethEverySampleSizeMinimumIsEnforcedOneBelowTheLine(t *testing.T) {
	// The numbers are the policy (160/800/10/20 overall, 80/400 per evaluation
	// split). They are literals here so lowering a constant fails this test.
	teethRunGateCases(t, "THROW_001", []teethGateCase{
		{"159 positive opportunities", func(m *detectorCalibrationMetric) {
			m.Overall.PositiveOpportunities, m.Overall.IndependentEvidence = 159, 159+1000
		}, []string{"need 160 positive opportunities; have 159"}},
		{"799 legitimate opportunities", func(m *detectorCalibrationMetric) {
			m.Overall.NegativeOpportunities, m.Overall.IndependentEvidence = 799, 200+799
		}, []string{"need 800 legitimate opportunities; have 799"}},
		{"9 players", func(m *detectorCalibrationMetric) { m.Overall.Players = 9 }, []string{"need 10 distinct players; have 9"}},
		{"19 matches", func(m *detectorCalibrationMetric) { m.Overall.Matches = 19 }, []string{"need 20 distinct matches; have 19"}},
		{"79 holdout positives", func(m *detectorCalibrationMetric) { m.Holdout.PositiveOpportunities = 79 },
			[]string{"holdout needs 80 positive opportunities; have 79"}},
		{"399 holdout legitimate", func(m *detectorCalibrationMetric) { m.Holdout.NegativeOpportunities = 399 },
			[]string{"holdout needs 400 legitimate opportunities; have 399"}},
		{"79 validation positives", func(m *detectorCalibrationMetric) { m.Validation.PositiveOpportunities = 79 },
			[]string{"validation needs 80 positive opportunities; have 79"}},
		{"399 validation legitimate", func(m *detectorCalibrationMetric) { m.Validation.NegativeOpportunities = 399 },
			[]string{"validation needs 400 legitimate opportunities; have 399"}},
		{"4 holdout players", func(m *detectorCalibrationMetric) { m.Holdout.Players = 4 },
			[]string{"holdout needs at least five distinct players and five matches"}},
		{"4 holdout matches", func(m *detectorCalibrationMetric) { m.Holdout.Matches = 4 },
			[]string{"holdout needs at least five distinct players and five matches"}},
		{"4 validation players", func(m *detectorCalibrationMetric) { m.Validation.Players = 4 },
			[]string{"validation needs at least five distinct players and five matches"}},
		{"4 validation matches", func(m *detectorCalibrationMetric) { m.Validation.Matches = 4 },
			[]string{"validation needs at least five distinct players and five matches"}},
	})
}

func TestTeethSampleSizeMinimumsPassExactlyOnTheLine(t *testing.T) {
	metric := teethPassingMetric("THROW_001")
	metric.Overall.PositiveOpportunities, metric.Overall.NegativeOpportunities, metric.Overall.IndependentEvidence = 160, 800, 960
	metric.Overall.Players, metric.Overall.Matches = 10, 20
	for _, split := range []*confusionMetric{&metric.Holdout, &metric.Validation} {
		split.PositiveOpportunities, split.NegativeOpportunities, split.Players, split.Matches = 80, 400, 5, 5
	}
	applyPromotionGate(&metric)
	if !metric.Eligible {
		t.Fatalf("evidence exactly at every minimum was refused: %q", metric.Reasons)
	}
}

func TestTeethRateAndIntegrityRequirementsNameTheirOwnReason(t *testing.T) {
	const evidence = "every decisive sample needs hash-verified attached evidence and two agreeing immutable ballots committed before reveal under the current candidate/window"
	teethRunGateCases(t, "THROW_001", []teethGateCase{
		{"overall recall", func(m *detectorCalibrationMetric) { m.Overall.Recall = .899 }, []string{"recall 89.9% is below 90.0%"}},
		{"overall precision", func(m *detectorCalibrationMetric) { m.Overall.Precision = .949 }, []string{"precision 94.9% is below 95.0%"}},
		{"overall false-positive rate", func(m *detectorCalibrationMetric) { m.Overall.FalsePositiveRate = .0101 },
			[]string{"false-positive rate 1.01% exceeds 1.00%"}},
		{"holdout recall", func(m *detectorCalibrationMetric) { m.Holdout.Recall = .899 }, []string{"holdout recall 89.9% is below 90.0%"}},
		{"holdout precision", func(m *detectorCalibrationMetric) { m.Holdout.Precision = .949 }, []string{"holdout precision 94.9% is below 95.0%"}},
		{"holdout false-positive rate", func(m *detectorCalibrationMetric) { m.Holdout.FalsePositiveRate = .0101 },
			[]string{"holdout false-positive rate 1.01% exceeds 1.00%"}},
		{"validation recall", func(m *detectorCalibrationMetric) { m.Validation.Recall = .899 }, []string{"validation recall 89.9% is below 90.0%"}},
		{"validation precision", func(m *detectorCalibrationMetric) { m.Validation.Precision = .949 }, []string{"validation precision 94.9% is below 95.0%"}},
		{"validation false-positive rate", func(m *detectorCalibrationMetric) { m.Validation.FalsePositiveRate = .0101 },
			[]string{"validation false-positive rate 1.01% exceeds 1.00%"}},

		// Wilson bounds are recomputed from counts. 72/72 has a lower bound of
		// 0.949; 100/130 of 0.69; 0/300 an upper bound of 0.0126.
		{"holdout precision is too uncertain", func(m *detectorCalibrationMetric) { m.Holdout.TruePositive = 72 },
			[]string{"holdout precision Wilson 95% lower bound must be at least 95%"}},
		{"validation recall is too uncertain", func(m *detectorCalibrationMetric) { m.Validation.FalseNegative = 30 },
			[]string{"validation recall Wilson 95% lower bound must be at least 80%"}},
		{"holdout false-positive rate is too uncertain", func(m *detectorCalibrationMetric) { m.Holdout.TrueNegative = 300 },
			[]string{"holdout false-positive-rate Wilson 95% upper bound must be at most 1%"}},
		{"a supplied confidence interval is never trusted", func(m *detectorCalibrationMetric) {
			m.Overall.TruePositive = 60
			m.Overall.PrecisionCI95 = confidenceInterval{Lower: .99, Upper: 1}
		}, []string{"overall precision Wilson 95% lower bound must be at least 95%"}},

		{"one unverified sample", func(m *detectorCalibrationMetric) { m.Overall.IndependentEvidence-- }, []string{evidence}},
		{"one stale sample", func(m *detectorCalibrationMetric) { m.Overall.StaleProvenance = 1 },
			[]string{"1 decisive samples were not analyzed by this app build and detector configuration; re-analyze their replays"}},
		{"one unusable-telemetry sample", func(m *detectorCalibrationMetric) { m.Overall.UnusableTelemetry = 1 },
			[]string{"samples include absent player-window telemetry or gated/unverified telemetry quality"}},
		{"one quarantined sample", func(m *detectorCalibrationMetric) { m.Overall.IsolationConflicts = 1 },
			[]string{"decisive samples include quarantined cross-split exposure"}},
	})
}

func TestTeethIndependenceRequirementsNameTheirOwnReason(t *testing.T) {
	const overallGroups = "overall needs at least 10 independent connected player/session groups, with positives and negatives in at least three groups"
	const holdoutGroups = "holdout needs at least 5 independent connected player/session groups, with positives and negatives in at least three groups"
	const validationGroups = "validation needs at least 5 independent connected player/session groups, with positives and negatives in at least three groups"
	teethRunGateCases(t, "THROW_001", []teethGateCase{
		{"9 overall groups", func(m *detectorCalibrationMetric) { m.Overall.Clusters.Groups = 9 }, []string{overallGroups}},
		{"4 holdout groups", func(m *detectorCalibrationMetric) { m.Holdout.Clusters.Groups = 4 }, []string{holdoutGroups}},
		{"4 validation groups", func(m *detectorCalibrationMetric) { m.Validation.Clusters.Groups = 4 }, []string{validationGroups}},
		{"positives from 2 groups", func(m *detectorCalibrationMetric) { m.Overall.Clusters.PositiveGroups = 2 }, []string{overallGroups}},
		{"negatives from 2 groups", func(m *detectorCalibrationMetric) { m.Holdout.Clusters.NegativeGroups = 2 }, []string{holdoutGroups}},
		{"overall dominated", func(m *detectorCalibrationMetric) { m.Overall.Clusters.LargestGroupFraction = .26 },
			[]string{"overall is dominated by one connected group (maximum 25% of opportunities)"}},
		{"holdout dominated", func(m *detectorCalibrationMetric) { m.Holdout.Clusters.LargestGroupFraction = .41 },
			[]string{"holdout is dominated by one connected group (maximum 40% of opportunities)"}},
		{"validation dominated", func(m *detectorCalibrationMetric) { m.Validation.Clusters.LargestGroupFraction = .41 },
			[]string{"validation is dominated by one connected group (maximum 40% of opportunities)"}},
	})
}

// Each detector family must show legitimate controls for the legal plays that
// resemble its signal. The required strata differ per family, which is exactly
// how a blocked BIO_/MOV_ detector used to be "rejected" by accident.
func TestTeethEveryRequiredControlStratumIsEnforcedPerFamily(t *testing.T) {
	families := map[string][]string{
		"THROW_001": {"normal", "stack", "block_push", "slap", "headbutt"},
		"MOV_001":   {"normal", "lean", "stack", "block_push"},
		"BIO_002":   {"normal", "transition"},
		"STATE_002": {"normal", "transition"},
	}
	for detectorID, legal := range families {
		var cases []teethGateCase
		add := func(kind, key string, values func(*detectorCalibrationMetric) map[string]confusionMetric) {
			reason := kind + " stratum " + key + " needs 20 legitimate opportunities from three connected groups"
			cases = append(cases,
				teethGateCase{kind + " " + key + " missing", func(m *detectorCalibrationMetric) { delete(values(m), key) }, []string{reason}},
				teethGateCase{kind + " " + key + " has 19 controls", func(m *detectorCalibrationMetric) {
					values(m)[key] = confusionMetric{NegativeOpportunities: 19, Clusters: clusterDescription{NegativeGroups: 3}}
				}, []string{reason}},
				teethGateCase{kind + " " + key + " from 2 groups", func(m *detectorCalibrationMetric) {
					values(m)[key] = confusionMetric{NegativeOpportunities: 20, Clusters: clusterDescription{NegativeGroups: 2}}
				}, []string{reason}},
			)
		}
		for _, key := range []string{"low_under_80ms", "medium_80_150ms", "high_over_150ms"} {
			add("ping", key, func(m *detectorCalibrationMetric) map[string]confusionMetric { return m.ByPing })
		}
		for _, key := range []string{"low_under_12hz", "standard_12_22hz", "high_over_22hz"} {
			add("capture", key, func(m *detectorCalibrationMetric) map[string]confusionMetric { return m.ByCaptureRate })
		}
		for _, key := range legal {
			add("legal context", key, func(m *detectorCalibrationMetric) map[string]confusionMetric { return m.ByLegalContext })
		}
		t.Run(detectorID, func(t *testing.T) { teethRunGateCases(t, detectorID, cases) })
	}
}
