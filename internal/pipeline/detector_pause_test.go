package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestPipelineBlocksInjectedPlayspaceDetectorsAcrossRuns(t *testing.T) {
	cfg := config.DefaultConfig()
	ids := []string{"MOV_006", "PAT_005", "THROW_003", "BIO_001", "STATE_001", "STATE_008"}
	detectors := make([]detect.Detector, 0, len(ids))
	for _, id := range ids {
		dc := cfg.Detectors[id]
		dc.Enabled = true
		cfg.Detectors[id] = dc
		detectors = append(detectors, newRecorder(id, "test", 0))
	}
	for run := 0; run < 2; run++ {
		p, _ := newPipeline(cfg, detectors)
		if len(p.detectors) != 4 || detectors[0].ID() != "MOV_006" || detectors[1].ID() != "PAT_005" {
			t.Fatal("pipeline failed to filter pause or mutated caller slice")
		}
		result, err := p.ProcessMatch(context.Background(), matchCtx("P1"), []model.PlayerTelemetryFrame{cleanFrame("P1", 0), cleanFrame("P1", 1)})
		if err != nil {
			t.Fatal(err)
		}
		pausedCoverage := 0
		for _, d := range result.PlayerCoverage["P1"].Detectors {
			if !model.IsPlayspaceDetector(d.DetectorID) {
				continue
			}
			pausedCoverage++
			if d.Enabled || d.Status != "disabled" || d.CandidateFrames != 0 || !strings.Contains(strings.Join(d.Limitations, " "), "paused") {
				t.Fatalf("paused coverage was evaluated/misrepresented: %+v", d)
			}
		}
		if pausedCoverage != 2 {
			t.Fatalf("expected both paused detectors in coverage, got %d", pausedCoverage)
		}
		for _, event := range result.DetectionEvents {
			if model.IsPlayspaceDetector(event.DetectorID) {
				t.Fatal("paused detector emitted")
			}
		}
		for _, d := range p.detectors {
			if model.IsPlayspaceDetector(d.ID()) {
				t.Fatal("paused detector constructed")
			}
			if d.(*recorder).calls == 0 {
				t.Fatalf("focus detector %s was not evaluated", d.ID())
			}
		}
	}
}
