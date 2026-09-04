package catalog

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// ExpectedDetectorCount is the documented detector count (README: 30).
const ExpectedDetectorCount = 30

func allEnabled() *config.Config {
	cfg := config.DefaultConfig()
	for id, dc := range cfg.Detectors {
		dc.Enabled = true
		cfg.Detectors[id] = dc
	}
	return cfg
}

func TestCatalogHas30UniqueDetectors(t *testing.T) {
	dets := detect.BuildAll(nil)
	if len(dets) != ExpectedDetectorCount {
		t.Fatalf("BuildAll built %d detectors, want %d", len(dets), ExpectedDetectorCount)
	}
	seen := map[string]bool{}
	for i, d := range dets {
		if seen[d.ID()] {
			t.Errorf("duplicate detector id %s", d.ID())
		}
		seen[d.ID()] = true
		if i > 0 && dets[i-1].ID() >= d.ID() {
			t.Errorf("detectors not in ID order: %s before %s", dets[i-1].ID(), d.ID())
		}
	}
	for _, prefix := range []struct {
		prefix string
		n      int
	}{{"THROW_", 8}, {"BIO_", 4}, {"MOV_", 6}, {"STATE_", 7}, {"PAT_", 5}} {
		got := 0
		for id := range seen {
			if len(id) > len(prefix.prefix) && id[:len(prefix.prefix)] == prefix.prefix {
				got++
			}
		}
		if got != prefix.n {
			t.Errorf("%s detectors = %d, want %d", prefix.prefix, got, prefix.n)
		}
	}
	// Every detector the default config knows about is in the catalog and
	// vice versa, so config keys cannot go stale silently.
	cfg := config.DefaultConfig()
	for id := range cfg.Detectors {
		if !seen[id] {
			t.Errorf("config detector %s missing from catalog", id)
		}
	}
	for id := range seen {
		if _, ok := cfg.Detectors[id]; !ok {
			t.Errorf("catalog detector %s has no default config block", id)
		}
	}
}

func TestBuildAppliesConfigAndStampsWeightOnEvents(t *testing.T) {
	cfg := allEnabled()
	mov2 := cfg.Detectors["MOV_002"]
	mov2.EnforcementWeight = 0.37
	mov2.Params["min_incidents"] = 1
	cfg.Detectors["MOV_002"] = mov2
	state1 := cfg.Detectors["STATE_001"]
	state1.Enabled = false
	cfg.Detectors["STATE_001"] = state1

	dets := Build(cfg, nil)
	if len(dets) != ExpectedDetectorCount-1 {
		t.Fatalf("built %d detectors with one disabled, want %d", len(dets), ExpectedDetectorCount-1)
	}
	var m2 detect.Detector
	for _, d := range dets {
		if d.ID() == "STATE_001" {
			t.Error("disabled detector was built")
		}
		if d.ID() == "MOV_002" {
			m2 = d
		}
		if d.AutoEnforce() {
			t.Errorf("%s auto-enforce on with the default config", d.ID())
		}
	}
	if m2 == nil {
		t.Fatal("MOV_002 not built")
	}
	if m2.DefaultEnforcementWeight() != 0.37 {
		t.Fatalf("configured weight not applied: %.2f", m2.DefaultEnforcementWeight())
	}
	// The configured weight must reach the emitted event.
	mc := &model.MatchContext{MatchID: "m", Physics: model.DefaultPhysics()}
	ps := &model.PlayerState{PlayerID: "p1", Position: model.Vec3{1, 1.6, 1}, FrameDt: 0.067, LastTimestamp: 10 * 0.067}
	m2.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 10)
	ps.Position = model.Vec3{1, 1.6, 11}
	ps.LastTimestamp = 11 * 0.067
	events := m2.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 11)
	if len(events) != 1 {
		t.Fatalf("expected one MOV_002 event, got %d", len(events))
	}
	if events[0].EnforcementWeight != 0.37 || events[0].AutoEnforce {
		t.Errorf("event weight/auto = %.2f/%v, want 0.37/false", events[0].EnforcementWeight, events[0].AutoEnforce)
	}

	// Params are copied: the history provider never leaks into shared config.
	opts := Options(cfg, nil)("MOV_002")
	opts.Params["injected"] = true
	if _, leaked := cfg.Detectors["MOV_002"].Params["injected"]; leaked {
		t.Error("Options mutated the shared config params")
	}
	if len(Names()) != ExpectedDetectorCount || Names()["THROW_001"] == "" {
		t.Errorf("Names() = %d entries", len(Names()))
	}
}
