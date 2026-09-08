package model

import (
	"fmt"
	"math"
)

const MaxMechanicsRecords = 128

// MechanicsEventIdentity retains no raw telemetry. The bounded recent tail
// catches a repeated event accidentally relabelled with a later frame even
// after raw evidence fills up. Canonical event frames must remain immutable;
// the chronological high-water mark deduplicates all older canonical frames.
type MechanicsEventIdentity struct {
	EventID    string `json:"event_id"`
	FrameIndex int    `json:"frame_index"`
}

// MechanicsReviewLog retains the first 128 independent events per player and
// check. Counts include omitted evidence; they are not cheating incidents or a
// ground-truth opportunity denominator. Producers finalize events in order.
type MechanicsReviewLog struct {
	Version            int                      `json:"version"`
	Records            []MechanicsAssessment    `json:"records"`
	Total              int                      `json:"total"`
	Dropped            int                      `json:"dropped"`
	Invalid            int                      `json:"invalid"`
	Consistent         int                      `json:"consistent"`
	Inconclusive       int                      `json:"inconclusive"`
	Anomaly            int                      `json:"anomaly"`
	ValidatedViolation int                      `json:"validated_violation"`
	LastFrameIndex     int                      `json:"last_frame_index"`
	RecentEvents       []MechanicsEventIdentity `json:"recent_events,omitempty"`
}

func NewMechanicsReviewLog() *MechanicsReviewLog {
	return &MechanicsReviewLog{Version: 1, Records: []MechanicsAssessment{}, LastFrameIndex: -1}
}

func (l *MechanicsReviewLog) Add(r MechanicsAssessment) {
	if r.Validate() != nil {
		if l.Invalid < math.MaxInt {
			l.Invalid++
		}
		return
	}
	if l.Total == math.MaxInt || (l.Total > 0 && r.FrameIndex <= l.LastFrameIndex) || l.hasEvent(r.EventID) {
		return
	}
	l.Version, l.LastFrameIndex = 1, r.FrameIndex
	l.Total++
	l.remember(MechanicsEventIdentity{EventID: r.EventID, FrameIndex: r.FrameIndex})
	switch r.Result {
	case MechanicsConsistent:
		l.Consistent++
	case MechanicsInconclusive:
		l.Inconclusive++
	case MechanicsAnomaly:
		l.Anomaly++
	case MechanicsValidatedViolation:
		l.ValidatedViolation++
	}
	if len(l.Records) >= MaxMechanicsRecords {
		l.Dropped++
		return
	}
	l.Records = append(l.Records, r.Clone())
}

func (l *MechanicsReviewLog) hasEvent(id string) bool {
	for _, r := range l.Records {
		if r.EventID == id {
			return true
		}
	}
	for _, r := range l.RecentEvents {
		if r.EventID == id {
			return true
		}
	}
	return false
}

func (l *MechanicsReviewLog) remember(r MechanicsEventIdentity) {
	l.RecentEvents = append(l.RecentEvents, r)
	if len(l.RecentEvents) > MaxMechanicsRecords {
		copy(l.RecentEvents, l.RecentEvents[len(l.RecentEvents)-MaxMechanicsRecords:])
		l.RecentEvents = l.RecentEvents[:MaxMechanicsRecords]
	}
}

func (l *MechanicsReviewLog) Clone() *MechanicsReviewLog {
	if l == nil {
		return nil
	}
	out := *l
	out.RecentEvents = append([]MechanicsEventIdentity(nil), l.RecentEvents...)
	out.Records = make([]MechanicsAssessment, len(l.Records))
	for i, r := range l.Records {
		out.Records[i] = r.Clone()
	}
	return &out
}

