package ingest

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

// hardeningCase is one malformed frame and what each of the two production
// entry points does with it: the live ingest guard (validateIngestFrame)
// and the pipeline's FrameValidator (the offline/reprocess path, and the
// second gate on the live path).
//
// The pipeline validator is the second gate on the live path, so a frame
// only has to be caught by one of the two; every remaining asymmetry below
// is documented in its note.
type hardeningCase struct {
	name          string
	mutate        func(f *model.PlayerTelemetryFrame)
	ingestReject  string   // expected IngestError.Reason, "" = accepted
	pipeReject    string   // expected ValidationError.Reason, "" = accepted
	pipeSanitized []string // expected sanitization codes when accepted
	note          string   // why the paths differ / what leaks through
}

func goodHardeningFrame() model.PlayerTelemetryFrame {
	return model.PlayerTelemetryFrame{
		PlayerID: "p1", FrameIndex: 10, Timestamp: 0.67, DeltaTime: 0.067,
		Position: model.Vec3{2, 1.6, 0}, Rotation: model.QuatIdentity(),
		LeftHandPosition: model.Vec3{1.7, 1.9, 0.2}, RightHandPosition: model.Vec3{2.3, 1.9, -0.2},
		LeftHandRotation: model.QuatIdentity(), RightHandRotation: model.QuatIdentity(),
		GamePhase: "playing",
		Disc:      &model.DiscState{Position: model.Vec3{0, 2, 0}},
	}
}

func hardeningCases() []hardeningCase {
	nan := math.NaN()
	inf := math.Inf(1)
	return []hardeningCase{
		{name: "clean", mutate: func(*model.PlayerTelemetryFrame) {}},
		{name: "nan_timestamp", mutate: func(f *model.PlayerTelemetryFrame) { f.Timestamp = nan },
			ingestReject: "invalid_timestamp", pipeReject: pipeline.ReasonInvalidTimestamp},
		{name: "inf_timestamp", mutate: func(f *model.PlayerTelemetryFrame) { f.Timestamp = inf },
			ingestReject: "", pipeReject: pipeline.ReasonInvalidTimestamp,
			note: "ingest only checks NaN/negative; +Inf passes the ingest guard and is caught by the pipeline (second gate)"},
		{name: "negative_timestamp", mutate: func(f *model.PlayerTelemetryFrame) { f.Timestamp = -1 },
			ingestReject: "invalid_timestamp", pipeReject: pipeline.ReasonInvalidTimestamp,
			note: "a negative timestamp is rejected on both paths with the same code"},
		{name: "zero_dt", mutate: func(f *model.PlayerTelemetryFrame) { f.DeltaTime = 0 },
			note: "dt <= 0 means unknown spacing on both paths"},
		{name: "negative_dt", mutate: func(f *model.PlayerTelemetryFrame) { f.DeltaTime = -5 },
			pipeSanitized: []string{pipeline.SanitizedNegativeDt},
			note:          "a negative DeltaTime is set to 0 (unknown spacing) with a code, never rejected"},
		{name: "nan_dt", mutate: func(f *model.PlayerTelemetryFrame) { f.DeltaTime = nan },
			pipeReject: pipeline.ReasonDtOutOfRange,
			note:       "ingest does not look at DeltaTime; the pipeline rejects non-finite time"},
		{name: "duplicate_tick_dt", mutate: func(f *model.PlayerTelemetryFrame) { f.DeltaTime = 0.001 },
			pipeReject: pipeline.ReasonDtOutOfRange,
			note:       "under half the configured min_frame_dt is a duplicated tick"},
		{name: "clock_jump_dt", mutate: func(f *model.PlayerTelemetryFrame) { f.DeltaTime = 120 },
			pipeReject: pipeline.ReasonDtOutOfRange,
			note:       "longer than MaxProducerDt (60 s) is a clock jump, not a stall"},
		{name: "stall_dt", mutate: func(f *model.PlayerTelemetryFrame) { f.DeltaTime = 2.5 },
			note: "a real stall is accepted; the extractor derives no kinematics across it"},
		{name: "zero_position", mutate: func(f *model.PlayerTelemetryFrame) { f.Position = model.Vec3{} },
			ingestReject: "zero_position", pipeReject: pipeline.ReasonZeroPosition},
		{name: "nan_position", mutate: func(f *model.PlayerTelemetryFrame) { f.Position = model.Vec3{nan, 0, 0} },
			ingestReject: "invalid_position", pipeReject: pipeline.ReasonInvalidPosition},
		{name: "inf_position", mutate: func(f *model.PlayerTelemetryFrame) { f.Position = model.Vec3{inf, 0, 0} },
			ingestReject: "invalid_position", pipeReject: pipeline.ReasonInvalidPosition},
		{name: "out_of_bounds_position", mutate: func(f *model.PlayerTelemetryFrame) { f.Position = model.Vec3{99999, 99999, 99999} },
			pipeReject: pipeline.ReasonOutOfBounds,
			note:       "ingest has no arena bounds; the pipeline rejects on physics extents"},
		{name: "zero_quaternion", mutate: func(f *model.PlayerTelemetryFrame) { f.Rotation = model.Quat{} },
			pipeSanitized: []string{pipeline.SanitizedZeroRotation},
			note:          "the zero quaternion is the 'no rotation data' sentinel: accepted unchanged, reported with a code, consumers skip it"},
		{name: "non_unit_quaternion", mutate: func(f *model.PlayerTelemetryFrame) { f.Rotation = model.Quat{0, 0, 0, 2} },
			note: "|q| > 0.1 is normalized in place without a sanitization code"},
		{name: "nan_quaternion", mutate: func(f *model.PlayerTelemetryFrame) { f.RightHandRotation = model.Quat{nan, 0, 0, 1} },
			pipeSanitized: []string{pipeline.SanitizedRotation}},
		{name: "nan_hand", mutate: func(f *model.PlayerTelemetryFrame) { f.LeftHandPosition = model.Vec3{nan, nan, nan} },
			pipeSanitized: []string{pipeline.SanitizedHandPosition}},
		{name: "far_hand", mutate: func(f *model.PlayerTelemetryFrame) { f.RightHandPosition = model.Vec3{500, 500, 500} },
			pipeSanitized: []string{pipeline.SanitizedFarHand},
			note:          "a hand further than MaxHandBodyDistance from the body becomes the zero-vector tracking-loss sentinel"},
		{name: "nan_disc", mutate: func(f *model.PlayerTelemetryFrame) { f.Disc = &model.DiscState{Position: model.Vec3{nan, 0, 0}} },
			pipeSanitized: []string{pipeline.SanitizedDisc}},
		{name: "out_of_bounds_disc", mutate: func(f *model.PlayerTelemetryFrame) {
			f.Disc = &model.DiscState{Position: model.Vec3{0, 0, 5000}, Velocity: model.Vec3{0, 0, 900}}
		},
			pipeSanitized: []string{pipeline.SanitizedDiscOutOfRange},
			note:          "a disc outside the arena or faster than DiscSpeedSanityFactor x DiscSpeedCap is dropped from the frame"},
		{name: "missing_player", mutate: func(f *model.PlayerTelemetryFrame) { f.PlayerID = "" },
			ingestReject: "missing_player_id", pipeReject: pipeline.ReasonMissingPlayerID},
		{name: "control_char_player", mutate: func(f *model.PlayerTelemetryFrame) { f.PlayerID = "p\x01" },
			ingestReject: "invalid_player_id",
			note:         "identifier hygiene is an ingest concern only"},
		{name: "negative_frame_index", mutate: func(f *model.PlayerTelemetryFrame) { f.FrameIndex = -3 },
			ingestReject: "negative_frame_index",
			note:         "the pipeline indexes by frame index and accepts any integer"},
	}
}

