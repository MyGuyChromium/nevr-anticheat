package throw

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestThrow004OrientationUnknownNeverEntersSignature(t *testing.T) {
	d := NewThrow004(nil)
	for i, tc := range []struct {
		valid bool
		rate  float64
	}{
		{false, 0}, {false, 25}, {true, math.NaN()},
		{true, math.Inf(1)}, {true, -1},
	} {
		players := withThrow("p1", i, 12, 15)
		players["p1"].LastThrow.WristKinematicsValid = tc.valid
		players["p1"].LastThrow.WristAngularVelocity = tc.rate
		if events := d.Evaluate(testCtx(), players, i); len(events) != 0 || len(d.signatures["p1"]) != 0 {
			t.Fatalf("missing/malformed wrist rate entered signature: valid=%t rate=%g", tc.valid, tc.rate)
		}
	}
	players := withThrow("p1", 10, 12, 15)
	players["p1"].LastThrow.WristAngularVelocity = 0
	d.Evaluate(testCtx(), players, 10)
	if len(d.signatures["p1"]) != 1 || d.signatures["p1"][0].values[3] != 0 {
		t.Fatal("an explicitly measured stationary wrist must retain its real zero")
	}
}

func TestThrow003OrientationEvidenceDoesNotInventControllerSetting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		q     model.Quat
		valid bool
		want  bool
	}{
		{"observed identity", model.QuatIdentity(), true, true},
		{"rounded observed identity", model.Quat{0, 0, 0, .995}, true, true},
		{"legacy fallback identity", model.QuatIdentity(), false, false},
		{"missing", model.Quat{}, false, false},
		{"invalid true", model.Quat{math.NaN(), 0, 0, 1}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			players := withThrow("p1", 10, 15, 179)
			te := players["p1"].LastThrow
			te.WristOrientation, te.WristOrientationValid = tc.q, tc.valid
			events := NewThrow003(nil).Evaluate(testCtx(), players, 10)
			// Orientation is NOT an input to the world-velocity comparison.
			if len(events) != 1 {
				t.Fatalf("valid sampled velocity comparison incorrectly required a wrist pose: %d events", len(events))
			}
			evidence := events[0].Evidence.(model.ReleaseAngleEvidence)
			if evidence.WristOrientationValid != tc.want || !evidence.HandKinematicsValid {
				t.Fatalf("incorrect observation provenance: %+v", evidence)
			}
			if tc.want && evidence.WristOrientation != model.QuatIdentity() {
				t.Fatal("observed identity lost")
			}
			if !tc.want && evidence.WristOrientation != (model.Quat{}) {
				t.Fatal("missing wrist pose was promoted to an observed identity")
			}
			encoded, err := json.Marshal(evidence)
			if err != nil {
				t.Fatalf("invalid optional pose leaked into persistent evidence: %v", err)
			}
			var roundtrip model.ReleaseAngleEvidence
			if err := json.Unmarshal(encoded, &roundtrip); err != nil || roundtrip.WristOrientationValid != tc.want {
				t.Fatalf("validity lost in JSON roundtrip: %v", err)
			}
		})
	}
}

func TestThrow003OrientationConsumerStillRequiresFiniteHandMotion(t *testing.T) {
	for _, mutate := range []func(*model.ThrowEvent){
		func(te *model.ThrowEvent) { te.HandKinematicsValid = false },
		func(te *model.ThrowEvent) { te.HandVelocity[0] = math.NaN() },
		func(te *model.ThrowEvent) { te.PlayerVelocity[0] = math.Inf(1) },
		func(te *model.ThrowEvent) { te.ReleaseAngle = math.Inf(1) },
	} {
		players := withThrow("p1", 10, 15, 179)
		mutate(players["p1"].LastThrow)
		if events := NewThrow003(nil).Evaluate(testCtx(), players, 10); len(events) != 0 {
			t.Fatal("unusable motion produced release-angle evidence")
		}
	}
}
