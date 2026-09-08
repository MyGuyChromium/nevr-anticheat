package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

// This deterministic fixture exercises the actual timestamped JSON parser,
// mapper, live tick assembler, feature extractor and SQLite JSON round trip.
// It tests implementation invariants, not independent detector accuracy.
type replayPerturbation struct {
	anomalous, rigid, rename, jitter, loss, trackingLoss, clockReset bool
	timeShift                                                        time.Duration
}

func readPerturbedReplay(t *testing.T, options replayPerturbation) []model.PlayerTelemetryFrame {
	t.Helper()
	rotate := func(v [3]float64) [3]float64 {
		if options.rigid {
			return [3]float64{-v[1], v[0], v[2]}
		}
		return v
	}
	position := func(v [3]float64) [3]float64 {
		v = rotate(v)
		if options.rigid {
			v[0] += 1
			v[1] += 1
			v[2] += 2
		}
		return v
	}
	pose := func(pos [3]float64, angle float64) adapter.EchoVRBodyHead {
		return adapter.EchoVRBodyHead{Position: position(pos), Forward: rotate([3]float64{math.Sin(angle), 0, math.Cos(angle)}), Left: rotate([3]float64{math.Cos(angle), 0, -math.Sin(angle)}), Up: rotate([3]float64{0, 1, 0})}
	}
	var lines strings.Builder
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Add(options.timeShift)
	for i := 0; i < 100; i++ {
		if options.loss && i >= 45 && i <= 48 {
			continue
		}
		ms := i * 67
		if options.jitter {
			ms += int(8 * math.Sin(float64(i)*.71))
		}
		ts := start.Add(time.Duration(ms) * time.Millisecond)
		if options.clockReset && i >= 45 {
			ts = ts.Add(-time.Hour)
		}
		players := make([]adapter.EchoVRPlayer, 2)
		for j := range players {
			name := fmt.Sprintf("Synthetic-%d", j)
			if options.rename {
				name = fmt.Sprintf("renamed-%d-\"<>unicode-λ", j)
			}
			x := 1 + .02*float64(i)
			if options.anomalous && j == 0 {
				x = float64(i%3)*5 - 5
			}
			if j == 1 {
				x = -3
			}
			p := adapter.EchoVRPlayer{Name: name, UserID: int64(1<<53) + int64(j) + 1, HoldingLeft: "none", HoldingRight: "none", Ping: 35, Velocity: rotate([3]float64{.02 / .067, 0, 0})}
			p.Body = pose([3]float64{x, 2, 3 + float64(j)*5}, 0)
			p.Head = pose([3]float64{x, 2.2, 3 + float64(j)*5}, 0)
			left := pose([3]float64{x - .3, 2.3, 3.2 + float64(j)*5}, float64(i)*.1)
			right := pose([3]float64{x + .3, 2.3, 2.8 + float64(j)*5}, float64(i)*.1)
			p.LHand = adapter.EchoVRHand{Position: left.Position, Forward: left.Forward, Left: left.Left, Up: left.Up}
			p.RHand = adapter.EchoVRHand{Position: right.Position, Forward: right.Forward, Left: right.Left, Up: right.Up}
			if options.trackingLoss && i >= 40 && i <= 42 {
				p.LHand = adapter.EchoVRHand{}
			}
			players[j] = p
		}
		bounce := 0
		raw := adapter.EchoVRSessionResponse{SessionID: "perturbation-match", ClientName: players[0].Name, GameStatus: "playing", GameClock: 300 - float64(ms)/1000,
			Disc:  &adapter.EchoVRDisc{Position: position([3]float64{0, 0, 12}), Velocity: rotate([3]float64{}), BounceCount: &bounce},
			Teams: []adapter.EchoVRTeam{{TeamName: "BLUE TEAM", Players: players[:1]}, {TeamName: "ORANGE TEAM", Players: players[1:]}}}
		data, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&lines, "%s\t%s\n", ts.Format("2006/01/02 15:04:05.000"), data)
	}
	path := filepath.Join(t.TempDir(), "synthetic recorder's sample.echoreplay")
	if err := os.WriteFile(path, []byte(lines.String()), 0600); err != nil {
		t.Fatal(err)
	}
	parser := adapter.NewEchoReplayParser()
	var frames []model.PlayerTelemetryFrame
	_, _, err := parser.ParseFileStream(path, func(tick *adapter.ParsedTick) error { frames = append(frames, tick.Frames...); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return frames
}

func runPerturbedLive(t *testing.T, frames []model.PlayerTelemetryFrame, retries, negateQuaternion bool) ([]model.DetectionEvent, map[string]*model.PlayerState) {
	t.Helper()
	mm, store, _ := newTestManager(t)
	mm.detectorFn = func() []detect.Detector { return []detect.Detector{movement.NewMov001(nil), bio.NewBio001(nil)} }
	for i := 0; i < len(frames); i += 2 {
		batch := append([]model.PlayerTelemetryFrame(nil), frames[i:i+2]...)
		if negateQuaternion && i%4 == 0 {
			for j := range batch {
				for k := 0; k < 4; k++ {
					batch[j].Rotation[k] *= -1
					batch[j].LeftHandRotation[k] *= -1
					batch[j].RightHandRotation[k] *= -1
				}
			}
		}
		// Normalized JSON is another public input path; preserve provenance.
		wire, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		var decoded []model.PlayerTelemetryFrame
		if err = json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		got := mm.HandleFrames("perturbation-match", "", decoded)
		if got.Accepted != len(batch) || got.Rejected != 0 {
			t.Fatalf("tick%d: %+v", i/2, got)
		}
		if retries {
			got = mm.HandleFrames("perturbation-match", "", decoded)
			if got.Accepted != 0 || got.Ignored != len(batch) {
				t.Fatalf("retry tick%d: %+v", i/2, got)
			}
		}
	}
	match := mm.matches["perturbation-match"]
	mm.EndMatch("perturbation-match")
	loaded, err := store.GetMatchFrames(context.Background(), "perturbation-match")
	if err != nil || len(loaded) != len(frames) {
		t.Fatalf("storedframes=%d err=%v", len(loaded), err)
	}
	for i, f := range loaded {
		if !f.Observation.SameSource(frames[i].Observation) || f.Observation.FrameIndex != f.FrameIndex || f.Observation.Timestamp != f.Timestamp {
			t.Fatal("source epoch/provenance lost through storage")
		}
	}
	events, err := store.GetMatchEvents(context.Background(), "perturbation-match")
	if err != nil {
		t.Fatal(err)
	}
	return events, match.Players
}

func assertEquivalentReviewEvents(t *testing.T, want, got []model.DetectionEvent) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("eventcount want%d got%d", len(want), len(got))
	}
	for i, a := range want {
		b := got[i]
		if a.DetectorID != b.DetectorID || a.PlayerID != b.PlayerID || a.FrameIndex != b.FrameIndex || a.FrameRangeStart != b.FrameRangeStart || a.FrameRangeEnd != b.FrameRangeEnd || !reflect.DeepEqual(a.CausalKey, b.CausalKey) || a.IsShadow != b.IsShadow || a.EnforcementWeight != b.EnforcementWeight || a.AutoEnforce != b.AutoEnforce || math.Abs(a.Severity-b.Severity) > 1e-9 || math.Abs(a.Confidence-b.Confidence) > 1e-9 || a.Timestamp != b.Timestamp {
			t.Fatalf("event semantics changed at%d:\n%+v\n%+v", i, a, b)
		}
	}
}

