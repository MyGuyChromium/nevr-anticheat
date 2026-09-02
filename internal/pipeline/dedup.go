package pipeline

import (
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Deduplicator collapses the per-frame emissions of a sustained anomaly into
// one incident event.
//
// Detectors that use a sliding window (MOV_001, BIO_00x, STATE_00x, THROW_006,
// ...) emit an event on every frame the condition holds, each with a causal
// key whose frame range shifts by one. The deduplicator keeps one OPEN
// incident per CausalKey.Key() (player + anomaly type). A new emission joins
// the open incident when its frame range overlaps it or starts within
// mergeWindow frames of its end; the incident's range is extended, the
// highest severity (with its evidence) and confidence are kept, and
// MergedCount is incremented. An incident is CLOSED, and emitted exactly
// once, when the current frame index has moved more than mergeWindow frames
// past its end, or on Flush at match end.
//
// The effect is that a 60-frame wrist-rate excursion becomes a single
// DetectionEvent spanning frames [start, end] instead of ~55 events.
type Deduplicator struct {
	mergeWindow int // frames
	open        map[string]*model.DetectionEvent
	merged      int64 // raw emissions folded into an existing incident
}

// NewDeduplicator creates a deduplicator with the given merge window (frames).
func NewDeduplicator(mergeWindow int) *Deduplicator {
	if mergeWindow < 0 {
		mergeWindow = 0
	}
	return &Deduplicator{
		mergeWindow: mergeWindow,
		open:        make(map[string]*model.DetectionEvent),
	}
}

// Reset clears pending state.
func (d *Deduplicator) Reset() {
	d.open = make(map[string]*model.DetectionEvent)
	d.merged = 0
}

// MergedCount returns how many raw emissions have been folded into an
// existing incident since the last Reset.
func (d *Deduplicator) MergedCount() int64 { return d.merged }

// PendingCount returns the number of open (not yet emitted) incidents.
func (d *Deduplicator) PendingCount() int { return len(d.open) }

// Deduplicate folds the emissions of frame frameIdx into the open incidents
// and returns the incidents that are now closed, in deterministic order.
func (d *Deduplicator) Deduplicate(events []model.DetectionEvent, frameIdx int) []model.DetectionEvent {
	var out []model.DetectionEvent
	for i := range events {
		ev := events[i]
		if ev.MergedCount <= 0 {
			ev.MergedCount = 1
		}
		key := ev.CausalKey.Key()
		inc, ok := d.open[key]
		if ok && d.belongs(inc, &ev) {
			mergeInto(inc, &ev)
			d.merged++
			continue
		}
		if ok {
			// Same key but too far apart: the old incident is over.
			out = append(out, *inc)
		}
		evCopy := ev
		d.open[key] = &evCopy
	}

	for key, inc := range d.open {
		if frameIdx > inc.FrameRangeEnd+d.mergeWindow {
			out = append(out, *inc)
			delete(d.open, key)
		}
	}
	sortIncidents(out)
	return out
}

// Flush closes and returns every open incident (match end).
func (d *Deduplicator) Flush() []model.DetectionEvent {
	var out []model.DetectionEvent
	for key, inc := range d.open {
		out = append(out, *inc)
		delete(d.open, key)
	}
	sortIncidents(out)
	return out
}

// belongs reports whether ev continues the open incident inc.
func (d *Deduplicator) belongs(inc, ev *model.DetectionEvent) bool {
	if inc.CausalKey.Overlaps(ev.CausalKey) {
		return true
	}
	// Within the merge window on either side (emissions are usually
	// monotonic, but a detector may report a range that starts earlier).
	return ev.FrameRangeStart <= inc.FrameRangeEnd+d.mergeWindow &&
		ev.FrameRangeEnd >= inc.FrameRangeStart-d.mergeWindow
}

// mergeInto extends inc with ev, keeping the strongest evidence.
func mergeInto(inc, ev *model.DetectionEvent) {
	if ev.Severity > inc.Severity {
		inc.Severity = ev.Severity
		inc.Evidence = ev.Evidence
		inc.ObservedValue = ev.ObservedValue
		inc.ExpectedRange = ev.ExpectedRange
		inc.BaselineComparison = ev.BaselineComparison
		inc.Attribution = ev.Attribution
		inc.FrameIndex = ev.FrameIndex
		inc.Timestamp = ev.Timestamp
	}
	if ev.Confidence > inc.Confidence {
		inc.Confidence = ev.Confidence
	}
	if ev.FrameRangeStart < inc.FrameRangeStart {
		inc.FrameRangeStart = ev.FrameRangeStart
	}
	if ev.FrameRangeEnd > inc.FrameRangeEnd {
		inc.FrameRangeEnd = ev.FrameRangeEnd
	}
	inc.CausalKey.FrameStart = inc.FrameRangeStart
	inc.CausalKey.FrameEnd = inc.FrameRangeEnd
	inc.MergedCount += ev.MergedCount
}

// sortIncidents orders emitted incidents deterministically.
func sortIncidents(evs []model.DetectionEvent) {
	sort.SliceStable(evs, func(i, j int) bool {
		a, b := evs[i], evs[j]
		if a.FrameRangeStart != b.FrameRangeStart {
			return a.FrameRangeStart < b.FrameRangeStart
		}
		if a.PlayerID != b.PlayerID {
			return a.PlayerID < b.PlayerID
		}
		if a.DetectorID != b.DetectorID {
			return a.DetectorID < b.DetectorID
		}
		return a.FrameIndex < b.FrameIndex
	})
}
