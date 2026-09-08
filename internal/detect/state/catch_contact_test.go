package state

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func catchContactTestPair() (catchSample, catchSample) {
	pose := catchPose{id: "tracked", body: model.Vec3{30, 30, 30}, head: model.Vec3{30, 31, 30},
		left: model.Vec3{30, 20, 20}, right: model.Vec3{30, 20, 21}}
	a := catchSample{frame: 1, timestamp: 10, position: model.Vec3{10, 10, 10}, poses: []catchPose{pose}}
	b := catchSample{frame: 2, timestamp: 10.1, position: a.position, poses: []catchPose{pose}}
	return a, b
}

func TestCatchContactEnvelopePointSweepsBetweenEndpoints(t *testing.T) {
	for _, part := range []string{catchContactHand, catchContactHead, catchContactBody} {
		t.Run(part, func(t *testing.T) {
			a, b := catchContactTestPair()
			start, end := model.Vec3{8, 10, 10}, model.Vec3{12, 10, 10}
			switch part {
			case catchContactHand:
				a.poses[0].left, b.poses[0].left = start, end
			case catchContactHead:
				a.poses[0].head, b.poses[0].head = start, end
				a.poses[0].body, b.poses[0].body = start.Add(model.Vec3{0, 1, 0}), end.Add(model.Vec3{0, 1, 0})
			case catchContactBody:
				a.poses[0].body, b.poses[0].body = start, end
				a.poses[0].head, b.poses[0].head = start.Add(model.Vec3{0, 1, 0}), end.Add(model.Vec3{0, 1, 0})
			}
			if a.position.Distance(start) <= .65 || b.position.Distance(end) <= .65 {
				t.Fatal("fixture must miss both endpoints")
			}
			got := catchContactEnvelope(a, b, .65, 0)
			if !got.Possible || got.Kind != part || got.ClosestDistance != 0 {
				t.Fatalf("between-endpoint %s excluded incorrectly: %+v", part, got)
			}
		})
	}
}

func TestCatchContactEnvelopeExpandsWithInterval(t *testing.T) {
	a, b := catchContactTestPair()
	a.poses[0].left, b.poses[0].left = model.Vec3{10, 10.67, 10}, model.Vec3{10, 10.67, 10}
	without := catchContactEnvelope(a, b, .65, 0)
	with := catchContactEnvelope(a, b, .65, 30)
	if without.Possible || !with.Possible || with.Kind != catchContactHand {
		t.Fatalf("near-contact should be excluded only by allowance: without=%+v with=%+v", without, with)
	}
	want := 30 * math.Pow(b.timestamp-a.timestamp, 2) / 8
	if math.Abs(with.Uncertainty-want) > 1e-12 || math.Abs(with.Margin-(.65+want)) > 1e-12 {
		t.Fatalf("wrong provisional interpolation expansion: %+v, want uncertainty %.12g", with, want)
	}
	b.timestamp = a.timestamp + .2
	longer := catchContactEnvelope(a, b, .65, 30)
	if longer.Margin <= with.Margin || longer.Uncertainty <= with.Uncertainty {
		t.Fatalf("longer interval narrowed uncertainty: short=%+v long=%+v", with, longer)
	}
}

func TestCatchContactEnvelopeContainsMovingTorso(t *testing.T) {
	for _, tc := range []struct {
		name         string
		body0, head0 model.Vec3
		body1, head1 model.Vec3
		dt           float64
	}{
		{"static interior", model.Vec3{10, 9, 10}, model.Vec3{10, 11, 10}, model.Vec3{10, 9, 10}, model.Vec3{10, 11, 10}, .1},
		{"fast between endpoints", model.Vec3{0, 9, 10}, model.Vec3{0, 11, 10}, model.Vec3{20, 9, 10}, model.Vec3{20, 11, 10}, .02},
		{"rotating segment", model.Vec3{8.5, 10, 10}, model.Vec3{11.5, 10, 10}, model.Vec3{10, 8.5, 10}, model.Vec3{10, 11.5, 10}, .1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := catchContactTestPair()
			a.poses[0].body, a.poses[0].head = tc.body0, tc.head0
			b.poses[0].body, b.poses[0].head = tc.body1, tc.head1
			b.timestamp = a.timestamp + tc.dt
			if catchRelativeSweep(a.position, b.position, tc.body0, tc.body1) <= .65 ||
				catchRelativeSweep(a.position, b.position, tc.head0, tc.head1) <= .65 {
				t.Fatal("fixture should evade the previous head/body point-only gate")
			}
			got := catchContactEnvelope(a, b, .65, 0)
			if !got.Possible || got.Kind != catchContactTorsoEnvelope || got.ClosestDistance != 0 {
				t.Fatalf("moving torso interior missed: %+v", got)
			}
		})
	}
}

