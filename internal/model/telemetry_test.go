package model

import (
	"encoding/json"
	"math"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestDiscState_UnmarshalHolderIDAlias(t *testing.T) {
	var d DiscState
	if err := json.Unmarshal([]byte(`{"position":[1,2,3],"velocity":[3,4,0],"holder_id":"PLR-001"}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.PossessorID != "PLR-001" || !d.IsHeld {
		t.Errorf("holder_id alias not applied: %+v", d)
	}
	if math.Abs(d.Speed-5) > 1e-12 {
		t.Errorf("speed not derived from velocity: %v", d.Speed)
	}

	// Empty holder_id: free disc.
	d = DiscState{}
	if err := json.Unmarshal([]byte(`{"position":[1,2,3],"velocity":[0,0,0],"holder_id":""}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.IsHeld || d.PossessorID != "" || d.Speed != 0 {
		t.Errorf("free disc decoded as %+v", d)
	}

	// Native keys still win and an explicit speed is preserved.
	d = DiscState{}
	if err := json.Unmarshal([]byte(`{"velocity":[3,4,0],"speed":7.5,"possessor_id":"A","is_held":true}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.PossessorID != "A" || !d.IsHeld || d.Speed != 7.5 {
		t.Errorf("native keys mishandled: %+v", d)
	}

	// Round trip through MarshalJSON keeps possessor_id/is_held readable.
	out, _ := json.Marshal(d)
	var back DiscState
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back != d {
		t.Errorf("round trip mismatch: %+v vs %+v", back, d)
	}
}

// TestDiscState_ContractExamples decodes every JSON example in
// docs/telemetry_contract.md (read-only reference) and checks that the documented
// disc shape ("holder_id", no "speed") yields a usable DiscState.
func TestDiscState_ContractExamples(t *testing.T) {
	data, err := os.ReadFile("../../docs/telemetry_contract.md")
	if err != nil {
		t.Skipf("contract doc not available: %v", err)
	}
	blocks := regexp.MustCompile("(?s)```json\\s*(.*?)```").FindAllStringSubmatch(string(data), -1)
	if len(blocks) < 4 {
		t.Fatalf("expected several json blocks in the contract, found %d", len(blocks))
	}

	var frames []PlayerTelemetryFrame
	for _, b := range blocks {
		body := strings.TrimSpace(b[1])
		// Batch example: decode only the frames array (the envelope carries
		// placeholder strings that are not part of the frame schema).
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &probe); err != nil {
			t.Fatalf("contract example is not valid JSON: %v\n%s", err, body)
		}
		if raw, ok := probe["frames"]; ok {
			var fs []PlayerTelemetryFrame
			if err := json.Unmarshal(raw, &fs); err != nil {
				t.Fatalf("frames example does not decode into PlayerTelemetryFrame: %v", err)
			}
			frames = append(frames, fs...)
			continue
		}
		if _, ok := probe["player_id"]; ok {
			var f PlayerTelemetryFrame
			if err := json.Unmarshal([]byte(body), &f); err != nil {
				t.Fatalf("frame example does not decode: %v", err)
			}
			frames = append(frames, f)
		}
	}
	if len(frames) < 3 {
		t.Fatalf("expected the batch frame plus the two throw-transition frames, got %d", len(frames))
	}

	// Batch example: free disc with velocity [12.5, 1.0, -0.5].
	batch := frames[0]
	if batch.Disc == nil {
		t.Fatal("batch example disc missing")
	}
	if batch.Disc.IsHeld || batch.Disc.PossessorID != "" {
		t.Errorf("batch disc should be free: %+v", batch.Disc)
	}
	if want := math.Sqrt(12.5*12.5 + 1 + 0.25); math.Abs(batch.Disc.Speed-want) > 1e-9 {
		t.Errorf("batch disc speed = %v, want %v (derived from velocity)", batch.Disc.Speed, want)
	}

	// Throw transition: frame N held by PLR-001, frame N+1 released.
	held, released := frames[1], frames[2]
	if held.Disc == nil || !held.Disc.IsHeld || held.Disc.PossessorID != "PLR-001" || !held.HasPossession {
		t.Errorf("frame N should be held by PLR-001: %+v", held.Disc)
	}
	if released.Disc == nil || released.Disc.IsHeld || released.Disc.PossessorID != "" || released.HasPossession {
		t.Errorf("frame N+1 should be released: %+v", released.Disc)
	}
	if released.Disc != nil {
		want := Vec3{15.2, 2.1, -0.8}.Magnitude()
		if math.Abs(released.Disc.Speed-want) > 1e-9 {
			t.Errorf("released disc speed = %v, want %v", released.Disc.Speed, want)
		}
	}
}
