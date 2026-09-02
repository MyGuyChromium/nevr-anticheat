package scoring

import (
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func testConfig() ScorerConfig {
	return ScorerConfig{
		MaxSingleContribution:         15,
		MaxContribPerDetectorPerMatch: 2,
		SameCategoryDiminishing:       0.8,
		ReviewThreshold:               60,
		AutoEnforceThreshold:          95,
		DecayHalfLifeHours:            168,
		CooldownFrames:                300,
		CorrelationBonusCap:           15,
	}
}

func ev(det, player string, frame int, sev, conf, weight float64) model.DetectionEvent {
	return model.DetectionEvent{
		EventID:           fmt.Sprintf("%s-%s-%d", det, player, frame),
		DetectorID:        det,
		PlayerID:          player,
		MatchID:           "m1",
		FrameIndex:        frame,
		FrameRangeStart:   frame,
		FrameRangeEnd:     frame,
		Timestamp:         float64(frame) / 15.0,
		Severity:          sev,
		Confidence:        conf,
		EnforcementWeight: weight,
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSingleContributionCap(t *testing.T) {
	s := NewSuspicionScorer(testConfig())
	got := s.IngestEvent(ev("THROW_001", "p", 0, 1, 1, 1)) // raw 100 -> capped 15
	if !near(got.BaseScore, 15) || !near(got.TotalScore, 15) {
		t.Fatalf("expected capped contribution 15, got base=%.2f total=%.2f", got.BaseScore, got.TotalScore)
	}
	if got.HighestSingleEvent != 15 || got.HighestSingleDetector != "THROW_001" {
		t.Errorf("highest single not tracked: %.2f %s", got.HighestSingleEvent, got.HighestSingleDetector)
	}
}

func TestPerDetectorCap(t *testing.T) {
	s := NewSuspicionScorer(testConfig())
	for i := 0; i < 6; i++ {
		s.IngestEvent(ev("THROW_001", "p", i*1000, 0.5, 0.5, 1))
	}
	sc := s.GetScore("p")
	if sc.EventCount != 2 || sc.DetectorCounts["THROW_001"] != 2 {
		t.Fatalf("expected 2 scored events from per-detector cap, got %d", sc.EventCount)
	}
	// A capped detector must not keep refreshing its cooldown frame.
	if sc.LastEventFrames["THROW_001"] != 1000 {
		t.Errorf("capped detector refreshed cooldown: last frame %d", sc.LastEventFrames["THROW_001"])
	}
}

func TestCooldownSuppressesNearbyEvents(t *testing.T) {
	cfg := testConfig()
	cfg.MaxContribPerDetectorPerMatch = 100
	s := NewSuspicionScorer(cfg)
	s.IngestEvent(ev("MOV_001", "p", 100, 0.5, 0.5, 1))
	s.IngestEvent(ev("MOV_001", "p", 140, 0.5, 0.5, 1)) // < 100 frames later: always suppressed
	if sc := s.GetScore("p"); sc.EventCount != 1 {
		t.Fatalf("expected cooldown to suppress, got %d events", sc.EventCount)
	}
	s.IngestEvent(ev("MOV_001", "p", 100+351, 0.5, 0.5, 1)) // beyond cooldown+jitter (max 350)
	if sc := s.GetScore("p"); sc.EventCount != 2 {
		t.Fatalf("expected event beyond cooldown to score, got %d events", sc.EventCount)
	}
	// Cooldown floor is 100 frames even when configured lower.
	cfg.CooldownFrames = 0
	s2 := NewSuspicionScorer(cfg)
	s2.IngestEvent(ev("MOV_001", "p", 0, 0.5, 0.5, 1))
	s2.IngestEvent(ev("MOV_001", "p", 99, 0.5, 0.5, 1))
	if sc := s2.GetScore("p"); sc.EventCount != 1 {
		t.Fatalf("expected 100-frame cooldown floor, got %d events", sc.EventCount)
	}
}

func TestSameCategoryDiminishing(t *testing.T) {
	cfg := testConfig()
	cfg.MaxContribPerDetectorPerMatch = 100
	s := NewSuspicionScorer(cfg)
	a := s.IngestEvent(ev("THROW_001", "p", 0, 0.5, 0.5, 0.4)).BaseScore    // 10
	b := s.IngestEvent(ev("THROW_002", "p", 1000, 0.5, 0.5, 0.4)).BaseScore // +8
	c := s.IngestEvent(ev("THROW_003", "p", 2000, 0.5, 0.5, 0.4)).BaseScore // +6.4
	if !near(a, 10) || !near(b-a, 8) || !near(c-b, 6.4) {
		t.Fatalf("diminishing wrong: %.2f %.2f %.2f", a, b-a, c-b)
	}
}

func TestTotalScoreClamp(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSingleContribution = 100
	cfg.SameCategoryDiminishing = 1
	cfg.MaxContribPerDetectorPerMatch = 100
	s := NewSuspicionScorer(cfg)
	for i := 0; i < 3; i++ {
		s.IngestEvent(ev("MOV_001", "p", i*1000, 1, 1, 1))
	}
	sc := s.GetScore("p")
	if sc.BaseScore != 300 || sc.TotalScore != 100 {
		t.Fatalf("expected base 300 clamped to 100, got base=%.1f total=%.1f", sc.BaseScore, sc.TotalScore)
	}
	if sc.Level() != model.LevelActionWorthy || !sc.ExceedsReview || !sc.ExceedsAutoEnforce {
		t.Errorf("flags wrong at 100: level=%s review=%v enforce=%v", sc.Level(), sc.ExceedsReview, sc.ExceedsAutoEnforce)
	}
}

func TestShadowEventsNeverScore(t *testing.T) {
	s := NewSuspicionScorer(testConfig())
	e := ev("THROW_001", "p", 0, 1, 1, 1)
	e.IsShadow = true
	got := s.IngestEvent(e)
	if got.TotalScore != 0 || got.EventCount != 0 || len(got.DetectorCounts) != 0 {
		t.Fatalf("shadow event scored: %+v", got)
	}
	s.ApplyCorrelationBonus()
	if s.GetScore("p").TotalScore != 0 {
		t.Fatal("shadow-only player gained a bonus")
	}
}

func TestCorrelationBonusIdempotent(t *testing.T) {
	s := NewSuspicionScorer(testConfig())
	s.IngestEvent(ev("MOV_001", "p", 0, 0.5, 0.5, 0.6))    // 15 -> 15
	s.IngestEvent(ev("BIO_002", "p", 1000, 0.5, 0.5, 0.6)) // 15 -> 30
	base := s.GetScore("p").TotalScore
	if !near(base, 30) {
		t.Fatalf("base setup wrong: %.2f", base)
	}
	for i := 0; i < 50; i++ {
		s.ApplyCorrelationBonus()
	}
	sc := s.GetScore("p")
	if !near(sc.CorrelationBonus, 5) || !near(sc.TotalScore, 35) || !near(sc.BaseScore, 30) {
		t.Fatalf("bonus not idempotent: base=%.2f bonus=%.2f total=%.2f", sc.BaseScore, sc.CorrelationBonus, sc.TotalScore)
	}
	// A third independent category raises the bonus to 10, still idempotent.
	s.IngestEvent(ev("STATE_001", "p", 2000, 0.5, 0.5, 0.6))
	s.ApplyCorrelationBonus()
	s.ApplyCorrelationBonus()
	sc = s.GetScore("p")
	if !near(sc.CorrelationBonus, 10) || !near(sc.TotalScore, 55) {
		t.Fatalf("expected bonus 10 total 55, got bonus=%.2f total=%.2f", sc.CorrelationBonus, sc.TotalScore)
	}
}

func TestCorrelationBonusCap(t *testing.T) {
	cfg := testConfig()
	cfg.CorrelationBonusCap = 7
	s := NewSuspicionScorer(cfg)
	for i, det := range []string{"THROW_001", "BIO_001", "MOV_001", "STATE_001", "PAT_001"} {
		s.IngestEvent(ev(det, "p", i*1000, 0.2, 0.5, 0.5))
	}
	s.ApplyCorrelationBonus()
	if b := s.GetScore("p").CorrelationBonus; !near(b, 7) {
		t.Fatalf("expected bonus capped at 7, got %.2f", b)
	}
}

func TestMetaDetectorsDoNotAddCategory(t *testing.T) {
	s := NewSuspicionScorer(testConfig())
	s.IngestEvent(ev("THROW_001", "p", 0, 0.5, 0.5, 0.6))
	s.IngestEvent(ev("BIO_001", "p", 1000, 0.5, 0.5, 0.6))
	s.IngestEvent(ev("MOV_001", "p", 2000, 0.5, 0.5, 0.6))
	s.IngestEvent(ev("PAT_004", "p", 3000, 0.5, 0.5, 0.6)) // composite meta-detector
	s.IngestEvent(ev("PAT_003", "p", 4000, 0.5, 0.5, 0.6)) // cross-match meta-detector
	s.ApplyCorrelationBonus()
	sc := s.GetScore("p")
	if !near(sc.CorrelationBonus, 10) {
		t.Fatalf("meta-detectors inflated bonus: got %.2f want 10 (3 independent categories)", sc.CorrelationBonus)
	}
	// A genuine pattern detector does add the category.
	s.IngestEvent(ev("PAT_001", "p", 5000, 0.5, 0.5, 0.6))
	s.ApplyCorrelationBonus()
	if b := s.GetScore("p").CorrelationBonus; !near(b, 15) {
		t.Fatalf("PAT_001 should add a category: got %.2f want 15", b)
	}
}

func TestReturnedScoresDoNotAlias(t *testing.T) {
	s := NewSuspicionScorer(testConfig())
	first := s.IngestEvent(ev("THROW_001", "p", 0, 0.5, 0.5, 0.6))
	all := s.GetAllScores()
	one := s.GetScore("p")
	s.IngestEvent(ev("BIO_001", "p", 1000, 0.5, 0.5, 0.6))
	for name, sc := range map[string]model.SuspicionScore{"ingest": first, "all": all["p"], "get": one} {
		if len(sc.DetectorCounts) != 1 || len(sc.ScoreByDetector) != 1 || sc.EventCount != 1 {
			t.Errorf("%s: returned copy mutated by later ingest: %+v", name, sc)
		}
	}
	// Mutating a returned copy must not touch the scorer.
	one.DetectorCounts["THROW_001"] = 99
	one.MatchIDs["zzz"] = true
	if s.GetScore("p").DetectorCounts["THROW_001"] != 1 || s.GetScore("p").MatchCount != 1 {
		t.Fatal("caller mutation leaked into scorer")
	}
}

func TestLevelTableFromReviewThreshold(t *testing.T) {
	cfg := testConfig()
	cfg.ReviewThreshold = 15
	s := NewSuspicionScorer(cfg)
	lv := s.Levels()
	if lv.HighRisk != 15 || lv.Suspicious != 15 || lv.Informational != 15 || lv.Critical != 80 || lv.ActionWorthy != 95 {
		t.Fatalf("threshold mapping wrong: %+v", lv)
	}
	sc := s.IngestEvent(ev("THROW_001", "p", 0, 1, 1, 1)) // 15
	if !sc.ExceedsReview || sc.Level() != model.LevelHighRisk || !s.ExceedsReviewThreshold("p") {
		t.Fatalf("ExceedsReview and Level disagree: review=%v level=%s", sc.ExceedsReview, sc.Level())
	}
	// Default table: 15 is clean and not review-worthy.
	s2 := NewSuspicionScorer(testConfig())
	sc2 := s2.IngestEvent(ev("THROW_001", "p", 0, 1, 1, 1))
	if sc2.ExceedsReview || sc2.Level() != model.LevelClean {
		t.Fatalf("default table: review=%v level=%s", sc2.ExceedsReview, sc2.Level())
	}
	// Levels alone (what the binaries pass: config.ScoringConfig.LevelTable()
	// with review_threshold already applied) is honoured verbatim.
	cfg3 := testConfig()
	cfg3.ReviewThreshold = 0
	cfg3.Levels = model.LevelTable{Informational: 5, Suspicious: 10, HighRisk: 15, Critical: 50, ActionWorthy: 90}
	s3 := NewSuspicionScorer(cfg3)
	if s3.Levels() != cfg3.Levels {
		t.Fatalf("Levels not honoured: %+v", s3.Levels())
	}
	sc3 := s3.IngestEvent(ev("THROW_001", "p", 0, 1, 1, 1))
	if !sc3.ExceedsReview || sc3.Level() != model.LevelHighRisk || sc3.ReviewThreshold != 15 {
		t.Fatalf("Levels-only config: review=%v level=%s threshold=%v", sc3.ExceedsReview, sc3.Level(), sc3.ReviewThreshold)
	}
}

func TestMatchCountAndTimes(t *testing.T) {
	cfg := testConfig()
	cfg.MaxContribPerDetectorPerMatch = 10
	s := NewSuspicionScorer(cfg)
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s.SetMatchStart(start)
	e1 := ev("THROW_001", "p", 150, 0.5, 0.5, 0.5) // t=10s
	e2 := ev("THROW_001", "p", 1500, 0.5, 0.5, 0.5)
	e2.MatchID = "m2" // t=100s
	s.IngestEvent(e1)
	sc := s.IngestEvent(e2)
	if sc.MatchCount != 2 || !sc.MatchIDs["m1"] || !sc.MatchIDs["m2"] {
		t.Fatalf("match count wrong: %d %v", sc.MatchCount, sc.MatchIDs)
	}
	if !sc.FirstEventTime.Equal(start.Add(10*time.Second)) || !sc.LastEventTime.Equal(start.Add(100*time.Second)) {
		t.Fatalf("event times not anchored on match start: first=%v last=%v", sc.FirstEventTime, sc.LastEventTime)
	}
}

func TestApplyDecay(t *testing.T) {
	cfg := testConfig()
	cfg.DecayHalfLifeHours = 24
	s := NewSuspicionScorer(cfg)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return t0 })
	s.IngestEvent(ev("THROW_001", "p", 0, 1, 1, 1))    // 15
	s.IngestEvent(ev("BIO_001", "p", 1000, 1, 1, 1))   // 30
	s.IngestEvent(ev("MOV_001", "p", 2000, 1, 1, 1))   // 45
	s.IngestEvent(ev("STATE_001", "p", 3000, 1, 1, 1)) // 60
	s.ApplyCorrelationBonus()                          // +15 -> 75
	s.getOrCreate("idle")                              // player with no scored events
	s.ApplyDecay(t0.Add(24 * time.Hour))
	sc := s.GetScore("p")
	if !near(sc.BaseScore, 30) || !near(sc.CorrelationBonus, 7.5) || !near(sc.TotalScore, 37.5) {
		t.Fatalf("one half-life should halve everything: base=%.2f bonus=%.2f total=%.2f", sc.BaseScore, sc.CorrelationBonus, sc.TotalScore)
	}
	if !near(sc.ScoreByDetector["THROW_001"], 7.5) || !near(sc.ScoreByCategory["bio"], 7.5) {
		t.Errorf("breakdowns not decayed: %v %v", sc.ScoreByDetector, sc.ScoreByCategory)
	}
	if sc.ExceedsReview || sc.Level() != model.LevelInformational {
		t.Errorf("flags not recomputed after decay: review=%v level=%s", sc.ExceedsReview, sc.Level())
	}
	if idle := s.GetScore("idle"); idle.TotalScore != 0 || !idle.LastDecayTime.IsZero() {
		t.Errorf("idle player should be untouched: %+v", idle)
	}
	// Decay is anchored on LastDecayTime: no elapsed time, no change.
	s.ApplyDecay(t0.Add(24 * time.Hour))
	if got := s.GetScore("p").TotalScore; !near(got, 37.5) {
		t.Errorf("repeated decay at same instant changed score: %.2f", got)
	}
}

func TestSnapshotJSONRoundTrip(t *testing.T) {
	s := NewSuspicionScorer(testConfig())
	s.IngestEvent(ev("THROW_001", "p", 0, 0.5, 0.5, 0.5))
	sc := s.GetScore("p")
	buf, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	var back model.SuspicionScore
	if err := json.Unmarshal(buf, &back); err != nil {
		t.Fatal(err)
	}
	if !back.IsRestorable() {
		t.Fatalf("JSON snapshot should be restorable: %+v", back)
	}
	if back.LastEventFrames["THROW_001"] != 0 || !back.MatchIDs["m1"] || back.Levels != sc.Levels {
		t.Fatalf("tracking state lost in round trip: %+v", back)
	}
	lossy := model.SuspicionScore{PlayerID: "p", TotalScore: 10, EventCount: 1}
	if lossy.IsRestorable() {
		t.Fatal("storage-shaped score must report itself as not restorable")
	}
}

func TestConcurrentIngestAndRead(t *testing.T) {
	cfg := testConfig()
	cfg.MaxContribPerDetectorPerMatch = 1000
	s := NewSuspicionScorer(cfg)
	var writers sync.WaitGroup
	for g := 0; g < 4; g++ {
		writers.Add(1)
		go func(g int) {
			defer writers.Done()
			for i := 0; i < 200; i++ {
				s.IngestEvent(ev(fmt.Sprintf("MOV_00%d", g+1), fmt.Sprintf("p%d", i%3), i*1000, 0.3, 0.5, 0.5))
				s.ApplyCorrelationBonus()
			}
		}(g)
	}
	done := make(chan struct{})
	go func() {
		writers.Wait()
		close(done)
	}()
	for reading := true; reading; {
		select {
		case <-done:
			reading = false
		default:
		}
		for _, sc := range s.GetAllScores() {
			for range sc.DetectorCounts {
			}
			_ = sc.Level()
		}
		_ = s.GetScore("p0")
	}
	total := 0
	for _, sc := range s.GetAllScores() {
		total += sc.EventCount
	}
	if total != 4*200 {
		t.Fatalf("expected 800 scored events, got %d", total)
	}
}
