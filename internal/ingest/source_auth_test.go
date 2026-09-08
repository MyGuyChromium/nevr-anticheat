package ingest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func testSourceGrant(principal, token, source string, matches ...string) SourceGrant {
	return SourceGrant{SourceGrantConfig: config.SourceGrantConfig{Principal: principal, TokenEnv: "TOKEN_" + principal, ServerIDs: []string{source}, MatchIDs: matches, PlayerIDs: []string{"P1"}}, Token: token}
}

func TestSourceAuthRequiresGrantsAwayFromLiteralLoopback(t *testing.T) {
	for _, addr := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "192.0.2.1:8080", "localhost:8080"} {
		cfg := testServerConfig()
		cfg.ListenAddr = addr
		for _, unauth := range []bool{false, true} {
			cfg.AllowUnauthenticated = unauth
			if err := ValidateServerAuth(cfg); !errors.Is(err, ErrSourceGrantsRequired) {
				t.Fatalf("%s unauth=%v: %v", addr, unauth, err)
			}
		}
	}
	for _, addr := range []string{"127.0.0.1:8080", "[::1]:8080"} {
		cfg := testServerConfig()
		cfg.ListenAddr = addr
		if err := ValidateServerAuth(cfg); err != nil {
			t.Fatalf("explicit loopback development: %v", err)
		}
	}
	cfg := testServerConfig()
	cfg.ListenAddr = ":8080"
	cfg.SourceGrants = []SourceGrant{testSourceGrant("A", "a-secret", "source-a", "M1")}
	if err := ValidateServerAuth(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.SourceGrants[0].Token = ""
	if err := ValidateServerAuth(cfg); err == nil {
		t.Fatal("empty principal credential accepted")
	}
}

func TestSourceAuthScopesFramesControlsAndCredentials(t *testing.T) {
	h := &fakeHandler{}
	cfg := testServerConfig()
	cfg.SourceGrants = []SourceGrant{testSourceGrant("A", "a-secret", "source-a", "M1")}
	s, url, _ := startServer(t, cfg, h)
	// Changing the caller's grant slices cannot widen a running server.
	cfg.SourceGrants[0].MatchIDs[0] = "M2"
	if _, ok := s.authenticate("Bearer " + cfg.AuthToken); ok {
		t.Fatal("legacy global credential bypassed configured grants")
	}
	conn := dial(t, url, "a-secret")
	if msg, err := recvControl(t, conn, 2*time.Second); err != nil || msg.Type != model.ControlHello {
		t.Fatalf("hello: %+v %v", msg, err)
	}
	for _, batch := range []FrameBatch{
		{MatchID: "M2", ServerID: "source-a", Frames: []model.PlayerTelemetryFrame{goodFrame("P1", 0)}},
		{MatchID: "M1", ServerID: "source-b", Frames: []model.PlayerTelemetryFrame{goodFrame("P1", 0)}},
		{MatchID: "M1", ServerID: "source-a", Frames: []model.PlayerTelemetryFrame{goodFrame("P2", 0)}},
	} {
		sendJSON(t, conn, batch)
		if ack := waitAck(t, conn); ack.Accepted != 0 || ack.Rejected != 1 {
			t.Fatalf("unauthorized batch accepted: %+v", ack)
		}
	}
	if len(s.playerRates) != 0 || h.frameCount() != 0 {
		t.Fatal("unauthorized data allocated per-player state or reached handler")
	}
	for _, msg := range []model.ControlMessage{
		{Type: model.ControlMatchStart, MatchID: "M2", ServerID: "source-a"},
		{Type: model.ControlMatchEnd, MatchID: "M1", ServerID: "source-b"},
		{Type: model.ControlMatchStart, MatchID: "M1", ServerID: "source-a", Teams: map[string]string{"P2": "blue"}},
	} {
		sendJSON(t, conn, msg)
	}
	sendJSON(t, conn, FrameBatch{MatchID: "M1", ServerID: "source-a", Frames: []model.PlayerTelemetryFrame{goodFrame("P1", 0)}})
	if ack := waitAck(t, conn); ack.Accepted != 1 {
		t.Fatalf("authorized frame failed: %+v", ack)
	}
	if got := h.controlTypes(); len(got) != 0 {
		t.Fatalf("unauthorized controls reached handler: %v", got)
	}
}

func TestMatchSourceBindingSurvivesEndAndManagerRestart(t *testing.T) {
	mm, store, _ := newTestManager(t)
	mm.SetLimits(1, 1)
	if got := mm.HandleFrames("M1", "source-a", []model.PlayerTelemetryFrame{goodFrame("P1", 0)}); got.Accepted != 1 {
		t.Fatal(got)
	}
	for i := 0; i < 20; i++ {
		mm.HandleControl(model.ControlMessage{Type: model.ControlMatchStart, MatchID: "M1", ServerID: "source-b", Map: "forged"})
		mm.HandleControl(model.ControlMessage{Type: model.ControlMatchEnd, MatchID: "M1", ServerID: "source-b"})
		if got := mm.HandleFrames("M1", "source-b", []model.PlayerTelemetryFrame{goodFrame("P1", i+1)}); got.Rejected != 1 {
			t.Fatal("cross-source data accepted", got)
		}
		if got := mm.HandleFrames(fmt.Sprintf("new-%d", i), "source-b", []model.PlayerTelemetryFrame{goodFrame("P1", 1)}); got.Rejected != 1 {
			t.Fatal("match association cap bypassed", got)
		}
	}
	if mc := mm.GetMatchContext("M1"); mc == nil || mc.ServerID != "source-a" || mc.Map == "forged" || mm.ActiveMatchCount() != 1 {
		t.Fatalf("cross-source control changed match: %+v", mc)
	}
	mm.HandleControl(model.ControlMessage{Type: model.ControlMatchEnd, MatchID: "M1", ServerID: "source-a"})
	if mm.ActiveMatchCount() != 0 {
		t.Fatal("owner could not end match")
	}
	// A new manager has no in-memory auth association. Durable match context
	// still prevents a different source (or missing ID) from taking it over.
	restarted := NewMatchManager(mm.cfg, store, mm.detectorFn, quietLogger())
	for _, source := range []string{"source-b", ""} {
		if got := restarted.HandleFrames("M1", source, []model.PlayerTelemetryFrame{goodFrame("P1", 1)}); got.Rejected != 1 {
			t.Fatal("durable source binding bypassed", got)
		}
	}
	if restarted.ActiveMatchCount() != 0 {
		t.Fatal("rejected source retained an association slot")
	}
	if got := restarted.HandleFrames("M1", "source-a", []model.PlayerTelemetryFrame{goodFrame("P1", 1)}); got.Accepted != 1 {
		t.Fatal("owner continuation rejected", got)
	}
	if mc, err := store.GetMatchContext(context.Background(), "M1"); err != nil || mc.ServerID != "source-a" {
		t.Fatalf("durable provenance overwritten: %+v %v", mc, err)
	}
	restarted.Close()
}
