package throw

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func persistedMechanics(t *testing.T, r model.MechanicsAssessment) model.MechanicsAssessment {
	t.Helper()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var out model.MechanicsAssessment
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestThrow005StoredSupportReproducesBoundedStatistics(t *testing.T) {
	d, records := shotRecorder()
	for frame := 1; frame <= 70; frame++ {
		ps := mechanicsThrowState(frame, 8+float64(frame%10), float64(frame%11)*7)
		// The scalar used by the statistic deliberately differs from |v|.
		ps.LastThrow.ReleaseSpeed = 9 + float64(frame%13)*.3
		if frame%7 == 0 {
			ps.LastThrow.GoalPosition = model.Vec3{}
			ps.LastThrow.TargetPosition = nil
		}
		d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
	}
	r := persistedMechanics(t, (*records)[69])
	var deviations []float64
	var pairs [][2]float64
	count := 0
	for _, sample := range r.RawSamples {
		if sample.SampleRole != "supporting_release" {
			continue
		}
		count++
		if sample.FrameIndex < 21 || sample.FrameIndex > 70 || sample.Timestamp != float64(sample.FrameIndex)*.067 || sample.EventID == "" || sample.SampledSpeedMPS == nil {
			t.Fatal("supporting window lost identity/time/scalar")
		}
		if sample.InferredReferenceGoal == nil {
			continue
		}
		if sample.DiscVelocity == nil || sample.DiscPosition == nil {
			t.Fatal("included direction has missing raw vectors")
		}
		angle := sample.DiscVelocity.AngleBetweenDeg(sample.InferredReferenceGoal.Sub(*sample.DiscPosition))
		deviations = append(deviations, angle)
		pairs = append(pairs, [2]float64{*sample.SampledSpeedMPS, angle})
	}
	if count != 50 || r.Metrics["observed_release_count"] != 50 || float64(len(deviations)) != r.Metrics["direction_sample_count"] {
		t.Fatal("bounded support count cannot be reproduced")
	}
	for key, want := range map[string]float64{"mean_reported_goal_direction_deg": model.Mean(deviations), "stddev_reported_goal_direction_deg": model.StdDev(deviations), "min_reported_goal_direction_deg": model.MinFloat(deviations), "max_reported_goal_direction_deg": model.MaxFloat(deviations)} {
		if math.Abs(r.Metrics[key]-want) > 1e-10 {
			t.Fatalf("%s stored=%g reproduced=%g", key, r.Metrics[key], want)
		}
	}
	corr, upper, ok := correlationUpperCI(pairs)
	if !ok || math.Abs(corr-r.Metrics["speed_direction_correlation"]) > 1e-10 || math.Abs(upper-r.Metrics["correlation_upper_95_ci"]) > 1e-10 {
		t.Fatal("stored raw inputs cannot reproduce correlation")
	}
	if r.Metrics["support_start_frame"] != 21 || r.Metrics["support_end_frame"] != 70 || r.Metrics["support_start_time_s"] != 21*.067 || r.Metrics["support_end_time_s"] != 70*.067 {
		t.Fatal("supporting interval missing")
	}
}

func TestThrow006Stored120FrameWindowReproducesEveryMeasuredAngle(t *testing.T) {
	d := NewThrow006(map[string]any{"post_release_frames": 120, "min_distance_from_thrower": 3.0, "min_trajectory_change": 1.0, "max_cumulative_change": 130.0})
	var records []model.MechanicsAssessment
	d.SetMechanicsObserver(func(_, _ string, r model.MechanicsAssessment) { records = append(records, r.Clone()) })
	for frame := 1; frame <= 121; frame++ {
		ps := flightState(frame, float64(frame-1)*2)
		d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
	}
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	r := persistedMechanics(t, records[0])
	var previous *model.MechanicsRawSample
	var releasePosition *model.Vec3
	count, measured, above := 0, 0, 0
	total, maximum := 0.0, 0.0
	for i := range r.RawSamples {
		sample := &r.RawSamples[i]
		if sample.SampleRole != "flight_sample" {
			if sample.FrameIndex == r.FrameIndex && sample.DiscPosition != nil {
				releasePosition = sample.DiscPosition
			}
			continue
		}
		count++
		if sample.FrameIndex != count || sample.BounceCount == nil || *sample.BounceCount != 0 {
			t.Fatalf("middle flight sample missing: frame=%d count=%d", sample.FrameIndex, count)
		}
		if previous != nil && sample.DiscPosition.Distance(*releasePosition) >= r.Metrics["min_distance_from_release_m"] {
			angle := previous.DiscVelocity.AngleBetweenDeg(*sample.DiscVelocity)
			total += angle
			maximum = math.Max(maximum, angle)
			measured++
			if angle > r.Metrics["sample_turn_filter_deg"] {
				above++
			}
		}
		previous = sample
	}
	if count != 121 || float64(measured) != r.Metrics["tracked_free_samples"] || float64(above) != r.Metrics["above_filter_sample_count"] || math.Abs(total-r.Metrics["sampled_cumulative_turn_deg"]) > 1e-10 || math.Abs(maximum-r.Metrics["sampled_max_turn_deg"]) > 1e-10 {
		t.Fatal("retained flight inputs cannot reproduce statistics")
	}
	if r.Metrics["inspection_start_frame"] != 1 || r.Metrics["inspection_end_frame"] != 121 || r.Metrics["inspection_sample_count"] != 121 || r.Metrics["raw_samples_omitted"] != 0 {
		t.Fatal("configured inspection window truncated or unlabelled")
	}
}

func TestThrow006OversizedContextCannotEvictInspectedFlight(t *testing.T) {
	d := NewThrow006(map[string]any{"post_release_frames": 120, "min_distance_from_thrower": 0.0})
	var record model.MechanicsAssessment
	d.SetMechanicsObserver(func(_, _ string, r model.MechanicsAssessment) { record = r.Clone() })
	for frame := 1; frame <= 121; frame++ {
		ps := flightState(frame, 0)
		if frame == 1 {
			for i := 0; i < 128; i++ {
				ps.LastThrow.ReleaseWindow.PlayerMovement = append(ps.LastThrow.ReleaseWindow.PlayerMovement, model.MovementObservation{FrameIndex: 0, Timestamp: 0, Position: model.Vec3{1, 1, 1}})
			}
		}
		d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
	}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(record.RawSamples) != 128 || record.Metrics["inspection_sample_count"] != 121 || record.Metrics["context_raw_samples_omitted"] != 122 || record.Metrics["raw_samples_omitted"] != 0 {
		t.Fatal("optional context silently evicted flight inputs")
	}
}

func TestThrow006EvidenceCapacityNeverReplacesMiddleSamples(t *testing.T) {
	r := model.MechanicsAssessment{RawSamples: make([]model.MechanicsRawSample, model.MaxMechanicsRawSamples)}
	r.RawSamples[4].FrameIndex = 44
	if appendFlightRaw(&r, flightReviewSample{frame: 999, timestamp: 10}) || len(r.RawSamples) != model.MaxMechanicsRawSamples || r.RawSamples[4].FrameIndex != 44 || r.Metrics["raw_samples_omitted"] != 1 {
		t.Fatal("evidence exhaustion silently replaced prior observations")
	}
}
