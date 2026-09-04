package detect_test

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/pattern"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// ownedIDs are the detectors registered by the packages under test here.
var ownedIDs = []string{
	"BIO_001", "BIO_002", "BIO_003", "BIO_004",
	"MOV_001", "MOV_002", "MOV_003", "MOV_004", "MOV_005", "MOV_006",
	"STATE_001", "STATE_002", "STATE_003", "STATE_004", "STATE_005", "STATE_006", "STATE_007",
	"PAT_001", "PAT_002", "PAT_003", "PAT_004", "PAT_005",
}

func TestCatalogListsRegisteredDetectorsOnce(t *testing.T) {
	cat := detect.Catalog()
	seen := map[string]int{}
	for _, d := range cat {
		seen[d.ID]++
		if d.New == nil || d.Name == "" || d.Category == "" || d.Version == "" {
			t.Errorf("descriptor %s incomplete: %+v", d.ID, d)
		}
	}
	for _, id := range ownedIDs {
		if seen[id] != 1 {
			t.Errorf("catalog has %d entries for %s, want 1", seen[id], id)
		}
	}
	for i := 1; i < len(cat); i++ {
		if cat[i-1].ID >= cat[i].ID {
			t.Fatalf("catalog not sorted: %s before %s", cat[i-1].ID, cat[i].ID)
		}
	}
	// Descriptor identity must match what the constructed detector reports.
	desc, ok := detect.Lookup("MOV_002")
	if !ok {
		t.Fatal("MOV_002 missing from catalog")
	}
	d := movement.NewMov002(nil)
	if desc.Weight != d.DefaultEnforcementWeight() || desc.Category != d.Category() || desc.Version != d.Version() {
		t.Errorf("descriptor drift for MOV_002: %+v vs %s/%s/%.2f", desc, d.Category(), d.Version(), d.DefaultEnforcementWeight())
	}
}

func TestRegisterConstructorRejectsDuplicates(t *testing.T) {
	err := detect.RegisterConstructor(func(p map[string]any) detect.Detector { return bio.NewBio001(p) })
	if err == nil {
		t.Fatal("expected duplicate registration of BIO_001 to be rejected")
	}
	if err := detect.RegisterConstructor(nil); err == nil {
		t.Fatal("expected nil constructor to be rejected")
	}
}

func TestRegistryRejectsDuplicateIDs(t *testing.T) {
	r := detect.NewRegistry()
	if err := r.Register(state.NewState001(nil)); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register(state.NewState001(nil)); err == nil {
		t.Fatal("expected second STATE_001 registration to fail")
	}
	if r.Count() != 1 {
		t.Fatalf("count = %d, want 1", r.Count())
	}
}

func TestBuildAllAppliesConfiguredWeightAndAutoEnforce(t *testing.T) {
	dets := detect.BuildAll(func(id string) detect.BuildOptions {
		switch id {
		case "STATE_001":
			return detect.BuildOptions{Enabled: true, Weight: 0.0, HasWeight: true, AutoEnforce: false, HasAuto: true}
		case "MOV_002":
			return detect.BuildOptions{Enabled: true, Weight: 0.8, HasWeight: true, AutoEnforce: false, HasAuto: true,
				Params: map[string]any{"min_incidents": 1}}
		default:
			return detect.BuildOptions{Enabled: false}
		}
	})
	if len(dets) != 2 {
		t.Fatalf("built %d detectors, want 2", len(dets))
	}
	byID := map[string]detect.Detector{}
	for _, d := range dets {
		byID[d.ID()] = d
	}
	s1 := byID["STATE_001"]
	if s1.DefaultEnforcementWeight() != 0 {
		t.Errorf("STATE_001 weight = %.2f, want 0 (observation-only tier)", s1.DefaultEnforcementWeight())
	}
	m2 := byID["MOV_002"]
	if m2.DefaultEnforcementWeight() != 0.8 || m2.AutoEnforce() {
		t.Errorf("MOV_002 weight/auto = %.2f/%v, want 0.8/false", m2.DefaultEnforcementWeight(), m2.AutoEnforce())
	}

	// The weight must reach the emitted event.
	mc := &model.MatchContext{MatchID: "m", Physics: model.DefaultPhysics()}
	ps := &model.PlayerState{PlayerID: "p1", Position: model.Vec3{1, 1.6, 1}, FrameDt: 0.067, LastTimestamp: 10 * 0.067}
	m2.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 10)
	ps.Position = model.Vec3{1, 1.6, 11} // 10 m jump, no expected motion
	ps.LastTimestamp = 11 * 0.067        // MOV_002 compares on elapsed telemetry time
	events := m2.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 11)
	if len(events) != 1 {
		t.Fatalf("expected one MOV_002 event, got %d", len(events))
	}
	if events[0].EnforcementWeight != 0.8 {
		t.Errorf("event weight = %.2f, want configured 0.8", events[0].EnforcementWeight)
	}
}

