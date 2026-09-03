// Package antievasion implements protections against cheat developers evading
// detection.
//
// STATUS: EXPERIMENTAL. Nothing in the pipeline, ingest server or CLI uses
// this package yet; the anomaly_clusters table has no writer. The types are
// kept because their contracts are tested and intended for the phase that
// gates detector evaluation windows and delays enforcement notifications.
// Do not treat the presence of this package as evidence that the running
// system randomises evaluation or delays enforcement.
package antievasion

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// WindowRandomizer jitters detector evaluation frames so cheat developers
// cannot predict exactly when measurements are taken.
//
// Schedule: frames are partitioned into consecutive blocks of baseInterval
// frames and exactly ONE frame per block is evaluated, chosen by a 64-bit
// mixed hash of (secret, matchID, detectorID, block). Consequences:
//
//   - the average rate is 1/baseInterval, like a fixed stride;
//   - the maximum gap between evaluations is 2*baseInterval-1 frames (bounded);
//   - the schedule is reproducible for the same inputs (so offline
//     reprocessing matches live), but not derivable from the public matchID
//     when a secret is supplied.
//
// Without a secret the schedule is deterministic from public data; use
// NewWindowRandomizerWithSecret in production.
type WindowRandomizer struct {
	seed uint64
}

// NewWindowRandomizer creates a randomizer seeded from the match ID only.
// The resulting schedule is reproducible from public data; prefer
// NewWindowRandomizerWithSecret where unpredictability matters.
func NewWindowRandomizer(matchID string) *WindowRandomizer {
	return NewWindowRandomizerWithSecret(matchID, nil)
}

// NewWindowRandomizerWithSecret seeds the schedule with a server-side secret
// in addition to the match ID.
func NewWindowRandomizerWithSecret(matchID string, secret []byte) *WindowRandomizer {
	h := fnv64(secret)
	h = mix64(h ^ fnv64([]byte(matchID)))
	return &WindowRandomizer{seed: h}
}

// ShouldEvaluate returns true if a detector should evaluate at this frame.
func (wr *WindowRandomizer) ShouldEvaluate(detectorID string, frameIdx int, baseInterval int) bool {
	if baseInterval <= 1 || frameIdx < 0 {
		return true // evaluate every frame
	}
	interval := uint64(baseInterval)
	block := uint64(frameIdx) / interval
	h := mix64(wr.seed ^ mix64(fnv64([]byte(detectorID))^block*0x9E3779B97F4A7C15))
	pick := h % interval
	return uint64(frameIdx)%interval == pick
}

// fnv64 is FNV-1a over the bytes.
func fnv64(b []byte) uint64 {
	h := uint64(14695981039346656037)
	for _, c := range b {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return h
}

// mix64 is the splitmix64 finalizer: every output bit depends on every
// input bit, so low bits of the result are not a function of low input bits.
func mix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// DelayedEnforcement delays enforcement decisions by a randomized amount
// to prevent cheat developers from correlating detection time with cheat activation.
type DelayedEnforcement struct {
	mu       sync.Mutex
	pending  []pendingAction
	minDelay time.Duration
	maxDelay time.Duration
	now      func() time.Time
}

type pendingAction struct {
	action    model.EnforcementAction
	executeAt time.Time
}

// NewDelayedEnforcement creates a delayed enforcer. maxDelay < minDelay is
// treated as maxDelay == minDelay.
func NewDelayedEnforcement(minDelay, maxDelay time.Duration) *DelayedEnforcement {
	if minDelay < 0 {
		minDelay = 0
	}
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	return &DelayedEnforcement{minDelay: minDelay, maxDelay: maxDelay, now: time.Now}
}

// SetClock overrides the wall clock (tests).
func (de *DelayedEnforcement) SetClock(now func() time.Time) {
	de.mu.Lock()
	defer de.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	de.now = now
}

// Queue adds an enforcement action to the delayed queue.
func (de *DelayedEnforcement) Queue(action model.EnforcementAction) {
	de.mu.Lock()
	defer de.mu.Unlock()

	// Deterministic delay based on action ID hash
	h := mix64(fnv64([]byte(action.ActionID)))
	delayRange := de.maxDelay - de.minDelay
	delayFraction := float64(h%1000) / 1000.0
	delay := de.minDelay + time.Duration(float64(delayRange)*delayFraction)

	de.pending = append(de.pending, pendingAction{
		action:    action,
		executeAt: de.now().Add(delay),
	})
}

// Pending returns the number of queued actions.
func (de *DelayedEnforcement) Pending() int {
	de.mu.Lock()
	defer de.mu.Unlock()
	return len(de.pending)
}

// Ready returns actions that are past their delay window, in queue order.
func (de *DelayedEnforcement) Ready() []model.EnforcementAction {
	de.mu.Lock()
	defer de.mu.Unlock()

	now := de.now()
	var ready []model.EnforcementAction
	var remaining []pendingAction

	for _, p := range de.pending {
		if !now.Before(p.executeAt) {
			ready = append(ready, p.action)
		} else {
			remaining = append(remaining, p)
		}
	}
	de.pending = remaining
	return ready
}

// AnomalyCluster tracks detection patterns across matches for a player.
type AnomalyCluster struct {
	mu       sync.RWMutex
	clusters map[string]*playerCluster // playerID -> cluster
	now      func() time.Time
}

type playerCluster struct {
	detectorHits map[string][]clusterEntry // detectorID -> entries
}

type clusterEntry struct {
	matchID    string
	timestamp  time.Time
	severity   float64
	confidence float64
}

// NewAnomalyCluster creates a cross-match anomaly tracker.
func NewAnomalyCluster() *AnomalyCluster {
	return &AnomalyCluster{clusters: make(map[string]*playerCluster), now: time.Now}
}

// SetClock overrides the wall clock used by Record and Prune (tests).
func (ac *AnomalyCluster) SetClock(now func() time.Time) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	ac.now = now
}

