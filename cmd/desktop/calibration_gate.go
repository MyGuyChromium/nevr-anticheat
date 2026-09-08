package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const (
	// Conservative release policy, not measured detector performance.
	promotionMinPositive           = 160
	promotionMinNegative           = 800
	promotionMinPlayers            = 10
	promotionMinMatches            = 20
	promotionMinHoldoutPositive    = 80
	promotionMinHoldoutNegative    = 400
	promotionMinValidationPositive = 80
	promotionMinValidationNegative = 400
	promotionMinPrecision          = 0.95
	promotionMinRecall             = 0.90
	promotionMaxFalseRate          = 0.01
	promotionMinRecallLower95      = 0.80
)

type confidenceInterval struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

type confusionMetric struct {
	Clusters              clusterDescription `json:"clusters"`
	TruePositive          int                `json:"true_positive"`
	FalsePositive         int                `json:"false_positive"`
	FalseNegative         int                `json:"false_negative"`
	TrueNegative          int                `json:"true_negative"`
	Uncertain             int                `json:"uncertain"`
	PositiveOpportunities int                `json:"positive_opportunities"`
	NegativeOpportunities int                `json:"negative_opportunities"`
	DirectLabels          int                `json:"direct_labels"`
	WindowLabels          int                `json:"window_labels"`
	BlindLabels           int                `json:"blind_labels"`
	Players               int                `json:"players"`
	Matches               int                `json:"matches"`
	Precision             float64            `json:"precision"`
	Recall                float64            `json:"recall"`
	FalsePositiveRate     float64            `json:"false_positive_rate"`
	FalsePositivesPer100  float64            `json:"false_positives_per_100"`
	CurrentProvenance     int                `json:"current_provenance_samples"`
	StaleProvenance       int                `json:"stale_provenance_samples"`
	IndependentEvidence   int                `json:"independent_evidence_samples"`
	UnusableTelemetry     int                `json:"unusable_telemetry_samples"`
	IsolationConflicts    int                `json:"isolation_conflict_samples"`
	PrecisionCI95         confidenceInterval `json:"precision_ci95"`
	RecallCI95            confidenceInterval `json:"recall_ci95"`
	FalsePositiveRateCI95 confidenceInterval `json:"false_positive_rate_ci95"`
}

type metricAccumulator struct {
	confusionMetric
	players  map[string]bool
	matches  map[string]bool
	clusters map[string]*clusterCounts
}

// Ranges across observed connected player/session groups are descriptive
// heterogeneity, NOT confidence intervals or guaranteed population accuracy.
type clusterDescription struct {
	Groups                 int                 `json:"groups"`
	PositiveGroups         int                 `json:"positive_groups"`
	NegativeGroups         int                 `json:"negative_groups"`
	LargestGroupFraction   float64             `json:"largest_group_fraction"`
	RecallObservedRange    *confidenceInterval `json:"recall_observed_range,omitempty"`
	FalseRateObservedRange *confidenceInterval `json:"false_rate_observed_range,omitempty"`
	MacroRecall            *float64            `json:"macro_recall,omitempty"`
	MacroFalseRate         *float64            `json:"macro_false_rate,omitempty"`
	Method                 string              `json:"method"`
}
type clusterCounts struct{ tp, fp, tn, fn int }

func (a *metricAccumulator) add(sample calibrationSample, predicted bool) {
	if a.players == nil {
		a.players, a.matches = make(map[string]bool), make(map[string]bool)
	}
	if sample.Source == "event_review" {
		a.DirectLabels++
	} else {
		a.WindowLabels++
	}
	if sample.BlindReview {
		a.BlindLabels++
	}
	if sample.NoWindowSamples {
		// Keep the annotation visible without treating missing observation as
		// either successful detection, a quiet legitimate play, or a missed cheat.
		a.Uncertain++
		a.UnusableTelemetry++
		return
	}
	if sample.Truth == sqlite.GroundTruthPositive || sample.Truth == sqlite.GroundTruthNegative {
		if sample.ClusterID != "" {
			if a.clusters == nil {
				a.clusters = make(map[string]*clusterCounts)
			}
			c := a.clusters[sample.ClusterID]
			if c == nil {
				c = &clusterCounts{}
				a.clusters[sample.ClusterID] = c
			}
			if sample.Truth == sqlite.GroundTruthPositive {
				if predicted {
					c.tp++
				} else {
					c.fn++
				}
			} else {
				if predicted {
					c.fp++
				} else {
					c.tn++
				}
			}
		}
		if sample.IndependentEvidence {
			a.IndependentEvidence++
		}
		if sample.UnusableTelemetry {
			a.UnusableTelemetry++
		}
		if sample.IsolationConflict {
			a.IsolationConflicts++
		}
	}
	switch sample.Truth {
	case sqlite.GroundTruthPositive:
		a.players[sample.PlayerID], a.matches[sample.MatchID] = true, true
		if sample.CurrentProvenance {
			a.CurrentProvenance++
		} else {
			a.StaleProvenance++
		}
		a.PositiveOpportunities++
		if predicted {
			a.TruePositive++
		} else {
			a.FalseNegative++
		}
	case sqlite.GroundTruthNegative:
		a.players[sample.PlayerID], a.matches[sample.MatchID] = true, true
		if sample.CurrentProvenance {
			a.CurrentProvenance++
		} else {
			a.StaleProvenance++
		}
		a.NegativeOpportunities++
		if predicted {
			a.FalsePositive++
		} else {
			a.TrueNegative++
		}
	default:
		a.Uncertain++
	}
}

func (a *metricAccumulator) finish() confusionMetric {
	a.Players, a.Matches = len(a.players), len(a.matches)
	if n := a.TruePositive + a.FalsePositive; n > 0 {
		a.Precision = float64(a.TruePositive) / float64(n)
		a.PrecisionCI95 = wilson(a.TruePositive, n)
	}
	if n := a.TruePositive + a.FalseNegative; n > 0 {
		a.Recall = float64(a.TruePositive) / float64(n)
		a.RecallCI95 = wilson(a.TruePositive, n)
	}
	if n := a.FalsePositive + a.TrueNegative; n > 0 {
		a.FalsePositiveRate = float64(a.FalsePositive) / float64(n)
		a.FalsePositivesPer100 = a.FalsePositiveRate * 100
		a.FalsePositiveRateCI95 = wilson(a.FalsePositive, n)
	}
	a.Clusters = describeClusters(a.clusters, a.PositiveOpportunities+a.NegativeOpportunities)
	return a.confusionMetric
}

