package pipeline

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// RateLimiter caps events per player per detector per match.
//
// It is first-N-wins: once a player/detector key reaches the cap every later
// event is dropped. Because the Deduplicator now collapses a sustained anomaly
// into one incident, the cap counts incidents rather than frames, so the
// budget is no longer exhausted by a single excursion. Every drop is counted
// per key so MatchResult can report how much evidence was truncated instead
// of losing it silently.
type RateLimiter struct {
	decisionObserver        func(model.DetectionEvent)
	maxPerPlayerPerDetector int
	counts                  map[string]int // key: playerID:detectorID
	dropped                 map[string]int // key: playerID:detectorID
}

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(max int) *RateLimiter {
	if max <= 0 {
		max = 100
	}
	return &RateLimiter{
		maxPerPlayerPerDetector: max,
		counts:                  make(map[string]int),
		dropped:                 make(map[string]int),
	}
}

// Reset clears rate limit state.
func (rl *RateLimiter) Reset() {
	rl.counts = make(map[string]int)
	rl.dropped = make(map[string]int)
}

// Filter removes events that exceed the rate limit and returns the kept
// events plus the drops made by this call keyed by "player:detector" (nil
// when nothing was dropped).
func (rl *RateLimiter) Filter(events []model.DetectionEvent) ([]model.DetectionEvent, map[string]int) {
	var out []model.DetectionEvent
	var droppedNow map[string]int
	for _, ev := range events {
		key := ev.PlayerID + ":" + ev.DetectorID
		if rl.counts[key] >= rl.maxPerPlayerPerDetector {
			if rl.decisionObserver != nil {
				rl.decisionObserver(ev)
			}
			rl.dropped[key]++
			if droppedNow == nil {
				droppedNow = make(map[string]int)
			}
			droppedNow[key]++
			continue
		}
		rl.counts[key]++
		out = append(out, ev)
	}
	return out, droppedNow
}

// Dropped returns a copy of the per-key drop counts since the last Reset.
func (rl *RateLimiter) Dropped() map[string]int {
	out := make(map[string]int, len(rl.dropped))
	for k, v := range rl.dropped {
		out[k] = v
	}
	return out
}