// Record adds a detection stamped with the current clock. Prefer RecordAt
// with the event's real time when replaying historical data, otherwise
// old events look fresh and never age out.
func (ac *AnomalyCluster) Record(event model.DetectionEvent) {
	ac.RecordAt(event, ac.clock())
}

func (ac *AnomalyCluster) clock() time.Time {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.now()
}

// RecordAt adds a detection with an explicit event time.
func (ac *AnomalyCluster) RecordAt(event model.DetectionEvent, at time.Time) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	pc, ok := ac.clusters[event.PlayerID]
	if !ok {
		pc = &playerCluster{detectorHits: make(map[string][]clusterEntry)}
		ac.clusters[event.PlayerID] = pc
	}
	pc.detectorHits[event.DetectorID] = append(pc.detectorHits[event.DetectorID], clusterEntry{
		matchID:    event.MatchID,
		timestamp:  at,
		severity:   event.Severity,
		confidence: event.Confidence,
	})
}

// Stats summarises a player's cluster.
type Stats struct {
	TotalEvents int
	Matches     []string
	Detectors   []string
	AvgSeverity float64
}

// Stats returns the current (post-prune) summary for a player.
func (ac *AnomalyCluster) Stats(playerID string) Stats {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	pc, ok := ac.clusters[playerID]
	if !ok {
		return Stats{}
	}
	return pc.stats()
}

func (pc *playerCluster) stats() Stats {
	matches := make(map[string]bool)
	var st Stats
	totalSev := 0.0
	for did, entries := range pc.detectorHits {
		if len(entries) == 0 {
			continue
		}
		st.Detectors = append(st.Detectors, did)
		for _, e := range entries {
			matches[e.matchID] = true
			totalSev += e.severity
			st.TotalEvents++
		}
	}
	for m := range matches {
		st.Matches = append(st.Matches, m)
	}
	sort.Strings(st.Matches)
	sort.Strings(st.Detectors)
	if st.TotalEvents > 0 {
		st.AvgSeverity = totalSev / float64(st.TotalEvents)
	}
	return st
}

// Score computes a cross-match anomaly score for a player from the entries
// currently held (so it shrinks after Prune). Higher = more suspicious
// pattern across matches. Scaled so 3 matches * 2 detectors * 0.8 severity
// ~= 5.0, capped at 10.
func (ac *AnomalyCluster) Score(playerID string) float64 {
	st := ac.Stats(playerID)
	if st.TotalEvents == 0 {
		return 0
	}
	return math.Min(10.0, float64(len(st.Matches))*float64(len(st.Detectors))*st.AvgSeverity)
}

// Prune removes entries older than maxAge relative to the clock and drops
// players with nothing left.
func (ac *AnomalyCluster) Prune(maxAge time.Duration) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	cutoff := ac.now().Add(-maxAge)
	for pid, pc := range ac.clusters {
		for did, entries := range pc.detectorHits {
			kept := entries[:0]
			for _, e := range entries {
				if e.timestamp.After(cutoff) {
					kept = append(kept, e)
				}
			}
			if len(kept) == 0 {
				delete(pc.detectorHits, did)
			} else {
				pc.detectorHits[did] = kept
			}
		}
		if len(pc.detectorHits) == 0 {
			delete(ac.clusters, pid)
		}
	}
}
