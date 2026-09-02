package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// sessionRoster is the bridge's view of who is a player and who is not in a
// /session response. Echo VR's teams array carries a third "SPECTATORS" team
// (which also holds moderators); those entries are never telemetry subjects.
type sessionRoster struct {
	// Teams maps player_id -> "blue" | "orange" for every real player.
	Teams map[string]string
	// Spectators lists the player_ids of everyone on a non-blue/orange team.
	Spectators []string
	// PlayerCount is the number of blue/orange players (frames expected).
	PlayerCount int
}

// bridgePlayerID mirrors the adapter's identity rule ("echovr:<userid>",
// falling back to "name:<name>") so the bridge can join /session entries to
// mapped frames without exporting the adapter's helper.
func bridgePlayerID(p adapter.EchoVRPlayer) string {
	if p.UserID != 0 {
		return fmt.Sprintf("echovr:%d", p.UserID)
	}
	return fmt.Sprintf("name:%s", p.Name)
}

// teamSideOf classifies a /session team by name (case-insensitive BLUE /
// ORANGE) and, only when the name is absent, by index (0=blue, 1=orange).
// Anything else — SPECTATORS, moderators, unknown third teams — returns "".
func teamSideOf(index int, name string) string {
	upper := strings.ToUpper(strings.TrimSpace(name))
	switch {
	case strings.Contains(upper, "BLUE"):
		return "blue"
	case strings.Contains(upper, "ORANGE"):
		return "orange"
	case upper == "" && index == 0:
		return "blue"
	case upper == "" && index == 1:
		return "orange"
	}
	return ""
}

// buildRoster classifies every entry of the /session teams array.
func buildRoster(s *adapter.EchoVRSessionResponse) sessionRoster {
	r := sessionRoster{Teams: make(map[string]string)}
	if s == nil {
		return r
	}
	for idx, team := range s.Teams {
		side := teamSideOf(idx, team.TeamName)
		for _, p := range team.Players {
			pid := bridgePlayerID(p)
			if side == "" {
				r.Spectators = append(r.Spectators, pid)
				continue
			}
			r.Teams[pid] = side
			r.PlayerCount++
		}
	}
	sort.Strings(r.Spectators)
	return r
}

// filterFrames drops frames that belong to spectators (or to anyone not on a
// blue/orange team) and fills in Team where the mapper left it empty. It
// returns the kept frames and the number dropped. The adapter is expected to
// drop spectators itself; this is the bridge's own guarantee at the trust
// boundary so a mapper regression can never turn a moderator into a suspect.
func filterFrames(frames []model.PlayerTelemetryFrame, roster sessionRoster) ([]model.PlayerTelemetryFrame, int) {
	kept := frames[:0]
	dropped := 0
	for _, f := range frames {
		side, ok := roster.Teams[f.PlayerID]
		if !ok {
			dropped++
			continue
		}
		if f.Team == "" {
			f.Team = side
		}
		kept = append(kept, f)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].PlayerID < kept[j].PlayerID })
	return kept, dropped
}

// teamsCopy returns a copy of the roster's team map for a control message.
func (r sessionRoster) teamsCopy() map[string]string {
	if len(r.Teams) == 0 {
		return nil
	}
	out := make(map[string]string, len(r.Teams))
	for k, v := range r.Teams {
		out[k] = v
	}
	return out
}