func describeClusters(groups map[string]*clusterCounts, total int) clusterDescription {
	out := clusterDescription{Groups: len(groups), Method: "observed_connected_group_ranges_not_confidence_intervals"}
	var recallSum, falseSum float64
	add := func(interval **confidenceInterval, value float64) {
		if *interval == nil {
			*interval = &confidenceInterval{Lower: value, Upper: value}
		} else {
			(*interval).Lower = math.Min((*interval).Lower, value)
			(*interval).Upper = math.Max((*interval).Upper, value)
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		c := groups[key]
		if total > 0 {
			out.LargestGroupFraction = math.Max(out.LargestGroupFraction, float64(c.tp+c.fp+c.tn+c.fn)/float64(total))
		}
		if c.tp+c.fn > 0 {
			r := float64(c.tp) / float64(c.tp+c.fn)
			out.PositiveGroups++
			recallSum += r
			add(&out.RecallObservedRange, r)
		}
		if c.fp+c.tn > 0 {
			r := float64(c.fp) / float64(c.fp+c.tn)
			out.NegativeGroups++
			falseSum += r
			add(&out.FalseRateObservedRange, r)
		}
	}
	if out.PositiveGroups > 0 {
		v := recallSum / float64(out.PositiveGroups)
		out.MacroRecall = &v
	}
	if out.NegativeGroups > 0 {
		v := falseSum / float64(out.NegativeGroups)
		out.MacroFalseRate = &v
	}
	return out
}

func wilson(successes, total int) confidenceInterval {
	if total <= 0 {
		return confidenceInterval{}
	}
	const z = 1.959963984540054
	n, p := float64(total), float64(successes)/float64(total)
	denom := 1 + z*z/n
	center := (p + z*z/(2*n)) / denom
	half := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n) / denom
	return confidenceInterval{Lower: math.Max(0, center-half), Upper: math.Min(1, center+half)}
}

type calibrationSample struct {
	ClusterID           string
	LegalContext        string
	OpportunityID       string
	MatchID             string
	PlayerID            string
	DetectorID          string
	FrameStart          int
	FrameEnd            int
	Truth               string
	BlindReview         bool
	Source              string
	PingBand            string
	CaptureBand         string
	QualityBand         string
	CurrentProvenance   bool
	IndependentEvidence bool
	UnusableTelemetry   bool
	NoWindowSamples     bool
	IsolationConflict   bool
}

type matchCalibrationMeta struct {
	PingByPlayer      map[string]float64
	CaptureBand       string
	QualityBand       string
	CurrentProvenance bool
	QualityGated      bool
}

type detectorCalibrationMetric struct {
	DetectorID     string                     `json:"detector_id"`
	Overall        confusionMetric            `json:"overall"`
	Training       confusionMetric            `json:"training"`
	Validation     confusionMetric            `json:"validation"`
	Holdout        confusionMetric            `json:"holdout"`
	ByPing         map[string]confusionMetric `json:"by_ping"`
	ByCaptureRate  map[string]confusionMetric `json:"by_capture_rate"`
	ByQuality      map[string]confusionMetric `json:"by_quality"`
	ByLegalContext map[string]confusionMetric `json:"by_legal_context"`
	Eligible       bool                       `json:"eligible"`
	PromotionBlock string                     `json:"promotion_block,omitempty"`
	Reasons        []string                   `json:"reasons"`
}

type splitAssignment struct {
	ClusterKey          string `json:"cluster_key"`
	MatchID             string `json:"match_id"`
	GroupKey            string `json:"group_key"`
	Split               string `json:"split"`
	OriginalSplit       string `json:"original_split,omitempty"`
	PolicyVersion       int    `json:"policy_version"`
	ExposureFingerprint string `json:"exposure_fingerprint,omitempty"`
}

type calibrationDashboard struct {
	SealedHoldout         bool                        `json:"sealed_holdout"`
	ProductionValidated   bool                        `json:"production_validated"`
	ReleaseEligible       bool                        `json:"release_eligible"`
	ValidationLimitations []string                    `json:"validation_limitations"`
	Detectors             []detectorCalibrationMetric `json:"detectors"`
	Splits                map[string][]string         `json:"splits"`
	Assignments           []splitAssignment           `json:"assignments"`
	PlayerLeakage         bool                        `json:"player_leakage"`
	Samples               int                         `json:"samples"`
	BlindSamples          int                         `json:"blind_samples"`
	Requirements          map[string]any              `json:"promotion_requirements"`
	Notice                string                      `json:"notice"`
	Drift                 map[string]any              `json:"drift,omitempty"`
	Disagreements         []map[string]any            `json:"disagreements,omitempty"`
	MatchRecall           map[string]any              `json:"match_recall,omitempty"`
	AutoRolledBack        []string                    `json:"auto_rolled_back,omitempty"`
}

type calibrationReportPayload struct {
	SchemaVersion          string                     `json:"schema_version"`
	GeneratedAt            time.Time                  `json:"generated_at"`
	AppVersion             string                     `json:"app_version"`
	BuildCommit            string                     `json:"build_commit"`
	ConfigFingerprint      string                     `json:"config_fingerprint"`
	CalibrationFingerprint string                     `json:"calibration_fingerprint"`
	ActiveProfile          string                     `json:"active_profile"`
	Dashboard              calibrationDashboard       `json:"dashboard"`
	Promotions             []sqlite.DetectorPromotion `json:"promotions"`
	EffectiveDetectors     []config.EffectiveDetector `json:"effective_detectors"`
	Physics                model.PhysicsConstants     `json:"physics"`
	Library                calibrationLibrarySnapshot `json:"library"`
}

type calibrationLibrarySnapshot struct {
	MatchLabels      map[string]int `json:"match_labels"`
	StoredMatches    int            `json:"stored_matches"`
	LabeledMatches   int            `json:"labeled_matches"`
	UnlabeledMatches int            `json:"unlabeled_matches"`
}

type calibrationReport struct {
	SHA256  string                   `json:"sha256"`
	Payload calibrationReportPayload `json:"payload"`
}

type unionFind struct{ parent map[string]string }

func newUnionFind(ids []string) *unionFind {
	u := &unionFind{parent: make(map[string]string, len(ids))}
	for _, id := range ids {
		u.parent[id] = id
	}
	return u
}

func (u *unionFind) find(id string) string {
	p := u.parent[id]
	if p != id {
		u.parent[id] = u.find(p)
	}
	return u.parent[id]
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if ra < rb {
		u.parent[rb] = ra
	} else {
		u.parent[ra] = rb
	}
}

func splitForGroup(key string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	switch bucket := h.Sum32() % 10; {
	case bucket < 2:
		return "holdout"
	case bucket < 4:
		return "validation"
	default:
		return "training"
	}
}

// groupedDatasetSplits assigns connected components of matches sharing any
// player to one split. This prevents the same player from leaking into a
// training and holdout calculation.
func groupedDatasetSplits(matches []sqlite.StoredMatch) (map[string]string, []splitAssignment) {
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		if match.Context != nil {
			ids = append(ids, match.Context.MatchID)
		}
	}
	u := newUnionFind(ids)
	firstMatchByPlayer := make(map[string]string)
	for _, match := range matches {
		if match.Context == nil {
			continue
		}
		mid := match.Context.MatchID
		for _, player := range match.Context.PlayerIDs {
			player = strings.TrimSpace(player)
			if player == "" {
				continue
			}
			if first, ok := firstMatchByPlayer[player]; ok {
				u.union(mid, first)
			} else {
				firstMatchByPlayer[player] = mid
			}
		}
	}
	componentMatches, componentPlayers := make(map[string][]string), make(map[string]map[string]bool)
	for _, match := range matches {
		if match.Context == nil {
			continue
		}
		root, mid := u.find(match.Context.MatchID), match.Context.MatchID
		componentMatches[root] = append(componentMatches[root], mid)
		if componentPlayers[root] == nil {
			componentPlayers[root] = make(map[string]bool)
		}
		for _, player := range match.Context.PlayerIDs {
			if player = strings.TrimSpace(player); player != "" {
				componentPlayers[root][player] = true
			}
		}
	}
	out, assignments := make(map[string]string), make([]splitAssignment, 0, len(matches))
	for root, mids := range componentMatches {
		players := make([]string, 0, len(componentPlayers[root]))
		for player := range componentPlayers[root] {
			players = append(players, player)
		}
		sort.Strings(players)
		sort.Strings(mids)
		key := "matches:" + strings.Join(mids, "|")
		if len(players) > 0 {
			key = "players:" + strings.Join(players, "|")
		}
		split := splitForGroup(key)
		for _, mid := range mids {
			out[mid] = split
			assignments = append(assignments, splitAssignment{MatchID: mid, GroupKey: key, Split: split})
		}
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].MatchID < assignments[j].MatchID })
	return out, assignments
}