func (l *MechanicsReviewLog) Validate() error {
	if l == nil {
		return nil
	}
	if l.Version != 1 || len(l.Records) > MaxMechanicsRecords || l.Total < 0 || l.Dropped < 0 || l.Invalid < 0 || l.Consistent < 0 || l.Inconclusive < 0 || l.Anomaly < 0 || l.ValidatedViolation < 0 ||
		l.Dropped > l.Total || l.Total-l.Dropped != len(l.Records) ||
		(l.Dropped > 0 && len(l.Records) != MaxMechanicsRecords) || (l.Total == 0 && l.LastFrameIndex != -1) {
		return fmt.Errorf("invalid mechanics diagnostic summary")
	}
	// Subtraction avoids accepting overflowed positive counters whose sum
	// wrapped back to Total. One accepted event per canonical frame also
	// bounds the claimed total independently of the retained evidence cap.
	remaining := l.Total
	for _, count := range []int{l.Consistent, l.Inconclusive, l.Anomaly, l.ValidatedViolation} {
		if count > remaining {
			return fmt.Errorf("invalid mechanics outcome counts")
		}
		remaining -= count
	}
	if remaining != 0 || (l.Total > 0 && l.Total-1 > l.LastFrameIndex) {
		return fmt.Errorf("invalid mechanics total")
	}
	retained := NewMechanicsReviewLog()
	for _, r := range l.Records {
		if err := r.Validate(); err != nil {
			return err
		}
		if r.FrameIndex <= retained.LastFrameIndex || r.FrameIndex > l.LastFrameIndex || retained.hasEvent(r.EventID) {
			return fmt.Errorf("invalid mechanics ordering or repeated event")
		}
		retained.Add(r)
	}
	if retained.Consistent > l.Consistent || retained.Inconclusive > l.Inconclusive || retained.Anomaly > l.Anomaly || retained.ValidatedViolation > l.ValidatedViolation ||
		(l.Total > 0 && len(l.Records) == 0) || (l.Dropped == 0 && retained.LastFrameIndex != l.LastFrameIndex) ||
		(l.Dropped > 0 && (retained.LastFrameIndex >= l.LastFrameIndex || l.Dropped > l.LastFrameIndex-retained.LastFrameIndex)) {
		return fmt.Errorf("inconsistent mechanics diagnostic summary")
	}
	if len(l.RecentEvents) > MaxMechanicsRecords || len(l.RecentEvents) > l.Total {
		return fmt.Errorf("oversized mechanics identity tail")
	}
	seen := make(map[string]int, len(l.Records)+len(l.RecentEvents))
	for _, r := range l.Records {
		seen[r.EventID] = r.FrameIndex
	}
	previous := -1
	for _, r := range l.RecentEvents {
		if len(r.EventID) == 0 || len(r.EventID) > 256 || r.FrameIndex <= previous || r.FrameIndex > l.LastFrameIndex {
			return fmt.Errorf("invalid mechanics identity tail")
		}
		if frame, ok := seen[r.EventID]; ok && frame != r.FrameIndex {
			return fmt.Errorf("repeated mechanics event identity")
		}
		seen[r.EventID], previous = r.FrameIndex, r.FrameIndex
	}
	return nil
}

// Merge supports later disjoint live chunks and idempotent replay. A partially
// overlapping omitted tail cannot be reconstructed from counts: abstain from
// merging it rather than manufacturing independent evidence.
func (l *MechanicsReviewLog) Merge(next *MechanicsReviewLog) {
	if next == nil || next.Validate() != nil || l.Validate() != nil {
		return
	}
	if next.Total == 0 {
		// Invalid-only chunks have no event identity/high-water mark. Adding
		// them repeatedly would fabricate failures; retain a lower bound.
		l.Invalid = max(l.Invalid, next.Invalid)
		return
	}
	if next.Total > 0 && l.Total > 0 && next.LastFrameIndex <= l.LastFrameIndex {
		return
	}
	if l.Total > 0 && next.Dropped > 0 && len(next.Records) > 0 && l.LastFrameIndex >= next.Records[len(next.Records)-1].FrameIndex {
		return
	}
	merged := l.Clone()
	if merged.mergeLater(next) && merged.Validate() == nil {
		*l = *merged
	}
}

func (l *MechanicsReviewLog) mergeLater(next *MechanicsReviewLog) bool {
	previousTotal := l.Total
	retained := NewMechanicsReviewLog()
	for _, r := range next.Records {
		retained.Add(r)
		l.Add(r)
	}
	if next.Dropped > 0 {
		if next.Dropped > math.MaxInt-l.Total {
			return false
		}
		// Tail identities are still bounded and contain no raw observations.
		for _, r := range next.RecentEvents {
			if r.FrameIndex > l.LastFrameIndex {
				if l.hasEvent(r.EventID) {
					return false // uncertain overlap: reject the whole clone
				}
				l.remember(r)
			}
		}
		l.Total += next.Dropped
		l.Dropped += next.Dropped
		l.Consistent += next.Consistent - retained.Consistent
		l.Inconclusive += next.Inconclusive - retained.Inconclusive
		l.Anomaly += next.Anomaly - retained.Anomaly
		l.ValidatedViolation += next.ValidatedViolation - retained.ValidatedViolation
		l.LastFrameIndex = next.LastFrameIndex
	}
	if l.Total == previousTotal {
		// An identity-rekeyed duplicate may have a newer frame watermark but
		// contributes no new canonical event. Its invalid count has no new
		// identity either; replay must not add that same failure repeatedly.
		l.Invalid = max(l.Invalid, next.Invalid)
	} else if next.Invalid > math.MaxInt-l.Invalid {
		l.Invalid = math.MaxInt
	} else {
		l.Invalid += next.Invalid
	}
	return true
}
