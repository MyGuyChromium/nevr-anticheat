package regression

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func FuzzRegressionManifest(f *testing.F) {
	f.Add([]byte(`{"schema":"nevr-private-replay-regression/v1","cases":[]}`))
	f.Add([]byte(`{"schema":"invalid","cases":[{"expected":[{"player_frames":{"":-1}}]}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		var m Manifest
		if json.Unmarshal(data, &m) != nil {
			return
		}
		before, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		_ = m.Validate()
		after, err := json.Marshal(m)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("validation mutated manifest")
		}
	})
}

type cancelAfterRead struct{ cancel context.CancelFunc }

func (r cancelAfterRead) Read(b []byte) (int, error) {
	copy(b, "private source")
	r.cancel()
	return len("private source"), nil
}

type shortWriter struct{}

func (shortWriter) Write(b []byte) (int, error) { return 0, nil }

func TestRegressionCopyCancellationAndShortWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	n, err := copyWithContext(ctx, &out, cancelAfterRead{cancel})
	if !errors.Is(err, context.Canceled) || n != 0 || out.Len() != 0 {
		t.Fatalf("copy after cancellation: n=%d err=%v", n, err)
	}
	if _, err := copyWithContext(context.Background(), shortWriter{}, bytes.NewReader([]byte("source"))); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %v", err)
	}
}

func TestManifestRejectsImpossibleBaselineStructure(t *testing.T) {
	for _, kind := range []string{"blank player", "negative frames", "absent signal player", "signal beyond match", "range beyond match"} {
		t.Run(kind, func(t *testing.T) {
			m := simpleManifest(t)
			s := &m.Cases[0].Expected[0]
			s.Signals = []Signal{{MatchID: "match", PlayerID: "player", DetectorID: "THROW_001", Frame: 20, Start: 19, End: 21}}
			switch kind {
			case "blank player":
				s.PlayerFrames[""] = 1
			case "negative frames":
				s.PlayerFrames["player"] = -1
			case "absent signal player":
				s.Signals[0].PlayerID = "absent"
			case "signal beyond match":
				s.Signals[0].Frame = 100
			case "range beyond match":
				s.Signals[0].End = 100
			}
			if m.Validate() == nil {
				t.Fatal("invalid baseline accepted")
			}
		})
	}
}
