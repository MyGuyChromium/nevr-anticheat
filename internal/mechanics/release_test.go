package mechanics

import (
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"math"
	"testing"
)

func releaseInputForTest() ReleaseInput {
	rules := model.DefaultProjectRules()
	return ReleaseInput{Rules: rules, Assessment: model.MechanicsAssessment{EventID: "release-test", FrameIndex: 10, Timestamp: 1, IntervalStart: .95, IntervalEnd: 1},
		InitialDiscSpeed: &SpeedBounds{19, 19}, ReleaseMovementSpeed: &SpeedBounds{4.6, 4.6},
		Knowledge: ReleaseKnowledge{ruleVersion: rules.Version, engineBuild: "synthetic-only", validationReference: "hypothetical-test-model", sourceValidated: true, simultaneousBoundsValidated: true}}
}

func TestReleaseRuleRequiresSimultaneousVerifiedBounds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		disc, move SpeedBounds
		want       string
	}{
		{"controlled contradiction", SpeedBounds{19, 19}, SpeedBounds{4.6, 4.69}, model.MechanicsValidatedViolation},
		{"inclusive movement", SpeedBounds{19, 20}, SpeedBounds{4.7, 4.7}, model.MechanicsConsistent},
		{"below trigger", SpeedBounds{18.9, 18.999}, SpeedBounds{0, 1}, model.MechanicsConsistent},
		{"uncertain launch", SpeedBounds{18.8, 19.2}, SpeedBounds{4, 4.6}, model.MechanicsInconclusive},
		{"uncertain movement", SpeedBounds{19, 19.2}, SpeedBounds{4.6, 4.8}, model.MechanicsInconclusive},
		{"nonfinite", SpeedBounds{19, math.Inf(1)}, SpeedBounds{1, 1}, model.MechanicsInconclusive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := releaseInputForTest()
			in.InitialDiscSpeed = &tc.disc
			in.ReleaseMovementSpeed = &tc.move
			got := EvaluateRelease(in)
			if got.Result != tc.want {
				t.Fatalf("%+v", got)
			}
			if err := got.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
	in := releaseInputForTest()
	in.Knowledge = ReleaseKnowledge{}
	in.Assessment.Authority = "authoritative" // producer labels cannot grant rule/source verification
	in.Assessment.VerifiedEngineBuild = "untrusted-wire-build"
	in.Assessment.ValidationReference = "untrusted-wire-reference"
	in.Assessment.GeometryDefinition = "untrusted-wire-geometry"
	if got := EvaluateRelease(in); got.Result != model.MechanicsInconclusive {
		t.Fatalf("wire trust escalated: %+v", got)
	} else if got.VerifiedEngineBuild != "" || got.ValidationReference != "" || got.GeometryDefinition != "" {
		t.Fatalf("unverified wire metadata survived: %+v", got)
	}
	in = releaseInputForTest()
	in.Knowledge.simultaneousBoundsValidated = false
	if got := EvaluateRelease(in); got.Result != model.MechanicsInconclusive {
		t.Fatalf("unbounded timing accepted: %+v", got)
	}
}

func TestReleaseMovementAtReleaseNotLaterSlowing(t *testing.T) {
	in := releaseInputForTest()
	in.ReleaseMovementSpeed = &SpeedBounds{4.8, 5}
	laterZero := model.Vec3{}
	in.Assessment.RawSamples = []model.MechanicsRawSample{{FrameIndex: 20, Timestamp: 2, ReportedVelocity: &laterZero}}
	if got := EvaluateRelease(in); got.Result != model.MechanicsConsistent {
		t.Fatalf("later movement replaced release bounds: %+v", got)
	}
}

func TestWristOffsetNotPhysicalRotationOrNonzeroCheat(t *testing.T) {
	base := releaseInputForTest().Assessment
	base.RuleVersion = model.DefaultProjectRules().Version
	base.VerifiedEngineBuild, base.ValidationReference, base.GeometryDefinition = "wire", "wire", "wire"
	value := 15.0
	known := SettingsKnowledge{ruleVersion: base.RuleVersion, engineBuild: "synthetic", validationReference: "synthetic-range-not-Echo", sourceValidated: true, minimum: -20, maximum: 20}
	if got := EvaluateWristSetting(base, &value, known); got.Result != model.MechanicsConsistent {
		t.Fatalf("permitted nonzero: %+v", got)
	}
	if got := EvaluateWristSetting(base, &value, SettingsKnowledge{}); got.Result != model.MechanicsInconclusive {
		t.Fatal(got)
	} else if got.VerifiedEngineBuild != "" || got.ValidationReference != "" || got.GeometryDefinition != "" {
		t.Fatalf("unverified wire metadata survived: %+v", got)
	}
	if got := EvaluateWristSetting(base, nil, known); got.Result != model.MechanicsInconclusive {
		t.Fatal(got)
	}
	value = 21
	if got := EvaluateWristSetting(base, &value, known); got.Result != model.MechanicsValidatedViolation {
		t.Fatal(got)
	}
}
