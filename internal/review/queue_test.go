package review

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

// caseOnlyStore mimics today's sqlite.Store surface (no lifecycle methods).
type caseOnlyStore struct {
	cases map[string]model.ReviewCase
	fail  map[string]bool // player IDs whose store should fail
}

func newCaseOnlyStore() *caseOnlyStore {
	return &caseOnlyStore{cases: map[string]model.ReviewCase{}, fail: map[string]bool{}}
}

func (s *caseOnlyStore) StoreReviewCase(_ context.Context, rc model.ReviewCase) error {
	if s.fail[rc.PlayerID] {
		return errors.New("disk full")
	}
	s.cases[rc.CaseID] = rc
	return nil
}
func (s *caseOnlyStore) GetReviewCase(_ context.Context, id string) (model.ReviewCase, error) {
	rc, ok := s.cases[id]
	if !ok {
		return rc, errors.New("not found")
	}
	return rc, nil
}
func (s *caseOnlyStore) GetPendingReviewCases(_ context.Context, limit int) ([]model.ReviewCase, error) {
	var out []model.ReviewCase
	for _, rc := range s.cases {
		if rc.Status == model.CaseStatusPending && len(out) < limit {
			out = append(out, rc)
		}
	}
	return out, nil
}

// fullStore adds the lifecycle methods storage-cli is expected to provide.
type fullStore struct {
	*caseOnlyStore
	decisions []model.ModeratorDecision
}