func TestProductionReplaySceneTimeNameQuaternionAndRetryInvariance(t *testing.T) {
	base, _ := runPerturbedLive(t, readPerturbedReplay(t, replayPerturbation{anomalous: true}), false, false)
	if len(base) == 0 {
		t.Fatal("positive implementation fixture produced no events; invariance would be vacuous")
	}
	for _, tc := range []struct {
		name            string
		options         replayPerturbation
		retry, negative bool
	}{
		{"rigid_scene", replayPerturbation{anomalous: true, rigid: true}, false, false},
		{"name_and_wall_time", replayPerturbation{anomalous: true, rename: true, timeShift: 31 * 24 * time.Hour}, false, false},
		{"quaternion_double_cover", replayPerturbation{anomalous: true}, false, true},
		{"identical_network_retries", replayPerturbation{anomalous: true}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := runPerturbedLive(t, readPerturbedReplay(t, tc.options), tc.retry, tc.negative)
			assertEquivalentReviewEvents(t, base, got)
		})
	}
}

func TestProductionReplayLossJitterTrackingAndClockResetAbstain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options replayPerturbation
	}{
		{"loss", replayPerturbation{loss: true}}, {"jitter", replayPerturbation{jitter: true}},
		{"tracking_loss", replayPerturbation{trackingLoss: true}}, {"clock_reset", replayPerturbation{clockReset: true}},
		{"combined", replayPerturbation{loss: true, jitter: true, trackingLoss: true, clockReset: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := readPerturbedReplay(t, tc.options)
			if tc.options.clockReset {
				fe := pipeline.NewFeatureExtractor(400)
				ps := &model.PlayerState{PlayerID: frames[0].PlayerID, Team: "blue", LastFrameIdx: -1}
				mc := &model.MatchContext{MatchID: "perturbation-match", Physics: model.DefaultPhysics()}
				sawBoundary := false
				for _, f := range frames {
					if f.PlayerID != ps.PlayerID {
						continue
					}
					previous := ps.Observation.Clone()
					fe.UpdatePlayerState(ps, &f, mc)
					if previous != nil && previous.SourceEpoch != f.Observation.SourceEpoch {
						sawBoundary = true
						if ps.FrameDt != 0 || ps.Speed != 0 || ps.LeftWristAngularRateValid || ps.LastThrow != nil {
							t.Fatal("derived kinematics/release crossed clock epoch")
						}
					}
				}
				if !sawBoundary {
					t.Fatal("clock fixture did not reach epoch boundary")
				}
			}
			events, _ := runPerturbedLive(t, frames, true, false)
			if len(events) != 0 {
				t.Fatalf("degraded clean implementation fixture emitted%d events", len(events))
			}
		})
	}
}
