package pipeline

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestExtractorReleaseRequiresBoundObservationFrames(t *testing.T) {
	for _, field := range []string{"frame", "timestamp"} {
		for _, changedFrame := range []int{2, 3, 4} {
			t.Run(fmt.Sprintf("%s/at_%d", field, changedFrame), func(t *testing.T) {
				fe, ps := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}
				for i := 0; i <= 4; i++ {
					attachment := "right"
					if i >= 3 {
						attachment = "free"
					}
					f := releaseFrame(i, attachment)
					if i == changedFrame {
						if field == "frame" {
							f.Observation.FrameIndex--
						} else {
							f.Observation.Timestamp -= .01
						}
					}
					fe.UpdatePlayerState(ps, &f, feTestCtx())
				}
				if ps.LastThrow != nil || ps.ThrowCount != 0 {
					t.Fatalf("unbound %s on frame %d published a throw", field, changedFrame)
				}
				// A later fresh held-held-free-free sequence must recover.
				for i := 5; i <= 8; i++ {
					attachment := "right"
					if i >= 7 {
						attachment = "free"
					}
					f := releaseFrame(i, attachment)
					fe.UpdatePlayerState(ps, &f, feTestCtx())
				}
				if ps.LastThrow == nil || ps.ThrowCount != 1 || ps.LastThrow.FrameIndex != 7 {
					t.Fatal("fresh bound release did not recover")
				}
				for _, snapshot := range ps.LastThrow.PreReleaseFrames {
					if snapshot.FrameIndex <= changedFrame {
						t.Fatalf("unbound sample leaked into later release evidence: %+v", snapshot)
					}
				}
			})
		}
	}
}

func TestExtractorRejectsDifferentSubjectWithoutConsumingConfirmation(t *testing.T) {
	fe, ps := releaseStart(t)
	f := releaseFrame(3, "free")
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	f = releaseFrame(4, "free")
	f.PlayerID = "p2"
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	if ps.LastFrameIdx != 3 || ps.LastThrow != nil || len(fe.pendingReleases) != 1 {
		t.Fatal("different subject changed release state")
	}
	f.PlayerID = "p1"
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	if ps.LastThrow == nil || ps.ThrowCount != 1 || !ps.LastThrow.ObservedAt(4) {
		t.Fatal("matching subject confirmation did not recover")
	}
}

func TestExtractorRecentThrowHistoryIsBoundedWithoutLosingEvents(t *testing.T) {
	fe, ps := NewFeatureExtractor(5), &model.PlayerState{PlayerID: "p1"}
	const throws = 20
	var firstPublished *model.ThrowEvent
	for throw := 0; throw < throws; throw++ {
		for part := 0; part < 4; part++ {
			attachment := "right"
			if part >= 2 {
				attachment = "free"
			}
			f := releaseFrame(throw*4+part, attachment)
			fe.UpdatePlayerState(ps, &f, feTestCtx())
		}
		if throw == 0 {
			firstPublished = ps.LastThrow
		}
		if ps.ThrowCount != throw+1 || ps.LastThrow == nil || ps.LastThrow.FrameIndex != throw*4+2 {
			t.Fatalf("bounded recent history lost a published release at %d", throw)
		}
		if len(ps.ThrowHistory) > 5 {
			t.Fatalf("recent throw history grew beyond configured window: %d", len(ps.ThrowHistory))
		}
	}
	if len(ps.ThrowHistory) != 5 || firstPublished == nil || firstPublished.FrameIndex != 2 {
		t.Fatal("retention changed an already published event or final history size")
	}
	for i, event := range ps.ThrowHistory {
		if event.FrameIndex != (throws-5+i)*4+2 {
			t.Fatalf("recent history out of order at %d: frame %d", i, event.FrameIndex)
		}
	}
}

