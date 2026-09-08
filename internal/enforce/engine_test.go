package enforce

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type memStore struct {
	mu      sync.Mutex
	actions []model.EnforcementAction
	err     error
}

func (m *memStore) StoreEnforcementAction(_ context.Context, a model.EnforcementAction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.actions = append(m.actions, a)
	return nil
}

func (m *memStore) StoreEnforcementActionOnce(_ context.Context, a model.EnforcementAction) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	for _, existing := range m.actions {
		if existing.ActionID == a.ActionID {
			return false, nil
		}
	}
	m.actions = append(m.actions, a)
	return true, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func mkEvent(det, player string, conf float64, hard bool) model.DetectionEvent {
	return model.DetectionEvent{
		EventID: det + "-" + player, DetectorID: det, PlayerID: player, MatchID: "m1",
		Severity: 0.9, Confidence: conf, EnforcementWeight: 1, AutoEnforce: hard,
	}
}

func score(total float64, matches int) model.SuspicionScore {
	sc := model.SuspicionScore{PlayerID: "p", TotalScore: total, MatchCount: matches}
	sc.Init()
	sc.MatchIDs["m0"] = true
	return sc
}

func newEngine(mode Mode, store ActionStore) *Engine {
	cfg := DefaultEngineConfig()
	cfg.Mode = mode
	e := NewEngine(cfg, store, quiet())
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e.SetClock(func() time.Time { return t0 })
	return e
}

func TestModesAndGates(t *testing.T) {
	multi := []model.DetectionEvent{mkEvent("THROW_001", "p", 0.99, true), mkEvent("MOV_001", "p", 0.97, false)}
	single := []model.DetectionEvent{mkEvent("THROW_001", "p", 0.9, false)}
	cases := []struct {
		name   string
		mode   Mode
		score  model.SuspicionScore
		events []model.DetectionEvent
		want   string // "" = nil
	}{
		{"shadow never acts", ModeShadow, score(100, 5), multi, ""},
		{"flag below suspicious", ModeFlag, score(39.9, 1), multi, ""},
		{"flag at suspicious", ModeFlag, score(40, 1), multi, model.ActionFlag},
		{"review below high_risk", ModeReview, score(59.9, 1), multi, ""},
		{"review at high_risk", ModeReview, score(60, 1), multi, model.ActionReviewQueue},
		{"enforce review", ModeEnforce, score(60, 1), single, model.ActionReviewQueue},
		{"enforce kick needs 2 categories", ModeEnforce, score(85, 1), single, model.ActionReviewQueue},
		{"enforce kick", ModeEnforce, score(80, 1), multi, model.ActionKick},
		{"enforce ban needs 2 matches", ModeEnforce, score(96, 1), multi, model.ActionKick},
		{"enforce ban", ModeEnforce, score(96, 2), multi, model.ActionTempBan},
		{"enforce ban needs hard evidence", ModeEnforce, score(96, 2),
			[]model.DetectionEvent{mkEvent("THROW_001", "p", 0.99, false), mkEvent("MOV_001", "p", 0.97, false)}, model.ActionKick},
		{"no events no action", ModeEnforce, score(100, 5), nil, ""},
		{"shadow events ignored", ModeEnforce, score(100, 5),
			[]model.DetectionEvent{{DetectorID: "THROW_001", PlayerID: "p", IsShadow: true, Confidence: 1, AutoEnforce: true}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &memStore{}
			e := newEngine(tc.mode, st)
			got := e.Evaluate(context.Background(), "p", "m1", tc.score, tc.events)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("expected no action, got %s", got.ActionType)
				}
				if len(st.actions) != 0 {
					t.Fatal("nothing should be stored")
				}
				return
			}
			if got == nil || got.ActionType != tc.want {
				t.Fatalf("expected %s, got %+v", tc.want, got)
			}
			if len(st.actions) != 1 || st.actions[0].ActionID != got.ActionID {
				t.Fatalf("action not stored: %+v", st.actions)
			}
			if got.ActionID == "" || got.IssuedAt.IsZero() || got.IssuedBy != "auto" || got.ScoreAtTime != tc.score.TotalScore {
				t.Errorf("action metadata incomplete: %+v", got)
			}
			if len(got.EvidenceIDs) == 0 {
				t.Errorf("evidence ids missing")
			}
			if len(got.MatchIDs) != 2 || got.MatchIDs[0] != "m0" || got.MatchIDs[1] != "m1" {
				t.Errorf("match ids wrong: %v", got.MatchIDs)
			}
			if tc.want == model.ActionTempBan && got.Duration != 7*24*time.Hour {
				t.Errorf("temp ban duration missing: %v", got.Duration)
			}
			if tc.want != model.ActionTempBan && got.Duration != 0 {
				t.Errorf("non-ban action should have no duration: %v", got.Duration)
			}
		})
	}
}

func TestCooldownAndPrune(t *testing.T) {
	st := &memStore{}
	e := newEngine(ModeReview, st)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := t0
	e.SetClock(func() time.Time { return now })
	events := []model.DetectionEvent{mkEvent("THROW_001", "p", 0.9, false)}
	if e.Evaluate(context.Background(), "p", "m1", score(70, 1), events) == nil {
		t.Fatal("first evaluation should act")
	}
	now = t0.Add(5 * time.Minute)
	if e.Evaluate(context.Background(), "p", "m1", score(70, 1), events) != nil {
		t.Fatal("cooldown should suppress")
	}
	now = t0.Add(11 * time.Minute)
	events[0].EventID += "-new-independent-evidence"
	if e.Evaluate(context.Background(), "p", "m1", score(70, 1), events) == nil {
		t.Fatal("after cooldown should act again")
	}
	// Expired entries are pruned.
	now = t0.Add(60 * time.Minute)
	e.Evaluate(context.Background(), "other", "m1", score(10, 1), events)
	e.mu.Lock()
	_, stale := e.enforcementCooldowns["p"]
	e.mu.Unlock()
	if stale {
		t.Fatal("expired cooldown entry not pruned")
	}
}

