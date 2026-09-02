package tests

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// TestValidation_PipelineCountsEveryRejection (F171): malformed frames
// injected into a clean stream are counted by reason, sanitized frames
// are counted separately, and the rest of the stream is processed. The
// injected-but-accepted frames are the documented gaps of the validator
// (negative timestamp, negative dt, zero quaternion, far hand, wild disc).
func TestValidation_PipelineCountsEveryRejection(t *testing.T) {
	frames := player1().NormalMovingPlayer(200, 4.0)
	nan := math.NaN()
	inject := map[int]func(f *model.PlayerTelemetryFrame){
		10:  func(f *model.PlayerTelemetryFrame) { f.Position = model.Vec3{nan, 0, 0} },
		20:  func(f *model.PlayerTelemetryFrame) { f.Position = model.Vec3{math.Inf(1), 0, 0} },
		30:  func(f *model.PlayerTelemetryFrame) { f.Timestamp = -1.0 },
		40:  func(f *model.PlayerTelemetryFrame) { f.Position = model.Vec3{99999, 99999, 99999} },
		50:  func(f *model.PlayerTelemetryFrame) { f.Position = model.Vec3{} },
		60:  func(f *model.PlayerTelemetryFrame) { f.DeltaTime = -5.0 },
		70:  func(f *model.PlayerTelemetryFrame) { f.Rotation = model.Quat{} },
		80:  func(f *model.PlayerTelemetryFrame) { f.Timestamp = nan },
		90:  func(f *model.PlayerTelemetryFrame) { f.DeltaTime = 0.001 },
		100: func(f *model.PlayerTelemetryFrame) { f.LeftHandPosition = model.Vec3{nan, nan, nan} },
		110: func(f *model.PlayerTelemetryFrame) { f.Disc = &model.DiscState{Position: model.Vec3{nan, 0, 0}} },
		120: func(f *model.PlayerTelemetryFrame) { f.RightHandRotation = model.Quat{nan, 0, 0, 1} },
		130: func(f *model.PlayerTelemetryFrame) { f.Disc = &model.DiscState{Position: model.Vec3{0, 0, 5000}} },
		140: func(f *model.PlayerTelemetryFrame) { f.PlayerID = "" },
	}
	for i, m := range inject {
		m(&frames[i])
	}

	hr := testutil.NewHarness(t).WithEnabledDetectors().
		WithMatchContext(matchContextForPlayer("player1")).Run(t, frames)

	wantReasons := map[string]int{
		pipeline.ReasonInvalidPosition: 2, // NaN, Inf
		pipeline.ReasonOutOfBounds:     1,
		pipeline.ReasonZeroPosition:    1,
		pipeline.ReasonDtOutOfRange:    2, // NaN timestamp, duplicated tick
		pipeline.ReasonMissingPlayerID: 1,
	}
	wantInvalid := 0
	for r, n := range wantReasons {
		wantInvalid += n
		if hr.Result.InvalidFrameReasons[r] != n {
			t.Errorf("reason %s: %d, want %d", r, hr.Result.InvalidFrameReasons[r], n)
		}
	}
	if hr.Result.InvalidFrames != wantInvalid {
		t.Errorf("InvalidFrames %d, want %d: %v", hr.Result.InvalidFrames, wantInvalid, hr.Result.InvalidFrameReasons)
	}
	if hr.Result.InvalidFramesByPlayer["player1"] != wantInvalid-1 {
		t.Errorf("per-player invalid %d, want %d (the frame without a player id is unattributed)", hr.Result.InvalidFramesByPlayer["player1"], wantInvalid-1)
	}
	if hr.Result.FramesProcessed != 200-wantInvalid {
		t.Errorf("processed %d, want %d", hr.Result.FramesProcessed, 200-wantInvalid)
	}
	wantSanitized := map[string]int{
		pipeline.SanitizedHandPosition: 1,
		pipeline.SanitizedDisc:         1,
		pipeline.SanitizedRotation:     1,
	}
	for s, n := range wantSanitized {
		if hr.Result.SanitizedFrames[s] != n {
			t.Errorf("sanitized %s: %d, want %d", s, hr.Result.SanitizedFrames[s], n)
		}
	}
	// Nothing malformed may surface as a detection on an otherwise clean
	// 4 m/s player.
	hr.AssertNoDetections()
}

// TestValidation_FarHandReachesBio002 documents the F171 gap: a single
// frame with a hand hundreds of metres away passes both validators and
// the extractor turns it into an impossible hand speed. BIO_002 needs two
// consecutive frames, and the hand's return is the second one.
func TestValidation_FarHandReachesBio002(t *testing.T) {
	frames := player1().NormalMovingPlayer(200, 4.0)
	frames[100].RightHandPosition = model.Vec3{500, 500, 500}
	if invalid, _ := testutil.ValidateFrames(frames); invalid != 0 {
		t.Fatalf("the far hand was rejected; the gap is closed, update this test and the report")
	}
	hr := runOnly(t, frames, "BIO_002")
	hr.AssertDetectorFiredN("BIO_002", 1)
	evd := hr.Events[0].Evidence.(model.HandSpeedEvidence)
	if evd.Speed < 10000 {
		t.Errorf("expected a ~12800 m/s hand speed from one bad sample, got %.0f", evd.Speed)
	}
}

// TestValidation_NegativeTimestampIsAcceptedOffline documents the F171 gap
// on the pipeline side: a negative timestamp is accepted (ingest rejects
// it), the extractor sees a negative dt and derives no kinematics for that
// frame and the next.
func TestValidation_NegativeTimestampIsAcceptedOffline(t *testing.T) {
	frames := player1().NormalMovingPlayer(60, 4.0)
	frames[30].Timestamp = -1
	frames[30].DeltaTime = 0
	hr := runOnly(t, frames, "MOV_001", "MOV_002")
	if hr.Result.InvalidFrames != 0 {
		t.Fatalf("negative timestamp rejected: %v (gap closed; update this test and the report)", hr.Result.InvalidFrameReasons)
	}
	hr.AssertNoDetections()
}
