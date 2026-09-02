package tests

import (
	"fmt"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func benchmarkMultiPlayer(b *testing.B, playerCount, frameCount int) {
	b.Helper()
	var allFrames []model.PlayerTelemetryFrame
	for p := 0; p < playerCount; p++ {
		pid := fmt.Sprintf("player%d", p)
		fb := testutil.NewFrameBuilder(pid).
			WithStartPos(model.Vec3{float64(p%5)*3 - 6, 1.6, float64(p/5)*3 - 3})
		frames := fb.NormalMovingPlayer(frameCount, 3.0+float64(p%4))
		allFrames = append(allFrames, frames...)
	}

	h := testutil.NewBenchHarness()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.MustRunBench(b, allFrames)
	}
}

func BenchmarkPipeline_4Players_1000Frames(b *testing.B) {
	benchmarkMultiPlayer(b, 4, 1000)
}

func BenchmarkPipeline_8Players_1000Frames(b *testing.B) {
	benchmarkMultiPlayer(b, 8, 1000)
}

func BenchmarkPipeline_10Players_2000Frames(b *testing.B) {
	benchmarkMultiPlayer(b, 10, 2000)
}

// BenchmarkPushHistory_LongMatch measures PushHistory over 10000 frames
// to evaluate whether copy-trim or ring buffer is better.
func BenchmarkPushHistory_10000Frames(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var hist []model.Vec3
		for f := 0; f < 10000; f++ {
			model.PushVec3History(&hist, model.Vec3{float64(f), 0, 0}, 30)
		}
	}
}

// BenchmarkPushHistoryFloat_10000Frames same for float64.
func BenchmarkPushHistoryFloat_10000Frames(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var hist []float64
		model.PushFloat64History(&hist, 0, 30) // pre-grow
		for f := 0; f < 10000; f++ {
			model.PushFloat64History(&hist, float64(f), 30)
		}
	}
}
