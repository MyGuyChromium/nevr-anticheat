package pipeline

import (
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// A disc knocked out of a stunned holder's hand is not that player's throw.
// The stun may be sampled on the last held, the first free or (flag lag) the
// confirming sample. Mutations: removing the `wasStunned || frame.IsStunned`
// branch publishes the 19 m/s knock-out for the first two cases; removing the
// `frame.IsStunned` case in confirmRelease publishes it for the third.
func TestReleaseFromStunnedHolderIsForcedDropNotThrow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stunFrame int // -1: never stunned (control)
		want      []string
		published bool
	}{
		{"control_unstunned", -1, nil, true},
		{"stunned_on_last_held", 2, []string{"release_forced_drop"}, false},
		{"stunned_on_first_free", 3, []string{"release_forced_drop"}, false},
		{"stunned_on_confirmation", 4, []string{"release_forced_drop"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fe, ps := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}
			var reasons []string
			fe.SetReleaseObserver(func(pid string, o model.ReleaseObservation, reason string) {
				if pid != "p1" || o.FirstFreeFrame != 3 || o.StartFrame != 2 {
					t.Errorf("wrong diagnostic identity: %s %+v", pid, o)
				}
				reasons = append(reasons, reason)
			})
			for i := 0; i <= 5; i++ {
				attachment := "right"
				if i >= 3 {
					attachment = "free"
				}
				f := releaseFrame(i, attachment)
				f.IsStunned = i == tc.stunFrame
				fe.UpdatePlayerState(ps, &f, feTestCtx())
			}
			fe.DrainPendingReleases("eof")
			if !reflect.DeepEqual(reasons, tc.want) {
				t.Fatalf("reasons=%v, want %v", reasons, tc.want)
			}
			if published := ps.LastThrow != nil; published != tc.published || (ps.ThrowCount == 1) != tc.published || (len(ps.ThrowHistory) == 1) != tc.published {
				t.Fatalf("published=%v count=%d history=%d, want published=%v", published, ps.ThrowCount, len(ps.ThrowHistory), tc.published)
			}
			if tc.published && ps.LastThrow.ReleaseSpeed != 19 {
				t.Fatalf("control release changed: %+v", ps.LastThrow)
			}
		})
	}
}

// Sources with a separately tracked head (.tape) must measure the head-contact
// envelope from the head, not the torso. Here the body is 1 m below the disc
// and the head is 0.1 m from it, both controllers 0.7 m away. Mutation:
// measuring from ps.Position / the previous body position again gives
// HeadToDiscDistance 1.0 and no PossibleHeadContact.
func TestExtractor_HeadContactUsesTrackedHeadNotBody(t *testing.T) {
	run := func(withHead bool) *model.ThrowEvent {
		fe, mc, ps := NewFeatureExtractor(30), feTestCtx(), &model.PlayerState{PlayerID: "p1"}
		body := model.Vec3{1, 1, 0}
		head := body.Add(model.Vec3{0, 1, 0})
		for i := 0; i < 3; i++ {
			f := feFrame("p1", i, float64(i)*0.067, body)
			if withHead {
				f.HeadPosition = vecPtr(head)
			}
			f.HasPossession = true
			f.Disc = &model.DiscState{Position: f.RightHandPosition, IsHeld: true, PossessorID: "p1"}
			feObserve(fe, ps, &f, mc)
		}
		release := feFrame("p1", 3, 3*0.067, body)
		if withHead {
			release.HeadPosition = vecPtr(head)
		}
		release.LeftHandPosition = head.Add(model.Vec3{-0.7, 0, 0})
		release.RightHandPosition = head.Add(model.Vec3{0.7, 0, 0})
		release.Disc = &model.DiscState{Position: head.Add(model.Vec3{0, 0, 0.1}), Velocity: model.Vec3{0, 0, -12}, Speed: 12}
		feObserve(fe, ps, &release, mc)
		feConfirm(fe, ps, release, mc, .067)
		if ps.LastThrow == nil {
			t.Fatal("release was not reconstructed")
		}
		return ps.LastThrow
	}
	tracked := run(true)
	if !approx(tracked.HeadToDiscDistance, 0.1, 1e-9) || !tracked.PossibleHeadContact {
		t.Fatalf("tracked head ignored: distance=%v contact=%v", tracked.HeadToDiscDistance, tracked.PossibleHeadContact)
	}
	// No tracked head: the body position remains the documented fallback.
	fallback := run(false)
	if fallback.HeadToDiscDistance < 0.99 || fallback.PossibleHeadContact {
		t.Fatalf("body fallback changed: distance=%v contact=%v", fallback.HeadToDiscDistance, fallback.PossibleHeadContact)
	}
}