func (s *server) isolatedDatasetSplits(ctx context.Context, matches []sqlite.StoredMatch) (map[string]string, []splitAssignment, error) {
	proposed, _ := groupedDatasetSplits(matches)
	stored, err := s.engine.Store().ReconcileCalibrationSplits(ctx, matches, proposed)
	if err != nil {
		return nil, nil, err
	}
	out := make(map[string]string, len(stored))
	assignments := make([]splitAssignment, 0, len(stored))
	for id, a := range stored {
		split := a.Split
		if a.Quarantined {
			split = "quarantined"
		}
		out[id] = split
		assignments = append(assignments, splitAssignment{MatchID: id, ClusterKey: a.ClusterKey, GroupKey: a.GroupKey, Split: split, OriginalSplit: a.Split, PolicyVersion: a.PolicyVersion, ExposureFingerprint: a.ExposureFingerprint})
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].MatchID < assignments[j].MatchID })
	return out, assignments, nil
}

func (s *server) experimentSplits(ctx context.Context) (map[string]string, error) {
	if err := s.recordCalibrationViewExposure(ctx); err != nil {
		return nil, err
	}
	matches, err := s.engine.Store().ListMatches(ctx, 100000)
	if err != nil {
		return nil, err
	}
	splits, _, err := s.isolatedDatasetSplits(ctx, matches)
	return splits, err
}

func trustedCalibrationBuild(info *debug.BuildInfo, revision string) bool {
	if info == nil || len(revision) != 40 {
		return false
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return false
	}
	var recorded string
	var clean bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			recorded = setting.Value
		case "vcs.modified":
			clean = setting.Value == "false"
		}
	}
	return clean && recorded == revision
}

func analysisBuildRevision() string {
	info, _ := debug.ReadBuildInfo()
	if !trustedCalibrationBuild(info, buildCommit) {
		return "unverified:" + buildCommit
	}
	return buildCommit
}

func exposureCandidateFingerprint(run sqlite.AnalysisRun, version, revision, behavior string, trusted bool) string {
	if !trusted || run.AppVersion != version || run.BuildCommit != revision || run.CalibrationFingerprint != behavior {
		return ""
	}
	return shortHash([]byte(version + "\x00" + revision + "\x00" + behavior))
}

// recordCalibrationViewExposure must run after durable analysis provenance and
// before findings are returned to a reviewer. This freezes a candidate, not a
// claim of psychological blinding or an independently sealed prospective test.
func (s *server) recordCalibrationViewExposure(ctx context.Context) error {
	matches, err := s.engine.Store().ListMatches(ctx, 100000)
	if err != nil {
		return err
	}
	splits, _, err := s.isolatedDatasetSplits(ctx, matches)
	if err != nil {
		return err
	}
	info, _ := debug.ReadBuildInfo()
	trusted := trustedCalibrationBuild(info, buildCommit)
	behavior := calibrationFingerprint(s.engine.Config())
	fingerprints := make(map[string]string)
	for id, split := range splits {
		if split != "holdout" {
			continue
		}
		runs, err := s.engine.Store().ListAnalysisRuns(ctx, id, 1)
		if err != nil {
			return fmt.Errorf("reading held-out analysis provenance: %w", err)
		}
		fingerprints[id] = ""
		if len(runs) == 1 {
			fingerprints[id] = exposureCandidateFingerprint(runs[0], appVersion, buildCommit, behavior, trusted)
		}
	}
	if err := s.engine.Store().RecordCalibrationHoldoutExposure(ctx, fingerprints); err != nil {
		return err
	}
	// Propagate quarantine to historically connected matches, including deleted ones.
	_, _, err = s.isolatedDatasetSplits(ctx, matches)
	return err
}

func checkExperimentMatch(splits map[string]string, matchID string) error {
	switch splits[matchID] {
	case "training", "validation":
		return nil
	case "holdout":
		return fmt.Errorf("match %s is reserved holdout; threshold experiments are forbidden", matchID)
	case "quarantined":
		return fmt.Errorf("match %s has conflicting dataset exposure and is quarantined", matchID)
	default:
		return fmt.Errorf("match %s has no verified dataset assignment", matchID)
	}
}

func captureBand(rate float64) string {
	switch {
	case rate <= 0:
		return "unknown"
	case rate < 12:
		return "low_under_12hz"
	case rate <= 22:
		return "standard_12_22hz"
	default:
		return "high_over_22hz"
	}
}

func pingBand(ping float64) string {
	switch {
	case ping <= 0:
		return "unknown"
	case ping < 80:
		return "low_under_80ms"
	case ping <= 150:
		return "medium_80_150ms"
	default:
		return "high_over_150ms"
	}
}

func (s *server) calibrationMatchMeta(ctx context.Context, matches []sqlite.StoredMatch, needed map[string]bool) map[string]matchCalibrationMeta {
	out := make(map[string]matchCalibrationMeta, len(matches))
	currentCalibrationFingerprint := calibrationFingerprint(s.engine.Config())
	for _, match := range matches {
		if match.Context == nil || !needed[match.Context.MatchID] {
			continue
		}
		meta := matchCalibrationMeta{PingByPlayer: make(map[string]float64), CaptureBand: captureBand(match.Context.TickRate)}
		if runs, err := s.engine.Store().ListAnalysisRuns(ctx, match.Context.MatchID, 1); err == nil && len(runs) == 1 {
			meta.QualityBand = runs[0].QualityGrade
			meta.QualityGated = runs[0].QualityGated
			meta.CurrentProvenance = runs[0].AppVersion == appVersion && runs[0].BuildCommit == analysisBuildRevision() && runs[0].CalibrationFingerprint == currentCalibrationFingerprint
		}
		if meta.QualityBand == "" {
			meta.QualityBand = "unknown"
		}
		if doc, err := s.engine.Store().GetMatchSummaryJSON(ctx, match.Context.MatchID); err == nil {
			var summary replay.MatchSummary
			if json.Unmarshal(doc, &summary) == nil {
				for _, player := range summary.Players {
					if player != nil {
						meta.PingByPlayer[player.PlayerID] = player.PingAvg
					}
				}
			}
		}
		out[match.Context.MatchID] = meta
	}
	return out
}

func eventFrameRange(event model.DetectionEvent) (int, int) {
	start, end := event.FrameRangeStart, event.FrameRangeEnd
	if start == 0 && end == 0 {
		start, end = event.FrameIndex, event.FrameIndex
	}
	return start, end
}

func samplePredicted(sample calibrationSample, events []model.DetectionEvent) bool {
	for _, event := range events {
		if event.DetectorID != sample.DetectorID || event.PlayerID != sample.PlayerID {
			continue
		}
		start, end := eventFrameRange(event)
		if end >= sample.FrameStart && start <= sample.FrameEnd {
			return true
		}
	}
	return false
}

