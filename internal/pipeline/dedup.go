package pipeline

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// Deduplicator merges overlapping detection events by causal key.
type Deduplicator struct {
	mergeWindow int // frames
	pending     map[string]model.DetectionEvent
}

// NewDeduplicator creates a deduplicator with the given merge window.
func NewDeduplicator(mergeWindow int) *Deduplicator {
	return &Deduplicator{
		mergeWindow: mergeWindow,
		pending:     make(map[string]model.DetectionEvent),
	}
}

// Reset clears pending state.
func (d *Deduplicator) Reset() {
	d.pending = make(map[string]model.DetectionEvent)
}

// Deduplicate filters events, merging those with overlapping causal keys.
// Returns only the highest-severity version of each causal group.
func (d *Deduplicator) Deduplicate(events []model.DetectionEvent) []model.DetectionEvent {
	// First pass: merge overlapping events into pending map, keeping best severity/confidence
	for _, ev := range events {
		key := ev.CausalKey.Key()
		if existing, ok := d.pending[key]; ok {
			if existing.CausalKey.Overlaps(ev.CausalKey) {
				if ev.Severity > existing.Severity {
					existing.Severity = ev.Severity
					existing.Evidence = ev.Evidence
					existing.ObservedValue = ev.ObservedValue
				}
				if ev.Confidence > existing.Confidence {
					existing.Confidence = ev.Confidence
				}
				if ev.FrameRangeEnd > existing.FrameRangeEnd {
					existing.FrameRangeEnd = ev.FrameRangeEnd
					existing.CausalKey.FrameEnd = ev.FrameRangeEnd
				}
				d.pending[key] = existing
				continue
			}
		}
		d.pending[key] = ev
	}

	// Second pass: emit unique events (one per causal key, with merged values)
	seen := make(map[string]bool, len(events))
	var out []model.DetectionEvent
	for _, ev := range events {
		key := ev.CausalKey.Key()
		if seen[key] {
			continue
		}
		seen[key] = true
		if merged, ok := d.pending[key]; ok {
			out = append(out, merged)
		} else {
			out = append(out, ev)
		}
	}
	return out
}
