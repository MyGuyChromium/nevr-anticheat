package sqlite

import (
	"encoding/json"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// EvidenceTypeMarshalError tags an evidence_json row that holds an error
// message because the detector's evidence value could not be serialized
// (typically NaN/Inf in a float field).
const EvidenceTypeMarshalError = "marshal_error"

// RawEvidence carries evidence read back from the database whose concrete type
// is unknown to this binary (legacy rows without a type, or a type added by a
// newer version). The JSON is preserved verbatim so nothing is lost.
type RawEvidence struct {
	Type string          `json:"type,omitempty"`
	JSON json.RawMessage `json:"json"`
}

// EvidenceType implements model.Evidence.
func (r RawEvidence) EvidenceType() string {
	if r.Type == "" {
		return "raw"
	}
	return r.Type
}

// KnownEvidenceTypes returns the evidence type names this binary can decode,
// sorted. The registry lives in internal/model (the same one
// DetectionEvent's JSON marshalling uses); storage keeps no copy.
func KnownEvidenceTypes() []string {
	return model.EvidenceTypes()
}

// encodeEvidence serializes typed evidence with model.MarshalEvidence, i.e.
// the concrete fields plus a "type" discriminator, so the stored JSON alone
// is enough to decode it again. The evidence_type column is written as a
// convenience for SQL filtering. A value that cannot be serialized (NaN/Inf)
// is recorded as a visible marker instead of being silently dropped, so the
// moderator report shows why evidence is missing.
func encodeEvidence(ev model.Evidence) (evidenceJSON, evidenceType string) {
	if ev == nil {
		return "", ""
	}
	b, err := model.MarshalEvidence(ev)
	if err != nil {
		msg, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(msg), EvidenceTypeMarshalError
	}
	return string(b), ev.EvidenceType()
}

// DecodeEvidence reconstructs typed evidence from the stored (evidence_type,
// evidence_json) pair through model.DecodeEvidence. The "type" key embedded
// in the JSON wins; rows written before the typed envelope existed fall back
// to the evidence_type column. Known types decode to their concrete model
// struct (by value, matching what detectors emit); anything else is returned
// as RawEvidence so the JSON still reaches the moderator. Empty/null JSON
// yields nil.
func DecodeEvidence(evidenceType, evidenceJSON string) model.Evidence {
	if evidenceJSON == "" || evidenceJSON == "null" {
		return nil
	}
	raw := []byte(evidenceJSON)
	if ev, err := model.DecodeEvidence(raw); err == nil && ev != nil {
		return ev
	}
	if evidenceType != "" && evidenceType != EvidenceTypeMarshalError {
		if ev, err := model.DecodeEvidenceAs(evidenceType, raw); err == nil && ev != nil {
			return ev
		}
	}
	return RawEvidence{Type: evidenceType, JSON: json.RawMessage(raw)}
}
