package pipeline

import (
	"math"
	"slices"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestExtractorHeadObservationClearsAndCopies(t *testing.T) {
	fe := NewFeatureExtractor(30)
	ps := &model.PlayerState{PlayerID: "p1"}
	mc := feTestCtx()
	for i, missing := range []*model.Vec3{nil, vecPtr(model.Vec3{}), vecPtr(model.Vec3{math.NaN(), 1, 2}), vecPtr(model.Vec3{1, math.Inf(1), 2})} {
		frame := feFrame("p1", i*2, float64(i)*0.134, model.Vec3{1, 1.6, 2})
		frame.HeadPosition = vecPtr(model.Vec3{1, 1.8, 2})
		fe.UpdatePlayerState(ps, &frame, mc)
		if ps.HeadPosition == nil || *ps.HeadPosition != *frame.HeadPosition {
			t.Fatalf("explicit head lost: %+v", ps.HeadPosition)
		}
		(*frame.HeadPosition)[0] = 8
		if (*ps.HeadPosition)[0] != 1 {
			t.Fatal("player state aliases input head pointer")
		}
		frame.FrameIndex++
		frame.Timestamp += 0.067
		frame.HeadPosition = missing
		fe.UpdatePlayerState(ps, &frame, mc)
		if ps.HeadPosition != nil {
			t.Fatalf("missing/invalid head retained a stale or body-substitute pose: %v", ps.HeadPosition)
		}
	}
}

func TestValidatorOptionalHeadObservation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		head      *model.Vec3
		known     bool
		sanitized bool
	}{
		{name: "legacy absent"},
		{name: "explicit tracked", head: vecPtr(model.Vec3{1, 1.8, 2}), known: true},
		{name: "zero tracking loss", head: vecPtr(model.Vec3{}), sanitized: true},
		{name: "nan", head: vecPtr(model.Vec3{1, math.NaN(), 2}), sanitized: true},
		{name: "infinity", head: vecPtr(model.Vec3{math.Inf(-1), 1.8, 2}), sanitized: true},
		{name: "outside arena", head: vecPtr(model.Vec3{10000, 1.8, 2}), sanitized: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := cleanFrame("P1", 0)
			frame.HeadPosition = tc.head
			codes, err := NewFrameValidator(testConfig("enforce")).Validate(&frame, matchCtx("P1"))
			if err != nil {
				t.Fatal(err)
			}
			if (frame.HeadPosition != nil) != tc.known || slices.Contains(codes, SanitizedHeadPosition) != tc.sanitized {
				t.Fatalf("head=%v codes=%v", frame.HeadPosition, codes)
			}
		})
	}
}

func TestValidatorOptionalDiscObservation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		bounce     *int
		count      int
		known      bool
		wantBounce bool
		wantKnown  bool
		wantCount  int
		sanitized  bool
	}{
		{name: "legacy absent"},
		{name: "observed zero", bounce: pipelineObservationInt(0), count: 2, known: true, wantBounce: true, wantKnown: true, wantCount: 2},
		{name: "observed positive", bounce: pipelineObservationInt(9), count: 2, known: true, wantBounce: true, wantKnown: true, wantCount: 2},
		{name: "negative bounce", bounce: pipelineObservationInt(-1), count: 2, known: true, wantKnown: true, wantCount: 2, sanitized: true},
		{name: "negative count", count: -1, known: true, sanitized: true},
		{name: "known with no sampled players", known: true, sanitized: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := cleanFrame("P1", 0)
			disc := &model.DiscState{Position: frame.Position, Velocity: model.Vec3{1, 0, 0}, Speed: 1,
				IsHeld: true, PossessorID: "P1", BounceCount: tc.bounce, SampledPlayerCount: tc.count,
				PossessionKnown: tc.known, PossessionConflict: true}
			frame.Disc = disc
			codes, err := NewFrameValidator(testConfig("enforce")).Validate(&frame, matchCtx("P1"))
			if err != nil {
				t.Fatal(err)
			}
			d := frame.Disc
			if d == nil || (d.BounceCount != nil) != tc.wantBounce || d.PossessionKnown != tc.wantKnown ||
				d.SampledPlayerCount != tc.wantCount || !d.PossessionConflict || !d.IsHeld || d.PossessorID != "P1" || d.Velocity != disc.Velocity {
				t.Fatalf("optional repair lost knowledge distinction or changed legacy disc/holder: %+v", d)
			}
			if slices.Contains(codes, SanitizedDiscObservation) != tc.sanitized {
				t.Fatalf("codes=%v", codes)
			}
			if disc.BounceCount != tc.bounce || disc.SampledPlayerCount != tc.count || disc.PossessionKnown != tc.known {
				t.Fatal("repair mutated a disc shared with another frame")
			}
		})
	}
}

func pipelineObservationInt(v int) *int { return &v }
