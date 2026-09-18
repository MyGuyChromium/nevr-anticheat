package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// freshLiveFrames never guesses that an old index is a producer restart.
// Existing identities are immutable/idempotent, while unseen late frames and
// non-monotonic times are rejected. We can sort a bounded batch before any of
// it is processed; we cannot safely reopen a tick closed by an earlier batch.
// A continuing producer must preserve its match-relative index/time across
// reconnects. Caller-reported source metadata does not authorize a new epoch.
func (mm *MatchManager) freshLiveFrames(ctx context.Context, match *LiveMatch, input []model.PlayerTelemetryFrame) ([]model.PlayerTelemetryFrame, FrameResult) {
	frames := append([]model.PlayerTelemetryFrame(nil), input...)
	sort.SliceStable(frames, func(i, j int) bool {
		if frames[i].FrameIndex != frames[j].FrameIndex {
			return frames[i].FrameIndex < frames[j].FrameIndex
		}
		return frames[i].PlayerID < frames[j].PlayerID
	})
	type identity struct {
		player string
		index  int
	}
	seen := make(map[identity]model.PlayerTelemetryFrame, len(frames))
	admittedPlayers := make(map[string]bool, len(match.MatchCtx.PlayerIDs))
	for _, id := range match.MatchCtx.PlayerIDs {
		admittedPlayers[id] = true
	}
	kept := make([]model.PlayerTelemetryFrame, 0, len(frames))
	res := FrameResult{}
	lastIndex, lastTime, haveTime := match.lastFrameIndex, match.lastTimestamp, match.haveTimestamp
	for _, f := range frames {
		key := identity{f.PlayerID, f.FrameIndex}
		if f.FrameIndex < 0 || f.Timestamp < 0 || math.IsNaN(f.Timestamp) || math.IsInf(f.Timestamp, 0) {
			res.Rejected++
			continue
		}
		// A player outside the bounded roster must not establish the tick
		// clock, close a pending tick, or suppress admitted peers. Reserve a
		// new slot only after that player's frame passes ordering below.
		if !admittedPlayers[f.PlayerID] && len(admittedPlayers) >= mm.maxPlayersPerMatch {
			res.Rejected++
			mm.warnThrottled("maxplayers:"+match.MatchCtx.MatchID, "player cap reached; frames for additional players rejected",
				"match", match.MatchCtx.MatchID, "player", f.PlayerID, "cap", mm.maxPlayersPerMatch)
			continue
		}
		if prior, ok := seen[key]; ok {
			equal, err := sameLiveFrame(prior, f)
			if err != nil {
				res.Rejected++
				continue
			}
			if !equal {
				res.Rejected++
				mm.suspendAnalysis(ctx, match, sqlite.LiveSourceIdentityConflictReason, fmt.Errorf("conflicting telemetry under an existing frame identity"))
			} else {
				res.Ignored++
			}
			continue
		}
		if f.FrameIndex <= match.lastFrameIndex {
			var priorJSON string
			err := mm.store.DB().QueryRowContext(ctx,
				`SELECT frame_json FROM telemetry_frames WHERE match_id = ? AND player_id = ? AND frame_index = ?`,
				match.MatchCtx.MatchID, f.PlayerID, f.FrameIndex).Scan(&priorJSON)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				// A failed identity lookup must not turn a retry into analysis.
				res.Rejected++
				continue
			}
			if err == nil {
				equal, compareErr := sameStoredLiveFrame(priorJSON, f)
				if compareErr != nil {
					res.Rejected++
					continue
				}
				if !equal {
					res.Rejected++
					mm.suspendAnalysis(ctx, match, sqlite.LiveSourceIdentityConflictReason, fmt.Errorf("conflicting telemetry under an existing frame identity"))
				} else {
					res.Ignored++
				}
				continue
			}
		}
		// A restart or finalized segment has no open in-memory tick to
		// augment. Identical stored retries above remain idempotent, but a
		// previously unseen peer cannot reopen its last stored tick and be
		// analyzed alone under an already-used snapshot identity.
		if f.FrameIndex == match.lastFrameIndex && len(match.pending) == 0 {
			res.Rejected++
			continue
		}
		if f.FrameIndex < lastIndex || (haveTime &&
			((f.FrameIndex == lastIndex && !sameLiveTickTime(f.Timestamp, lastTime)) ||
				(f.FrameIndex > lastIndex && f.Timestamp <= lastTime))) {
			res.Rejected++
			continue
		}
		seen[key] = f
		admittedPlayers[f.PlayerID] = true
		kept = append(kept, f)
		lastIndex, lastTime, haveTime = f.FrameIndex, f.Timestamp, true
	}
	if res.Ignored > 0 {
		if mm.metrics != nil {
			mm.metrics.FramesIgnored.Add(int64(res.Ignored))
		}
		mm.warnThrottled("retry:"+match.MatchCtx.MatchID, "duplicate frame identities ignored; no new observations or time rebasing",
			"match", match.MatchCtx.MatchID, "frames", res.Ignored)
	}
	if res.Rejected > 0 {
		mm.warnThrottled("ordering:"+match.MatchCtx.MatchID, "late or non-monotonic frame identities rejected; producer restart requires a distinct authorized stream",
			"match", match.MatchCtx.MatchID, "frames", res.Rejected)
	}
	return kept, res
}

// Separate producers of normalized per-player frames can arrive at the same
// tick timestamp via different floating-point arithmetic. Permit only a few
// ULPs, capped at a nanosecond; preserve every original value in storage. This
// tolerance never applies to chronological progress between distinct ticks.
func sameLiveTickTime(a, b float64) bool {
	ulp := math.Max(math.Nextafter(a, math.Inf(1))-a, math.Nextafter(b, math.Inf(1))-b)
	return math.Abs(a-b) <= math.Min(1e-9, 8*ulp)
}
