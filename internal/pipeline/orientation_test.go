package pipeline

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestExtractorOrientationExplicitValidityAndReacquisition(t *testing.T) {
	fe, ps, mc := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}, feTestCtx()
	no := false
	for i, tc := range []struct {
		q         model.Quat
		known     *bool
		rateValid bool
	}{
		{model.Quat{0, 0, 0, .995}, nil, false},
		{model.Quat{0, 0, 0, -.995}, nil, true},
		{model.QuatIdentity(), &no, false},
		{model.Quat{0, 1, 0, 0}, nil, false},
		{model.Quat{0, -1, 0, 0}, nil, true},
	} {
		f := feFrame("p1", i, float64(i)*.02, model.Vec3{1, 1, 2})
		f.LeftHandRotation = tc.q
		if tc.known != nil {
			f.LeftHandRotationValid = tc.known
		}
		fe.UpdatePlayerState(ps, &f, mc)
		if ps.LeftWristAngularRateValid != tc.rateValid || ps.LeftWristAngularRate != 0 {
			t.Fatalf("frame%d: validity=%t rate=%g", i, ps.LeftWristAngularRateValid, ps.LeftWristAngularRate)
		}
		if tc.known != nil && !*tc.known && (ps.LeftHandRotationValid || ps.LeftHandRot.IsUnit()) {
			t.Fatal("explicit fallback identity entered state/history as an observation")
		}
	}
}

func TestExtractorOrientationLegacyUnknownRemainsUnknown(t *testing.T) {
	fe, ps, mc := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}, feTestCtx()
	for i := 0; i < 8; i++ {
		f := feFrame("p1", i, float64(i)*.02, model.Vec3{1, 1, 2})
		f.LeftHandRotationValid, f.RightHandRotationValid = nil, nil
		fe.UpdatePlayerState(ps, &f, mc)
		if ps.LeftWristAngularRateValid || ps.RightWristAngularRateValid || ps.LeftHandRot.IsUnit() || ps.RightHandRot.IsUnit() {
			t.Fatal("legacy unknown provenance became observed wrist data")
		}
	}
}

func TestExtractorOrientationDoesNotBridgeUntrustedTime(t *testing.T) {
	for _, badTime := range []float64{.02, .01} {
		fe, ps, mc := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}, feTestCtx()
		for i, at := range []float64{0, .02} {
			f := feFrame("p1", i, at, model.Vec3{1, 1, 2})
			fe.UpdatePlayerState(ps, &f, mc)
		}
		history := append([]model.Quat(nil), ps.LeftHandRotHistory...)
		bad := feFrame("p1", 2, badTime, model.Vec3{1, 1, 2})
		bad.LeftHandRotation = model.Quat{0, 1, 0, 0}
		fe.UpdatePlayerState(ps, &bad, mc)
		if ps.LastFrameIdx != 1 || ps.LastTimestamp != .02 || ps.FrameCount != 2 ||
			ps.LeftHandRot != model.QuatIdentity() || !reflect.DeepEqual(ps.LeftHandRotHistory, history) {
			t.Fatal("rejected duplicate/out-of-order sample rewound accepted wrist state/history")
		}
		good := feFrame("p1", 3, .04, model.Vec3{1, 1, 2})
		fe.UpdatePlayerState(ps, &good, mc)
		if !ps.LeftWristAngularRateValid || ps.LeftWristAngularRate != 0 {
			t.Fatal("rejected pose contaminated derivative between accepted samples")
		}
	}
	fe, ps, mc := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}, feTestCtx()
	for i, at := range []float64{0, .02, 2, 2.02, 2.04} {
		f := feFrame("p1", i, at, model.Vec3{1, 1, 2})
		if i == 2 {
			f.LeftHandRotation = model.Quat{0, 1, 0, 0}
		}
		fe.UpdatePlayerState(ps, &f, mc)
		if (i == 2 || i == 3) && ps.LeftWristAngularRateValid {
			t.Fatalf("gap seeded derivative on frame %d", i)
		}
		if i == 4 && (!ps.LeftWristAngularRateValid || ps.LeftWristAngularRate != 0) {
			t.Fatal("later valid observation pair did not recover")
		}
	}
}

func TestExtractorOrientationPreReleaseSnapshotPreservesObservedIdentity(t *testing.T) {
	fe, ps, mc := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}, feTestCtx()
	no := false
	for i := 0; i < 3; i++ {
		f := feFrame("p1", i, float64(i)*.02, model.Vec3{1, 1, 2})
		if i == 1 {
			f.LeftHandRotationValid = &no
		}
		fe.UpdatePlayerState(ps, &f, mc)
	}
	release := feFrame("p1", 3, .06, model.Vec3{1, 1, 2})
	snaps := fe.buildPreReleaseSnapshots(ps, &release, "left")
	if len(snaps) != 3 {
		t.Fatalf("snapshot count=%d", len(snaps))
	}
	for i, snap := range snaps {
		want := i != 1
		if snap.HandRotationValid != want || (want && snap.HandRotation != model.QuatIdentity()) || (!want && snap.HandRotation != (model.Quat{})) {
			t.Fatalf("snapshot %d lost explicit orientation validity: %+v", i, snap)
		}
		data, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		var restored model.ThrowFrameSnapshot
		if err := json.Unmarshal(data, &restored); err != nil || restored.HandRotationValid != want {
			t.Fatalf("snapshot validity JSON roundtrip: %v", err)
		}
	}
}

func TestValidatorOrientationNeverUpgradesUnknownOrMutatesValidityPointer(t *testing.T) {
	for _, observed := range []*bool{nil, func() *bool { v := false; return &v }(), func() *bool { v := true; return &v }()} {
		f := feFrame("p1", 1, .02, model.Vec3{1, 1, 2})
		f.LeftHandRotation, f.LeftHandRotationValid = model.Quat{0, 0, 0, .995}, observed
		_, err := NewFrameValidator(config.DefaultConfig()).Validate(&f, feTestCtx())
		if err != nil {
			t.Fatal(err)
		}
		if observed == nil || !*observed {
			if f.LeftHandRotation.IsUnit() {
				t.Fatal("unknown/fallback normalized to valid orientation")
			}
		} else if f.LeftHandRotation != model.QuatIdentity() {
			t.Fatalf("observed near-unit identity was not normalized: %v", f.LeftHandRotation)
		}
	}
	yes := true
	q, validity, _ := sanitizeObservedHandRotation(model.Quat{0, 0, 0, 2}, &yes)
	if !yes || validity == nil || *validity || q != (model.Quat{}) {
		t.Fatal("invalid norm upgraded or shared provenance pointer mutated")
	}
}
