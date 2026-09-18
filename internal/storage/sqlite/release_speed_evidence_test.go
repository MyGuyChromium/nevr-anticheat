package sqlite

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// All measurements and identities are synthetic. This exercises the persisted
// evidence contract, not a claim about legal throws or detector accuracy.
func TestReleaseSpeedEvidenceSurvivesDatabaseReopen(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason string
		speeds               [3]float64
		above                int
		median               float64
	}{
		{"repeated observations", "corroborated", "release_speed_three_samples_above_cap", [3]float64{20, 20, 20}, 3, 20},
		{"isolated first sample", "uncorroborated", "release_speed_not_sustained", [3]float64{30, 18, 18}, 1, 18},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			dbPath := filepath.Join(t.TempDir(), "synthetic-release-evidence.db")
			store := newTestStoreAt(t, dbPath)
			median := tc.median
			review := &model.ReleaseSpeedReview{
				Status: tc.status, Reason: tc.reason, ReleaseFrame: 100, ResolvedFrame: 102,
				RequiredSamples: 3, AboveCapSamples: tc.above, MedianSampledSpeed: &median,
				LocalReportStatus: "bound_local_client_report_unverified",
				Limitations:       []string{"Synthetic client-reported observations are not authoritative physics.", "Complete contact geometry is unavailable."},
			}
			for i, speed := range tc.speeds {
				bounce := 0 // Observed zero must survive distinctly from an absent counter.
				source := &model.ObservationContext{
					Source: "echoreplay", Authority: "client_reported", TimeBasis: "recorder_prefix",
					SessionID: "synthetic-match", SourceEpoch: 2, SourcePlayerID: "synthetic-player",
					FrameIndex: 100 + i, Timestamp: 5 + float64(i)*.05,
				}
				review.Samples = append(review.Samples, model.ReleaseSpeedSample{
					FrameIndex: source.FrameIndex, Timestamp: source.Timestamp,
					Position: model.Vec3{1 + float64(i), 2, 3}, Velocity: model.Vec3{speed, 0, 0},
					Speed: speed, BounceCount: &bounce, Attachment: "free", Source: source,
				})
			}
			provenance := review.Samples[0].Source.Clone()
			provenance.Freshness = "value_change"
			want := model.ThrowEvidence{
				SpeedReview: review, GameLastThrowProvenance: provenance,
				ReleaseVelocity: review.Samples[0].Velocity, ReleaseSpeed: tc.speeds[0], SampledDiscSpeed: tc.speeds[0],
				ReleasePosition: review.Samples[0].Position,
				GameLastThrow: &model.GameThrowDetails{
					ArmSpeed: 11.5, TotalSpeed: 25, OffAxisSpinDeg: 2.5, WristThrowPenalty: .1,
					RotPerSec: 3, PotentialSpeedFromRot: 10, SpeedFromArm: 12,
					SpeedFromMovement: 4, SpeedFromWrist: 9, WristAlignToThrowDeg: 5,
					ThrowAlignToMovementDeg: 6, OffAxisPenalty: .2, ThrowMovePenalty: .3,
				},
				PlayerVelocity: model.Vec3{2, 0, 0}, PlayerSpeed: 2, AlignedMovementSpeed: 2,
				PlayerRelativeVelocity: model.Vec3{tc.speeds[0] - 2, 0, 0}, PlayerRelativeSpeed: tc.speeds[0] - 2,
				HandVelocity: model.Vec3{8, 0, 0}, HandSpeed: 8,
				HandRelativeVelocity: model.Vec3{6, 0, 0}, HandRelativeSpeed: 6,
				HandKinematicsValid: true, HandAttributionConfidence: .75, HandAttributionAnchor: "synthetic-held-hand",
				SpeedRatio: tc.speeds[0] / 8, EffectiveCap: 18.9, PingMs: 45,
			}
			event := mkEvent("THROW_001", "synthetic-player", "synthetic-match", 100, .2, .15)
			event.DetectorVersion = "2.0.0"
			event.FrameRangeStart, event.FrameRangeEnd, event.Timestamp = 100, 102, 5
			event.CausalKey = model.CausalKey{PlayerID: event.PlayerID, FrameStart: 100, FrameEnd: 102, AnomalyType: "disc_speed"}
			event.IsShadow, event.AutoEnforce, event.EnforcementWeight = true, false, 0
			event.Evidence = want
			mustStoreEvent(t, store, event)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened := newTestStoreAt(t, dbPath)
			events, err := reopened.GetMatchPlayerEvents(ctx, event.MatchID, event.PlayerID)
			if err != nil || len(events) != 1 {
				t.Fatalf("reopened events=%d, error=%v", len(events), err)
			}
			got := events[0]
			if got.EventID != event.EventID || got.DetectorVersion != event.DetectorVersion || got.FrameIndex != 100 ||
				got.FrameRangeStart != 100 || got.FrameRangeEnd != 102 || got.Timestamp != 5 ||
				got.CausalKey != event.CausalKey || !got.IsShadow || got.AutoEnforce || got.EnforcementWeight != 0 {
				t.Fatalf("release identity or review-only fields changed after reopen: %+v", got)
			}
			assertEvidence := func(label string, evidence model.Evidence) {
				t.Helper()
				throw, ok := evidence.(model.ThrowEvidence)
				if !ok || !reflect.DeepEqual(throw, want) {
					t.Fatalf("%s: typed raw measurements, review or local report changed\ngot: %#v\nwant: %#v", label, evidence, want)
				}
				for i, sample := range throw.SpeedReview.Samples {
					if sample.BounceCount == nil || *sample.BounceCount != 0 || sample.Source == nil || sample.Source.SourceID != "" {
						t.Fatalf("%s: sample %d lost observed zero/source provenance: %+v", label, i, sample)
					}
				}
			}
			assertEvidence("store reader", got.Evidence)

			var payload, evidenceType string
			if err := reopened.DB().QueryRowContext(ctx,
				`SELECT evidence_json, evidence_type FROM detection_events WHERE event_id = ?`, event.EventID).Scan(&payload, &evidenceType); err != nil {
				t.Fatal(err)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal([]byte(payload), &envelope); err != nil || evidenceType != "throw" || string(envelope["type"]) != `"throw"` {
				t.Fatalf("typed evidence discriminator missing: type=%q payload=%s error=%v", evidenceType, payload, err)
			}
			decoded, err := model.DecodeEvidence([]byte(payload))
			if err != nil {
				t.Fatal(err)
			}
			assertEvidence("model registry decoder", decoded)
			assertEvidence("storage typed decoder", DecodeEvidence(evidenceType, payload))
		})
	}
}
