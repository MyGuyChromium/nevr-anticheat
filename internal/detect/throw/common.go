// Package throw implements the disc/throw detectors (THROW_001 - THROW_008).
package throw

import (
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// sortedPlayerIDs returns the player IDs in sorted order so every detector
// iterates deterministically (reproducible reprocessing, stable rate-limit
// victims).
func sortedPlayerIDs(players map[string]*model.PlayerState) []string {
	ids := make([]string, 0, len(players))
	for id := range players {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// sortedKeys returns the keys of a string-keyed map in sorted order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// currentDisc picks the frame's disc state deterministically and reports
// whether the disc is held at frameIdx. The disc is global, but every
// player's frame carries its own copy and older producers only marked
// IsHeld on the possessor's copy. Preference order:
//
//  1. the possessor's copy (player with HasDisc, or a copy marked IsHeld),
//     among players that have a frame at frameIdx;
//  2. the first player in sorted PlayerID order with a frame at frameIdx;
//  3. the same two rules over all players (for callers that do not maintain
//     LastFrameIdx, e.g. unit tests feeding hand-built states).
//
// Players whose last frame is older than frameIdx (left the match) never
// shadow a live copy.
//
// held is true when the chosen copy is marked IsHeld OR any player with a
// frame at frameIdx reports possession (PlayerState.HasDisc, from the wire
// field has_possession). The telemetry contract requires has_possession but
// only desires is_held, so a catch signalled by has_possession alone must
// end a throw track exactly like one signalled by is_held (contract E).
func currentDisc(players map[string]*model.PlayerState, frameIdx int) (disc *model.DiscState, held bool) {
	ids := sortedPlayerIDs(players)
	var firstFresh, firstAny, heldAny *model.DiscState
	freshPossession := false
	for _, id := range ids {
		ps := players[id]
		if ps == nil {
			continue
		}
		fresh := ps.LastFrameIdx == frameIdx
		if fresh && ps.HasDisc {
			freshPossession = true
		}
		if ps.CurrentDisc == nil {
			continue
		}
		dc := ps.CurrentDisc
		copyHeld := ps.HasDisc || dc.IsHeld
		if fresh {
			if copyHeld {
				return dc, true
			}
			if firstFresh == nil {
				firstFresh = dc
			}
		}
		if copyHeld && heldAny == nil {
			heldAny = dc
		}
		if firstAny == nil {
			firstAny = dc
		}
	}
	if firstFresh != nil {
		return firstFresh, firstFresh.IsHeld || freshPossession
	}
	if freshPossession {
		// A fresh player holds the disc but carries no disc copy: the disc
		// is held even though no state can be returned.
		return nil, true
	}
	if heldAny != nil {
		return heldAny, true
	}
	if firstAny != nil {
		return firstAny, firstAny.IsHeld
	}
	return nil, false
}

// throwAt returns the player's throw event if it was released at frameIdx.
func throwAt(ps *model.PlayerState, frameIdx int) *model.ThrowEvent {
	if ps == nil || ps.LastThrow == nil || ps.LastThrow.FrameIndex != frameIdx {
		return nil
	}
	return ps.LastThrow
}
