package adapter

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestMapperOrientationIdentityAndInvalidBasisProvenance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*EchoVRHand)
		valid  bool
	}{
		{"genuine identity", func(*EchoVRHand) {}, true},
		{"reflected identity", func(h *EchoVRHand) { h.Left = [3]float64{-1, 0, 0} }, true},
		{"missing", func(h *EchoVRHand) { h.Up = [3]float64{} }, false},
		{"collinear", func(h *EchoVRHand) { h.Up = h.Forward }, false},
		{"nan left", func(h *EchoVRHand) { h.Left[0] = math.NaN() }, false},
		{"infinite up", func(h *EchoVRHand) { h.Up[1] = math.Inf(1) }, false},
		{"overflowing forward", func(h *EchoVRHand) { h.Forward[2] = math.MaxFloat64 }, false},
		{"repaired skew basis", func(h *EchoVRHand) { h.Up[2] = .1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPlayer("observed", 1, [3]float64{1, 1, 2})
			tc.change(&p.LHand)
			got := NewMapper().MapSessionAt(twoTeamSession("orientation", []EchoVRPlayer{p}, nil), time.Unix(100, 0))
			if len(got.Frames) != 1 {
				t.Fatalf("mapping failed: %+v", got.Errors)
			}
			f := got.Frames[0]
			if f.LeftHandRotationValid == nil || *f.LeftHandRotationValid != tc.valid ||
				(tc.valid && !f.LeftHandRotation.IsUnit()) || (!tc.valid && f.LeftHandRotation != (model.Quat{})) {
				t.Fatalf("incorrect validity/value: %+v", f)
			}
			data, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip model.PlayerTelemetryFrame
			if err := json.Unmarshal(data, &roundTrip); err != nil {
				t.Fatal(err)
			}
			if roundTrip.LeftHandRotationValid == nil || *roundTrip.LeftHandRotationValid != tc.valid {
				t.Fatal("explicit false/true rotation provenance lost during JSON round trip")
			}
		})
	}
}

func TestMapperOrientationFingerprintIncludesFullBasis(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*EchoVRPlayer)
	}{
		{"left roll", func(p *EchoVRPlayer) { p.LHand.Left, p.LHand.Up = [3]float64{0, 1, 0}, [3]float64{-1, 0, 0} }},
		{"right roll", func(p *EchoVRPlayer) { p.RHand.Left, p.RHand.Up = [3]float64{0, 1, 0}, [3]float64{-1, 0, 0} }},
		{"body roll", func(p *EchoVRPlayer) { p.Body.Left, p.Body.Up = [3]float64{0, 1, 0}, [3]float64{-1, 0, 0} }},
		{"left validity loss", func(p *EchoVRPlayer) { p.LHand.Left = [3]float64{} }},
		{"right validity loss", func(p *EchoVRPlayer) { p.RHand.Up = [3]float64{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMapper()
			m.SetDedupeIdentical(true)
			s := twoTeamSession("orientation", []EchoVRPlayer{testPlayer("observed", 1, [3]float64{1, 1, 2})}, nil)
			at := time.Unix(100, 0)
			m.MapSessionAt(s, at)
			tc.change(&s.Teams[0].Players[0])
			if got := m.MapSessionAt(s, at.Add(50*time.Millisecond)); got.SkippedDuplicate || len(got.Frames) != 1 {
				t.Fatal("fresh roll/basis-validity change was suppressed")
			}
			if got := m.MapSessionAt(s, at.Add(100*time.Millisecond)); !got.SkippedDuplicate {
				t.Fatal("exact duplicate was not suppressed")
			}
		})
	}
}