func samplesFromLabels(reviews []sqlite.EventReview, opportunities []sqlite.CalibrationOpportunity, meta map[string]matchCalibrationMeta) []calibrationSample {
	out := make([]calibrationSample, 0, len(reviews)+len(opportunities))
	opportunitiesBySignal := make(map[string][]sqlite.CalibrationOpportunity)
	for _, opportunity := range opportunities {
		key := opportunity.MatchID + "\x00" + opportunity.PlayerID + "\x00" + opportunity.DetectorID
		opportunitiesBySignal[key] = append(opportunitiesBySignal[key], opportunity)
	}
	decorate := func(sample *calibrationSample) {
		m := meta[sample.MatchID]
		sample.PingBand = pingBand(m.PingByPlayer[sample.PlayerID])
		sample.CaptureBand, sample.QualityBand = m.CaptureBand, m.QualityBand
		sample.CurrentProvenance = m.CurrentProvenance
		sample.UnusableTelemetry = m.QualityGated || (m.QualityBand != "excellent" && m.QualityBand != "good")
		if sample.CaptureBand == "" {
			sample.CaptureBand = "unknown"
		}
		if sample.QualityBand == "" {
			sample.QualityBand = "unknown"
		}
	}
	for _, review := range reviews {
		key := review.MatchID + "\x00" + review.PlayerID + "\x00" + review.DetectorID
		covered := false
		for _, opportunity := range opportunitiesBySignal[key] {
			if opportunity.FrameEnd >= max(0, review.FrameIndex-10) && opportunity.FrameStart <= review.FrameIndex+10 {
				covered = true
				break
			}
		}
		if covered {
			// A deliberately annotated opportunity is the stronger label and
			// prevents the emitted event review from counting the same play twice.
			continue
		}
		truth := sqlite.GroundTruthUncertain
		if review.Verdict == "yes" {
			truth = sqlite.GroundTruthPositive
		} else if review.Verdict == "no" {
			truth = sqlite.GroundTruthNegative
		}
		sample := calibrationSample{MatchID: review.MatchID, PlayerID: review.PlayerID,
			DetectorID: review.DetectorID, FrameStart: max(0, review.FrameIndex-10),
			FrameEnd: review.FrameIndex + 10, Truth: truth, BlindReview: review.BlindReview, Source: "event_review"}
		decorate(&sample)
		out = append(out, sample)
	}
	for _, opportunity := range opportunities {
		sample := calibrationSample{OpportunityID: opportunity.OpportunityID, MatchID: opportunity.MatchID, PlayerID: opportunity.PlayerID,
			DetectorID: opportunity.DetectorID, FrameStart: opportunity.FrameStart,
			FrameEnd: opportunity.FrameEnd, Truth: opportunity.GroundTruth,
			BlindReview: opportunity.BlindReview, Source: "ground_truth_window"}
		// Historical/free-text attestations remain descriptive labels. Only
		// current hash-bound immutable ballots can satisfy promotion evidence.
		decorate(&sample)
		out = append(out, sample)
	}
	return out
}

func metricsForSamples(samples []calibrationSample, eventsByMatch map[string][]model.DetectionEvent, splitByMatch map[string]string) map[string]detectorCalibrationMetric {
	type detectorAcc struct {
		overall, training, validation, holdout metricAccumulator
		ping, capture, quality, legal          map[string]*metricAccumulator
	}
	all := make(map[string]*detectorAcc)
	for _, sample := range samples {
		acc := all[sample.DetectorID]
		if acc == nil {
			acc = &detectorAcc{ping: make(map[string]*metricAccumulator), capture: make(map[string]*metricAccumulator), quality: make(map[string]*metricAccumulator), legal: make(map[string]*metricAccumulator)}
			all[sample.DetectorID] = acc
		}
		sample.IsolationConflict = splitByMatch[sample.MatchID] == "quarantined"
		predicted := samplePredicted(sample, eventsByMatch[sample.MatchID])
		acc.overall.add(sample, predicted)
		switch splitByMatch[sample.MatchID] {
		case "holdout":
			acc.holdout.add(sample, predicted)
		case "validation":
			acc.validation.add(sample, predicted)
		case "quarantined":
			// Keep evidence visible overall; it must never feed a split metric.
		default:
			acc.training.add(sample, predicted)
		}
		addStratum := func(key string, target map[string]*metricAccumulator) {
			if target[key] == nil {
				target[key] = &metricAccumulator{}
			}
			target[key].add(sample, predicted)
		}
		addStratum(sample.PingBand, acc.ping)
		addStratum(sample.CaptureBand, acc.capture)
		addStratum(sample.QualityBand, acc.quality)
		addStratum(sample.LegalContext, acc.legal)
	}
	out := make(map[string]detectorCalibrationMetric, len(all))
	for id, acc := range all {
		metric := detectorCalibrationMetric{DetectorID: id, Overall: acc.overall.finish(), Training: acc.training.finish(), Validation: acc.validation.finish(), Holdout: acc.holdout.finish(), ByPing: make(map[string]confusionMetric), ByCaptureRate: make(map[string]confusionMetric), ByQuality: make(map[string]confusionMetric), ByLegalContext: make(map[string]confusionMetric)}
		for key, item := range acc.ping {
			metric.ByPing[key] = item.finish()
		}
		for key, item := range acc.capture {
			metric.ByCaptureRate[key] = item.finish()
		}
		for key, item := range acc.quality {
			metric.ByQuality[key] = item.finish()
		}
		for key, item := range acc.legal {
			metric.ByLegalContext[key] = item.finish()
		}
		out[id] = metric
	}
	return out
}

var promotionBlocked = map[string]string{
	"MOV_006":   config.DetectorPauseReason("MOV_006"),
	"PAT_005":   config.DetectorPauseReason("PAT_005"),
	"BIO_001":   "wrist angular speed is capture-rate limited and aliased; requires a separate observability validation design",
	"THROW_004": "known unsafe on legitimate regrab play",
	"THROW_007": "stub detector without required telemetry",
	"MOV_003":   "known unsafe around legitimate wall reversals",
	"MOV_004":   "requires confirmed boost-state telemetry semantics",
	"MOV_005":   "requires confirmed boost-state telemetry semantics",
	"STATE_003": "requires confirmed shield-state telemetry semantics",
	"STATE_004": "requires confirmed immunity-state telemetry semantics",
	"STATE_005": "requires confirmed shield-state telemetry semantics",
	"STATE_006": "suspended because no impossible score invariant is known",
	"STATE_007": "requires confirmed per-frame stun-count telemetry semantics",
	"STATE_008": "observation-only catch review: causal catch/contact/input behavior and real-play accuracy are unvalidated; promotion is unavailable",
	"STATE_001": "disc-grab geometry, acquisition timing and the owner rule are not engine-validated; diagnostics only",
	"THROW_005": "accuracy alone cannot establish assistance; release model, permitted settings and target geometry are unvalidated",
	"THROW_006": "sampled free-flight changes lack authoritative contact/impulse coverage and calibration; diagnostics only",
	"PAT_001":   "known unsafe on legitimate regrab rhythm",
	"PAT_002":   "known unsafe on consistent legitimate throwing form",
	"PAT_003":   "cross-match detector requires a separate history calibration design",
	"PAT_004":   "meta-detector cannot be promoted before its upstream detectors",
}

