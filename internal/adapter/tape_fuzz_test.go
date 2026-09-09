package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
	"google.golang.org/protobuf/proto"
)

// FuzzNativeTapeRaw mutates the retained protobuf bytes directly so malformed
// messages reach the native decoder instead of stopping at base64 syntax. The
// same inputs also exercise malformed JSON envelopes and the timestamp helper.
// All seeds are synthetic; this does not validate detector accuracy.
func FuzzNativeTapeRaw(f *testing.F) {
	header, err := proto.Marshal(tapeTestHeader())
	if err != nil {
		f.Fatal(err)
	}
	frame, err := proto.Marshal(tapeTestFrame(0, tapeTestGrab(1, "none", "disc")))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(header, frame)
	f.Add(header, []byte{})
	f.Add([]byte{}, frame)
	f.Add([]byte(`{"_nevr_tape":null}`), []byte("\x80"))
	for _, cut := range []int{1, len(frame) / 2, len(frame) - 1} {
		f.Add(header, frame[:cut])
	}
	f.Fuzz(func(t *testing.T, headerBytes, frameBytes []byte) {
		if len(headerBytes)+len(frameBytes) > fuzzReplayLimit {
			t.Skip()
		}
		data, err := json.Marshal(map[string]any{"_nevr_tape": TapeRawRecord{Version: 1, Header: headerBytes, Frame: frameBytes}})
		if err != nil {
			t.Fatal(err)
		}
		d := NewTapeRawDecoder()
		d.maxRecordBytes = 64 << 10
		d.maxNormalizedFrames = 128
		tick, err := d.Decode(string(data))
		if err == nil {
			if tick == nil || tick.MatchCtx == nil || tick.MatchCtx.Source != "tape" || tick.MatchCtx.NativeCapture == nil {
				t.Fatal("successful native decode omitted source/provenance")
			}
			timestamp, err := NativeTapeSampleTime(tick.RawJSON)
			if err != nil || !timestamp.Equal(tick.SampleTime) {
				t.Fatalf("native time changed during projection: %v", err)
			}
			if _, err := d.Decode(string(data)); err == nil {
				t.Fatal("duplicate native record was accepted")
			}
		} else if _, err := d.Decode(string(data)); err == nil {
			t.Fatal("decoder recovered silently after rejected native record")
		}
		_, _ = NativeTapeSampleTime(string(data))
		_, _ = NewTapeRawDecoder().Decode(string(frameBytes))
		_, _ = NativeTapeSampleTime(string(headerBytes))
	})
}

// FuzzNativeTapeContainer exercises actual file dispatch, compression preflight,
// the pinned codec, and mapping together. Limits stay enabled. Truncation seeds
// include the magic/header, mid-stream envelopes, and footer/checksum boundary.
func FuzzNativeTapeContainer(f *testing.F) {
	dir := f.TempDir()
	path := filepath.Join(dir, "fuzz.tape")
	if err := testutil.WriteNativeTapeFixture(path); err != nil {
		f.Fatal(err)
	}
	seed, err := os.ReadFile(path)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte("not a tape"))
	f.Add([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x04, 0xff})
	for _, cut := range []int{0, 1, 3, 4, 5, 8, 17, len(seed) / 2, len(seed) - 4, len(seed) - 1} {
		f.Add(seed[:cut])
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzReplayLimit {
			t.Skip()
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		parser := NewEchoReplayParser()
		parser.SetMaxLineBytes(64 << 10)
		parser.SetMaxReplayBytes(fuzzReplayLimit)
		ticks := 0
		ctx, _, err := parser.ParseFileStream(path, func(tick *ParsedTick) error {
			if tick == nil || tick.MatchCtx == nil || tick.MatchCtx.Source != "tape" || tick.FrameIndex != ticks {
				t.Fatal("native container delivered invalid tick/source/index")
			}
			ticks++
			return nil
		})
		if err == nil && (ticks == 0 || ctx == nil || ctx.NativeCapture == nil || ctx.NativeCapture.ContainerIntegrity == "not_verified_from_records") {
			t.Fatal("successful container import lacked completed integrity check")
		}
	})
}