func TestNilStoreAndStoreError(t *testing.T) {
	e := newEngine(ModeReview, nil)
	got := e.Evaluate(context.Background(), "p", "m1", score(70, 1), []model.DetectionEvent{mkEvent("THROW_001", "p", 0.9, false)})
	if got == nil {
		t.Fatal("nil store must not prevent a decision")
	}
	// Typed nil pointer must also be tolerated.
	var typedNil *memStore
	e2 := newEngine(ModeReview, typedNil)
	if e2.store != nil {
		t.Fatal("typed nil store should be normalised to nil")
	}
	e3 := newEngine(ModeReview, &memStore{err: errors.New("disk full")})
	if e3.Evaluate(context.Background(), "p", "m1", score(70, 1), []model.DetectionEvent{mkEvent("THROW_001", "p", 0.9, false)}) != nil {
		t.Fatal("store error must withhold recommendation delivery")
	}
}

func TestCallbacksRunOutsideLock(t *testing.T) {
	e := newEngine(ModeEnforce, &memStore{})
	events := []model.DetectionEvent{mkEvent("THROW_001", "p", 0.99, true), mkEvent("MOV_001", "p", 0.97, false)}
	reentered := make(chan *model.EnforcementAction, 1)
	e.OnKick = func(playerID, matchID, reason string) {
		// Re-entering the engine from a callback must not deadlock.
		reentered <- e.Evaluate(context.Background(), "q", matchID, score(60, 1), []model.DetectionEvent{mkEvent("THROW_001", "q", 0.9, false)})
	}
	done := make(chan struct{})
	go func() {
		e.Evaluate(context.Background(), "p", "m1", score(85, 1), events)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: callback could not re-enter engine")
	}
	if a := <-reentered; a == nil || a.ActionType != model.ActionReviewQueue {
		t.Fatalf("re-entrant evaluation failed: %+v", a)
	}
}

func TestCustomLevels(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.Mode = ModeReview
	cfg.Levels = model.DefaultLevelTable().WithReviewThreshold(15)
	e := NewEngine(cfg, nil, quiet())
	if e.Evaluate(context.Background(), "p", "m1", score(15, 1), []model.DetectionEvent{mkEvent("THROW_001", "p", 0.9, false)}) == nil {
		t.Fatal("engine should gate on the configured level table")
	}
}

func TestPolicy(t *testing.T) {
	p := NewPolicy("", quiet())
	if p.mode != string(ModeShadow) {
		t.Fatal("default mode should be shadow")
	}
	if p.Evaluate("p", score(100, 5), nil) != nil {
		t.Fatal("shadow policy must return nil")
	}
	hard := []model.DetectionEvent{mkEvent("THROW_001", "p", 0.99, true)}
	soft := []model.DetectionEvent{mkEvent("THROW_001", "p", 0.9, false)}
	enf := NewPolicy("enforce", quiet())
	cases := []struct {
		sc     model.SuspicionScore
		events []model.DetectionEvent
		want   string
	}{
		{score(59.9, 1), soft, ""},
		{score(60, 1), soft, model.ActionReviewQueue},
		{score(80, 1), soft, model.ActionRestrict},
		{score(95, 1), hard, model.ActionRestrict},
		{score(95, 2), hard, model.ActionTempBan},
		{score(95, 2), soft, model.ActionRestrict},
	}
	for i, tc := range cases {
		got := enf.Evaluate("p", tc.sc, tc.events)
		if tc.want == "" {
			if got != nil {
				t.Errorf("case %d: expected nil, got %s", i, got.ActionType)
			}
			continue
		}
		if got == nil || got.ActionType != tc.want {
			t.Errorf("case %d: expected %s, got %+v", i, tc.want, got)
			continue
		}
		if got.ActionID == "" || got.IssuedAt.IsZero() {
			t.Errorf("case %d: policy action lacks ID/time: %+v", i, got)
		}
		if tc.want == model.ActionTempBan && got.Duration == 0 {
			t.Errorf("case %d: temp ban without duration", i)
		}
	}
	rev := NewPolicy("review", quiet())
	if a := rev.Evaluate("p", score(60, 1), soft); a == nil || a.ActionType != model.ActionReviewQueue {
		t.Fatalf("review policy: %+v", a)
	}
	if rev.Evaluate("p", score(59, 1), soft) != nil {
		t.Fatal("review policy below threshold should be nil")
	}
	if !p.ShouldCreateCase(score(40, 1)) || p.ShouldCreateCase(score(39, 1)) {
		t.Fatal("ShouldCreateCase should gate at suspicious")
	}
	if p.RecommendedAction(score(60, 1)) != "review_only" || p.RecommendedAction(score(10, 1)) != "none" {
		t.Fatal("RecommendedAction mapping wrong")
	}
	lowered := NewPolicy("review", quiet()).WithLevels(model.DefaultLevelTable().WithReviewThreshold(15))
	if lowered.Evaluate("p", score(15, 1), soft) == nil {
		t.Fatal("policy should honour configured level table")
	}
}
