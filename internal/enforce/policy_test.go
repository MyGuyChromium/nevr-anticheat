package enforce

import (
	"context"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestPolicyModeValidation(t *testing.T) {
	soft := []model.DetectionEvent{mkEvent("THROW_001", "p", 0.9, false)}

	// Unknown and mistyped modes fall back to shadow instead of enforcing.
	for _, raw := range []string{"bogus", "Shadow ", "SHADOW", "enforce-now", "  "} {
		p := NewPolicy(raw, quiet())
		if p.Mode() != ModeShadow {
			t.Errorf("mode %q should validate to shadow, got %q", raw, p.Mode())
		}
		if a := p.Evaluate("p", score(100, 5), soft); a != nil {
			t.Errorf("mode %q produced %s", raw, a.ActionType)
		}
	}
	// Known modes are matched case-insensitively after trimming.
	if p := NewPolicy(" ENFORCE ", quiet()); p.Mode() != ModeEnforce {
		t.Fatalf("expected enforce, got %q", p.Mode())
	}
	if p := NewPolicy("Review", quiet()); p.Mode() != ModeReview {
		t.Fatalf("expected review, got %q", p.Mode())
	}

	// Flag mode mirrors the engine: a flag from suspicious, never restrict.
	flag := NewPolicy("flag", quiet())
	if flag.Mode() != ModeFlag {
		t.Fatalf("expected flag, got %q", flag.Mode())
	}
	if a := flag.Evaluate("p", score(39.9, 1), soft); a != nil {
		t.Fatalf("flag below suspicious should be nil, got %s", a.ActionType)
	}
	if a := flag.Evaluate("p", score(40, 1), soft); a == nil || a.ActionType != model.ActionFlag {
		t.Fatalf("flag at suspicious should flag, got %+v", a)
	}
	if a := flag.Evaluate("p", score(100, 5), soft); a == nil || a.ActionType != model.ActionFlag || a.Duration != 0 {
		t.Fatalf("flag mode must never escalate beyond a flag, got %+v", a)
	}
}

func TestEngineMetaDetectorsDoNotSatisfyCategoryGates(t *testing.T) {
	// THROW_001 plus a PAT_003 cross-match flag derived from it is ONE
	// independent category, exactly as the scorer counts it.
	derived := []model.DetectionEvent{mkEvent("THROW_001", "p", 0.9, false), mkEvent("PAT_003", "p", 0.9, false)}
	e := newEngine(ModeEnforce, &memStore{})
	got := e.Evaluate(context.Background(), "p", "m1", score(85, 1), derived)
	if got == nil || got.ActionType != model.ActionReviewQueue {
		t.Fatalf("meta-detector satisfied the kick gate: %+v", got)
	}
	// PAT_004 likewise; a genuine pattern detector (PAT_001) does count.
	derived[1] = mkEvent("PAT_004", "p", 0.9, false)
	e = newEngine(ModeEnforce, &memStore{})
	if got := e.Evaluate(context.Background(), "p", "m1", score(85, 1), derived); got == nil || got.ActionType != model.ActionReviewQueue {
		t.Fatalf("PAT_004 satisfied the kick gate: %+v", got)
	}
	genuine := []model.DetectionEvent{mkEvent("THROW_001", "p", 0.9, false), mkEvent("PAT_001", "p", 0.9, false)}
	e = newEngine(ModeEnforce, &memStore{})
	if got := e.Evaluate(context.Background(), "p", "m1", score(85, 1), genuine); got == nil || got.ActionType != model.ActionKick {
		t.Fatalf("two independent categories should recommend a kick: %+v", got)
	}
	// The ban gate is guarded the same way.
	hardDerived := []model.DetectionEvent{mkEvent("THROW_001", "p", 0.99, true), mkEvent("PAT_003", "p", 0.97, false)}
	e = newEngine(ModeEnforce, &memStore{})
	if got := e.Evaluate(context.Background(), "p", "m1", score(96, 2), hardDerived); got == nil || got.ActionType != model.ActionReviewQueue {
		t.Fatalf("meta-detector satisfied the ban gate: %+v", got)
	}
}