// TestValidation_IngestAndPipelineTable (F171) runs every malformed frame
// through both guards and pins the outcome of each.
func TestValidation_IngestAndPipelineTable(t *testing.T) {
	cfg := config.DefaultConfig()
	validator := pipeline.NewFrameValidator(cfg)
	mc := &model.MatchContext{MatchID: "m", Physics: model.DefaultPhysics()}
	for _, tc := range hardeningCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := goodHardeningFrame()
			tc.mutate(&f)

			in := f
			ierr := validateIngestFrame(&in, cfg.Server.MaxMessageBytes)
			gotIngest := ""
			if ierr != nil {
				gotIngest = ierr.Reason
			}
			if gotIngest != tc.ingestReject {
				t.Errorf("ingest: reason %q, want %q (%s)", gotIngest, tc.ingestReject, tc.note)
			}

			pf := f
			sanitized, perr := validator.Validate(&pf, mc)
			gotPipe := ""
			if perr != nil {
				var verr *pipeline.ValidationError
				if e, ok := perr.(*pipeline.ValidationError); ok {
					verr = e
				}
				if verr == nil {
					t.Fatalf("pipeline: non-ValidationError %v", perr)
				}
				gotPipe = verr.Reason
			}
			if gotPipe != tc.pipeReject {
				t.Errorf("pipeline: reason %q, want %q (%s)", gotPipe, tc.pipeReject, tc.note)
			}
			if len(sanitized) != len(tc.pipeSanitized) {
				t.Errorf("pipeline: sanitized %v, want %v", sanitized, tc.pipeSanitized)
			} else {
				for i := range sanitized {
					if sanitized[i] != tc.pipeSanitized[i] {
						t.Errorf("pipeline: sanitized %v, want %v", sanitized, tc.pipeSanitized)
					}
				}
			}
			// An accepted frame must be safe to consume: no NaN/Inf left
			// in anything the extractor reads.
			if perr == nil {
				for _, v := range []model.Vec3{pf.Position, pf.LeftHandPosition, pf.RightHandPosition} {
					if v.HasNaN() || v.HasInf() {
						t.Errorf("accepted frame carries non-finite vector %v", v)
					}
				}
				for _, q := range []model.Quat{pf.Rotation, pf.LeftHandRotation, pf.RightHandRotation} {
					for _, c := range q {
						if math.IsNaN(c) || math.IsInf(c, 0) {
							t.Errorf("accepted frame carries non-finite quaternion %v", q)
						}
					}
				}
				if pf.Disc != nil && (pf.Disc.Position.HasNaN() || pf.Disc.Velocity.HasNaN()) {
					t.Errorf("accepted frame carries a NaN disc")
				}
			}
		})
	}
}

// TestValidation_IngestDerivesDiscSpeed: a contract-conformant sender gives
// velocity but no speed; ingest derives it so the throw detectors see it.
func TestValidation_IngestDerivesDiscSpeed(t *testing.T) {
	f := goodHardeningFrame()
	f.Disc = &model.DiscState{Velocity: model.Vec3{3, 4, 0}}
	if err := validateIngestFrame(&f, 64); err != nil {
		t.Fatal(err.Reason)
	}
	if f.Disc.Speed != 5 {
		t.Errorf("speed %.2f, want 5", f.Disc.Speed)
	}
	f.Disc = &model.DiscState{Velocity: model.Vec3{math.NaN(), 0, 0}}
	if err := validateIngestFrame(&f, 64); err != nil || f.Disc.Speed != 0 {
		t.Errorf("NaN velocity: err %v speed %v", err, f.Disc.Speed)
	}
}
