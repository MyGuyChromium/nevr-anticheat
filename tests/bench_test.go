package tests

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func BenchmarkPipeline_200Frames(b *testing.B) {
	h := testutil.NewBenchHarness().WithAllDetectors()
	frames := testutil.NewFrameBuilder("player1").
		WithStartPos(model.Vec3{5, 1.6, 0}).
		NormalMovingPlayer(200, 5.0)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.RunBench(frames)
	}
}

func BenchmarkPipeline_1000Frames(b *testing.B) {
	h := testutil.NewBenchHarness().WithAllDetectors()
	frames := testutil.NewFrameBuilder("player1").
		WithStartPos(model.Vec3{5, 1.6, 0}).
		NormalMovingPlayer(1000, 5.0)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.RunBench(frames)
	}
}

func BenchmarkDetectors_AllOnSingleFrame(b *testing.B) {
	cfg := config.DefaultConfig()
	for id, dc := range cfg.Detectors {
		dc.Mode = "enforce"
		cfg.Detectors[id] = dc
	}

	mc := testutil.NewMatchContext()
	players := map[string]*model.PlayerState{
		"p1": testutil.NewPlayerState("p1"),
	}
	players["p1"].Speed = 5.0
	players["p1"].FrameDt = 0.067

	detectors := testutil.BuildDetectors(cfg)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, d := range detectors {
			d.Evaluate(mc, players, 100)
		}
	}
}
