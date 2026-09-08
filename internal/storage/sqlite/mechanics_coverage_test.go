package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"reflect"
	"testing"
)

func TestMechanicsCoverageAtomicAndLiveMerge(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	log := model.NewMechanicsReviewLog()
	log.Add(model.MechanicsAssessment{Kind: model.MechanicsGrabGeometry, Result: model.MechanicsInconclusive, Reason: "grab_geometry_unverified", RuleVersion: model.DefaultProjectRules().Version, EventID: "release-A", FrameIndex: 3, Timestamp: 1, IntervalStart: .9, IntervalEnd: 1})
	coverage := map[string]*model.PlayerCoverage{"P": {Version: 1, Status: "limited", ValidFrames: 20, Detectors: []model.DetectorCoverage{{DetectorID: "STATE_001", Enabled: true, MechanicsReview: log}}}}
	if _, err := s.WriteMatchAnalysis(ctx, MatchAnalysisWrite{MatchID: "M", Coverage: coverage}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.MergeMatchCatchReviews(ctx, "M", coverage); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetMatchAnalysisCoverage(ctx, "M")
	if err != nil {
		t.Fatal(err)
	}
	if got["P"].ValidFrames != 20 || got["P"].Detectors[0].MechanicsReview.Total != 1 {
		t.Fatal(got)
	}
	if err := s.MergeMatchCatchReviews(ctx, "LIVE", coverage); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetMatchAnalysisCoverage(ctx, "LIVE")
	if err != nil {
		t.Fatal(err)
	}
	if got["P"].Status != model.ReviewStatusInsufficientData || got["P"].ValidFrames != 0 || got["P"].Detectors[0].MechanicsReview.Total != 1 || got["P"].Detectors[0].CatchReview != nil {
		t.Fatal(got)
	}
	log.Total++
	if err := s.MergeMatchCatchReviews(ctx, "M", coverage); err == nil {
		t.Fatal("corrupt mechanics log accepted")
	}
	got, err = s.GetMatchAnalysisCoverage(ctx, "M")
	if err != nil || got["P"].Detectors[0].MechanicsReview.Total != 1 {
		t.Fatal("invalid merge changed stored report")
	}
	if _, err := s.WriteMatchAnalysis(ctx, MatchAnalysisWrite{MatchID: "M", Replace: true}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetMatchAnalysisCoverage(ctx, "M")
	if err != nil || got != nil {
		t.Fatal("replacement left stale evidence")
	}
}

func mechanicsCoverageChunk(start, end int) map[string]*model.PlayerCoverage {
	log := model.NewMechanicsReviewLog()
	for i := start; i < end; i++ {
		log.Add(model.MechanicsAssessment{Kind: model.MechanicsThrowPhysics, Result: model.MechanicsInconclusive, Reason: "release_rule_unverified", RuleVersion: model.DefaultProjectRules().Version, PlayerID: "P", EventID: fmt.Sprintf("release-%d", i), FrameIndex: i, Timestamp: float64(i), IntervalStart: float64(i), IntervalEnd: float64(i)})
	}
	return map[string]*model.PlayerCoverage{"P": {Version: 1, Status: "limited", ValidFrames: 100, Detectors: []model.DetectorCoverage{{DetectorID: "THROW_001", Enabled: true, Status: "evaluated", InputFrames: 25, MechanicsReview: log}}}}
}

func TestMechanicsCoverageLaterPartialChunkCannotRetainAdequateClaim(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := mechanicsCoverageChunk(0, 2)
	if _, err := s.WriteMatchAnalysis(ctx, MatchAnalysisWrite{MatchID: "M", Coverage: base}); err != nil {
		t.Fatal(err)
	}
	// An exact replay changes neither confidence/coverage nor counters.
	if err := s.MergeMatchCatchReviews(ctx, "M", base); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetMatchAnalysisCoverage(ctx, "M")
	if got["P"].Status != "limited" {
		t.Fatal("idempotent replay downgraded existing analysis")
	}
	next := mechanicsCoverageChunk(2, 4)
	for i := 0; i < 2; i++ {
		if err := s.MergeMatchCatchReviews(ctx, "M", next); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = s.GetMatchAnalysisCoverage(ctx, "M")
	p := got["P"]
	d := p.Detectors[0]
	if p.Status != model.ReviewStatusInsufficientData || p.ValidFrames != 100 || d.InputFrames != 25 || d.MechanicsReview.Total != 4 || len(p.Limitations) != 1 {
		t.Fatalf("partial chunk fabricated coverage or repeated limitation: %+v %+v", p, d)
	}
	if result := model.AssessPlayerWithCoverage("P", nil, p); result.Status != model.ReviewStatusInsufficientData {
		t.Fatal("silence in partial mechanics data was reported as adequately covered")
	}
}

func TestMechanicsCoverageRejectsWrongPlayerAndCorruptStoredSummary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := mechanicsCoverageChunk(0, 1)
	if err := s.MergeMatchCatchReviews(ctx, "M", base); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetMatchAnalysisCoverage(ctx, "M")
	wrong := mechanicsCoverageChunk(1, 2)
	wrong["P"].Detectors[0].MechanicsReview.Records[0].PlayerID = "OTHER"
	if err := s.MergeMatchCatchReviews(ctx, "M", wrong); err == nil {
		t.Fatal("evidence assigned to wrong player")
	}
	after, _ := s.GetMatchAnalysisCoverage(ctx, "M")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected merge mutated stored evidence")
	}
	corrupt := mechanicsCoverageChunk(0, 1)
	corrupt["P"].Detectors[0].MechanicsReview.Total++
	data, _ := json.Marshal(corrupt)
	if _, err := s.db.Exec(`UPDATE match_analysis_coverage SET coverage_json=? WHERE match_id='M'`, string(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMatchAnalysisCoverage(ctx, "M"); err == nil {
		t.Fatal("corrupt stored mechanics summary presented as valid coverage")
	}
	if err := s.MergeMatchCatchReviews(ctx, "M", base); err == nil {
		t.Fatal("merge concealed corrupt existing summary")
	}
}

func TestMechanicsCoverageBoundedMergePersistsIdempotentTail(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, chunk := range []map[string]*model.PlayerCoverage{mechanicsCoverageChunk(0, 150), mechanicsCoverageChunk(150, 300), mechanicsCoverageChunk(150, 300)} {
		if err := s.MergeMatchCatchReviews(ctx, "M", chunk); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetMatchAnalysisCoverage(ctx, "M")
	log := got["P"].Detectors[0].MechanicsReview
	if log.Total != 300 || log.Dropped != 172 || len(log.Records) != 128 || len(log.RecentEvents) != 128 || log.Validate() != nil {
		t.Fatalf("bounded merge counters=%+v", log)
	}
	if err := s.MergeMatchCatchReviews(ctx, "M", mechanicsCoverageChunk(160, 400)); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetMatchAnalysisCoverage(ctx, "M")
	if !reflect.DeepEqual(after, got) {
		t.Fatal("overlap inside omitted tail changed persisted counts")
	}
}