func applyPromotionGate(metric *detectorCalibrationMetric) {
	metric.Reasons, metric.PromotionBlock = nil, ""
	if blocked := promotionBlocked[metric.DetectorID]; blocked != "" {
		metric.PromotionBlock = blocked
		metric.Reasons = append(metric.Reasons, blocked)
	}
	o, h := metric.Overall, metric.Holdout
	v := metric.Validation
	checks := []struct {
		failed bool
		text   string
	}{
		{o.PositiveOpportunities < promotionMinPositive, fmt.Sprintf("need %d positive opportunities; have %d", promotionMinPositive, o.PositiveOpportunities)},
		{o.NegativeOpportunities < promotionMinNegative, fmt.Sprintf("need %d legitimate opportunities; have %d", promotionMinNegative, o.NegativeOpportunities)},
		{o.Players < promotionMinPlayers, fmt.Sprintf("need %d distinct players; have %d", promotionMinPlayers, o.Players)},
		{o.Matches < promotionMinMatches, fmt.Sprintf("need %d distinct matches; have %d", promotionMinMatches, o.Matches)},
		{h.PositiveOpportunities < promotionMinHoldoutPositive, fmt.Sprintf("holdout needs %d positive opportunities; have %d", promotionMinHoldoutPositive, h.PositiveOpportunities)},
		{h.NegativeOpportunities < promotionMinHoldoutNegative, fmt.Sprintf("holdout needs %d legitimate opportunities; have %d", promotionMinHoldoutNegative, h.NegativeOpportunities)},
		{v.PositiveOpportunities < promotionMinValidationPositive, fmt.Sprintf("validation needs %d positive opportunities; have %d", promotionMinValidationPositive, v.PositiveOpportunities)},
		{v.NegativeOpportunities < promotionMinValidationNegative, fmt.Sprintf("validation needs %d legitimate opportunities; have %d", promotionMinValidationNegative, v.NegativeOpportunities)},
		{o.StaleProvenance > 0, fmt.Sprintf("%d decisive samples were not analyzed by this app build and detector configuration; re-analyze their replays", o.StaleProvenance)},
		{o.IndependentEvidence != o.PositiveOpportunities+o.NegativeOpportunities, "every decisive sample needs hash-verified attached evidence and two agreeing immutable ballots committed before reveal under the current candidate/window"},
		{o.UnusableTelemetry > 0, "samples include absent player-window telemetry or gated/unverified telemetry quality"},
		{o.IsolationConflicts > 0, "decisive samples include quarantined cross-split exposure"},
		{o.PositiveOpportunities > 0 && o.Precision < promotionMinPrecision, fmt.Sprintf("precision %.1f%% is below %.1f%%", o.Precision*100, promotionMinPrecision*100)},
		{o.PositiveOpportunities > 0 && o.Recall < promotionMinRecall, fmt.Sprintf("recall %.1f%% is below %.1f%%", o.Recall*100, promotionMinRecall*100)},
		{o.NegativeOpportunities > 0 && o.FalsePositiveRate > promotionMaxFalseRate, fmt.Sprintf("false-positive rate %.2f%% exceeds %.2f%%", o.FalsePositiveRate*100, promotionMaxFalseRate*100)},
		{h.PositiveOpportunities > 0 && h.Precision < promotionMinPrecision, fmt.Sprintf("holdout precision %.1f%% is below %.1f%%", h.Precision*100, promotionMinPrecision*100)},
		{h.PositiveOpportunities > 0 && h.Recall < promotionMinRecall, fmt.Sprintf("holdout recall %.1f%% is below %.1f%%", h.Recall*100, promotionMinRecall*100)},
		{h.NegativeOpportunities > 0 && h.FalsePositiveRate > promotionMaxFalseRate, fmt.Sprintf("holdout false-positive rate %.2f%% exceeds %.2f%%", h.FalsePositiveRate*100, promotionMaxFalseRate*100)},
		{v.PositiveOpportunities > 0 && v.Precision < promotionMinPrecision, fmt.Sprintf("validation precision %.1f%% is below %.1f%%", v.Precision*100, promotionMinPrecision*100)},
		{v.PositiveOpportunities > 0 && v.Recall < promotionMinRecall, fmt.Sprintf("validation recall %.1f%% is below %.1f%%", v.Recall*100, promotionMinRecall*100)},
		{v.NegativeOpportunities > 0 && v.FalsePositiveRate > promotionMaxFalseRate, fmt.Sprintf("validation false-positive rate %.2f%% exceeds %.2f%%", v.FalsePositiveRate*100, promotionMaxFalseRate*100)},
	}
	for _, split := range []struct {
		name string
		m    confusionMetric
	}{{"overall", o}, {"validation", v}, {"holdout", h}} {
		m := split.m
		// Recompute from counts, never trust a caller-supplied confidence interval.
		precision := wilson(m.TruePositive, m.TruePositive+m.FalsePositive)
		recall := wilson(m.TruePositive, m.TruePositive+m.FalseNegative)
		falseRate := wilson(m.FalsePositive, m.FalsePositive+m.TrueNegative)
		checks = append(checks,
			struct {
				failed bool
				text   string
			}{m.TruePositive+m.FalsePositive == 0 || precision.Lower < promotionMinPrecision, fmt.Sprintf("%s precision Wilson 95%% lower bound must be at least %.0f%%", split.name, promotionMinPrecision*100)},
			struct {
				failed bool
				text   string
			}{m.TruePositive+m.FalseNegative == 0 || recall.Lower < promotionMinRecallLower95, fmt.Sprintf("%s recall Wilson 95%% lower bound must be at least %.0f%%", split.name, promotionMinRecallLower95*100)},
			struct {
				failed bool
				text   string
			}{m.FalsePositive+m.TrueNegative == 0 || falseRate.Upper > promotionMaxFalseRate, fmt.Sprintf("%s false-positive-rate Wilson 95%% upper bound must be at most %.0f%%", split.name, promotionMaxFalseRate*100)},
		)
		if split.name != "overall" {
			checks = append(checks, struct {
				failed bool
				text   string
			}{m.Players < 5 || m.Matches < 5, split.name + " needs at least five distinct players and five matches"})
		}
		minGroups, maxShare := 5, 0.40
		if split.name == "overall" {
			minGroups, maxShare = 10, 0.25
		}
		if m.Clusters.Groups < minGroups || m.Clusters.PositiveGroups < 3 || m.Clusters.NegativeGroups < 3 {
			metric.Reasons = append(metric.Reasons, fmt.Sprintf("%s needs at least %d independent connected player/session groups, with positives and negatives in at least three groups", split.name, minGroups))
		}
		if m.Clusters.LargestGroupFraction > maxShare {
			metric.Reasons = append(metric.Reasons, fmt.Sprintf("%s is dominated by one connected group (maximum %.0f%% of opportunities)", split.name, maxShare*100))
		}
	}
	checkStrata := func(kind string, keys []string, values map[string]confusionMetric) {
		for _, key := range keys {
			m := values[key]
			if m.NegativeOpportunities < 20 || m.Clusters.NegativeGroups < 3 {
				metric.Reasons = append(metric.Reasons, fmt.Sprintf("%s stratum %s needs 20 legitimate opportunities from three connected groups", kind, key))
			}
		}
	}
	checkStrata("ping", []string{"low_under_80ms", "medium_80_150ms", "high_over_150ms"}, metric.ByPing)
	checkStrata("capture", []string{"low_under_12hz", "standard_12_22hz", "high_over_22hz"}, metric.ByCaptureRate)
	legal := []string{"normal", "transition"}
	if strings.HasPrefix(metric.DetectorID, "THROW_") {
		legal = []string{"normal", "stack", "block_push", "slap", "headbutt"}
	} else if strings.HasPrefix(metric.DetectorID, "MOV_") {
		legal = []string{"normal", "lean", "stack", "block_push"}
	}
	checkStrata("legal context", legal, metric.ByLegalContext)
	for _, check := range checks {
		if check.failed {
			metric.Reasons = append(metric.Reasons, check.text)
		}
	}
	metric.Eligible = len(metric.Reasons) == 0
}

