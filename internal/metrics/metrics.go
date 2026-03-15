// Package metrics provides lightweight instrumentation for the anticheat pipeline.
package metrics

import (
	"sync"
	"sync/atomic"
)

// Metrics holds all metric collectors.
type Metrics struct {
	FramesProcessed   Counter
	FramesInvalid     Counter
	DetectionEvents   LabeledCounter
	EventsDeduplicated Counter
	EventsRateLimited  Counter
}

// NewMetrics creates a new Metrics instance.
func NewMetrics() *Metrics {
	return &Metrics{
		DetectionEvents: LabeledCounter{values: make(map[string]*Counter)},
	}
}

// Counter is a thread-safe monotonic counter.
type Counter struct {
	value atomic.Int64
}

func (c *Counter) Inc()         { c.value.Add(1) }
func (c *Counter) Add(n int64)  { c.value.Add(n) }
func (c *Counter) Get() int64   { return c.value.Load() }

// LabeledCounter is a counter with string labels.
type LabeledCounter struct {
	mu     sync.RWMutex
	values map[string]*Counter
}

func (lc *LabeledCounter) Inc(label string) {
	lc.mu.Lock()
	if lc.values == nil {
		lc.values = make(map[string]*Counter)
	}
	c, ok := lc.values[label]
	if !ok {
		c = &Counter{}
		lc.values[label] = c
	}
	lc.mu.Unlock()
	c.Inc()
}

func (lc *LabeledCounter) Get(label string) int64 {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	if c, ok := lc.values[label]; ok {
		return c.Get()
	}
	return 0
}

func (lc *LabeledCounter) Snapshot() map[string]int64 {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	out := make(map[string]int64, len(lc.values))
	for k, v := range lc.values {
		out[k] = v.Get()
	}
	return out
}
