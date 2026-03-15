package pipeline

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// RateLimiter caps events per player per detector per match.
type RateLimiter struct {
	maxPerPlayerPerDetector int
	counts                  map[string]int // key: playerID:detectorID
}

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(max int) *RateLimiter {
	if max <= 0 {
		max = 100
	}
	return &RateLimiter{
		maxPerPlayerPerDetector: max,
		counts:                  make(map[string]int),
	}
}

// Reset clears rate limit state.
func (rl *RateLimiter) Reset() {
	rl.counts = make(map[string]int)
}

// Filter removes events that exceed the rate limit.
func (rl *RateLimiter) Filter(events []model.DetectionEvent) []model.DetectionEvent {
	var out []model.DetectionEvent
	for _, ev := range events {
		key := ev.PlayerID + ":" + ev.DetectorID
		if rl.counts[key] >= rl.maxPerPlayerPerDetector {
			continue
		}
		rl.counts[key]++
		out = append(out, ev)
	}
	return out
}
