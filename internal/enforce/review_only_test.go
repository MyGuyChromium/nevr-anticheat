package enforce

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func attachPunitiveProbes(e *Engine, calls *atomic.Int64) {
	e.OnKick = func(string, string, string) { calls.Add(1) }
	e.OnBan = func(string, time.Duration, string) { calls.Add(1) }
}

func TestReviewOnlyPreservesRecommendationsWithoutPunitiveDispatch(t *testing.T) {
	if !ReviewOnly || PolicyVersion != "review-only-v1" {
		t.Fatal("test deployment safety policy must not be runtime configurable")
	}
	for _, hard := range []bool{false, true} {
		name, want := "kick_without_auto_enforce", model.ActionKick
		if hard {
			name, want = "ban_with_all_evidence_gates", model.ActionTempBan
		}
		t.Run(name, func(t *testing.T) {
			// Deliberately satisfy the strongest recommendation gates. Turning
			// AutoEnforce off is not the dispatch barrier: the kick gate never
			// required it, and caller-supplied evidence may set it to true.
			events := []model.DetectionEvent{mkEvent("THROW_001", "p", .99, hard), mkEvent("MOV_001", "p", .99, false)}
			var calls atomic.Int64
			store := &memStore{}
			e := newEngine(ModeEnforce, store)
			attachPunitiveProbes(e, &calls)
			action := e.Evaluate(context.Background(), "p", "m1", score(100, 5), events)
			if action == nil || action.ActionType != want || len(store.actions) != 1 {
				t.Fatalf("review recommendation was lost or changed: action=%+v stored=%+v", action, store.actions)
			}
			if want == model.ActionTempBan && action.Duration != 7*24*time.Hour {
				t.Fatal("recorded recommendation duration changed")
			}
			if calls.Load() != 0 {
				t.Fatal("a durable recommendation dispatched a punitive callback")
			}
			// Nil storage remains a pure recommendation, never a bypass.
			pure := newEngine(ModeEnforce, nil)
			attachPunitiveProbes(pure, &calls)
			if a := pure.Evaluate(context.Background(), "p", "m1", score(100, 5), events); a == nil || a.ActionType != want || calls.Load() != 0 {
				t.Fatalf("pure recommendation bypassed review-only boundary: %+v calls=%d", a, calls.Load())
			}
		})
	}
}

func TestReviewOnlyRetryRestartAndConcurrentClaimsCannotDispatch(t *testing.T) {
	ctx := context.Background()
	events := []model.DetectionEvent{mkEvent("THROW_001", "p", .99, true), mkEvent("MOV_001", "p", .99, false)}
	store := &memStore{err: errors.New("synthetic persistence failure")}
	var calls atomic.Int64
	e := newEngine(ModeEnforce, store)
	attachPunitiveProbes(e, &calls)
	if e.Evaluate(ctx, "p", "m1", score(100, 5), events) != nil {
		t.Fatal("failed claim returned a durable recommendation")
	}
	store.err = nil
	if e.Evaluate(ctx, "p", "m1", score(100, 5), events) == nil {
		t.Fatal("retry failed to preserve the moderator recommendation")
	}
	if e.Evaluate(ctx, "p", "m1", score(100, 5), events) != nil {
		t.Fatal("same-process retry duplicated the recommendation")
	}
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			// Reconstructed engines model independent workers/restart. They
			// share a durable store, not an in-memory cooldown reservation.
			restarted := newEngine(ModeEnforce, store)
			attachPunitiveProbes(restarted, &calls)
			if restarted.Evaluate(ctx, "p", "m1", score(100, 5), events) != nil {
				t.Error("restart duplicated a recorded recommendation")
			}
		}()
	}
	workers.Wait()
	if calls.Load() != 0 || len(store.actions) != 1 {
		t.Fatalf("retry/restart boundary: callbacks=%d recommendations=%d", calls.Load(), len(store.actions))
	}
	// New evidence after cooldown may create another review record, but
	// cannot enable punitive dispatch either.
	e.SetClock(func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) })
	events[0].EventID += "-new-evidence"
	if e.Evaluate(ctx, "p", "m1", score(100, 5), events) == nil || len(store.actions) != 2 || calls.Load() != 0 {
		t.Fatal("new evidence bypassed the immutable review-only boundary")
	}
}

func TestReviewOnlyCannotBeOverriddenByEngineConfiguration(t *testing.T) {
	for _, mode := range []Mode{ModeShadow, ModeFlag, ModeReview, ModeEnforce, "ENFORCE", "unknown", ""} {
		t.Run(string(mode), func(t *testing.T) {
			// Zero/negative thresholds intentionally remove recommendation
			// gates. No configuration value can enable a punitive callback.
			cfg := EngineConfig{Mode: mode, CooldownDuration: -1, MinCategoriesForKick: -1, MinCategoriesForBan: -1, MinMatchesForBan: -1, MinConfidenceForBan: -1}
			e := NewEngine(cfg, &memStore{}, quiet())
			var calls atomic.Int64
			attachPunitiveProbes(e, &calls)
			e.Evaluate(context.Background(), "p", "m1", score(100, 5), []model.DetectionEvent{mkEvent("THROW_001", "p", .99, true)})
			if calls.Load() != 0 {
				t.Fatal("engine configuration enabled punitive dispatch")
			}
		})
	}
}

// This architecture regression is deliberately narrower than an assurance
// about arbitrary future code: the audited backend has no punitive adapter,
// and these legacy hooks must remain unbound and unread in production. A new
// integration must be explicitly reviewed rather than silently reviving them.
func TestReviewOnlyProductionHasNoLegacyPunitiveBindings(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository boundary")
	}
	root := filepath.Join(filepath.Dir(current), "..", "..")
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				if sel, ok := node.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "OnKick", "OnBan", "OnEnforcement":
						t.Errorf("review-only production must not bind/read legacy punitive hook: %s", fset.Position(sel.Pos()))
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
