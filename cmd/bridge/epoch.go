package main

import (
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// frameEpoch is the bridge-side frame identity for one Nakama match_id.
//
// The ingest store keys telemetry on (match_id, player_id, frame_index), so
// every producer of frames for a match must emit a monotonic FrameIndex and a
// monotonic Timestamp for the whole life of that match_id — across poller
// restarts (30-failure stop + re-discovery, session mismatch, bridge-side
// hiccups) and across a rematch that reuses the lobby's match_id. Keeping the
// epoch here, keyed by match_id, means a recreated poller continues the
// sequence instead of restarting at 0 (which the store would silently ignore).
//
// Timestamp is seconds since the first sample the bridge took for the match;
// DeltaTime is the per-player spacing between consecutive samples (0 on a
// player's first frame). Both come from the real wall-clock sample time, never
// from an assumed poll cadence.
type frameEpoch struct {
	mu        sync.Mutex
	startWall time.Time
	nextIndex int
	lastTS    float64
	playerTS  map[string]float64
	sessionID string // last Echo VR sessionid observed for this match_id
	lastSeen  time.Time
}

// stamp assigns FrameIndex/Timestamp/DeltaTime to every frame of one sample
// and advances the epoch. It returns the index and timestamp used.
func (e *frameEpoch) stamp(sample time.Time, frames []model.PlayerTelemetryFrame) (int, float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.startWall.IsZero() {
		e.startWall = sample
	}
	ts := sample.Sub(e.startWall).Seconds()
	if ts < e.lastTS {
		// Wall clock stepped backwards; never let the sequence go non-monotonic.
		ts = e.lastTS
	}
	idx := e.nextIndex
	e.nextIndex++
	if e.playerTS == nil {
		e.playerTS = make(map[string]float64)
	}
	for i := range frames {
		f := &frames[i]
		f.FrameIndex = idx
		f.Timestamp = ts
		if prev, ok := e.playerTS[f.PlayerID]; ok {
			f.DeltaTime = ts - prev
		} else {
			f.DeltaTime = 0
		}
		e.playerTS[f.PlayerID] = ts
	}
	e.lastTS = ts
	e.lastSeen = sample
	return idx, ts
}

// setSession records the Echo VR sessionid currently backing the match and
// reports whether it changed from a previously recorded one.
func (e *frameEpoch) setSession(id string) (changed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	changed = e.sessionID != "" && id != "" && e.sessionID != id
	e.sessionID = id
	return changed
}

func (e *frameEpoch) session() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessionID
}

func (e *frameEpoch) snapshot() (nextIndex int, lastTS float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nextIndex, e.lastTS
}

// epochRegistry holds one frameEpoch per match_id for the bridge process.
type epochRegistry struct {
	mu     sync.Mutex
	epochs map[string]*frameEpoch
}

func newEpochRegistry() *epochRegistry {
	return &epochRegistry{epochs: make(map[string]*frameEpoch)}
}

// get returns the epoch for a match, creating it on first use.
func (r *epochRegistry) get(matchID string) *frameEpoch {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.epochs[matchID]
	if !ok {
		e = &frameEpoch{lastSeen: time.Now()}
		r.epochs[matchID] = e
	}
	return e
}

// touch marks a match as still present in Nakama so its epoch is retained.
func (r *epochRegistry) touch(matchID string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.epochs[matchID]; ok {
		e.mu.Lock()
		if now.After(e.lastSeen) {
			e.lastSeen = now
		}
		e.mu.Unlock()
	}
}

// expire drops epochs for matches not seen for longer than ttl. Returns the
// number removed. Epochs of matches that are still listed by Nakama are kept
// alive by touch(), so a match that briefly disappears from one discovery
// page keeps its sequence.
func (r *epochRegistry) expire(now time.Time, ttl time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for id, e := range r.epochs {
		e.mu.Lock()
		stale := now.Sub(e.lastSeen) > ttl
		e.mu.Unlock()
		if stale {
			delete(r.epochs, id)
			removed++
		}
	}
	return removed
}

func (r *epochRegistry) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.epochs)
}
