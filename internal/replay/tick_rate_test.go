package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func tickRateFrames(spacings []float64, playersPerTick int) []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	t := 0.0
	for i := 0; i <= len(spacings); i++ {
		if i > 0 {
			t += spacings[i-1]
		}
		for p := 0; p < playersPerTick; p++ {
			frames = append(frames, model.PlayerTelemetryFrame{PlayerID: fmt.Sprintf("p%d", p), FrameIndex: i, Timestamp: t})
		}
	}
	return frames
}

func TestMeasuredTickRateIsTheMedianPositiveSpacing(t *testing.T) {
	repeat := func(dt float64, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = dt
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		spacings []float64
		want     float64
	}{
		{"15 Hz", repeat(1/15.2, 300), 15.2},
		{"20 Hz", repeat(0.05, 300), 20},
		{"30 Hz", repeat(0.033, 300), 30.3},
		{"stalls and a stepped clock do not move the median", append(append(repeat(0.05, 200), repeat(2.5, 20)...), repeat(0, 40)...), 20},
		{"too short to state a rate", repeat(0.05, minTickRateSamples-1), 0},
		{"no positive spacing", repeat(0, 50), 0},
		{"non-finite spacings are ignored", append(repeat(0.05, 50), math.NaN(), math.Inf(1)), 20},
		{"implausible rate is not stated", repeat(0.0001, 50), 0},
	} {
		if got := measuredTickRate(tickRateFrames(tc.spacings, 4)); math.Abs(got-tc.want) > 0.011 {
			t.Errorf("%s: measured %.2f Hz, want %.2f", tc.name, got, tc.want)
		}
	}
	if got := measuredTickRate(nil); got != 0 {
		t.Errorf("no frames measured %.2f Hz", got)
	}

	mc := &model.MatchContext{TickRate: 15}
	applyMeasuredTickRate(mc, tickRateFrames(repeat(0.05, 5), 1))
	if mc.TickRate != 15 {
		t.Errorf("an unmeasurable recording must keep the adapter's context, got %.2f", mc.TickRate)
	}
	applyMeasuredTickRate(mc, tickRateFrames(repeat(0.05, 50), 2))
	if mc.TickRate != 20 {
		t.Errorf("measured rate not applied: %.2f", mc.TickRate)
	}
	applyMeasuredTickRate(nil, tickRateFrames(repeat(0.05, 50), 2)) // must not panic
}

// The measured rate reaches the pipeline's match context and is persisted
// with the match, for a recording at each of the three real capture rates.
func TestAnalyzeReplayStoresMeasuredTickRate(t *testing.T) {
	for _, tc := range []struct {
		stepMs int
		want   float64
	}{{66, 15.15}, {50, 20}, {33, 30.3}} {
		var lines strings.Builder
		start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
		id := fmt.Sprintf("TICK-%d", tc.stepMs)
		for frame := 0; frame < 60; frame++ {
			var session adapter.EchoVRSessionResponse
			if err := json.Unmarshal([]byte(remapSession(t, frame, "none")), &session); err != nil {
				t.Fatal(err)
			}
			session.SessionID = id
			session.GameClock = 100 - float64(frame)*float64(tc.stepMs)/1000
			session.Disc.Velocity = [3]float64{}
			encoded, err := json.Marshal(session)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&lines, "%s\t%s\n", start.Add(time.Duration(frame*tc.stepMs)*time.Millisecond).Format("2006/01/02 15:04:05.000"), encoded)
		}
		path := filepath.Join(t.TempDir(), "synthetic-rate.echoreplay")
		if err := os.WriteFile(path, []byte(lines.String()), 0600); err != nil {
			t.Fatal(err)
		}
		engine := newTestEngine(t)
		result, err := engine.AnalyzeFile(context.Background(), path, false)
		if err != nil || result.PersistError() != nil {
			t.Fatalf("%d ms: analysis failed: %v / %v", tc.stepMs, err, result.PersistError())
		}
		if math.Abs(result.MatchCtx.TickRate-tc.want) > 0.011 {
			t.Errorf("%d ms spacing: analysed at %.2f Hz, want %.2f", tc.stepMs, result.MatchCtx.TickRate, tc.want)
		}
		stored, err := engine.Store().GetMatchContext(context.Background(), result.MatchCtx.MatchID)
		if err != nil || stored == nil {
			t.Fatalf("read stored context: %v", err)
		}
		if math.Abs(stored.TickRate-tc.want) > 0.011 {
			t.Errorf("%d ms spacing: stored %.2f Hz, want %.2f", tc.stepMs, stored.TickRate, tc.want)
		}
	}
}
