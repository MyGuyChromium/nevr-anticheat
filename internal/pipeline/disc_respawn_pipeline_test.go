package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/throw"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// respawnFrames is a goal cycle for one player: live play with the disc flying
// into the goal, then (from jumpFrame on, in jumpPhase) the disc sitting on a
// team's nest, where the game put it between two samples.
func respawnFrames(n, jumpFrame int, jumpPhase string) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, n)
	for i := range frames {
		f := qualityFrame(i)
		f.Disc = &model.DiscState{Position: model.Vec3{0.4, 1.2, 30 + 0.4*float64(i)}, Velocity: model.Vec3{0, 0, 6}, Speed: 6}
		if i >= jumpFrame {
			f.GamePhase = jumpPhase
			f.Disc = &model.DiscState{Position: model.Vec3{0, 4.536, -27.5}}
		}
		frames[i] = f
	}
	return frames
}

// The frame loop names the game's disc respawn in the disc detectors' traces.
// It is never an anomaly: no event, no sanitised or rejected frame, no score.
func TestPipelineTracesServerRespawnAndNeverFlagsIt(t *testing.T) {
	detectors := func() []detect.Detector {
		return []detect.Detector{state.NewState001(nil), state.NewState008(nil), throw.NewThrow001(nil)}
	}
	const jumpFrame = 12

	p, _ := newPipeline(config.DefaultConfig(), detectors())
	result, err := p.ProcessMatch(context.Background(), matchCtx("P1"), respawnFrames(30, jumpFrame, "round_over"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DetectionEvents) != 0 || len(result.ReviewCases) != 0 {
		t.Fatalf("a server respawn produced findings: %+v", result.DetectionEvents)
	}
	if result.InvalidFrames != 0 || len(result.SanitizedFrames) != 0 {
		t.Fatalf("a server respawn was treated as bad data: invalid=%d sanitized=%v", result.InvalidFrames, result.SanitizedFrames)
	}
	if score := result.PlayerScores["P1"].TotalScore; score != 0 {
		t.Fatalf("a server respawn scored %.2f", score)
	}
	traced := 0
	for _, d := range result.PlayerCoverage["P1"].Detectors {
		if d.DecisionTrace == nil {
			continue
		}
		for _, r := range d.DecisionTrace.Reasons {
			if r.Code != DiscJumpServerRespawn {
				continue
			}
			traced++
			if r.Count != 1 || r.FirstFrame != jumpFrame || r.LastFrame != jumpFrame {
				t.Errorf("%s: respawn traced as %+v, want once at frame %d", d.DetectorID, r, jumpFrame)
			}
		}
	}
	if traced != 3 {
		t.Fatalf("server_respawn traced on %d of the 3 disc detectors", traced)
	}

	// The same landing during live play is not the game's choreography and is
	// left unclassified.
	p, _ = newPipeline(config.DefaultConfig(), detectors())
	result, err = p.ProcessMatch(context.Background(), matchCtx("P1"), respawnFrames(30, jumpFrame, "playing"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range result.PlayerCoverage["P1"].Detectors {
		if d.DecisionTrace == nil {
			continue
		}
		for _, r := range d.DecisionTrace.Reasons {
			if r.Code == DiscJumpServerRespawn {
				t.Fatalf("%s traced a respawn during live play: %+v", d.DetectorID, r)
			}
		}
	}
}

// The trace code has its own reviewer-facing sentence, and that sentence does
// not read as a finding.
func TestServerRespawnReasonText(t *testing.T) {
	text := detect.DecisionReasonDescription(DiscJumpServerRespawn)
	if text == detect.DecisionReasonDescription("no_such_reason_code") {
		t.Fatalf("server_respawn has no description: %q", text)
	}
	if !strings.Contains(text, "not a finding") || !strings.Contains(text, "nest") {
		t.Fatalf("description does not say what happened and that it is not a finding: %q", text)
	}
}
