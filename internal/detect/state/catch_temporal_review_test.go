package state

import (
	"fmt"
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// catchReviewBriefTurn samples one fixed analytic flight, with an 80ms turn
// offset from the coarse sampling grid. A rate changes only observation times:
// the path, angular velocity, burst duration, receiver, and catch location stay
// fixed. This is an adversarial sampling regression, not game-physics truth.
func catchReviewBriefTurn(rate int, jitter bool) []map[string]*model.PlayerState {
	const start, stop, speed = .45, .53, 10.0
	const omega = 300 * math.Pi / 180
	positionAt := func(tm float64) (model.Vec3, model.Vec3) {
		if tm <= start {
			return model.Vec3{3 + speed*tm, 2, 1}, model.Vec3{speed, 0, 0}
		}
		angle := omega * (math.Min(tm, stop) - start)
		position := model.Vec3{3 + speed*start + speed*math.Sin(angle)/omega, 2 + speed*(1-math.Cos(angle))/omega, 1}
		velocity := model.Vec3{speed * math.Cos(angle), speed * math.Sin(angle), 0}
		if tm > stop {
			position = position.Add(velocity.Scale(tm - stop))
		}
		return position, velocity
	}
	last, velocity := positionAt(1)
	hand := last.Add(velocity.Normalized().Scale(.9))
	ticks := make([]map[string]*model.PlayerState, 0, rate+3)
	for i := 0; i < rate+3; i++ {
		tm := float64(i) / float64(rate)
		if jitter && i > 0 && i < rate {
			tm += .1 / float64(rate) * math.Sin(float64(i)*.73)
		}
		position, velocity := positionAt(tm)
		held := i > rate
		holder := ""
		if held {
			position, velocity, holder = hand, model.Vec3{}, "receiver"
		}
		players := make(map[string]*model.PlayerState)
		for _, id := range []string{"receiver", "other"} {
			left := hand
			if id == "other" {
				left = model.Vec3{3, 2, 20}
			}
			head, bounce := left.Add(model.Vec3{0, 1.2, 0}), 0
			players[id] = &model.PlayerState{PlayerID: id, LastFrameIdx: i + 1, FrameCount: i + 1, LastTimestamp: 10 + tm,
				Position: left.Add(model.Vec3{0, 1, 0}), HeadPosition: &head,
				LeftHand: left, RightHand: left.Add(model.Vec3{0, 0, .5}), HasDisc: held && id == "receiver",
				CurrentDisc: &model.DiscState{Position: position, Velocity: velocity, Speed: velocity.Magnitude(), PossessorID: holder,
					IsHeld: held, PossessionKnown: true, SampledPlayerCount: 2, BounceCount: &bounce}}
		}
		ticks = append(ticks, players)
	}
	return ticks
}

func TestState008TemporalReviewPhaseShiftedBriefTurnNoObservation(t *testing.T) {
	for _, rate := range []int{10, 15, 20, 30, 60, 120} {
		for _, jitter := range []bool{false, true} {
			t.Run(fmt.Sprintf("hz%d_jitter%t", rate, jitter), func(t *testing.T) {
				d := NewState008(nil)
				var reasons []string
				d.SetDecisionObserver(func(_, _ string, _ int, reason string) { reasons = append(reasons, reason) })
				events := catchRun(d, catchReviewBriefTurn(rate, jitter))
				if len(events) != 0 {
					metrics := events[0].Evidence.(model.StateEvidence).Metrics
					t.Fatalf("same 80ms burst must not satisfy 120ms sustained-turn filter: duration=%g samples=%g reasons=%v",
						metrics["correction_duration_s"], metrics["correction_samples"], reasons)
				}
			})
		}
	}
}

func TestState008TemporalReviewContiguousFrameLabelsDoNotHideTimeGap(t *testing.T) {
	for _, rate := range []int{10, 15, 20, 30, 60, 120} {
		t.Run(fmt.Sprintf("hz%d", rate), func(t *testing.T) {
			var retained []map[string]*model.PlayerState
			for _, players := range catchAnalyticFlight(rate, false, 100, 1) {
				tm := players["receiver"].LastTimestamp - 10
				if tm > .45 && tm < .65 {
					continue
				}
				// Ingestion may assign contiguous indices to surviving unique ticks.
				// Keep the actual timestamps/path; only frame labels are renumbered.
				for _, p := range players {
					p.LastFrameIdx, p.FrameCount = len(retained)+1, len(retained)+1
				}
				retained = append(retained, players)
			}
			d, gap := NewState008(nil), false
			d.SetDecisionObserver(func(_, _ string, _ int, reason string) { gap = gap || reason == "catch_sample_gap" })
			if events := catchRun(d, retained); len(events) != 0 || !gap {
				t.Fatalf("sample-time gap was bridged despite insufficient continuity: events=%d gap=%t", len(events), gap)
			}
		})
	}
}

func TestState008TemporalReviewUncertainContactCannotBecomeClear(t *testing.T) {
	for _, rate := range []int{10, 15, 20, 30, 60, 120} {
		t.Run(fmt.Sprintf("hz%d", rate), func(t *testing.T) {
			ticks := catchAnalyticFlight(rate, false, 100, 1)
			for _, players := range ticks {
				p := players["other"]
				// Finite, stationary tracking passes continuity, but the oversized
				// head/body geometry cannot support a contact-free conclusion.
				head := p.Position.Add(model.Vec3{0, 3.1, 0})
				p.HeadPosition = &head
			}
			d, uncertain := NewState008(nil), false
			d.SetDecisionObserver(func(_, _ string, _ int, reason string) { uncertain = uncertain || reason == "catch_contact_uncertain" })
			if events := catchRun(d, ticks); len(events) != 0 || !uncertain {
				t.Fatalf("unknown contact geometry used as clear: events=%d uncertain=%t", len(events), uncertain)
			}
		})
	}
}
