package adapter

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const fuzzReplayLimit = 1 << 20

// FuzzEchoReplayNDJSON protects the timestamp/NDJSON/session mapping boundary.
// The production parser is intentionally exercised with its streaming limits
// enabled so this target also guards against accidental unbounded allocation.
func FuzzEchoReplayNDJSON(f *testing.F) {
	f.Add([]byte("2026/03/01 12:00:00.000\t{\"sessionid\":\"fuzz\",\"game_status\":\"playing\",\"teams\":[]}\n"))
	f.Add([]byte("not a replay\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzReplayLimit {
			t.Skip()
		}
		parser := NewEchoReplayParser()
		parser.SetMaxLineBytes(64 << 10)
		parser.SetMaxReplayBytes(fuzzReplayLimit)
		_, _, _ = parser.parseReader(bytes.NewReader(data), "fuzz.echoreplay", func(tick *ParsedTick) error {
			if tick == nil {
				t.Fatal("parser delivered a nil tick")
			}
			return nil
		})
	})
}

// FuzzEchoReplayZIP protects ZIP discovery, entry selection, decompression
// limits, and the nested NDJSON parser as one end-to-end input boundary.
func FuzzEchoReplayZIP(f *testing.F) {
	var seed bytes.Buffer
	zw := zip.NewWriter(&seed)
	w, err := zw.Create("seed.echoreplay")
	if err != nil {
		f.Fatal(err)
	}
	_, _ = w.Write([]byte("2026/03/01 12:00:00.000\t{\"sessionid\":\"fuzz\",\"game_status\":\"playing\",\"teams\":[]}\n"))
	if err := zw.Close(); err != nil {
		f.Fatal(err)
	}
	f.Add(seed.Bytes())
	f.Add([]byte("PK\x03\x04broken"))

	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzReplayLimit {
			t.Skip()
		}
		path := filepath.Join(dir, "fuzz.echoreplay")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		parser := NewEchoReplayParser()
		parser.SetMaxLineBytes(64 << 10)
		parser.SetMaxReplayBytes(fuzzReplayLimit)
		_, _, _ = parser.ParseFileStream(path, func(tick *ParsedTick) error { return nil })
	})
}

// FuzzEchoSessionMapping checks that arbitrary JSON cannot panic either the
// wire decoder or the mapper that creates detector-facing telemetry.
func FuzzEchoSessionMapping(f *testing.F) {
	f.Add([]byte(`{"sessionid":"fuzz","teams":[]}`))
	f.Add([]byte(`{"teams":[{"team":"BLUE TEAM","players":[{"name":"p","userid":1}]}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzReplayLimit {
			t.Skip()
		}
		var session EchoVRSessionResponse
		if err := json.Unmarshal(data, &session); err != nil {
			return
		}
		_ = NewMapper().MapSession(&session)
	})
}
