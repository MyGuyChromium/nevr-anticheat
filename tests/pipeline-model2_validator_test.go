package tests

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

// Fix pass 2, workstream pipeline-model: regression tests for the validator
// hardening (X0) and the validator's dt bounds versus pipeline.max_frame_dt
// (C0 / contract B).

func hardeningFrame() model.PlayerTelemetryFrame {
	return model.PlayerTelemetryFrame{
		PlayerID: "p1", FrameIndex: 10, Timestamp: 0.67, DeltaTime: 0.067,
		Position: model.Vec3{2, 1.6, 0}, Rotation: model.QuatIdentity(),
		LeftHandPosition: model.Vec3{1.7, 1.9, 0.2}, RightHandPosition: model.Vec3{2.3, 1.9, -0.2},
		LeftHandRotation: model.QuatIdentity(), RightHandRotation: model.QuatIdentity(),
		GamePhase: "playing",
		Disc:      &model.DiscState{Position: model.Vec3{0, 2, 0}},
	}
}

func validateOne(t *testing.T, cfg *config.Config, f *model.PlayerTelemetryFrame) (string, []string) {
	t.Helper()
	v := pipeline.NewFrameValidator(cfg)
	mc := &model.MatchContext{MatchID: "m", Physics: model.DefaultPhysics()}
	sanitized, err := v.Validate(f, mc)
	if err == nil {
		return "", sanitized
	}
	verr, ok := err.(*pipeline.ValidationError)
	if !ok {
		t.Fatalf("non-ValidationError %v", err)
	}
	return verr.Reason, sanitized
}

// TestPipelineModel2_ValidatorRejectsBadTimestamps: negative, NaN and +Inf
// timestamps are all rejected with invalid_timestamp, the code the ingest
// guard uses, so both entry points agree.
func TestPipelineModel2_ValidatorRejectsBadTimestamps(t *testing.T) {
	cfg := config.DefaultConfig()
	for _, ts := range []float64{-1, -0.001, math.NaN(), math.Inf(1), math.Inf(-1)} {
		f := hardeningFrame()
		f.Timestamp = ts
		if reason, _ := validateOne(t, cfg, &f); reason != pipeline.ReasonInvalidTimestamp {
			t.Errorf("timestamp %v: reason %q, want %s", ts, reason, pipeline.ReasonInvalidTimestamp)
		}
	}
	f := hardeningFrame()
	f.Timestamp = 0
	if reason, _ := validateOne(t, cfg, &f); reason != "" {
		t.Errorf("timestamp 0 must be accepted, got %s", reason)
	}
}

// TestPipelineModel2_ValidatorSanitizations pins each new in-place repair:
// the code reported, the value left in the frame, and that the frame is
// accepted.
func TestPipelineModel2_ValidatorSanitizations(t *testing.T) {
	cfg := config.DefaultConfig()
	cases := []struct {
		name   string
		mutate func(f *model.PlayerTelemetryFrame)
		code   string
		check  func(t *testing.T, f *model.PlayerTelemetryFrame)
	}{
		{"negative_dt", func(f *model.PlayerTelemetryFrame) { f.DeltaTime = -5 }, pipeline.SanitizedNegativeDt,
			func(t *testing.T, f *model.PlayerTelemetryFrame) {
				if f.DeltaTime != 0 {
					t.Errorf("dt %v, want 0 (unknown)", f.DeltaTime)
				}
			}},
		{"zero_body_rotation", func(f *model.PlayerTelemetryFrame) { f.Rotation = model.Quat{} }, pipeline.SanitizedZeroRotation,
			func(t *testing.T, f *model.PlayerTelemetryFrame) {
				if f.Rotation != (model.Quat{}) {
					t.Errorf("the zero sentinel must be left in place, got %v", f.Rotation)
				}
			}},
		{"zero_hand_rotation", func(f *model.PlayerTelemetryFrame) { f.LeftHandRotation = model.Quat{} }, pipeline.SanitizedZeroRotation, nil},
		{"near_zero_rotation", func(f *model.PlayerTelemetryFrame) { f.Rotation = model.Quat{0.01, 0, 0, 0.02} }, pipeline.SanitizedRotation,
			func(t *testing.T, f *model.PlayerTelemetryFrame) {
				if f.Rotation != (model.Quat{}) {
					t.Errorf("an unusable |q| <= 0.1 quaternion must become the zero sentinel, got %v", f.Rotation)
				}
			}},
		{"far_right_hand", func(f *model.PlayerTelemetryFrame) { f.RightHandPosition = model.Vec3{500, 500, 500} }, pipeline.SanitizedFarHand,
			func(t *testing.T, f *model.PlayerTelemetryFrame) {
				if !f.RightHandPosition.IsZero() {
					t.Errorf("far hand must become the zero-vector sentinel, got %v", f.RightHandPosition)
				}
				if f.LeftHandPosition.IsZero() {
					t.Errorf("the other hand must be untouched")
				}
			}},
		{"hand_just_over_3m", func(f *model.PlayerTelemetryFrame) { f.LeftHandPosition = f.Position.Add(model.Vec3{3.01, 0, 0}) }, pipeline.SanitizedFarHand, nil},
		{"disc_out_of_arena", func(f *model.PlayerTelemetryFrame) { f.Disc = &model.DiscState{Position: model.Vec3{0, 0, 5000}} }, pipeline.SanitizedDiscOutOfRange,
			func(t *testing.T, f *model.PlayerTelemetryFrame) {
				if f.Disc != nil {
					t.Errorf("disc must be dropped, got %+v", f.Disc)
				}
			}},
		{"disc_velocity_over_4x_cap", func(f *model.PlayerTelemetryFrame) {
			f.Disc = &model.DiscState{Position: model.Vec3{0, 2, 0}, Velocity: model.Vec3{0, 0, 4*model.DefaultPhysics().DiscSpeedCap + 0.1}}
		}, pipeline.SanitizedDiscOutOfRange, nil},
		{"disc_speed_over_4x_cap", func(f *model.PlayerTelemetryFrame) {
			f.Disc = &model.DiscState{Position: model.Vec3{0, 2, 0}, Speed: 4*model.DefaultPhysics().DiscSpeedCap + 0.1}
		}, pipeline.SanitizedDiscOutOfRange, nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := hardeningFrame()
			tc.mutate(&f)
			reason, sanitized := validateOne(t, cfg, &f)
			if reason != "" {
				t.Fatalf("rejected with %s, want sanitized %s", reason, tc.code)
			}
			if len(sanitized) != 1 || sanitized[0] != tc.code {
				t.Fatalf("sanitized %v, want [%s]", sanitized, tc.code)
			}
			if tc.check != nil {
				tc.check(t, &f)
			}
		})
	}
}

