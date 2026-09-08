package mechanics

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func verifiedGrabFixture(distance, uncertainty float64) GrabInput {
	rules := model.DefaultProjectRules()
	return GrabInput{Rules: rules,
		Assessment: model.MechanicsAssessment{EventID: "synthetic-only", FrameIndex: 1, Timestamp: 1, IntervalStart: 1, IntervalEnd: 1},
		Knowledge: GrabKnowledge{ruleVersion: rules.Version, engineBuild: "synthetic-not-echo", validationReference: "hypothetical-boundary-fixture",
			geometry: "hand_origin_to_disc_center", sourceValidated: true, boundsValidated: true, linearMotionValidated: true},
		Candidates: []GrabCandidate{{Hand: "right", UncertaintyM: uncertainty, Samples: []GrabSample{{Timestamp: 1, DiscPosition: model.Vec3{distance, 0, 0}, Attachment: "free"}}}},
	}
}

func TestGrabExactInclusiveBoundaryWithHypotheticalVerifiedGeometry(t *testing.T) {
	for _, tc := range []struct {
		distance float64
		result   string
	}{{.249, model.MechanicsConsistent}, {.250, model.MechanicsConsistent}, {.251, model.MechanicsValidatedViolation}} {
		out := EvaluateGrab(verifiedGrabFixture(tc.distance, 0))
		if out.Result != tc.result || out.Metrics["legal_limit_m"] != .25 {
			t.Fatalf("distance %g: %+v", tc.distance, out)
		}
		if err := out.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGrabUncertaintyDoesNotExpandLegalBoundary(t *testing.T) {
	out := EvaluateGrab(verifiedGrabFixture(.28, .04))
	if out.Result != model.MechanicsInconclusive || out.Reason != "grab_boundary_uncertain" || out.Metrics["legal_limit_m"] != .25 {
		t.Fatalf("uncertain .28 m: %+v", out)
	}
	if out.Metrics["minimum_distance_lower_bound_m"] >= .25 {
		t.Fatal("fixture must straddle the rule")
	}
}

func TestGrabEveryCandidateMustExcludeLegalReach(t *testing.T) {
	in := verifiedGrabFixture(.9, 0)
	left := verifiedGrabFixture(.24, 0).Candidates[0]
	left.Hand = "left"
	in.Candidates = append(in.Candidates, left)
	if out := EvaluateGrab(in); out.Result != model.MechanicsConsistent {
		t.Fatalf("plausible left hand excluded: %+v", out)
	}
	in.Candidates[1].Samples = nil
	if out := EvaluateGrab(in); out.Result != model.MechanicsInconclusive {
		t.Fatalf("missing candidate became violation: %+v", out)
	}
}

func TestGrabFreeFreeCrossingAndNoAttachmentInterpolation(t *testing.T) {
	in := verifiedGrabFixture(1, 0)
	in.Assessment.IntervalStart, in.Assessment.IntervalEnd = 1, 2
	in.Candidates[0].Samples = []GrabSample{{Timestamp: 1, DiscPosition: model.Vec3{-1, 0, 0}, Attachment: "free"}, {Timestamp: 2, DiscPosition: model.Vec3{1, 0, 0}, Attachment: "free"}}
	if out := EvaluateGrab(in); out.Result != model.MechanicsConsistent || out.Metrics["minimum_distance_upper_bound_m"] != 0 {
		t.Fatalf("missed crossing: %+v", out)
	}
	in.Knowledge.linearMotionValidated = false
	if out := EvaluateGrab(in); out.Reason != "grab_interpolation_unverified" {
		t.Fatalf("unverified interpolation: %+v", out)
	}
	in.Knowledge.linearMotionValidated = true
	in.Candidates[0].Samples[1].Attachment = "held"
	if out := EvaluateGrab(in); out.Result != model.MechanicsInconclusive || out.Reason != "grab_attachment_snap_excluded" {
		t.Fatalf("interpolated snap: %+v", out)
	}
}

func TestGrabWireAuthorityCannotGrantKnowledge(t *testing.T) {
	in := verifiedGrabFixture(10, 0)
	in.Knowledge = GrabKnowledge{}
	in.Assessment.Source, in.Assessment.Authority = "server", "authoritative"
	in.Assessment.VerifiedEngineBuild, in.Assessment.ValidationReference, in.Assessment.GeometryDefinition = "wire", "wire", "wire"
	if out := EvaluateGrab(in); out.Result != model.MechanicsInconclusive || out.Reason != "grab_geometry_unverified" {
		t.Fatalf("metadata granted trust: %+v", out)
	} else if out.VerifiedEngineBuild != "" || out.ValidationReference != "" || out.GeometryDefinition != "" {
		t.Fatalf("unverified wire metadata survived: %+v", out)
	}
	in = verifiedGrabFixture(10, 0)
	in.Knowledge.sourceValidated = false
	if out := EvaluateGrab(in); out.Reason != "grab_source_unverified" {
		t.Fatalf("source not gated: %+v", out)
	}
	in.Knowledge.sourceValidated, in.Knowledge.boundsValidated = true, false
	if out := EvaluateGrab(in); out.Reason != "grab_uncertainty_unbounded" {
		t.Fatalf("bounds not gated: %+v", out)
	}
}

func TestGrabMissingGapNonfiniteCandidatesAbstain(t *testing.T) {
	for _, mutate := range []func(*GrabInput){
		func(in *GrabInput) { in.Candidates = nil },
		func(in *GrabInput) { in.Assessment.IntervalEnd = 2 },
		func(in *GrabInput) { in.Candidates[0].UncertaintyM = math.NaN() },
		func(in *GrabInput) { in.Candidates[0].Samples[0].DiscPosition[0] = math.Inf(1) },
		func(in *GrabInput) { in.Candidates[0].Samples[0].Timestamp = 0 },
	} {
		in := verifiedGrabFixture(10, 0)
		mutate(&in)
		if out := EvaluateGrab(in); out.Result != model.MechanicsInconclusive {
			t.Fatalf("bad input became violation: %+v", out)
		}
	}
}