func (s *server) buildCalibrationDashboard(ctx context.Context, candidateEvents map[string][]model.DetectionEvent) (calibrationDashboard, error) {
	if err := s.recordCalibrationViewExposure(ctx); err != nil {
		return calibrationDashboard{}, fmt.Errorf("recording evaluation exposure: %w", err)
	}
	store := s.engine.Store()
	matches, err := store.ListMatches(ctx, 100000)
	if err != nil {
		return calibrationDashboard{}, err
	}
	splitByMatch, assignments, err := s.isolatedDatasetSplits(ctx, matches)
	if err != nil {
		return calibrationDashboard{}, fmt.Errorf("verifying dataset isolation: %w", err)
	}
	reviews, err := store.ListEventReviews(ctx, time.Time{})
	if err != nil {
		return calibrationDashboard{}, err
	}
	opportunities, err := store.ListCalibrationOpportunities(ctx, "", "")
	if err != nil {
		return calibrationDashboard{}, err
	}
	neededMatches := make(map[string]bool)
	for _, review := range reviews {
		neededMatches[review.MatchID] = true
	}
	for _, opportunity := range opportunities {
		neededMatches[opportunity.MatchID] = true
	}
	meta := s.calibrationMatchMeta(ctx, matches, neededMatches)
	samples := samplesFromLabels(reviews, opportunities, meta)
	clusterByMatch := make(map[string]string)
	for _, a := range assignments {
		clusterByMatch[a.MatchID] = a.ClusterKey
	}
	candidate, candidateErr := s.blindCandidateFingerprint()
	if candidateErr != nil {
		return calibrationDashboard{}, candidateErr
	}
	proofs, proofErr := store.VerifiedBlindOpportunities(ctx, opportunities, candidate)
	if proofErr != nil {
		return calibrationDashboard{}, proofErr
	}
	measurable := samples[:0]
	for _, sample := range samples {
		if splitByMatch[sample.MatchID] != "" {
			sample.ClusterID = clusterByMatch[sample.MatchID]
			sample.LegalContext = "unknown"
			if proof, ok := proofs[sample.OpportunityID]; ok {
				sample.IndependentEvidence, sample.LegalContext = proof.Verified, proof.LegalContext
			}
			if sample.Truth == sqlite.GroundTruthPositive || sample.Truth == sqlite.GroundTruthNegative {
				available, sampleErr := store.CalibrationWindowHasSamples(ctx, sample.MatchID, sample.PlayerID, sample.FrameStart, sample.FrameEnd)
				if sampleErr != nil {
					return calibrationDashboard{}, sampleErr
				}
				sample.NoWindowSamples = !available
			}
			measurable = append(measurable, sample)
		}
	}
	samples = measurable
	if candidateEvents == nil {
		candidateEvents = make(map[string][]model.DetectionEvent)
		seen := make(map[string]bool)
		for _, sample := range samples {
			if seen[sample.MatchID] {
				continue
			}
			seen[sample.MatchID] = true
			events, eventErr := store.GetMatchEvents(ctx, sample.MatchID)
			if eventErr != nil {
				return calibrationDashboard{}, fmt.Errorf("loading current detector events for %s: %w", sample.MatchID, eventErr)
			}
			candidateEvents[sample.MatchID] = events
		}
	}
	byID := metricsForSamples(samples, candidateEvents, splitByMatch)
	for _, spec := range config.DetectorSpecs() {
		metric := byID[spec.ID]
		metric.DetectorID = spec.ID
		applyPromotionGate(&metric)
		byID[spec.ID] = metric
	}
	detectors := make([]detectorCalibrationMetric, 0, len(byID))
	for _, metric := range byID {
		detectors = append(detectors, metric)
	}
	sort.Slice(detectors, func(i, j int) bool { return detectors[i].DetectorID < detectors[j].DetectorID })
	splits := map[string][]string{"training": {}, "validation": {}, "holdout": {}, "quarantined": {}}
	for matchID, split := range splitByMatch {
		splits[split] = append(splits[split], matchID)
	}
	for _, ids := range splits {
		sort.Strings(ids)
	}
	blind := 0
	for _, sample := range samples {
		if sample.BlindReview {
			blind++
		}
	}
	return calibrationDashboard{Detectors: detectors, Splits: splits, Assignments: assignments,
		ValidationLimitations: []string{
			"Reserved holdout is not a sealed/blinded prospective test: ordinary replay views and dashboard metrics can expose findings. First exposure locks the app/build/behavior candidate; changes quarantine that cohort.",
			"Attached evidence bytes, window/candidate hashes and two immutable ballots are verified locally; reviewer identities and independence remain unauthenticated human attestations.",
			"Wilson intervals describe labeled opportunities under an independence assumption. Connected-group ranges/macro averages describe observed heterogeneity, not population confidence intervals; group/stratum floors are conservative policy, not measured validity.",
			"Public release/automatic enforcement remain unvalidated; require a prospectively collected external holdout under a locked candidate and independent review protocol.",
		},
		Samples: len(samples), BlindSamples: blind, PlayerLeakage: len(splits["quarantined"]) > 0,
		Requirements: map[string]any{"positive_opportunities": promotionMinPositive, "legitimate_opportunities": promotionMinNegative,
			"distinct_players": promotionMinPlayers, "distinct_matches": promotionMinMatches,
			"holdout_positive": promotionMinHoldoutPositive, "holdout_legitimate": promotionMinHoldoutNegative,
			"validation_positive": promotionMinValidationPositive, "validation_legitimate": promotionMinValidationNegative,
			"minimum_precision": promotionMinPrecision, "minimum_recall": promotionMinRecall,
			"maximum_false_positive_rate": promotionMaxFalseRate, "minimum_precision_lower95": promotionMinPrecision,
			"minimum_recall_lower95": promotionMinRecallLower95, "maximum_false_rate_upper95": promotionMaxFalseRate,
			"independent_second_review_required": true, "hash_bound_blind_ballots_required": true,
			"minimum_overall_connected_groups": 10, "minimum_evaluation_connected_groups": 5, "minimum_positive_and_negative_groups": 3,
			"maximum_overall_group_fraction": .25, "maximum_evaluation_group_fraction": .4,
			"minimum_stratum_legitimate_opportunities": 20, "minimum_stratum_negative_groups": 3,
			"isolation_policy_version": sqlite.CalibrationSplitPolicyVersion, "auto_enforce": false},
		Notice: "Conservative human-review policy, not production validation: promotion needs actual hash-bound evidence and two agreeing immutable pre-reveal ballots, diverse connected groups and representative legal/ping/capture controls, plus the existing Wilson policy. Group ranges are descriptive, not population confidence. Reserved holdout is not sealed; changed candidates quarantine exposure. Automatic enforcement remains disabled."}, nil
}