func (s *fullStore) UpdateReviewCaseStatus(_ context.Context, caseID, status, assignedTo string) error {
	rc, ok := s.cases[caseID]
	if !ok {
		return errors.New("no such case")
	}
	rc.Status = status
	rc.AssignedTo = assignedTo
	s.cases[caseID] = rc
	return nil
}
func (s *fullStore) StoreModeratorDecision(_ context.Context, d model.ModeratorDecision) error {
	s.decisions = append(s.decisions, d)
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func ev(det, player string, frame int, shadow bool) model.DetectionEvent {
	return model.DetectionEvent{
		EventID: det + player, DetectorID: det, PlayerID: player, MatchID: "m1", FrameIndex: frame,
		Severity: 0.8, Confidence: 0.9, EnforcementWeight: 0.8, ObservedValue: "x", IsShadow: shadow,
	}
}

func sc(pid string, total float64) model.SuspicionScore {
	s := model.SuspicionScore{PlayerID: pid, TotalScore: total, EventCount: 1}
	s.Init()
	return s
}

func TestCreateCasesFromResult(t *testing.T) {
	st := newCaseOnlyStore()
	mc := &model.MatchContext{MatchID: "m1", StartTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	result := &pipeline.MatchResult{
		MatchID: "m1",
		PlayerScores: map[string]model.SuspicionScore{
			"zed":    sc("zed", 65),   // high_risk with events -> case
			"amy":    sc("amy", 95),   // action_worthy -> case, sorted first
			"bob":    sc("bob", 59.9), // below high_risk
			"shadow": sc("shadow", 70),
			"noev":   sc("noev", 90), // score but no events in result
		},
		DetectionEvents: []model.DetectionEvent{
			ev("THROW_001", "zed", 100, false), ev("MOV_001", "zed", 900, false),
			ev("THROW_001", "amy", 100, false),
			ev("THROW_001", "bob", 100, false),
			ev("THROW_001", "shadow", 100, true),
		},
	}
	fixed := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	cases, err := CreateCasesFromResult(context.Background(), st, mc, result, model.DefaultLevelTable(),
		WithLogger(quiet()), WithDetectorNames(map[string]string{"THROW_001": "Release Velocity Cap"}),
		WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 || cases[0].PlayerID != "amy" || cases[1].PlayerID != "zed" {
		t.Fatalf("expected cases for amy, zed in order; got %+v", cases)
	}
	if len(st.cases) != 2 {
		t.Fatalf("cases not stored: %d", len(st.cases))
	}
	zed := cases[1]
	if len(zed.DetectorsTriggered) != 2 || zed.DetectorsTriggered[0].DetectorName != "Release Velocity Cap" ||
		zed.DetectorsTriggered[0].FrameIndex != 100 || zed.DetectorsTriggered[0].EventID != "THROW_001zed" {
		t.Errorf("evidence incomplete: %+v", zed.DetectorsTriggered)
	}
	if zed.Level != "high_risk" || zed.Status != model.CaseStatusPending || zed.MatchID != "m1" || !zed.CreatedAt.Equal(fixed) {
		t.Errorf("case fields wrong: %+v", zed)
	}
	pending, _ := NewQueue(st, quiet()).GetPending(context.Background(), 10)
	if len(pending) != 2 {
		t.Errorf("expected 2 pending, got %d", len(pending))
	}

	// Lowered threshold table pulls bob in; min level option can raise it.
	st2 := newCaseOnlyStore()
	cases, _ = CreateCasesFromResult(context.Background(), st2, mc, result, model.DefaultLevelTable().WithReviewThreshold(15), WithLogger(quiet()))
	if len(cases) != 3 {
		t.Errorf("lowered table should create 3 cases, got %d", len(cases))
	}
	st3 := newCaseOnlyStore()
	cases, _ = CreateCasesFromResult(context.Background(), st3, mc, result, model.DefaultLevelTable(), WithLogger(quiet()), WithMinLevel(model.LevelActionWorthy))
	if len(cases) != 1 || cases[0].PlayerID != "amy" {
		t.Errorf("min level option ignored: %+v", cases)
	}

	// Store failure for one player does not stop the others.
	st4 := newCaseOnlyStore()
	st4.fail["amy"] = true
	cases, err = CreateCasesFromResult(context.Background(), st4, mc, result, model.DefaultLevelTable(), WithLogger(quiet()))
	if err == nil || len(cases) != 1 || cases[0].PlayerID != "zed" {
		t.Errorf("partial failure handling wrong: err=%v cases=%+v", err, cases)
	}
	if cs, err := CreateCasesFromResult(context.Background(), st4, mc, nil, model.DefaultLevelTable()); cs != nil || err != nil {
		t.Errorf("nil result should be a no-op")
	}
}

func TestLifecycle(t *testing.T) {
	base := newCaseOnlyStore()
	q := NewQueue(base, quiet())
	rc, err := q.Enqueue(context.Background(), "p", &model.MatchContext{MatchID: "m1"}, sc("p", 70), []model.DetectionEvent{ev("THROW_001", "p", 1, false)})
	if err != nil {
		t.Fatal(err)
	}
	// Store without lifecycle support: explicit error, never silent.
	if _, err := q.Assign(context.Background(), rc.CaseID, "mod1"); !errors.Is(err, ErrLifecycleUnsupported) {
		t.Fatalf("expected ErrLifecycleUnsupported, got %v", err)
	}
	dec := model.ModeratorDecision{CaseID: rc.CaseID, ModeratorID: "mod1", Verdict: model.VerdictFalsePositive,
		DetectorFeedback: []model.DetectorVerdict{{DetectorID: "THROW_001", Correct: model.DetectorVerdictNo}}}
	if _, err := q.Decide(context.Background(), dec); !errors.Is(err, ErrLifecycleUnsupported) {
		t.Fatalf("expected ErrLifecycleUnsupported, got %v", err)
	}

	full := &fullStore{caseOnlyStore: base}
	q2 := NewQueue(full, quiet())
	got, err := q2.Assign(context.Background(), rc.CaseID, "mod1")
	if err != nil || got.Status != model.CaseStatusAssigned || got.AssignedTo != "mod1" {
		t.Fatalf("assign: %v %+v", err, got)
	}
	if _, err := q2.Assign(context.Background(), rc.CaseID, ""); err == nil {
		t.Fatal("assign without moderator should fail")
	}
	got, err = q2.Start(context.Background(), rc.CaseID, "")
	if err != nil || got.Status != model.CaseStatusInReview || got.AssignedTo != "mod1" {
		t.Fatalf("start: %v %+v", err, got)
	}
	got, err = q2.Decide(context.Background(), dec)
	if err != nil || got.Status != model.CaseStatusDecided {
		t.Fatalf("decide: %v %+v", err, got)
	}
	if len(full.decisions) != 1 || full.decisions[0].DecisionID == "" || full.decisions[0].DecidedAt.IsZero() ||
		len(full.decisions[0].DetectorFeedback) != 1 {
		t.Fatalf("decision not persisted properly: %+v", full.decisions)
	}
	if pending, _ := q2.GetPending(context.Background(), 10); len(pending) != 0 {
		t.Fatalf("decided case still pending: %+v", pending)
	}
	// Deciding twice is an invalid transition.
	if _, err := q2.Decide(context.Background(), dec); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
	// Invalid verdict is rejected before touching the store.
	bad := dec
	bad.Verdict = "guilty"
	if _, err := q2.Decide(context.Background(), bad); err == nil {
		t.Fatal("invalid verdict accepted")
	}
	if _, err := q2.Assign(context.Background(), "nope", "mod1"); err == nil {
		t.Fatal("unknown case should error")
	}
	if ValidTransition(model.CaseStatusClosed, model.CaseStatusPending) || !ValidTransition(model.CaseStatusDecided, model.CaseStatusAppealed) {
		t.Fatal("transition table wrong")
	}
}
