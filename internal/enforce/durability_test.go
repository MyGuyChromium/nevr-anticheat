package enforce

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestRecommendationRequiresDurabilityAndRetryDoesNotDuplicate(t *testing.T) {
	store := &memStore{err: errors.New("synthetic disk failure")}
	e := newEngine(ModeFlag, store)
	events := []model.DetectionEvent{mkEvent("MOV_001", "p", .9, false)}
	callbacks := 0
	e.OnFlag = func(string, string, float64, []model.DetectionEvent) { callbacks++ }
	if e.Evaluate(context.Background(), "p", "m1", score(50, 1), events) != nil || callbacks != 0 {
		t.Fatal("failed durable write delivered a recommendation")
	}
	store.err = nil
	if e.Evaluate(context.Background(), "p", "m1", score(50, 1), events) == nil || callbacks != 1 {
		t.Fatal("failed write consumed cooldown or prevented a safe retry")
	}
	restarted := newEngine(ModeFlag, store)
	restarted.OnFlag = e.OnFlag
	if restarted.Evaluate(context.Background(), "p", "m1", score(50, 1), events) != nil || callbacks != 1 || len(store.actions) != 1 {
		t.Fatal("engine restart duplicated the same evidence recommendation")
	}
	pure := newEngine(ModeFlag, nil)
	pure.OnFlag = e.OnFlag
	if pure.Evaluate(context.Background(), "p", "m1", score(50, 1), events) == nil || callbacks != 1 {
		t.Fatal("nil store must permit only a pure recommendation, never a callback")
	}
}

func TestConcurrentEnginesClaimRecommendationOnce(t *testing.T) {
	store := &memStore{}
	var callbacks atomic.Int64
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			e := newEngine(ModeFlag, store)
			e.OnFlag = func(string, string, float64, []model.DetectionEvent) { callbacks.Add(1) }
			e.Evaluate(context.Background(), "p", "m1", score(50, 1), []model.DetectionEvent{mkEvent("MOV_001", "p", .9, false)})
		}()
	}
	workers.Wait()
	if callbacks.Load() != 1 || len(store.actions) != 1 {
		t.Fatalf("callback/action retries inflated: %d/%d", callbacks.Load(), len(store.actions))
	}
}
