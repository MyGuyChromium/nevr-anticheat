package sqlite

import (
	"encoding/json"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// EvidenceTypeMarshalError tags an evidence_json row that holds an error
// message because the detector's evidence value could not be serialized
// (typically NaN/Inf in a float field).
const EvidenceTypeMarshalError = "marshal_error"

// RawEvidence carries evidence read back from the database whose concrete type
// is unknown to this binary (legacy rows without evidence_type, or a type added
// by a newer version). The JSON is preserved verbatim so nothing is lost.
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

func decodeAs[T model.Evidence](b []byte) (model.Evidence, error) {
	var e T
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	return e, nil
}

// evidenceDecoders maps model.Evidence.EvidenceType() to a decoder for the
// concrete type. Every type in internal/model/evidence_types.go is listed;
// the registry test asserts nothing is missing.
var evidenceDecoders = map[string]func([]byte) (model.Evidence, error){
	model.ThrowEvidence{}.EvidenceType():            decodeAs[model.ThrowEvidence],
	model.DiscAccelerationEvidence{}.EvidenceType(): decodeAs[model.DiscAccelerationEvidence],
	model.ReleaseAngleEvidence{}.EvidenceType():     decodeAs[model.ReleaseAngleEvidence],
	model.SignatureRepeatEvidence{}.EvidenceType():  decodeAs[model.SignatureRepeatEvidence],
	model.PrecisionEvidence{}.EvidenceType():        decodeAs[model.PrecisionEvidence],
	model.TrajectoryEvidence{}.EvidenceType():       decodeAs[model.TrajectoryEvidence],
	model.PenaltyFieldEvidence{}.EvidenceType():     decodeAs[model.PenaltyFieldEvidence],
	model.SpeedDistanceEvidence{}.EvidenceType():    decodeAs[model.SpeedDistanceEvidence],
	model.WristRotationEvidence{}.EvidenceType():    decodeAs[model.WristRotationEvidence],
	model.HandSpeedEvidence{}.EvidenceType():        decodeAs[model.HandSpeedEvidence],
	model.ZeroJitterEvidence{}.EvidenceType():       decodeAs[model.ZeroJitterEvidence],
	model.ZeroWobbleEvidence{}.EvidenceType():       decodeAs[model.ZeroWobbleEvidence],
	model.MovementEvidence{}.EvidenceType():         decodeAs[model.MovementEvidence],
	model.StateEvidence{}.EvidenceType():            decodeAs[model.StateEvidence],
	model.PatternEvidence{}.EvidenceType():          decodeAs[model.PatternEvidence],
}

// KnownEvidenceTypes returns the evidence type names this binary can decode, sorted.
func KnownEvidenceTypes() []string {
	out := make([]string, 0, len(evidenceDecoders))
	for k := range evidenceDecoders {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DecodeEvidence reconstructs typed evidence from the stored (evidence_type,
// evidence_json) pair. Known types decode to their concrete model struct
// (by value, matching what detectors emit); anything else is returned as
// RawEvidence so the JSON still reaches the moderator. Empty/null JSON yields nil.
func DecodeEvidence(evidenceType, evidenceJSON string) model.Evidence {
	if evidenceJSON == "" || evidenceJSON == "null" {
		return nil
	}
	if dec, ok := evidenceDecoders[evidenceType]; ok {
		if ev, err := dec([]byte(evidenceJSON)); err == nil {
			return ev
		}
	}
	return RawEvidence{Type: evidenceType, JSON: json.RawMessage(evidenceJSON)}
}