// TestPipelineModel2_ValidatorAcceptsPlausibleExtremes: the new bounds must
// not touch data a real match produces: a hand at full reach, a disc at the
// far wall, a disc at 3x the cap (a THROW_001 case) and the zero-vector
// hand sentinel on a player far from the origin.
func TestPipelineModel2_ValidatorAcceptsPlausibleExtremes(t *testing.T) {
	cfg := config.DefaultConfig()
	ph := model.DefaultPhysics()
	cases := []struct {
		name   string
		mutate func(f *model.PlayerTelemetryFrame)
	}{
		{"hand_at_2.9m", func(f *model.PlayerTelemetryFrame) { f.RightHandPosition = f.Position.Add(model.Vec3{2.9, 0, 0}) }},
		{"zero_hand_far_from_origin", func(f *model.PlayerTelemetryFrame) {
			f.Position = model.Vec3{1, 1, 70}
			f.LeftHandPosition = model.Vec3{}
			f.RightHandPosition = model.Vec3{}
		}},
		{"disc_at_far_wall", func(f *model.PlayerTelemetryFrame) {
			f.Disc = &model.DiscState{Position: model.Vec3{0, 0, ph.ArenaLength/2 + 4}}
		}},
		{"disc_at_3x_cap", func(f *model.PlayerTelemetryFrame) {
			f.Disc = &model.DiscState{Position: model.Vec3{0, 2, 0}, Velocity: model.Vec3{0, 0, 3 * ph.DiscSpeedCap}, Speed: 3 * ph.DiscSpeedCap}
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := hardeningFrame()
			tc.mutate(&f)
			reason, sanitized := validateOne(t, cfg, &f)
			if reason != "" || len(sanitized) != 0 {
				t.Errorf("reason %q sanitized %v, want clean acceptance", reason, sanitized)
			}
			if f.Disc == nil {
				t.Errorf("disc must be kept")
			}
		})
	}
}

// TestPipelineModel2_MaxFrameDtIsNotAValidatorBound (C0 / contract B):
// pipeline.max_frame_dt is the feature extractor's gap threshold, not a
// rejection bound. Whatever it is set to, the validator accepts every
// producer dt up to MaxProducerDt (60 s) and rejects only beyond it.
func TestPipelineModel2_MaxFrameDtIsNotAValidatorBound(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Pipeline.MaxFrameDt = 0.1
	for _, dt := range []float64{0.06, 0.3, 2.5, pipeline.MaxProducerDt} {
		f := hardeningFrame()
		f.DeltaTime = dt
		if reason, _ := validateOne(t, cfg, &f); reason != "" {
			t.Errorf("dt %.3f with max_frame_dt=0.1: rejected with %s; the validator's bound is MaxProducerDt", dt, reason)
		}
	}
	f := hardeningFrame()
	f.DeltaTime = pipeline.MaxProducerDt + 1
	if reason, _ := validateOne(t, cfg, &f); reason != pipeline.ReasonDtOutOfRange {
		t.Errorf("dt > MaxProducerDt: reason %q, want %s", reason, pipeline.ReasonDtOutOfRange)
	}
	f = hardeningFrame()
	f.DeltaTime = cfg.Pipeline.MinFrameDt * 0.4
	if reason, _ := validateOne(t, cfg, &f); reason != pipeline.ReasonDtOutOfRange {
		t.Errorf("dt < min_frame_dt/2: reason %q, want %s", reason, pipeline.ReasonDtOutOfRange)
	}
}