func (s *server) handleCalibrationOpportunities(w http.ResponseWriter, r *http.Request) {
	items, err := s.engine.Store().ListCalibrationOpportunities(r.Context(), r.URL.Query().Get("match_id"), r.URL.Query().Get("detector_id"))
	if err != nil {
		writeError(w, 500, "loading calibration opportunities: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"opportunities": items})
}

// handleCalibrationReport emits a self-identifying, hashed promotion packet.
// The hash covers the payload exactly as encoded below and lets reviewers keep
// a durable record of the build, physics, thresholds, splits, and confidence
// intervals on which a promotion decision was based.
func (s *server) handleCalibrationReport(w http.ResponseWriter, r *http.Request) {
	dashboard, err := s.buildCalibrationDashboard(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "building calibration report: %v", err)
		return
	}
	runs, _ := s.engine.Store().ListAnalysisRuns(r.Context(), "", 500)
	dashboard.Drift = computeDrift(runs)
	promotions, err := s.engine.Store().ListDetectorPromotions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading detector promotions: %v", err)
		return
	}
	counts, err := s.engine.Store().MatchLabelCounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading calibration library labels: %v", err)
		return
	}
	stored, err := s.engine.Store().GetStoredMatchCount(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "counting calibration replays: %v", err)
		return
	}
	labeled := counts[sqlite.MatchLabelKnownClean] + counts[sqlite.MatchLabelSuspected] + counts[sqlite.MatchLabelConfirmedCheat]
	payload := calibrationReportPayload{
		SchemaVersion: "nevr-calibration-report/v1", GeneratedAt: time.Now().UTC(),
		AppVersion: appVersion, BuildCommit: buildCommit,
		ConfigFingerprint: s.configFingerprint(), CalibrationFingerprint: calibrationFingerprint(s.engine.Config()),
		ActiveProfile: s.activeProfileName(r.Context()), Dashboard: dashboard, Promotions: promotions,
		EffectiveDetectors: s.engine.Config().EffectiveTable(), Physics: s.engine.Physics(),
		Library: calibrationLibrarySnapshot{MatchLabels: counts, StoredMatches: stored, LabeledMatches: labeled, UnlabeledMatches: maxInt(0, stored-labeled)},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding calibration report: %v", err)
		return
	}
	sum := sha256.Sum256(raw)
	report := calibrationReport{SHA256: hex.EncodeToString(sum[:]), Payload: payload}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="nevr-calibration-%s.json"`, payload.GeneratedAt.Format("20060102T150405Z")))
	w.Header().Set("X-NEVR-Payload-SHA256", report.SHA256)
	writeJSON(w, http.StatusOK, report)
}

func (s *server) handleStoreCalibrationOpportunity(w http.ResponseWriter, r *http.Request) {
	var item sqlite.CalibrationOpportunity
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&item); err != nil {
		writeError(w, 400, "invalid calibration opportunity: %v", err)
		return
	}
	if _, ok := config.DetectorSpecFor(strings.ToUpper(strings.TrimSpace(item.DetectorID))); !ok {
		writeError(w, 400, "unknown detector %q", item.DetectorID)
		return
	}
	stored, err := s.engine.Store().StoreCalibrationOpportunity(r.Context(), item)
	if err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	s.reconcileCalibrationChange(r.Context())
	writeJSON(w, 200, stored)
}

func (s *server) handleDeleteCalibrationOpportunity(w http.ResponseWriter, r *http.Request) {
	ok, err := s.engine.Store().DeleteCalibrationOpportunity(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "deleting calibration opportunity: %v", err)
		return
	}
	if !ok {
		writeError(w, 404, "calibration opportunity not found")
		return
	}
	s.reconcileCalibrationChange(r.Context())
	writeJSON(w, 200, map[string]any{"deleted": true})
}

func (s *server) handlePromotions(w http.ResponseWriter, r *http.Request) {
	items, err := s.engine.Store().ListDetectorPromotions(r.Context())
	if err != nil {
		writeError(w, 500, "loading promotions: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"promotions": items, "auto_enforce": false})
}

func (s *server) handleStoreThresholdCandidate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DetectorID string  `json:"detector_id"`
		Parameter  string  `json:"parameter"`
		Value      float64 `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeError(w, 400, "invalid threshold candidate: %v", err)
		return
	}
	body.DetectorID = strings.ToUpper(strings.TrimSpace(body.DetectorID))
	candidate := cloneConfig(s.engine.Config())
	enabled := true
	applied, unit, err := setSandboxParam(candidate, thresholdPreviewRequest{Detector: body.DetectorID, Parameter: body.Parameter, Value: body.Value, Enabled: &enabled})
	if err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	candidate.Detectors = shadowSafeDetectors(candidate.Detectors)
	doc, err := json.Marshal(candidate.Detectors)
	if err != nil {
		writeError(w, 500, "encoding candidate profile: %v", err)
		return
	}
	profileName := "Threshold candidate " + body.DetectorID
	profile, err := s.engine.Store().StoreConfigProfile(r.Context(), sqlite.ConfigProfile{
		Name: profileName, Description: fmt.Sprintf("Shadow-only candidate: %s.%s = %v %s", body.DetectorID, body.Parameter, applied, unit), DetectorsJSON: string(doc),
	})
	if err != nil {
		writeError(w, 500, "storing threshold candidate: %v", err)
		return
	}
	promotions, err := s.engine.Store().ListDetectorPromotions(r.Context())
	if err != nil {
		writeError(w, 500, "checking existing promotions: %v", err)
		return
	}
	for _, promotion := range promotions {
		if promotion.Status != sqlite.PromotionActive {
			continue
		}
		_, rollbackErr := s.rollbackDetector(r.Context(), promotion.DetectorID)
		if rollbackErr != nil {
			writeError(w, 500, "revoking existing promotion %s: %v", promotion.DetectorID, rollbackErr)
			return
		}
	}
	if err := s.engine.Store().ActivateConfigProfile(r.Context(), profile.Name); err != nil {
		writeError(w, 500, "activating threshold candidate: %v", err)
		return
	}
	profile.Active = true
	promotion, err := s.engine.Store().StoreDetectorPromotion(r.Context(), sqlite.DetectorPromotion{
		DetectorID: body.DetectorID, ProfileName: profile.Name, Status: sqlite.PromotionCandidate,
		Metrics: json.RawMessage(`{}`), ConfigFingerprint: shortHash(doc),
	})
	if err != nil {
		writeError(w, 500, "recording threshold candidate: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"profile": profile, "promotion": promotion, "restart_required": true,
		"message": fmt.Sprintf("Saved %s.%s = %v %s as a shadow-only profile. Restart, reanalyze the library, then inspect validation results.", body.DetectorID, body.Parameter, applied, unit)})
}

func detectorMetric(dashboard calibrationDashboard, detectorID string) (detectorCalibrationMetric, bool) {
	for _, metric := range dashboard.Detectors {
		if metric.DetectorID == detectorID {
			return metric, true
		}
	}
	return detectorCalibrationMetric{}, false
}

