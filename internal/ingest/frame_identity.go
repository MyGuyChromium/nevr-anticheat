package ingest

import (
	"encoding/json"
	"math"
	"reflect"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func sameStoredLiveFrame(priorJSON string, incoming model.PlayerTelemetryFrame) (bool, error) {
	var prior model.PlayerTelemetryFrame
	if err := json.Unmarshal([]byte(priorJSON), &prior); err != nil {
		return false, err
	}
	return sameLiveFrame(prior, incoming)
}

// Compare the same canonical JSON representation used by storage. This
// removes harmless serialization aliases/defaults without dropping unknown vs
// known input provenance. Source changes are not retransmission metadata and
// remain significant. Quaternion double-cover and bounded float roundoff are
// equivalent; identity strings, epochs and integer counters remain exact.
func sameLiveFrame(a, b model.PlayerTelemetryFrame) (bool, error) {
	canonical := func(f model.PlayerTelemetryFrame) (model.PlayerTelemetryFrame, error) {
		data, err := json.Marshal(f)
		if err != nil {
			return model.PlayerTelemetryFrame{}, err
		}
		var out model.PlayerTelemetryFrame
		err = json.Unmarshal(data, &out)
		return out, err
	}
	x, err := canonical(a)
	if err != nil {
		return false, err
	}
	y, err := canonical(b)
	if err != nil {
		return false, err
	}
	return sameFrameValue(reflect.ValueOf(x), reflect.ValueOf(y)), nil
}

func sameFrameValue(a, b reflect.Value) bool {
	if a.Type() != b.Type() {
		return false
	}
	if a.Type() == reflect.TypeOf(model.Quat{}) {
		qa, qb := a.Interface().(model.Quat), b.Interface().(model.Quat)
		if qa.IsUnit() && qb.IsUnit() && qa.Dot(qb) < 0 {
			for i := range qb {
				qb[i] = -qb[i]
			}
			b = reflect.ValueOf(qb)
		}
	}
	switch a.Kind() {
	case reflect.Float32, reflect.Float64:
		if a.Float() == b.Float() {
			return true
		}
		// sameLiveTickTime handles non-negative values; use magnitudes for
		// signed pose/velocity components while preserving the difference.
		return closeFrameFloat(a.Float(), b.Float())
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		return sameFrameValue(a.Elem(), b.Elem())
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !sameFrameValue(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Array, reflect.Slice:
		if a.Len() != b.Len() {
			return false
		}
		if a.Kind() == reflect.Slice && a.IsNil() != b.IsNil() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !sameFrameValue(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
}

func closeFrameFloat(a, b float64) bool {
	magnitude := math.Max(math.Abs(a), math.Abs(b))
	ulp := math.Nextafter(magnitude, math.Inf(1)) - magnitude
	return math.Abs(a-b) <= math.Min(1e-9, 8*ulp)
}
