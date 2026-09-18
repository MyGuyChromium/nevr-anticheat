package adapter

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	capture "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
)

func stunPresencePlayer(t *testing.T, fields string) EchoVRPlayer {
	t.Helper()
	var p EchoVRPlayer
	if err := json.Unmarshal([]byte(`{"name":"Synthetic","userid":1,"body":{"position":[1,2,3]},`+fields+`}`), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStunPresenceAbsentNullAndExplicitZeroRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, fields           string
		stateKnown, countKnown bool
		stunned                bool
		stuns                  int
	}{
		{"absent stats", `"level":1`, false, false, false, 0},
		{"null stats and state", `"stats":null,"stunned":null`, false, false, false, 0},
		{"missing counter", `"stats":{},"stunned":false`, true, false, false, 0},
		{"null counter", `"stats":{"stuns":null},"stunned":false`, true, false, false, 0},
		{"explicit false zero", `"stats":{"stuns":0},"stunned":false`, true, true, false, 0},
		{"explicit true integer", `"stats":{"stuns":2.0},"stunned":true`, true, true, true, 2},
		{"counter without state", `"stats":{"stuns":0}`, false, true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := stunPresencePlayer(t, tc.fields)
			if p.hasStunned() != tc.stateKnown || p.hasStuns() != tc.countKnown || p.Stunned != tc.stunned || p.Stats.Stuns != tc.stuns {
				t.Fatalf("presence/value mismatch: %+v", p)
			}
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var back EchoVRPlayer
			if err := json.Unmarshal(data, &back); err != nil || !reflect.DeepEqual(p, back) {
				t.Fatalf("roundtrip invented known state/counter: %s, %v", data, err)
			}
			var serialized struct {
				Stunned *bool `json:"stunned"`
				Stats   struct {
					Stuns *int `json:"stuns"`
				} `json:"stats"`
			}
			if err := json.Unmarshal(data, &serialized); err != nil {
				t.Fatal(err)
			}
			if (serialized.Stunned != nil) != tc.stateKnown || (serialized.Stats.Stuns != nil) != tc.countKnown {
				t.Fatalf("serialized a missing observation as zero: %s", data)
			}
			for _, player := range []EchoVRPlayer{p, back} {
				s := twoTeamSession("stun-presence", []EchoVRPlayer{player}, nil)
				r := NewMapper().MapSession(s)
				if len(r.Frames) != 1 {
					t.Fatalf("missing mapped frame: %+v", r)
				}
				f := r.Frames[0]
				if f.IsStunnedKnown == nil || *f.IsStunnedKnown != tc.stateKnown || f.StunsKnown == nil || *f.StunsKnown != tc.countKnown || f.IsStunned != tc.stunned || f.Stuns != tc.stuns {
					t.Fatalf("mapped knowledge lost: %+v", f)
				}
			}
		})
	}
}

func TestStunPresenceFingerprintSeparatesUnknownAndZero(t *testing.T) {
	variants := []string{`"level":1`, `"level":1,"stunned":false`, `"level":1,"stats":{"stuns":0}`, `"level":1,"stunned":false,"stats":{"stuns":0}`}
	seen := map[uint64]bool{}
	mapper := NewMapper()
	mapper.SetDedupeIdentical(true)
	for i, fields := range variants {
		s := twoTeamSession("stun-presence", []EchoVRPlayer{stunPresencePlayer(t, fields)}, nil)
		fingerprint := sessionFingerprint(s)
		if seen[fingerprint] {
			t.Fatal("presence-only change did not alter raw fingerprint")
		}
		seen[fingerprint] = true
		at := time.Unix(100, int64(i)*100_000_000)
		if got := mapper.MapSessionAt(s, at); got.SkippedDuplicate || len(got.Frames) != 1 {
			t.Fatal("presence-only observation discarded as duplicate")
		}
		if got := mapper.MapSessionAt(s, at.Add(time.Millisecond)); !got.SkippedDuplicate {
			t.Fatal("identical presence observation no longer deduplicates")
		}
	}
}

func TestStunPresenceTypedProducerFingerprintMatchesJSON(t *testing.T) {
	s := observationSession()
	before := sessionFingerprint(s)
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EchoVRSessionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if sessionFingerprint(&decoded) != before || !decoded.Teams[0].Players[0].hasStunned() || !decoded.Teams[0].Players[0].hasStuns() {
		t.Fatal("typed producer nil-presence convention differs from its serialized observations")
	}
}

func TestStunPresenceTapeSparseCounterIsFreshOnlyOnUpdate(t *testing.T) {
	d := NewTapeRawDecoder()
	for i := 0; i < 4; i++ {
		var events []*capture.EchoEvent
		if i == 1 || i == 3 {
			events = []*capture.EchoEvent{{Event: &capture.EchoEvent_PlayerStatsUpdated{PlayerStatsUpdated: &capture.PlayerStatsUpdated{PlayerSlot: 1, Stuns: int32(i - 1)}}}}
		}
		f := tapeTestFrame(uint32(i), events...)
		if i == 1 {
			f.GetEchoArena().Players[0].Flags = 2 // observed bitmask with false stun bit
		} else if i == 2 {
			f.GetEchoArena().Players[0].Flags = 1 // observed true stun bit
		}
		tick, err := d.Decode(tapeTestRaw(t, tapeTestHeader(), f))
		if err != nil || len(tick.Frames) != 1 {
			t.Fatalf("native decode: %v", err)
		}
		got := tick.Frames[0]
		wantKnown := i == 1 || i == 3
		if got.StunsKnown == nil || *got.StunsKnown != wantKnown {
			t.Fatalf("frame %d cached/missing counter presented as fresh: %+v", i, got)
		}
		if got.IsStunnedKnown == nil || *got.IsStunnedKnown != (i == 1 || i == 2) || got.IsStunned != (i == 2) {
			t.Fatalf("frame %d invented zero-flags presence or lost an observed bitmask", i)
		}
		if i == 3 && got.Stuns != 2 {
			t.Fatal("explicit native count lost")
		}
		// Native original records stay authoritative for reprocessing the
		// projection: no absent stats event can become an observed zero.
		var projection EchoVRSessionResponse
		data, err := json.Marshal(tick.Session)
		if err != nil || json.Unmarshal(data, &projection) != nil {
			t.Fatal("native projection roundtrip failed")
		}
		if projection.Teams[0].Players[0].hasStuns() != wantKnown {
			t.Fatal("projection manufactured fresh cached stats")
		}
	}
}

func TestStunPresenceTapeGapDoesNotRefreshCachedCounter(t *testing.T) {
	d := NewTapeRawDecoder()
	first := tapeTestFrame(0, &capture.EchoEvent{Event: &capture.EchoEvent_PlayerStatsUpdated{PlayerStatsUpdated: &capture.PlayerStatsUpdated{PlayerSlot: 1, Stuns: 4}}})
	if tick, err := d.Decode(tapeTestRaw(t, tapeTestHeader(), first)); err != nil || tick.Frames[0].StunsKnown == nil || !*tick.Frames[0].StunsKnown {
		t.Fatalf("explicit initial counter unavailable: %v", err)
	}
	tick, err := d.Decode(tapeTestRaw(t, tapeTestHeader(), tapeTestFrame(3)))
	if err != nil {
		t.Fatal(err)
	}
	if tick.Frames[0].StunsKnown == nil || *tick.Frames[0].StunsKnown {
		t.Fatal("cached pre-gap counter became a current observation")
	}
}