func TestCatchContactEnvelopeContainsInteriorAtArbitraryInterpolationTime(t *testing.T) {
	// Regression witnesses at irregular times supplement the continuous enclosure
	// proof in the implementation. The production helper does NOT sample time.
	for _, at := range []float64{.001, .19, .503, .937, .999} {
		for _, along := range []float64{.1, .3, .5, .9} {
			a, b := catchContactTestPair()
			a.poses[0].body, a.poses[0].head = model.Vec3{5, 9, 10}, model.Vec3{5, 11, 10}
			b.poses[0].body, b.poses[0].head = model.Vec3{14, 10, 9}, model.Vec3{16, 10, 11}
			body := a.poses[0].body.Scale(1 - at).Add(b.poses[0].body.Scale(at))
			head := a.poses[0].head.Scale(1 - at).Add(b.poses[0].head.Scale(at))
			a.position = body.Scale(1 - along).Add(head.Scale(along))
			b.position = a.position
			got := catchContactEnvelope(a, b, 0, 0)
			if !got.Possible || got.ClosestDistance != 0 || got.Kind == catchContactUncertainGeometry {
				t.Fatalf("interior at t=%g u=%g missed: %+v", at, along, got)
			}
		}
	}
}

func TestCatchContactEnvelopeCanConservativelyOverExclude(t *testing.T) {
	a, b := catchContactTestPair()
	a.poses[0].body, a.poses[0].head = model.Vec3{8.8, 8.5, 10}, model.Vec3{8.8, 11.5, 10}
	b.poses[0] = a.poses[0]
	got := catchContactEnvelope(a, b, .65, 0)
	// Actual distance to this static torso segment is 1.2m, outside the margin.
	// Its containing sphere deliberately gives a zero conservative lower bound.
	if !got.Possible || got.Kind != catchContactTorsoEnvelope || got.ClosestDistance != 0 {
		t.Fatalf("must preserve the conservative enclosing volume: %+v", got)
	}
}

func TestCatchContactEnvelopeKeepsSameTimeMotion(t *testing.T) {
	a, b := catchContactTestPair()
	a.position, b.position = model.Vec3{1, 10, 10}, model.Vec3{20, 10, 10}
	a.poses[0].left, b.poses[0].left = model.Vec3{11, 10, 10}, model.Vec3{30, 10, 10}
	got := catchContactEnvelope(a, b, .65, 0)
	if got.Possible || got.Kind != catchContactClear || math.Abs(got.ClosestDistance-(10-catchContactDistanceSlack)) > 1e-9 {
		t.Fatalf("overlapping spatial paths at different times should remain clear: %+v", got)
	}
}

func TestCatchContactEnvelopeNeverNarrowsOriginalMargin(t *testing.T) {
	for _, margin := range []float64{0, .5, .65, 2} {
		for _, dt := range []float64{.02, .05, 1.0 / 15, .12, .2} {
			for _, acceleration := range []float64{0, 30, 200} {
				a, b := catchContactTestPair()
				b.timestamp = a.timestamp + dt
				a.poses[0].left = a.position.Add(model.Vec3{0, margin, 0})
				b.poses[0].left = b.position.Add(model.Vec3{0, margin, 0})
				got := catchContactEnvelope(a, b, margin, acceleration)
				if !got.Possible || got.Margin < margin || got.Uncertainty < 0 || got.Kind == catchContactUncertainGeometry {
					t.Fatalf("legacy boundary narrowed for margin=%g dt=%g acceleration=%g: %+v", margin, dt, acceleration, got)
				}
			}
		}
	}
}

func TestCatchContactEnvelopeIncludesTinyRelativeMotion(t *testing.T) {
	a, b := catchContactTestPair()
	// This motion is small enough that catchRelativeSweep retains its initial
	// endpoint. The conservative witness slack must still include the contact.
	a.poses[0].left = a.position.Add(model.Vec3{5e-7, 0, 0})
	b.poses[0].left = b.position.Add(model.Vec3{-5e-7, 0, 0})
	got := catchContactEnvelope(a, b, 0, 0)
	if !got.Possible || got.Kind != catchContactHand || got.ClosestDistance != 0 || got.Margin != 0 {
		t.Fatalf("near-stationary projection fallback missed contact: %+v", got)
	}
}

func TestCatchContactEnvelopeAllowsDegenerateTrackedTorso(t *testing.T) {
	a, b := catchContactTestPair()
	a.poses[0].head, b.poses[0].head = a.poses[0].body, b.poses[0].body
	got := catchContactEnvelope(a, b, .65, 30)
	if got.Possible || got.Kind != catchContactClear {
		t.Fatalf("finite coincident tracked head/body should reduce to a point: %+v", got)
	}
}

