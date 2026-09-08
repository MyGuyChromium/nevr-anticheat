package model

import (
	"crypto/sha256"
	"fmt"
)

// EventID is the canonical identity of one sampled release, independent of
// which diagnostic examines it or when confirmation arrives. Source metadata
// scopes identity only; it grants no authority. Private recorder paths and
// endpoints are hashed, never included verbatim in the resulting identifier.
func (r *ReleaseObservation) EventID() string {
	if r == nil {
		return ""
	}
	var source ObservationContext
	if r.Source != nil {
		source = *r.Source
	}
	// Quoting preserves field boundaries even for embedded delimiter bytes.
	identity := fmt.Appendf(nil, "%q\x00%q\x00%q\x00%q\x00%q\x00%q\x00%q\x00%d\x00%.17g\x00%q\x00%q\x00%d\x00%.17g",
		source.SessionID, source.Source, source.SourceID, source.SourcePlayerID,
		source.Authority, source.TimeBasis, source.Freshness, source.FrameIndex, source.Timestamp,
		"sampled_release", r.PlayerID, r.FirstFreeFrame, r.EndTime)
	return fmt.Sprintf("release:%x", sha256.Sum256(identity))
}
