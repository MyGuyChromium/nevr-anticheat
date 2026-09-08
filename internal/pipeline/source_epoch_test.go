package pipeline

import (
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestClockEpochCannotConfirmPendingRelease(t *testing.T) {
	fe, ps := releaseStart(t)
	var reasons []string
	fe.SetReleaseObserver(func(_ string, _ model.ReleaseObservation, reason string) { reasons = append(reasons, reason) })
	firstFree := releaseFrame(3, "free")
	fe.UpdatePlayerState(ps, &firstFree, feTestCtx())
	if len(fe.pendingReleases) != 1 {
		t.Fatal("fixture did not establish a pending release")
	}
	secondFree := releaseFrame(4, "free")
	secondFree.Observation.SourceEpoch = 1
	fe.UpdatePlayerState(ps, &secondFree, feTestCtx())
	fe.DrainPendingReleases("eof")
	if ps.LastThrow != nil || ps.ThrowCount != 0 || ps.FrameDt != 0 || !reflect.DeepEqual(reasons, []string{"release_source_changed"}) {
		t.Fatalf("pending release crossed clock epoch: throw=%+v dt=%v reasons=%v", ps.LastThrow, ps.FrameDt, reasons)
	}
}
