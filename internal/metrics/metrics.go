// Package metrics provides lightweight instrumentation for the anticheat pipeline.
//
// The ingest server and MatchManager update these collectors; the Prometheus
// exporter renders them on /metrics. Operator runbooks use
// nevr_ac_frames_invalid_total and nevr_ac_detection_events_total as STOP
// conditions, so every counter here must be fed by real code paths.
package metrics

import (
	"sync"
	"sync/atomic"
)

// Metrics holds all metric collectors.
type Metrics struct {
	// Frames
	FramesReceived       Counter // player-frames present in decoded batches
	FramesProcessed      Counter // player-frames the pipeline processed (same unit as FramesReceived)
	TicksProcessed       Counter // frame ticks (distinct frame indices) the pipeline processed
	FramesInvalid        Counter // frames rejected by ingest or pipeline validation
	FramesInvalidReasons LabeledCounter
	FramesRateLimited    Counter // frames dropped by the per-player ingest rate limit
	FramesIgnored        Counter // frames the store discarded as duplicates
	FramesRebased        Counter // frames whose index was re-based to stay monotonic

	// Batches / protocol
	BatchesReceived  Counter
	BatchesMalformed Counter
	BatchesRejected  LabeledCounter // reason label
	ControlMessages  LabeledCounter // type label
	AuthFailures     Counter

	// Detection
	DetectionEvents      LabeledCounter // detector label (non-shadow + shadow)
	ShadowEvents         LabeledCounter // detector label
	EventsDeduplicated   Counter        // raw emissions folded into incidents
	EventsRateLimited    Counter
	EventsInvalid        Counter
	StoreErrors          Counter
	MatchesCreated       Counter
	MatchesEnded         Counter
	ScoreSnapshotsStored Counter

	// Gauges
	ActiveConnections Gauge
	ActiveMatches     Gauge
}

// NewMetrics creates a new Metrics instance.
func NewMetrics() *Metrics {
	return &Metrics{
		FramesInvalidReasons: LabeledCounter{values: make(map[string]*Counter)},
		BatchesRejected:      LabeledCounter{values: make(map[string]*Counter)},
		ControlMessages:      LabeledCounter{values: make(map[string]*Counter)},
		DetectionEvents:      LabeledCounter{values: make(map[string]*Counter)},
		ShadowEvents:         LabeledCounter{values: make(map[string]*Counter)},
	}
}

// Counter is a thread-safe monotonic counter.
type Counter struct {
	value atomic.Int64
}

func (c *Counter) Inc()        { c.value.Add(1) }
func (c *Counter) Add(n int64) { c.value.Add(n) }
func (c *Counter) Get() int64  { return c.value.Load() }

// Gauge is a thread-safe settable value.
type Gauge struct {
	value atomic.Int64
}

func (g *Gauge) Set(n int64) { g.value.Store(n) }
func (g *Gauge) Inc()        { g.value.Add(1) }
func (g *Gauge) Dec()        { g.value.Add(-1) }
func (g *Gauge) Get() int64  { return g.value.Load() }

// LabeledCounter is a counter with string labels.
type LabeledCounter struct {
	mu     sync.RWMutex
	values map[string]*Counter
}

func (lc *LabeledCounter) counter(label string) *Counter {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.values == nil {
		lc.values = make(map[string]*Counter)
	}
	c, ok := lc.values[label]
	if !ok {
		c = &Counter{}
		lc.values[label] = c
	}
	return c
}

func (lc *LabeledCounter) Inc(label string)          { lc.counter(label).Inc() }
func (lc *LabeledCounter) Add(label string, n int64) { lc.counter(label).Add(n) }

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
