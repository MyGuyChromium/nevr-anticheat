package adapter

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestMapperFingerprintKeepsExactRemoteInt64Identity(t *testing.T) {
	for _, id := range []int64{1 << 53, 1<<53 + 1, math.MaxInt64, math.MinInt64} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			local := testPlayer("Local", 1, [3]float64{1, 2, 3})
			remote := testPlayer("Remote", 1<<53, [3]float64{2, 2, 3})
			raw := twoTeamSession("m", []EchoVRPlayer{local}, []EchoVRPlayer{remote})
			raw.ClientName = "Local"
			m := NewMapper()
			m.SetDedupeIdentical(true)
			m.MapSessionAt(raw, time.Unix(100, 0))
			raw.Teams[1].Players[0].UserID = id
			data, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			var decoded EchoVRSessionResponse
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Teams[1].Players[0].UserID != id {
				t.Fatal("identifier rounded during JSON decode")
			}
			got := m.MapSessionAt(&decoded, time.Unix(100, 100_000_000))
			if id == 1<<53 {
				if !got.SkippedDuplicate {
					t.Fatal("identical identity no longer deduplicates")
				}
				return
			}
			if got.SkippedDuplicate || frameByPlayer(got.Frames, fmt.Sprintf("echovr:%d", id)) == nil {
				t.Fatalf("distinct exact remote identity discarded: %d", id)
			}
		})
	}
	for _, invalid := range []string{"9223372036854775808", "1.5", "1e3", `"123"`} {
		var player EchoVRPlayer
		if json.Unmarshal([]byte(`{"userid":`+invalid+`}`), &player) == nil {
			t.Fatalf("non-int64 ID accepted: %s", invalid)
		}
	}
}

func TestMapperFingerprintIncludesMappedStateChanges(t *testing.T) {
	for name, mutate := range map[string]func(*EchoVRPlayer){
		"ping":     func(p *EchoVRPlayer) { p.Ping++ },
		"immunity": func(p *EchoVRPlayer) { p.Invulnerable = !p.Invulnerable },
		"goals":    func(p *EchoVRPlayer) { p.Stats.Goals++ },
		"stuns":    func(p *EchoVRPlayer) { p.Stats.Stuns++ },
	} {
		t.Run(name, func(t *testing.T) {
			raw := twoTeamSession("m", []EchoVRPlayer{testPlayer("P", 1, [3]float64{1, 2, 3})}, nil)
			m := NewMapper()
			m.SetDedupeIdentical(true)
			m.MapSessionAt(raw, time.Unix(100, 0))
			mutate(&raw.Teams[0].Players[0])
			if got := m.MapSessionAt(raw, time.Unix(100, 100_000_000)); got.SkippedDuplicate || len(got.Frames) != 1 {
				t.Fatal("mapped state change was treated as a duplicate")
			}
		})
	}
}

func TestMapperClockBoundaryResetsThrowAndDuplicateBaselines(t *testing.T) {
	raw := twoTeamSession("m", []EchoVRPlayer{testPlayer("Local", 1, [3]float64{1, 2, 3})}, nil)
	raw.ClientName = "Local"
	raw.LastThrow = &EchoVRLastThrow{TotalSpeed: 10}
	m := NewMapper()
	m.SetDedupeIdentical(true)
	tm := time.Unix(10000, 0)
	m.MapSessionAt(raw, tm)
	got := m.MapSessionAt(raw, tm.Add(-time.Hour))
	if got.SkippedDuplicate || len(got.Frames) != 1 || got.Frames[0].DeltaTime != 0 || got.Frames[0].Observation.SourceEpoch != 1 {
		t.Fatalf("unchanged payload hid clock boundary: %+v", got)
	}
	// A changed report at a second reset establishes a baseline, never a
	// fresh release report spanning either discontinuity.
	raw.LastThrow.TotalSpeed = 11
	got = m.MapSessionAt(raw, tm.Add(-2*time.Hour))
	if got.Frames[0].GameLastThrow != nil || got.Frames[0].Observation.SourceEpoch != 2 {
		t.Fatalf("clock reset emitted stale report: %+v", got.Frames[0])
	}
}

func TestMapperClockStepThresholdIsInclusive(t *testing.T) {
	raw := twoTeamSession("m", []EchoVRPlayer{testPlayer("P", 1, [3]float64{1, 2, 3})}, nil)
	m := NewMapper()
	m.MapSessionAt(raw, time.Unix(100, 0))
	got := m.MapSessionAt(raw, time.Unix(99, 0))
	if got.Frames[0].Observation.SourceEpoch != 1 || got.Frames[0].DeltaTime != 0 {
		t.Fatal("exact clock-step threshold did not isolate history")
	}
}

func TestReusedRawDecodeCannotCarryOmittedOrNullObservations(t *testing.T) {
	for _, missing := range []string{`{}`, `null`, `{"userid":null,"velocity":null,"holding_left":null}`} {
		var p EchoVRPlayer
		if err := json.Unmarshal([]byte(`{"userid":9007199254740993,"velocity":[1,2,3],"holding_left":"disc"}`), &p); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(missing), &p); err != nil {
			t.Fatal(err)
		}
		if p.UserID != 0 || p.hasVelocity() || p.HoldingLeft != "" {
			t.Fatalf("reused player retained previous observation: %+v", p)
		}
		var d EchoVRDisc
		if err := json.Unmarshal([]byte(`{"position":[1,2,3],"velocity":[4,5,6],"bounce_count":2}`), &d); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(missing), &d); err != nil {
			t.Fatal(err)
		}
		if d.hasPosition() || d.hasVelocity() || d.BounceCount != nil {
			t.Fatalf("reused disc retained previous observation: %+v", d)
		}
	}
}
