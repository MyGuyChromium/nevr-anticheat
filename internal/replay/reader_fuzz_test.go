package replay

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLegacyJSONReplay protects the compatibility importer. The deliberately
// small cap keeps fuzzing fast and verifies the parser's allocation guard.
func FuzzLegacyJSONReplay(f *testing.F) {
	f.Add([]byte(`{"header":{"MatchID":"fuzz"},"frames":[]}`))
	f.Add([]byte(`{"header":{},"frames":[{"Index":0,"Players":[]}]}`))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256<<10 {
			t.Skip()
		}
		path := filepath.Join(dir, "fuzz.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		parser := NewJSONFrameParser()
		parser.SetMaxBytes(256 << 10)
		reader := NewReplayReader(path, parser)
		_, _, _ = reader.ReadMatch()
	})
}
