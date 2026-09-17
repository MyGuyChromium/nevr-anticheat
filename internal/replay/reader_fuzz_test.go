package replay

import (
	"bytes"
	"testing"
)

// FuzzLegacyJSONReplay protects the compatibility importer. It feeds the
// decoder from memory: a per-exec os.WriteFile made every exec (and every
// minimisation step) wait on the filesystem, which stalled the engine. The
// file path itself stays covered by TestLegacyJSONLoadMatchesOpen.
func FuzzLegacyJSONReplay(f *testing.F) {
	f.Add([]byte(`{"header":{"MatchID":"fuzz"},"frames":[]}`))
	f.Add([]byte(`{"header":{},"frames":[{"Index":0,"Players":[]}]}`))
	f.Add([]byte(`{"frames":[{"Players":[{},{},{}]},{"Players":[{"player_id":"a","left_hand_position":[1,2,3]}],"Disc":{"possessor_id":"a"}}]}`))
	const maxFrames = 96
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256<<10 {
			t.Skip()
		}
		parser := NewJSONFrameParser()
		parser.SetMaxBytes(256 << 10)
		parser.maxFrames = maxFrames
		if err := parser.load(bytes.NewReader(data)); err != nil {
			if parser.header != nil || parser.frames != nil {
				t.Fatalf("a failed load kept data: %v", err)
			}
			return
		}
		if len(parser.frames) > maxFrames {
			t.Fatalf("%d frames accepted, limit %d", len(parser.frames), maxFrames)
		}
		for i := range parser.frames {
			if n := len(parser.frames[i].Players); n > MaxLegacyReplayPlayersPerFrame {
				t.Fatalf("frame %d holds %d players, limit %d", i, n, MaxLegacyReplayPlayersPerFrame)
			}
		}
		_, frames, err := NewReplayReader("fuzz.json", parser).readOpened()
		if err != nil {
			t.Fatalf("an accepted document failed to convert: %v", err)
		}
		if len(frames) > maxFrames {
			t.Fatalf("%d per-player frames produced, limit %d", len(frames), maxFrames)
		}
	})
}
