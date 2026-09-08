package model

import "math"

// ObservationContext describes the measurement path, not its truth. Client
// reports are never promoted to engine authority by a valid numeric payload.
type ObservationContext struct {
	Source   string `json:"source"`
	SourceID string `json:"source_id,omitempty"`
	// SourceEpoch isolates locally detected clock discontinuities. It is
	// not an authenticated producer epoch or proof of a new capture.
	SourceEpoch    uint64  `json:"source_epoch,omitempty"`
	Authority      string  `json:"authority"`
	TimeBasis      string  `json:"time_basis"`
	SessionID      string  `json:"session_id"`
	SourcePlayerID string  `json:"source_player_id,omitempty"`
	FrameIndex     int     `json:"frame_index"`
	Timestamp      float64 `json:"timestamp"`
	Freshness      string  `json:"freshness,omitempty"`
}

func (o *ObservationContext) Clone() *ObservationContext {
	if o == nil {
		return nil
	}
	copy := *o
	return &copy
}

func (o *ObservationContext) Valid() bool {
	return o != nil && o.Source != "" && o.Authority != "" && o.TimeBasis != "" && o.SessionID != "" &&
		o.FrameIndex >= 0 && o.Timestamp >= 0 && !math.IsNaN(o.Timestamp) && !math.IsInf(o.Timestamp, 0)
}

func (o *ObservationContext) SameSource(other *ObservationContext) bool {
	return o.Valid() && other.Valid() && o.Source == other.Source && o.SourceID == other.SourceID && o.SourceEpoch == other.SourceEpoch &&
		o.Authority == other.Authority && o.TimeBasis == other.TimeBasis && o.SessionID == other.SessionID && o.SourcePlayerID == other.SourcePlayerID
}

func (o *ObservationContext) BoundLocalThrow(player string, frame int, timestamp float64) bool {
	return o.Valid() && o.Authority == "client_reported" && o.Freshness == "value_change" &&
		o.SourcePlayerID == player && o.FrameIndex == frame && o.Timestamp == timestamp
}

// HandAttachments retains exact reported object labels independently of global
// disc ownership. Nil means absent; an empty/unsupported label is unknown.
type HandAttachments struct {
	Left  *string `json:"left,omitempty"`
	Right *string `json:"right,omitempty"`
}

func (h *HandAttachments) Clone() *HandAttachments {
	if h == nil {
		return nil
	}
	out := *h
	if h.Left != nil {
		v := *h.Left
		out.Left = &v
	}
	if h.Right != nil {
		v := *h.Right
		out.Right = &v
	}
	return &out
}

type DiscAttachment struct {
	State          string   `json:"state"` // unknown, free, held
	HolderID       string   `json:"holder_id,omitempty"`
	HandCandidates []string `json:"hand_candidates,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

func (a *DiscAttachment) Clone() *DiscAttachment {
	if a == nil {
		return nil
	}
	out := *a
	out.HandCandidates = append([]string(nil), a.HandCandidates...)
	return &out
}

func (a *DiscAttachment) Known() bool {
	if a == nil {
		return false
	}
	if a.State == "free" {
		return a.HolderID == "" && len(a.HandCandidates) == 0
	}
	if a.State != "held" || a.HolderID == "" || len(a.HandCandidates) < 1 || len(a.HandCandidates) > 2 {
		return false
	}
	for i, hand := range a.HandCandidates {
		if hand != "left" && hand != "right" {
			return false
		}
		if i > 0 && hand == a.HandCandidates[0] {
			return false
		}
	}
	return true
}

func (a *DiscAttachment) HeldBy(id string) bool {
	return a.Known() && a.State == "held" && a.HolderID == id
}
func (a *DiscAttachment) Free() bool { return a.Known() && a.State == "free" }

type MovementObservation struct {
	FrameIndex       int     `json:"frame_index"`
	Timestamp        float64 `json:"timestamp"`
	Position         Vec3    `json:"position"`
	ReportedVelocity *Vec3   `json:"reported_velocity,omitempty"`
}

// ReleaseObservation bounds a sampled transition. It never claims the first
// free sample is the exact acquisition/release tick inside that interval.
type ReleaseObservation struct {
	PlayerID       string                `json:"player_id"`
	FirstFreeFrame int                   `json:"first_free_frame"`
	StartFrame     int                   `json:"start_frame"`
	EndFrame       int                   `json:"end_frame"`
	StartTime      float64               `json:"start_time"`
	EndTime        float64               `json:"end_time"`
	Source         *ObservationContext   `json:"source,omitempty"`
	HandCandidates []string              `json:"hand_candidates,omitempty"`
	PlayerMovement []MovementObservation `json:"player_movement,omitempty"`
}

func (r *ReleaseObservation) Clone() *ReleaseObservation {
	if r == nil {
		return nil
	}
	out := *r
	out.Source = r.Source.Clone()
	out.HandCandidates = append([]string(nil), r.HandCandidates...)
	out.PlayerMovement = append([]MovementObservation(nil), r.PlayerMovement...)
	for i := range out.PlayerMovement {
		if v := out.PlayerMovement[i].ReportedVelocity; v != nil {
			copy := *v
			out.PlayerMovement[i].ReportedVelocity = &copy
		}
	}
	return &out
}
