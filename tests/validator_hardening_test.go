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
// are counted separately, and the rest of the stream is processed. Every
// injected frame is either rejected with a reason code or accepted with a
// sanitization code; nothing malformed passes silently.
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
		150: func(f *model.PlayerTelemetryFrame) { f.RightHandPosition = model.Vec3{500, 500, 500} },
		160: func(f *model.PlayerTelemetryFrame) { f.Timestamp = math.Inf(1) },
	}
	for i, m := range inject {
		m(&frames[i])
	}

	hr := testutil.NewHarness(t).WithEnabledDetectors().
		WithMatchContext(matchContextForPlayer("player1")).Run(t, frames)

	wantReasons := map[string]int{
		pipeline.ReasonInvalidPosition:  2, // NaN, Inf
		pipeline.ReasonOutOfBounds:      1,
		pipeline.ReasonZeroPosition:     1,
		pipeline.ReasonInvalidTimestamp: 3, // negative, NaN, +Inf
		pipeline.ReasonDtOutOfRange:     1, // duplicated tick
		pipeline.ReasonMissingPlayerID:  1,
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
		pipeline.SanitizedHandPosition:   1,
		pipeline.SanitizedFarHand:        1,
		pipeline.SanitizedDisc:           1,
		pipeline.SanitizedDiscOutOfRange: 1,
		pipeline.SanitizedRotation:       1,
		pipeline.SanitizedZeroRotation:   1,
		pipeline.SanitizedNegativeDt:     1,
	}
	for s, n := range wantSanitized {
		if hr.Result.SanitizedFrames[s] != n {
			t.Errorf("sanitized %s: %d, want %d", s, hr.Result.SanitizedFrames[s], n)
		}
	}
	if len(hr.Result.SanitizedFrames) != len(wantSanitized) {
		t.Errorf("unexpected sanitization codes: %v", hr.Result.SanitizedFrames)
	}
	// Nothing malformed may surface as a detection on an otherwise clean
	// 4 m/s player.
	hr.AssertNoDetections()
}

// TestValidation_FarHandIsSanitizedBeforeBio002 (F171, closed): a single
// frame with a hand hundreds of metres away used to pass both validators
// and the extractor turned it into a ~12800 m/s hand speed that BIO_002
// reported. The validator now replaces any hand further than
// MaxHandBodyDistance from the body with the zero-vector tracking-loss
// sentinel (far_hand_position), so the extractor derives no hand speed
// from it and BIO_002 stays silent.
func TestValidation_FarHandIsSanitizedBeforeBio002(t *testing.T) {
	frames := player1().NormalMovingPlayer(200, 4.0)
	frames[100].RightHandPosition = model.Vec3{500, 500, 500}
	if invalid, reasons := testutil.ValidateFrames(frames); invalid != 0 {
		t.Fatalf("the far hand must be sanitized, not rejected: %v", reasons)
	}
	hr := runOnly(t, frames, "BIO_002")
	if hr.Result.SanitizedFrames[pipeline.SanitizedFarHand] != 1 {
		t.Errorf("far_hand_position sanitizations %d, want 1: %v", hr.Result.SanitizedFrames[pipeline.SanitizedFarHand], hr.Result.SanitizedFrames)
	}
	hr.AssertNoDetections()
}

// TestValidation_NegativeTimestampIsRejectedOffline (F171, closed): a
// negative timestamp is rejected by the pipeline validator with the same
// invalid_timestamp code the ingest guard uses, so the offline and live
// paths agree and the extractor never sees a negative dt.
func TestValidation_NegativeTimestampIsRejectedOffline(t *testing.T) {
	frames := player1().NormalMovingPlayer(60, 4.0)
	frames[30].Timestamp = -1
	frames[30].DeltaTime = 0
	hr := testutil.NewHarness(t).WithDetectors("MOV_001", "MOV_002").
		WithMatchContext(matchContextForPlayer("player1")).Run(t, frames)
	if hr.Result.InvalidFrames != 1 || hr.Result.InvalidFrameReasons[pipeline.ReasonInvalidTimestamp] != 1 {
		t.Fatalf("negative timestamp: invalid %d reasons %v, want one %s", hr.Result.InvalidFrames, hr.Result.InvalidFrameReasons, pipeline.ReasonInvalidTimestamp)
	}
	if hr.Result.FramesProcessed != 59 {
		t.Errorf("processed %d, want 59", hr.Result.FramesProcessed)
	}
	hr.AssertNoDetections()
}
