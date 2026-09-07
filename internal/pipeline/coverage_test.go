package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestCoverageUsesActualWarmupPhaseAndWristReachability(t *testing.T) {
	for _, test := range []struct {
		name, phase string
		count       int
		dt          float64
		wantInputs  bool
	}{
		{"15Hz unreachable", "playing", 30, 1.0 / 15, false},
		{"30Hz necessary inputs", "playing", 30, 1.0 / 30, true},
		{"round transition", "round_start", 30, 1.0 / 30, false},
		{"short clip", "playing", 3, 1.0 / 30, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			p, _ := newPipeline(cfg, []detect.Detector{bio.NewBio001(nil)})
			frames := make([]model.PlayerTelemetryFrame, test.count)
			for i := range frames {
				frames[i] = qualityFrame(i)
				frames[i].Timestamp = float64(i) * test.dt
				frames[i].DeltaTime = test.dt
				frames[i].GamePhase = test.phase
			}
			before, _ := json.Marshal(frames)
			got, err := p.ProcessMatch(context.Background(), matchCtx("P1", "absent"), frames)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(frames)
			if string(before) != string(after) {
				t.Fatal("coverage mutated caller input")
			}
			if got.PlayerCoverage["absent"].Status != "insufficient_data" {
				t.Fatal("absent player declared observable")
			}
			pc := got.PlayerCoverage["P1"]
			if !test.wantInputs && pc.Status != "insufficient_data" {
				t.Fatalf("no observable detector incorrectly marked player limited: %+v", pc)
			}
			for _, d := range pc.Detectors {
				if d.DetectorID == "BIO_001" {
					if (d.InputFrames > 0) != test.wantInputs {
						t.Fatalf("coverage=%+v", d)
					}
					if test.phase != "playing" || test.count <= 5 {
						if d.CandidateFrames != 0 || pc.Status != "insufficient_data" {
							t.Fatalf("warmup/phase ignored: %+v", pc)
						}
					}
				} else if d.Enabled || d.Status != "disabled" {
					t.Fatalf("unbuilt detector marked enabled: %+v", d)
				}
			}
		})
	}
}

func TestCoverageQualityGateAppliesToEveryDetector(t *testing.T) {
	c := newCoverageTracker([]detect.Detector{bio.NewBio001(nil)}, config.DefaultConfig(), []string{"P"})
	c.player("P").ValidFrames = 30
	c.candidate("BIO_001", &model.PlayerState{PlayerID: "P", FrameDt: 1.0 / 30, LeftHandRotHistory: []model.Quat{model.QuatIdentity(), model.QuatIdentity()}}, 20)
	p := c.finish(TelemetryQualityReport{Gated: true})["P"]
	if p.Status != "insufficient_data" {
		t.Fatal("source quality gate lost")
	}
	for _, d := range p.Detectors {
		if d.Enabled && d.Status != "insufficient_data" {
			t.Fatalf("detector bypassed quality gate: %+v", d)
		}
	}
}

func TestCoverageTracksRejectedFramesAndResetsPerMatch(t *testing.T) {
	p, _ := newPipeline(config.DefaultConfig(), []detect.Detector{bio.NewBio001(nil)})
	frame := qualityFrame(0)
	frame.Position = model.Vec3{}
	r, err := p.ProcessMatch(context.Background(), matchCtx("P1"), []model.PlayerTelemetryFrame{frame})
	if err != nil || r.PlayerCoverage["P1"].RejectedFrames != 1 || r.PlayerCoverage["P1"].Status != "insufficient_data" {
		t.Fatalf("invalid coverage=%+v err=%v", r, err)
	}
	r, err = p.ProcessMatch(context.Background(), matchCtx("P1"), []model.PlayerTelemetryFrame{qualityFrame(0)})
	if err != nil || r.PlayerCoverage["P1"].RejectedFrames != 0 {
		t.Fatal("coverage leaked between matches")
	}
}