func TestSetWeightClampsAndSetAutoEnforce(t *testing.T) {
	d := pattern.NewPat005(nil)
	d.SetWeight(1.7)
	if d.DefaultEnforcementWeight() != 1 {
		t.Errorf("weight not clamped: %.2f", d.DefaultEnforcementWeight())
	}
	d.SetWeight(-1)
	if d.DefaultEnforcementWeight() != 0 {
		t.Errorf("weight not clamped to 0: %.2f", d.DefaultEnforcementWeight())
	}
	d.SetAutoEnforce(true)
	if !d.AutoEnforce() {
		t.Error("SetAutoEnforce(true) not applied")
	}
}

func TestSortedAndActivePlayers(t *testing.T) {
	players := map[string]*model.PlayerState{
		"zed":   {PlayerID: "zed", FrameCount: 3, LastFrameIdx: 10},
		"amy":   {PlayerID: "amy", FrameCount: 3, LastFrameIdx: 7}, // stale
		"bob":   {PlayerID: "bob"},                                 // never updated
		"carol": {PlayerID: "carol", FrameCount: 1, LastFrameIdx: 10},
	}
	sorted := detect.SortedPlayers(players)
	if len(sorted) != 4 || sorted[0].PlayerID != "amy" || sorted[3].PlayerID != "zed" {
		t.Fatalf("SortedPlayers order wrong: %v", ids(sorted))
	}
	active := detect.ActivePlayers(players, 10)
	got := ids(active)
	want := []string{"bob", "carol", "zed"}
	if len(got) != len(want) {
		t.Fatalf("ActivePlayers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ActivePlayers = %v, want %v", got, want)
		}
	}
	if !detect.IsStale(players["amy"], 10) || detect.IsStale(players["bob"], 10) {
		t.Error("IsStale wrong for amy/bob")
	}
}

func ids(ps []*model.PlayerState) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.PlayerID
	}
	return out
}

func TestMakeEventNormalisesRangeAndClamps(t *testing.T) {
	b := &detect.BaseDetector{DetectorID: "X_001", DetectorVersion: "1", Weight: 0.5}
	mc := &model.MatchContext{MatchID: "m1", Physics: model.DefaultPhysics()}
	ev := b.MakeEvent(mc, "p1", 5, 0.3, 1.7, -0.2, model.StateEvidence{}, "o", "e",
		model.CausalKey{FrameStart: -4, FrameEnd: -9, AnomalyType: "a"})
	if ev.Severity != 1 || ev.Confidence != 0 {
		t.Errorf("severity/confidence not clamped: %.2f/%.2f", ev.Severity, ev.Confidence)
	}
	if ev.FrameRangeStart != 0 || ev.FrameRangeEnd != 0 || ev.CausalKey.PlayerID != "p1" {
		t.Errorf("causal key not normalised: %+v", ev.CausalKey)
	}
	if ev.MatchID != "m1" || ev.EnforcementWeight != 0.5 || ev.AutoEnforce {
		t.Errorf("identity fields wrong: %+v", ev)
	}
	if err := ev.Validate(); err != nil {
		t.Errorf("event should validate: %v", err)
	}

	// Contract: an event without a match is not a valid production event.
	// MakeEvent tolerates a nil context (unit tests) but the pipeline's
	// Validate gate rejects the result, so it can never be scored or stored.
	orphan := b.MakeEvent(nil, "p1", 5, 0.3, 0.5, 0.5, model.StateEvidence{}, "o", "e",
		model.CausalKey{AnomalyType: "a"})
	if orphan.MatchID != "" {
		t.Errorf("nil context must not invent a match id: %q", orphan.MatchID)
	}
	if err := orphan.Validate(); err == nil {
		t.Error("event built without a match context must fail validation")
	}

	// AutoEnforce follows the detector flag (config-settable), not a literal.
	b.SetAutoEnforce(true)
	if ev := b.MakeEvent(mc, "p1", 5, 0.3, 0.5, 0.5, model.StateEvidence{}, "o", "e",
		model.CausalKey{AnomalyType: "a"}); !ev.AutoEnforce {
		t.Error("MakeEvent must stamp AutoEnforce from the detector flag")
	}
}

func TestParamAliasHelpers(t *testing.T) {
	params := map[string]any{"legacy_key": 7, "list": []any{"A", "B", 3}}
	if v := detect.GetFloatAlias(params, 1.0, "canonical", "legacy_key"); v != 7 {
		t.Errorf("GetFloatAlias = %v, want 7", v)
	}
	if v := detect.GetIntAlias(params, 1, "canonical", "other"); v != 1 {
		t.Errorf("GetIntAlias default = %v, want 1", v)
	}
	list := detect.GetStringList(params, "list")
	if len(list) != 2 || list[0] != "A" || list[1] != "B" {
		t.Errorf("GetStringList = %v", list)
	}
	if detect.GetStringList(params, "missing") != nil {
		t.Error("missing list should be nil")
	}
}
