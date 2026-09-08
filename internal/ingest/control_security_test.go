package ingest

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/metrics"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestControlMetricsHaveFixedCardinality(t *testing.T) {
	s := NewServer(testServerConfig(), &fakeHandler{}, quietLogger())
	m := metrics.NewMetrics()
	s.SetMetrics(m)
	for i := 0; i < 1000; i++ {
		typ := fmt.Sprintf("untrusted-type-%d", i)
		raw, _ := json.Marshal(model.ControlMessage{Type: typ})
		s.handleControlMessage(raw, typ, "test", nil)
	}
	if labels := m.ControlMessages.Snapshot(); len(labels) != 1 || labels["unknown"] != 1000 {
		t.Fatalf("wire input created metric series: %v", labels)
	}
}

func TestControlRosterIsBoundedAtWireAndManager(t *testing.T) {
	h := &fakeHandler{}
	cfg := testServerConfig()
	cfg.MaxPlayersPerMatch = 2
	s := NewServer(cfg, h, quietLogger())
	msg := model.ControlMessage{Type: model.ControlMatchStart, MatchID: "M1", Teams: map[string]string{"A": "blue", "B": "blue", "C": "orange"}}
	raw, _ := json.Marshal(msg)
	s.handleControlMessage(raw, msg.Type, "test", nil)
	if len(h.controls) != 0 {
		t.Fatal("oversized wire roster reached handler")
	}
	mm, _, _ := newTestManager(t)
	mm.SetLimits(1, 2)
	mm.HandleControl(msg)
	for i := 0; i < 100; i++ {
		mm.HandleControl(model.ControlMessage{Type: model.ControlMatchStart, MatchID: "M1", Teams: map[string]string{fmt.Sprintf("new-%d", i): "orange", "A": "orange"}})
	}
	mc := mm.GetMatchContext("M1")
	if len(mc.PlayerIDs) != 2 || len(mc.TeamAssignments) != 2 || mc.PlayerIDs[0] != "A" || mc.PlayerIDs[1] != "B" || mc.TeamAssignments["A"] != "orange" {
		t.Fatalf("roster grew or existing-team update failed: %+v", mc)
	}
}
