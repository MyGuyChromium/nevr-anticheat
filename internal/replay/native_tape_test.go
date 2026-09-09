package replay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	capture "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
	"google.golang.org/protobuf/proto"
)

func nativeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.tape")
	if err := testutil.WriteNativeTapeFixture(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeTapeEngineReanalysisUsesOriginalProtobuf(t *testing.T) {
	ctx := context.Background()
	engine := newTestEngine(t)
	path := nativeFixture(t)
	res, err := engine.AnalyzeFile(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.PersistError(); err != nil {
		t.Fatal(err)
	}
	if res.MatchCtx.Source != "tape" || res.MatchCtx.NativeCapture == nil || res.MatchCtx.NativeCapture.ContainerIntegrity != "verified_footer_and_checksum" {
		t.Fatalf("missing native provenance: %+v", res.MatchCtx)
	}
	before, err := engine.Store().GetMatchFrames(ctx, testutil.NativeTapeMatchID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := engine.Store().GetMatchRawTicks(ctx, testutil.NativeTapeMatchID, 0, testutil.NativeTapeFrames)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != testutil.NativeTapeFrames {
		t.Fatalf("raw ticks = %d", len(raw))
	}
	for _, value := range raw {
		if !adapter.IsTapeRawJSON(value) {
			t.Fatal("native bytes not retained")
		}
	}
	remapped, err := RemapStoredTelemetry(ctx, engine.Store(), res.MatchCtx, engine.Physics())
	if err != nil {
		t.Fatal(err)
	}
	if len(remapped.Frames) != len(before) || remapped.RawTicks != testutil.NativeTapeFrames {
		t.Fatalf("remap counts = %d/%d", len(remapped.Frames), remapped.RawTicks)
	}
	for i, f := range remapped.Frames {
		if f.Observation == nil || f.Observation.Source != "tape" || f.Observation.SourceID != testutil.NativeTapeCaptureID || f.Timestamp != before[i].Timestamp || f.LeftHandRotation != before[i].LeftHandRotation {
			t.Fatalf("native source/pose/time lost at %d: %+v", i, f)
		}
	}
	if remapped.Context.NativeCapture == res.MatchCtx.NativeCapture {
		t.Fatal("context clone shares native metadata")
	}
	remapped.Context.NativeCapture.Limitations[0] = "changed clone"
	if res.MatchCtx.NativeCapture.Limitations[0] == "changed clone" {
		t.Fatal("context clone shares limitations")
	}
	if _, err := engine.AnalyzeFile(ctx, path, true); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Store().GetMatchRawTicks(ctx, testutil.NativeTapeMatchID, 0, testutil.NativeTapeFrames)
	if err != nil || !reflect.DeepEqual(raw, after) {
		t.Fatalf("duplicate import changed evidence: %v", err)
	}
}

func TestNativeTapeCorruptionCannotReplaceStoredAnalysis(t *testing.T) {
	ctx := context.Background()
	engine := newTestEngine(t)
	path := nativeFixture(t)
	res, err := engine.AnalyzeFile(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	before, err := engine.Store().GetMatchRawTicks(ctx, res.MatchCtx.MatchID, 0, testutil.NativeTapeFrames)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)-7], 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.AnalyzeFile(ctx, path, true); err == nil {
		t.Fatal("truncated tape reported success")
	}
	after, err := engine.Store().GetMatchRawTicks(ctx, res.MatchCtx.MatchID, 0, testutil.NativeTapeFrames)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("corrupt reimport changed evidence: %v", err)
	}
}

func TestNativeTapeReanalysisNeverFallsBackWhenRawMissing(t *testing.T) {
	engine := newTestEngine(t)
	for _, mc := range []*model.MatchContext{{MatchID: "missing", Source: "tape"}, {MatchID: "missing", NativeCapture: &model.NativeCapture{CaptureID: "capture"}}} {
		_, err := RemapStoredTelemetry(context.Background(), engine.Store(), mc, engine.Physics())
		if err == nil || errors.Is(err, ErrNoRawTicks) {
			t.Fatalf("native missing evidence allowed normalized fallback: %v", err)
		}
	}
}

func TestNativeTapeBatchAndExtensionRouting(t *testing.T) {
	for _, path := range []string{"capture.tape", "capture.TAPE", "capture.echoreplay"} {
		if !IsSessionRecording(path) {
			t.Fatalf("not routed: %s", path)
		}
	}
	for _, path := range []string{"capture.json", "capture.nevrcap", "capture.tape.exe"} {
		if IsSessionRecording(path) {
			t.Fatalf("unexpected routing: %s", path)
		}
	}
	path := nativeFixture(t)
	store := testStore(t)
	ba := NewBatchAnalyzer(emptyPipeline(), store, func() FrameParser { return NewJSONFrameParser() }, 1, quietLogger())
	ba.SetPipelineFactory(emptyPipeline)
	res, err := ba.AnalyzeDirectory(context.Background(), filepath.Dir(path))
	if err != nil || res.Processed != 1 || res.Errors != 0 || res.FramesInserted != testutil.NativeTapeFrames {
		t.Fatalf("native batch=%+v err=%v", res, err)
	}
}

func TestNativeTapeEventOnlyTicksKeepCaptureClockOnRebuild(t *testing.T) {
	ctx := context.Background()
	p := adapter.NewEchoReplayParser()
	mc, _, _, err := p.ParseFile(nativeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	raw := p.RawSessionByFrame()
	// Construct a new synthetic records-only capture: initial event-only tick,
	// nonzero initial capture offset, then ordinary player samples.
	for i, text := range raw {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(text), &fields); err != nil {
			t.Fatal(err)
		}
		var record adapter.TapeRawRecord
		if err := json.Unmarshal(fields["_nevr_tape"], &record); err != nil {
			t.Fatal(err)
		}
		frame := new(capture.Frame)
		if err := proto.Unmarshal(record.Frame, frame); err != nil {
			t.Fatal(err)
		}
		frame.TimestampOffsetMs += 500
		if i == 0 {
			frame.GetEchoArena().Players = nil
		}
		record.Frame, err = proto.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		fields["_nevr_tape"], err = json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		// The native decoder, not this deliberately false projection, is truth.
		fields["blue_points"] = json.RawMessage("999")
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		raw[i] = string(data)
	}
	store := testStore(t)
	if _, err := store.StoreTelemetryFramesWithRaw(ctx, mc.MatchID, nil, raw); err != nil {
		t.Fatal(err)
	}
	remapped, err := RemapStoredTelemetry(ctx, store, mc, model.DefaultPhysics())
	if err != nil {
		t.Fatal(err)
	}
	if len(remapped.Frames) != testutil.NativeTapeFrames-1 || remapped.Frames[0].FrameIndex != 1 || remapped.Frames[0].Timestamp != .533 {
		t.Fatalf("event-only frame/time lost: %+v", remapped.Frames)
	}
	summary, err := RebuildSummary(ctx, store, mc)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Ticks != testutil.NativeTapeFrames || summary.BlueScore == 999 || summary.DurationSeconds != .863 {
		t.Fatalf("native summary used projection/fallback clock: %+v", summary)
	}
	if remapped.Context.Duration != 863*time.Millisecond {
		t.Fatalf("native duration=%v", remapped.Context.Duration)
	}
}

func TestNativeTapeStoredContextCannotDisagreeWithCapture(t *testing.T) {
	ctx := context.Background()
	engine := newTestEngine(t)
	res, err := engine.AnalyzeFile(ctx, nativeFixture(t), true)
	if err != nil {
		t.Fatal(err)
	}
	mc := cloneMatchContext(res.MatchCtx)
	mc.NativeCapture.CaptureID = "different-source"
	if _, err := RemapStoredTelemetry(ctx, engine.Store(), mc, engine.Physics()); err == nil || !strings.Contains(err.Error(), "capture identity") {
		t.Fatalf("wrong-source remap=%v", err)
	}
	if _, err := RebuildSummary(ctx, engine.Store(), mc); err == nil {
		t.Fatal("wrong-source summary accepted")
	}
}
