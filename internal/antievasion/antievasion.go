// Package antievasion implements protections against cheat developers evading detection.
package antievasion

import (
	"math"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// WindowRandomizer adds jitter to detector evaluation windows so cheat developers
// cannot predict exactly when measurements are taken.
type WindowRandomizer struct {
	// Deterministic jitter based on match+player+frame to be reproducible
	seed uint64
}

// NewWindowRandomizer creates a randomizer seeded from match context.
func NewWindowRandomizer(matchID string) *WindowRandomizer {
	// Simple hash of matchID for deterministic-but-unpredictable seed
	var h uint64
	for _, b := range []byte(matchID) {
		h = h*31 + uint64(b)
	}
	return &WindowRandomizer{seed: h}
}

// ShouldEvaluate returns true if a detector should evaluate at this frame.
// Adds deterministic jitter: instead of evaluating every frame, skip some frames
// in an unpredictable-but-reproducible pattern.
func (wr *WindowRandomizer) ShouldEvaluate(detectorID string, frameIdx int, baseInterval int) bool {
	if baseInterval <= 1 {
		return true // evaluate every frame
	}
	// Mix detector ID into the hash
	var dh uint64
	for _, b := range []byte(detectorID) {
		dh = dh*37 + uint64(b)
	}
	combined := wr.seed ^ dh ^ uint64(frameIdx)*2654435761
	return combined%uint64(baseInterval) == 0
}

// DelayedEnforcement delays enforcement decisions by a randomized amount
// to prevent cheat developers from correlating detection time with cheat activation.
type DelayedEnforcement struct {
	mu       sync.Mutex
	pending  []pendingAction
	minDelay time.Duration
	maxDelay time.Duration
}

type pendingAction struct {
	action    model.EnforcementAction
	executeAt time.Time
}

// NewDelayedEnforcement creates a delayed enforcer.
func NewDelayedEnforcement(minDelay, maxDelay time.Duration) *DelayedEnforcement {
	return &DelayedEnforcement{
		minDelay: minDelay,
		maxDelay: maxDelay,
	}
}

// Queue adds an enforcement action to the delayed queue.
func (de *DelayedEnforcement) Queue(action model.EnforcementAction) {
	de.mu.Lock()
	defer de.mu.Unlock()

	// Deterministic delay based on action ID hash
	var h uint64
	for _, b := range []byte(action.ActionID) {
		h = h*31 + uint64(b)
	}
	delayRange := de.maxDelay - de.minDelay
	delayFraction := float64(h%1000) / 1000.0
	delay := de.minDelay + time.Duration(float64(delayRange)*delayFraction)

	de.pending = append(de.pending, pendingAction{
		action:    action,
		executeAt: time.Now().Add(delay),
	})
}

// Ready returns actions that are past their delay window.
func (de *DelayedEnforcement) Ready() []model.EnforcementAction {
	de.mu.Lock()
	defer de.mu.Unlock()

	now := time.Now()
	var ready []model.EnforcementAction
	var remaining []pendingAction

	for _, p := range de.pending {
		if now.After(p.executeAt) {
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
}

type playerCluster struct {
	detectorHits map[string][]clusterEntry // detectorID -> entries
	totalEvents  int
	matchIDs     map[string]bool
}

type clusterEntry struct {
	matchID    string
	timestamp  time.Time
	severity   float64
	confidence float64
}

// NewAnomalyCluster creates a cross-match anomaly tracker.
func NewAnomalyCluster() *AnomalyCluster {
	return &AnomalyCluster{
		clusters: make(map[string]*playerCluster),
	}
}

// Record adds a detection to the cluster.
func (ac *AnomalyCluster) Record(event model.DetectionEvent) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	pc, ok := ac.clusters[event.PlayerID]
	if !ok {
		pc = &playerCluster{
			detectorHits: make(map[string][]clusterEntry),
			matchIDs:     make(map[string]bool),
		}
		ac.clusters[event.PlayerID] = pc
	}

	pc.detectorHits[event.DetectorID] = append(pc.detectorHits[event.DetectorID], clusterEntry{
		matchID:    event.MatchID,
		timestamp:  time.Now(),
		severity:   event.Severity,
		confidence: event.Confidence,
	})
	pc.totalEvents++
	pc.matchIDs[event.MatchID] = true
}

// Score computes a cross-match anomaly score for a player.
// Higher score = more suspicious pattern across matches.
func (ac *AnomalyCluster) Score(playerID string) float64 {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	pc, ok := ac.clusters[playerID]
	if !ok {
		return 0
	}

	// Factors: number of distinct matches, number of distinct detectors, total events
	matchCount := float64(len(pc.matchIDs))
	detectorCount := float64(len(pc.detectorHits))

	// Weight by average severity across all hits
	totalSev := 0.0
	totalHits := 0
	for _, entries := range pc.detectorHits {
		for _, e := range entries {
			totalSev += e.severity
			totalHits++
		}
	}
	avgSev := 0.0
	if totalHits > 0 {
		avgSev = totalSev / float64(totalHits)
	}

	// Cross-match score: matches * detectors * avgSeverity
	// Scaled so 3 matches * 2 detectors * 0.8 severity = ~5.0
	return math.Min(10.0, matchCount*detectorCount*avgSev)
}

// Prune removes entries older than maxAge.
func (ac *AnomalyCluster) Prune(maxAge time.Duration) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for pid, pc := range ac.clusters {
		for did, entries := range pc.detectorHits {
			var kept []clusterEntry
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
