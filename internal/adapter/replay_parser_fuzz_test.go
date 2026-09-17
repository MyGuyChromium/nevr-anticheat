package adapter

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"testing"
)

const fuzzReplayLimit = 1 << 20

// fuzzMatchFrameLimit is the per-match frame limit the replay fuzz targets
// run with, low enough for mutated inputs to reach it.
const fuzzMatchFrameLimit = 256

const (
	fuzzSeedLobbyLine = "2026/03/01 12:00:00.000\t{\"sessionid\":\"fuzz\",\"game_status\":\"playing\",\"teams\":[]}\n"
	// One player id twice in a snapshot: the second entry must be dropped.
	fuzzSeedDuplicateLine = "2026/03/01 12:00:00.033\t{\"sessionid\":\"fuzz\",\"teams\":[{\"team\":\"BLUE TEAM\",\"players\":[" +
		"{\"userid\":1,\"body\":{\"position\":[1,2,3]}},{\"userid\":1,\"body\":{\"position\":[2,2,3]}}]}]}\n"
)

func newFuzzReplayParser() *EchoReplayParser {
	parser := NewEchoReplayParser()
	parser.SetMaxLineBytes(64 << 10)
	parser.SetMaxReplayBytes(fuzzReplayLimit)
	parser.maxNormalizedFrames = fuzzMatchFrameLimit
	return parser
}

// fuzzTickInvariants returns a tick callback asserting what the parser
// promises about untrusted recordings: bounded rosters, at most one frame per
// player and tick, and a bounded number of frames and players per match.
func fuzzTickInvariants(t *testing.T) func(*ParsedTick) error {
	matchFrames := 0
	matchPlayers := map[string]struct{}{}
	return func(tick *ParsedTick) error {
		if tick == nil || tick.MatchCtx == nil {
			t.Fatal("parser delivered a nil tick or a tick without match context")
		}
		if tick.NewMatch {
			matchFrames, matchPlayers = 0, map[string]struct{}{}
		}
		if len(tick.Frames) > MaxSnapshotPlayers {
			t.Fatalf("tick carries %d frames, limit %d", len(tick.Frames), MaxSnapshotPlayers)
		}
		inTick := make(map[string]struct{}, len(tick.Frames))
		for i := range tick.Frames {
			f := &tick.Frames[i]
			if f.FrameIndex != tick.FrameIndex {
				t.Fatalf("frame index %d inside tick %d", f.FrameIndex, tick.FrameIndex)
			}
			if _, dup := inTick[f.PlayerID]; dup {
				t.Fatalf("player %q has two frames in tick %d", f.PlayerID, tick.FrameIndex)
			}
			inTick[f.PlayerID] = struct{}{}
			matchPlayers[f.PlayerID] = struct{}{}
		}
		matchFrames += len(tick.Frames)
		if matchFrames > fuzzMatchFrameLimit || len(matchPlayers) > MaxMatchPlayers {
			t.Fatalf("match grew to %d frames (limit %d) and %d players (limit %d)",
				matchFrames, fuzzMatchFrameLimit, len(matchPlayers), MaxMatchPlayers)
		}
		return nil
	}
}

// FuzzEchoReplayNDJSON protects the timestamp/NDJSON/session mapping boundary.
// The production parser is intentionally exercised with its streaming limits
// enabled so this target also guards against accidental unbounded allocation.
func FuzzEchoReplayNDJSON(f *testing.F) {
	f.Add([]byte(fuzzSeedLobbyLine))
	f.Add([]byte(fuzzSeedLobbyLine + fuzzSeedDuplicateLine))
	f.Add([]byte("not a replay\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzReplayLimit {
			t.Skip()
		}
		_, _, _ = newFuzzReplayParser().parseReader(bytes.NewReader(data), "fuzz.echoreplay", fuzzTickInvariants(t))
	})
}

// FuzzEchoReplayZIP protects ZIP discovery, entry selection, decompression
// limits, and the nested NDJSON parser as one end-to-end input boundary. It
// runs in memory: writing a file per exec (and per minimisation step) made the
// fuzzing engine wait on the filesystem and stall at 0 execs/sec. The on-disk
// dispatch (isZipFile, zip.OpenReader) stays covered by the parser tests.
func FuzzEchoReplayZIP(f *testing.F) {
	var seed bytes.Buffer
	zw := zip.NewWriter(&seed)
	w, err := zw.Create("seed.echoreplay")
	if err != nil {
		f.Fatal(err)
	}
	_, _ = w.Write([]byte(fuzzSeedLobbyLine + fuzzSeedDuplicateLine))
	if err := zw.Close(); err != nil {
		f.Fatal(err)
	}
	f.Add(seed.Bytes())
	f.Add([]byte("PK\x03\x04broken"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzReplayLimit {
			t.Skip()
		}
		parser := newFuzzReplayParser()
		// The same dispatch as ParseFileStream: ZIP magic, otherwise raw NDJSON.
		if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
			_, _, _ = parser.parseReader(bytes.NewReader(data), "fuzz.echoreplay", fuzzTickInvariants(t))
			return
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		_, _, _ = parser.parseZipArchive(zr, "fuzz.echoreplay", fuzzTickInvariants(t))
	})
}

// FuzzEchoSessionMapping checks that arbitrary JSON cannot panic either the
// wire decoder or the mapper that creates detector-facing telemetry, with and
// without the roster limits recordings are mapped under.
func FuzzEchoSessionMapping(f *testing.F) {
	f.Add([]byte(`{"sessionid":"fuzz","teams":[]}`))
	f.Add([]byte(`{"teams":[{"team":"BLUE TEAM","players":[{"name":"p","userid":1}]}]}`))
	f.Add([]byte(`{"teams":[{"players":[{"userid":7,"body":{"position":[1,1,1]}},{"userid":7,"body":{"position":[2,1,1]}}]}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzReplayLimit {
			t.Skip()
		}
		var session EchoVRSessionResponse
		if err := json.Unmarshal(data, &session); err != nil {
			return
		}
		_ = NewMapper().MapSession(&session)
		limited := NewMapper()
		limited.EnforceRosterLimits()
		result := limited.MapSession(&session)
		if result.LimitError != nil && (len(result.Frames) != 0 || result.MatchCtx != nil) {
			t.Fatal("a refused snapshot still produced frames or context")
		}
		if len(result.Frames) > MaxSnapshotPlayers {
			t.Fatalf("%d frames from one snapshot, limit %d", len(result.Frames), MaxSnapshotPlayers)
		}
		seen := make(map[string]struct{}, len(result.Frames))
		for i := range result.Frames {
			if _, dup := seen[result.Frames[i].PlayerID]; dup {
				t.Fatalf("player %q mapped twice in one snapshot", result.Frames[i].PlayerID)
			}
			seen[result.Frames[i].PlayerID] = struct{}{}
		}
	})
}