// handlePromoteDetector is deliberately the only path that can create a
// desktop profile containing review mode. Imported and hand-saved profiles
// remain forced shadow-only by applyActiveProfile.
func (s *server) handlePromoteDetector(w http.ResponseWriter, r *http.Request) {
	detectorID := strings.ToUpper(strings.TrimSpace(r.PathValue("detector")))
	if _, ok := config.DetectorSpecFor(detectorID); !ok {
		writeError(w, 400, "unknown detector %q", detectorID)
		return
	}
	if reason := config.DetectorPauseReason(detectorID); reason != "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "detector is paused and cannot be promoted", "promotion_block": reason,
		})
		return
	}
	// This detector is immutable observation-only, even if stored calibration
	// labels would otherwise meet every statistical gate. Reject before any
	// profile or promotion writes, matching config and emission safeguards.
	if detectorID == "STATE_008" || detectorID == "STATE_001" || detectorID == "THROW_005" || detectorID == "THROW_006" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "detector is observation-only and cannot be promoted", "promotion_block": promotionBlocked[detectorID],
		})
		return
	}
	if profile, active, profileErr := s.engine.Store().GetActiveConfigProfile(r.Context()); profileErr != nil {
		writeError(w, 500, "checking active calibration profile: %v", profileErr)
		return
	} else if active {
		var selectedDetectors map[string]config.DetectorConfig
		if err := json.Unmarshal([]byte(profile.DetectorsJSON), &selectedDetectors); err != nil {
			writeError(w, 409, "the selected profile is invalid; select a valid shadow profile and restart")
			return
		}
		selected := cloneConfig(s.engine.Config())
		selected.Detectors = selectedDetectors
		if calibrationFingerprint(selected) != calibrationFingerprint(s.engine.Config()) {
			writeError(w, 409, "restart NEVR and re-analyze the library with the selected threshold profile before promotion")
			return
		}
	}
	dashboard, err := s.buildCalibrationDashboard(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "evaluating promotion gate: %v", err)
		return
	}
	metric, ok := detectorMetric(dashboard, detectorID)
	if !ok || !metric.Eligible {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "detector has not passed the promotion gate", "metric": metric})
		return
	}
	promotions, err := s.engine.Store().ListDetectorPromotions(r.Context())
	if err != nil {
		writeError(w, 500, "checking staged promotions: %v", err)
		return
	}
	for _, promotion := range promotions {
		if promotion.Status == sqlite.PromotionActive && promotion.DetectorID != detectorID {
			writeError(w, 409, "only one detector may be promoted at a time; roll back %s first", promotion.DetectorID)
			return
		}
	}
	detectors := cloneConfig(s.engine.Config()).Detectors
	for id, detector := range detectors {
		detector.Mode = "shadow"
		detector.AutoEnforce = false
		if id == detectorID {
			detector.Enabled = true
			detector.Mode = "review"
			if detector.EnforcementWeight <= 0 {
				detector.EnforcementWeight = config.DefaultConfig().GetDetectorConfig(id).EnforcementWeight
			}
		} else {
			detector.EnforcementWeight = 0
		}
		detectors[id] = detector
	}
	doc, err := json.Marshal(detectors)
	if err != nil {
		writeError(w, 500, "encoding promotion profile: %v", err)
		return
	}
	profileName := "Calibration review " + detectorID
	profile, err := s.engine.Store().StoreConfigProfile(r.Context(), sqlite.ConfigProfile{
		Name: profileName, Description: "Promotion-gated single-detector review profile; automatic enforcement disabled", DetectorsJSON: string(doc),
	})
	if err != nil {
		writeError(w, 500, "storing promotion profile: %v", err)
		return
	}
	if err := s.engine.Store().ActivateConfigProfile(r.Context(), profile.Name); err != nil {
		writeError(w, 500, "activating promotion profile: %v", err)
		return
	}
	profile.Active = true
	metricsJSON, _ := json.Marshal(metric)
	promotion, err := s.engine.Store().StoreDetectorPromotion(r.Context(), sqlite.DetectorPromotion{
		DetectorID: detectorID, ProfileName: profile.Name, Status: sqlite.PromotionActive,
		Metrics: metricsJSON, ConfigFingerprint: shortHash(doc),
	})
	if err != nil {
		writeError(w, 500, "recording promotion approval: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"promotion": promotion, "profile": profile,
		"restart_required": true, "auto_enforce": false,
		"message": detectorID + " passed the gate. Restart NEVR to enter scored human-review mode for this detector only."})
}

func (s *server) handleRollbackDetector(w http.ResponseWriter, r *http.Request) {
	detectorID := strings.ToUpper(strings.TrimSpace(r.PathValue("detector")))
	changed, err := s.rollbackDetector(r.Context(), detectorID)
	if err != nil {
		writeError(w, 500, "rolling back promotion: %v", err)
		return
	}
	if !changed {
		writeError(w, 404, "no active promotion exists for %s", detectorID)
		return
	}
	writeJSON(w, 200, map[string]any{"rolled_back": true, "detector_id": detectorID,
		"message": detectorID + " returned to shadow mode immediately; restart keeps the rollback in force."})
}

func (s *server) rollbackDetector(ctx context.Context, detectorID string) (bool, error) {
	changed, err := s.engine.Store().RollBackDetectorPromotion(ctx, detectorID)
	if err != nil || !changed {
		return changed, err
	}
	s.analyzeMu.Lock()
	detector := s.engine.Config().GetDetectorConfig(detectorID)
	detector.Mode, detector.AutoEnforce, detector.EnforcementWeight = "shadow", false, 0
	s.engine.Config().Detectors[detectorID] = detector
	s.analyzeMu.Unlock()
	return true, nil
}

func (s *server) rollBackAllPromotions(ctx context.Context) []string {
	promotions, err := s.engine.Store().ListDetectorPromotions(ctx)
	if err != nil {
		return nil
	}
	var rolledBack []string
	for _, promotion := range promotions {
		if promotion.Status != sqlite.PromotionActive {
			continue
		}
		if changed, _ := s.rollbackDetector(ctx, promotion.DetectorID); changed {
			rolledBack = append(rolledBack, promotion.DetectorID)
		}
	}
	return rolledBack
}

func (s *server) reconcileCalibrationChange(ctx context.Context) {
	dashboard, err := s.buildCalibrationDashboard(ctx, nil)
	if err == nil {
		s.reconcilePromotions(ctx, dashboard)
		return
	}
	rolledBack := s.rollBackAllPromotions(ctx)
	if len(rolledBack) > 0 {
		s.engine.Logger().Warn("calibration could not be verified after an evidence change; active promotions were returned to shadow", "detectors", rolledBack, "error", err)
	}
}

// reconcilePromotions fails closed when new labels make an active detector no
// longer satisfy its gate. It is called after reviews and when the validation
// dashboard is opened.
func (s *server) reconcilePromotions(ctx context.Context, dashboard calibrationDashboard) []string {
	promotions, err := s.engine.Store().ListDetectorPromotions(ctx)
	if err != nil {
		return nil
	}
	var rolledBack []string
	for _, promotion := range promotions {
		if promotion.Status != sqlite.PromotionActive {
			continue
		}
		metric, ok := detectorMetric(dashboard, promotion.DetectorID)
		if ok && metric.Eligible {
			continue
		}
		if changed, _ := s.rollbackDetector(ctx, promotion.DetectorID); !changed {
			continue
		}
		rolledBack = append(rolledBack, promotion.DetectorID)
	}
	return rolledBack
}