func TestCatchContactEnvelopeUncertainGeometryAlwaysAbstains(t *testing.T) {
	tests := []struct {
		name string
		edit func(*catchSample, *catchSample, *float64, *float64)
	}{
		{"empty roster", func(a, b *catchSample, _, _ *float64) { a.poses, b.poses = nil, nil }},
		{"changed roster", func(_, b *catchSample, _, _ *float64) { b.poses[0].id = "other" }},
		{"missing player", func(_, b *catchSample, _, _ *float64) { b.poses = nil }},
		{"empty identity", func(a, b *catchSample, _, _ *float64) { a.poses[0].id, b.poses[0].id = "", "" }},
		{"duplicate identity", func(a, b *catchSample, _, _ *float64) {
			a.poses = append(a.poses, a.poses[0])
			b.poses = append(b.poses, b.poses[0])
		}},
		{"missing head", func(a, _ *catchSample, _, _ *float64) { a.poses[0].head = model.Vec3{} }},
		{"missing hand", func(_, b *catchSample, _, _ *float64) { b.poses[0].right = model.Vec3{} }},
		{"nan disc", func(a, _ *catchSample, _, _ *float64) { a.position[0] = math.NaN() }},
		{"infinite body", func(_, b *catchSample, _, _ *float64) { b.poses[0].body[1] = math.Inf(1) }},
		{"numerically unresolvable coordinates", func(a, _ *catchSample, _, _ *float64) { a.position[0] = math.MaxFloat64 }},
		{"implausible torso engineering guard", func(a, _ *catchSample, _, _ *float64) { a.poses[0].head = a.poses[0].body.Add(model.Vec3{0, 3.001, 0}) }},
		{"nonincreasing time", func(a, b *catchSample, _, _ *float64) { b.timestamp = a.timestamp }},
		{"nan time", func(a, _ *catchSample, _, _ *float64) { a.timestamp = math.NaN() }},
		{"negative time", func(a, _ *catchSample, _, _ *float64) { a.timestamp = -1 }},
		{"negative margin", func(_, _ *catchSample, margin, _ *float64) { *margin = -.1 }},
		{"infinite margin", func(_, _ *catchSample, margin, _ *float64) { *margin = math.Inf(1) }},
		{"nan acceleration", func(_, _ *catchSample, _, acceleration *float64) { *acceleration = math.NaN() }},
		{"negative acceleration", func(_, _ *catchSample, _, acceleration *float64) { *acceleration = -1 }},
		{"out of policy acceleration", func(_, _ *catchSample, _, acceleration *float64) { *acceleration = 201 }},
		{"overflowed allowance", func(_, b *catchSample, _, _ *float64) { b.timestamp = math.MaxFloat64 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := catchContactTestPair()
			margin, acceleration := .65, 30.0
			tc.edit(&a, &b, &margin, &acceleration)
			got := catchContactEnvelope(a, b, margin, acceleration)
			if !got.Possible || got.Kind != catchContactUncertainGeometry {
				t.Fatalf("bad geometry treated as contact-free: %+v", got)
			}
			if _, err := json.Marshal(got); err != nil {
				t.Fatalf("nonfinite diagnostic evidence: %+v: %v", got, err)
			}
		})
	}
}

func TestCatchContactEnvelopeRigidTransformInvariant(t *testing.T) {
	for _, near := range []bool{false, true} {
		a, b := catchContactTestPair()
		a.position, b.position = model.Vec3{8, 10, 10}, model.Vec3{12, 10, 10}
		if near {
			a.poses[0].body, a.poses[0].head = model.Vec3{10, 9, 10}, model.Vec3{10, 11, 10}
			b.poses[0].body, b.poses[0].head = model.Vec3{10.2, 9, 10}, model.Vec3{10.2, 11, 10}
		}
		want := catchContactEnvelope(a, b, .65, 30)
		for _, angle := range []float64{.1, 1, 2.3, math.Pi} {
			transform := func(v model.Vec3) model.Vec3 {
				// Orthogonal rotation in two planes followed by translation.
				s, c := math.Sincos(angle)
				x, y, z := c*v[0]-s*v[1], s*v[0]+c*v[1], v[2]
				return model.Vec3{x + 77, c*y - s*z - 21, s*y + c*z + 39}
			}
			move := func(s catchSample) catchSample {
				s.position = transform(s.position)
				s.poses = append([]catchPose(nil), s.poses...)
				for i := range s.poses {
					p := &s.poses[i]
					p.body, p.head, p.left, p.right = transform(p.body), transform(p.head), transform(p.left), transform(p.right)
				}
				return s
			}
			got := catchContactEnvelope(move(a), move(b), .65, 30)
			if got.Possible != want.Possible || got.Kind != want.Kind || got.Margin != want.Margin ||
				got.Uncertainty != want.Uncertainty || math.Abs(got.ClosestDistance-want.ClosestDistance) > 1e-10 {
				t.Fatalf("rigid transform changed geometry: near=%v angle=%g want=%+v got=%+v", near, angle, want, got)
			}
		}
	}
}
