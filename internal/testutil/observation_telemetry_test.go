package testutil

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func observedFixtureFrame(pid string, x float64) model.PlayerTelemetryFrame {
	head := model.Vec3{x, 1.8, 2}
	bounce := 0
	return model.PlayerTelemetryFrame{PlayerID: pid, Position: model.Vec3{x, 1.6, 2}, HeadPosition: &head,
		Disc: &model.DiscState{Position: model.Vec3{x, 1.6, 2}, BounceCount: &bounce}}
}

func TestSyntheticCompositionPreservesObservationGeometryAndOwnership(t *testing.T) {
	first := []model.PlayerTelemetryFrame{observedFixtureFrame("P1", 1)}
	second := []model.PlayerTelemetryFrame{observedFixtureFrame("P1", 4)}
	joined := Concat(first, second)
	if len(joined) != 2 || *joined[1].HeadPosition != (model.Vec3{1, 1.8, 2}) {
		t.Fatalf("head did not translate with body: %+v", joined)
	}
	*joined[0].HeadPosition = model.Vec3{2, 3, 4}
	*joined[1].Disc.BounceCount = 10
	if *first[0].HeadPosition != (model.Vec3{1, 1.8, 2}) || *second[0].HeadPosition != (model.Vec3{4, 1.8, 2}) || *second[0].Disc.BounceCount != 0 {
		t.Fatal("composition result aliases source observations")
	}
	originalHead := second[0].HeadPosition
	ShiftFrom(second, 0, model.Vec3{1, 0, 0})
	if *second[0].HeadPosition != (model.Vec3{5, 1.8, 2}) || *originalHead != (model.Vec3{4, 1.8, 2}) {
		t.Fatal("shift did not translate and own the head observation")
	}
}

func TestSyntheticSharedDiscCopiesOptionalObservations(t *testing.T) {
	a := []model.PlayerTelemetryFrame{observedFixtureFrame("P1", 1)}
	b := []model.PlayerTelemetryFrame{observedFixtureFrame("P2", 2)}
	_, frames := NewMatchBuilder("optional").AddPlayer("blue", a).AddPlayer("orange", b).WithSharedDisc("P1").Build()
	*frames[0].Disc.BounceCount = 8
	(*frames[0].HeadPosition)[0] = 9
	if *frames[1].Disc.BounceCount != 0 || *a[0].Disc.BounceCount != 0 || (*a[0].HeadPosition)[0] != 1 {
		t.Fatal("match result aliases source or other player's observations")
	}
}

func TestSyntheticCompositionDoesNotInventUnknownHeadOrBounce(t *testing.T) {
	for _, head := range []*model.Vec3{nil, new(model.Vec3)} {
		frame := model.PlayerTelemetryFrame{PlayerID: "P1", Position: model.Vec3{4, 1.6, 2}, HeadPosition: head, Disc: &model.DiscState{}}
		frames := Concat([]model.PlayerTelemetryFrame{observedFixtureFrame("P1", 1)}, []model.PlayerTelemetryFrame{frame})
		ShiftFrom(frames, 1, model.Vec3{1, 0, 0})
		if (head == nil && frames[1].HeadPosition != nil) || (head != nil && !frames[1].HeadPosition.IsZero()) || frames[1].Disc.BounceCount != nil {
			t.Fatal("synthetic translation invented missing head/bounce observations")
		}
	}
}