func TestExtractorReleaseRequiresFreshPossessionAfterInterruption(t *testing.T) {
	for _, interruption := range []string{"missing_frames", "long_gap", "inactive_phase"} {
		for _, heldSamples := range []int{1, 2} {
			t.Run(interruption+map[int]string{1: "/one_held_sample", 2: "/two_held_samples"}[heldSamples], func(t *testing.T) {
				fe, ps := releaseStart(t)
				var reasons []string
				fe.SetReleaseObserver(func(_ string, _ model.ReleaseObservation, reason string) { reasons = append(reasons, reason) })
				index, timestamp := 3, .15
				switch interruption {
				case "missing_frames":
					index, timestamp = 7, .35
				case "long_gap":
					timestamp = 1
				case "inactive_phase":
					f := releaseFrame(index, "right")
					f.GamePhase = "round_over"
					fe.UpdatePlayerState(ps, &f, feTestCtx())
					index++
					timestamp += .05
				}
				firstFreshHeld := index
				for i := 0; i < heldSamples+2; i++ {
					attachment := "right"
					if i >= heldSamples {
						attachment = "free"
					}
					f := releaseFrame(index+i, attachment)
					f.Timestamp, f.Observation.Timestamp = timestamp+float64(i)*.05, timestamp+float64(i)*.05
					fe.UpdatePlayerState(ps, &f, feTestCtx())
				}
				fe.DrainPendingReleases("eof")
				if heldSamples == 1 {
					if ps.LastThrow != nil || ps.ThrowCount != 0 || !reflect.DeepEqual(reasons, []string{"release_attachment_history_short"}) {
						t.Fatalf("pre-interruption possession fabricated a throw: throw=%+v count=%d reasons=%v", ps.LastThrow, ps.ThrowCount, reasons)
					}
					return
				}
				if ps.LastThrow == nil || ps.ThrowCount != 1 || len(reasons) != 0 || len(ps.LastThrow.PreReleaseFrames) != 2 {
					t.Fatalf("fresh two-sample control not reconstructed: throw=%+v reasons=%v", ps.LastThrow, reasons)
				}
				for i, snapshot := range ps.LastThrow.PreReleaseFrames {
					if snapshot.FrameIndex != firstFreshHeld+i {
						t.Fatalf("old history leaked through %s: %+v", interruption, ps.LastThrow.PreReleaseFrames)
					}
					if i == 0 && !snapshot.HandVelocity.IsZero() {
						t.Fatalf("first retained snapshot derived hand motion across %s: %+v", interruption, snapshot)
					}
					if i == 1 && !approx(snapshot.HandVelocity.Magnitude(), 5, 1e-9) {
						t.Fatalf("continuous retained hand motion lost: %+v", snapshot)
					}
				}
				if !approx(ps.LastThrow.PossessionDuration, .1, 1e-9) {
					t.Fatalf("possession duration bridged %s: %g", interruption, ps.LastThrow.PossessionDuration)
				}
			})
		}
	}
}

func TestExtractorSnapshotsDoNotInventEvictedFrameIdentity(t *testing.T) {
	fe, ps := releaseStart(t)
	delete(fe.discHistory, ps.PlayerID)
	f := releaseFrame(3, "free")
	if snapshots := fe.buildPreReleaseSnapshots(ps, &f, "right"); len(snapshots) != 0 {
		t.Fatalf("evicted metadata fabricated consecutive snapshot identities: %+v", snapshots)
	}
}

func TestExtractorReleaseCannotStartAtInactiveToActiveBoundary(t *testing.T) {
	fe, ps := releaseStart(t)
	var reasons []string
	fe.SetReleaseObserver(func(_ string, _ model.ReleaseObservation, reason string) { reasons = append(reasons, reason) })
	f := releaseFrame(3, "right")
	f.GamePhase = "round_over"
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	for i := 4; i <= 5; i++ {
		f = releaseFrame(i, "free")
		fe.UpdatePlayerState(ps, &f, feTestCtx())
	}
	if ps.LastThrow != nil || ps.ThrowCount != 0 || !reflect.DeepEqual(reasons, []string{"release_inactive_phase"}) {
		t.Fatalf("phase-boundary disc reset became a throw: %+v reasons=%v", ps.LastThrow, reasons)
	}
}

func TestExtractorContactContextRequiresObservedVelocityPair(t *testing.T) {
	for _, previousKnown := range []bool{false, true} {
		fe, ps := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}
		previous := feFrame("p1", 0, 0, model.Vec3{1, 2, 3})
		if previousKnown {
			previous.ReportedVelocity = vecPtr(model.Vec3{})
		}
		fe.UpdatePlayerState(ps, &previous, feTestCtx())
		current := feFrame("p1", 1, .05, model.Vec3{1, 2, 3})
		current.ReportedVelocity = vecPtr(model.Vec3{4, 0, 0})
		current.RightHandPosition[0] += .2
		fe.UpdatePlayerState(ps, &current, feTestCtx())
		if ps.LegalContext.PossibleSlapOrPush != previousKnown {
			t.Fatalf("previous velocity known=%v produced contact candidate=%v; absent is not measured rest", previousKnown, ps.LegalContext.PossibleSlapOrPush)
		}
	}
}

func TestExtractorTrackingContextHonorsOrientationPresence(t *testing.T) {
	for _, observed := range []bool{false, true} {
		fe, ps := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}
		f := feFrame("p1", 0, 0, model.Vec3{1, 2, 3})
		f.LeftHandRotationValid, f.RightHandRotationValid = &observed, &observed
		fe.UpdatePlayerState(ps, &f, feTestCtx())
		if ps.LegalContext.TrackingLimited == observed {
			t.Fatalf("observed=%v fallback identity misrepresented as tracked: %+v", observed, ps.LegalContext)
		}
	}
}
