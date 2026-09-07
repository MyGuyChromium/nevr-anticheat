package adapter

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestEchoVRDiscBouncePresenceRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
		want *int
	}{
		{name: "missing", json: `{}`},
		{name: "null", json: `{"bounce_count":null}`},
		{name: "zero", json: `{"bounce_count":0}`, want: observationInt(0)},
		{name: "positive", json: `{"bounce_count":5}`, want: observationInt(5)},
		{name: "negative source preserved", json: `{"bounce_count":-1}`, want: observationInt(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw EchoVRDisc
			if err := json.Unmarshal([]byte(tc.json), &raw); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			var back EchoVRDisc
			if err := json.Unmarshal(encoded, &back); err != nil {
				t.Fatal(err)
			}
			if (back.BounceCount == nil) != (tc.want == nil) || (tc.want != nil && *back.BounceCount != *tc.want) {
				t.Fatalf("bounce counter after roundtrip = %v; source %s", back.BounceCount, tc.json)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if _, present := fields["bounce_count"]; present != (tc.want != nil) {
				t.Fatalf("roundtrip invented/lost a bounce observation: %s", encoded)
			}
		})
	}
}

func observationInt(v int) *int { return &v }

func observationSession() *EchoVRSessionResponse {
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	b := testPlayer("B", 2, [3]float64{-1, 1.6, 10})
	a.Head.Position = [3]float64{1, 1.8, -10}
	b.Head.Position = [3]float64{-1, 1.8, 10}
	a.HoldingLeft, a.HoldingRight = "none", "none"
	b.HoldingLeft, b.HoldingRight = "none", "none"
	s := twoTeamSession("observation", []EchoVRPlayer{a}, []EchoVRPlayer{b})
	s.Disc.BounceCount = observationInt(0)
	return s
}

func TestMapperExplicitHeadObservation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		head  [3]float64
		known bool
	}{
		{name: "absent or zero"},
		{name: "tracked", head: [3]float64{1, 1.8, -10}, known: true},
		{name: "nan", head: [3]float64{1, math.NaN(), -10}},
		{name: "infinite", head: [3]float64{math.Inf(1), 1.8, -10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := observationSession()
			s.Teams[0].Players[0].Head.Position = tc.head
			result := NewMapper().MapSession(s)
			f := frameByPlayer(result.Frames, "echovr:1")
			if f == nil || (f.HeadPosition != nil) != tc.known {
				t.Fatalf("head knowledge = %+v, want known=%v", f, tc.known)
			}
			if tc.known && *f.HeadPosition != model.Vec3(tc.head) {
				t.Fatalf("head replaced by body or other pose: %v", *f.HeadPosition)
			}
		})
	}
}

func TestMapperDiscObservationKnowledge(t *testing.T) {
	for _, tc := range []struct {
		name            string
		change          func(*EchoVRSessionResponse)
		known, conflict bool
		holder          string
		frames          int
	}{
		{name: "explicit free ignores stale legacy possession", known: true, frames: 2, change: func(s *EchoVRSessionResponse) {
			s.Teams[0].Players[0].Possession = true
		}},
		{name: "legacy still uses possession", holder: "echovr:1", frames: 2, change: func(s *EchoVRSessionResponse) {
			p := &s.Teams[0].Players[0]
			p.HoldingLeft, p.HoldingRight, p.Possession = "", "", true
		}},
		{name: "partial hand fields remain unknown", holder: "echovr:1", frames: 2, change: func(s *EchoVRSessionResponse) {
			p := &s.Teams[0].Players[0]
			p.HoldingLeft, p.HoldingRight = "disc", ""
		}},
		{name: "both holders retain first holder", known: true, conflict: true, holder: "echovr:1", frames: 2, change: func(s *EchoVRSessionResponse) {
			s.Teams[0].Players[0].HoldingLeft = "disc"
			s.Teams[1].Players[0].HoldingRight = "disc"
		}},
		{name: "dropped record still counted and conflicts", known: true, conflict: true, holder: "echovr:1", frames: 1, change: func(s *EchoVRSessionResponse) {
			s.Teams[0].Players[0].HoldingLeft = "disc"
			s.Teams[1].Players[0].HoldingRight = "disc"
			s.Teams[1].Players[0].Body.Position = [3]float64{}
		}},
		{name: "dropped record lacking fields prevents knowledge", frames: 1, change: func(s *EchoVRSessionResponse) {
			p := &s.Teams[1].Players[0]
			p.HoldingLeft, p.HoldingRight = "", ""
			p.Body.Position = [3]float64{}
		}},
		{name: "spectator excluded from knowledge count and conflicts", known: true, frames: 2, change: func(s *EchoVRSessionResponse) {
			spectator := testPlayer("spectator", 3, [3]float64{1, 1, 1})
			spectator.Possession = true
			s.Teams = append(s.Teams, EchoVRTeam{TeamName: "SPECTATORS", Players: []EchoVRPlayer{spectator}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := observationSession()
			tc.change(s)
			result := NewMapper().MapSession(s)
			if len(result.Frames) != tc.frames {
				t.Fatalf("frames=%d, want %d", len(result.Frames), tc.frames)
			}
			for _, f := range result.Frames {
				d := f.Disc
				if d == nil || d.PossessionKnown != tc.known || d.PossessionConflict != tc.conflict ||
					d.SampledPlayerCount != 2 || d.PossessorID != tc.holder || d.IsHeld != (tc.holder != "") {
					t.Fatalf("shared disc metadata = %+v", d)
				}
			}
		})
	}
}

func TestMapperObservationCopiesAndRawRoundTrip(t *testing.T) {
	raw := observationSession()
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var restored EchoVRSessionResponse
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	result := NewMapper().MapSession(&restored)
	if len(result.Frames) != 2 {
		t.Fatalf("mapped %d frames", len(result.Frames))
	}
	a, b := &result.Frames[0], &result.Frames[1]
	if a.HeadPosition == nil || a.Disc.BounceCount == nil || b.Disc.BounceCount == nil {
		t.Fatal("raw roundtrip lost observations")
	}
	*a.Disc.BounceCount = 9
	(*a.HeadPosition)[0] = 8
	if *b.Disc.BounceCount != 0 || *restored.Disc.BounceCount != 0 || restored.Teams[0].Players[0].Head.Position[0] != 1 {
		t.Fatal("mapped observation pointers alias another player or raw source")
	}
	for _, bounce := range []*int{nil, observationInt(-1)} {
		raw.Disc.BounceCount = bounce
		f := NewMapper().MapSession(raw).Frames[0]
		if f.Disc.BounceCount != nil {
			t.Fatalf("missing/invalid bounce became known: %v", *f.Disc.BounceCount)
		}
	}
}

func TestMapperDedupeObservations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before func(*EchoVRSessionResponse)
		change func(*EchoVRSessionResponse)
	}{
		{name: "head only", change: func(s *EchoVRSessionResponse) { s.Teams[0].Players[0].Head.Position[0] += 0.1 }},
		{name: "head loss", change: func(s *EchoVRSessionResponse) { s.Teams[0].Players[0].Head.Position = [3]float64{} }},
		{name: "bounce only", change: func(s *EchoVRSessionResponse) { s.Disc.BounceCount = observationInt(1) }},
		{name: "bounce loss", change: func(s *EchoVRSessionResponse) { s.Disc.BounceCount = nil }},
		{name: "bounce explicit zero", before: func(s *EchoVRSessionResponse) { s.Disc.BounceCount = nil }, change: func(s *EchoVRSessionResponse) { s.Disc.BounceCount = observationInt(0) }},
		{name: "holding knowledge only", change: func(s *EchoVRSessionResponse) {
			p := &s.Teams[0].Players[0]
			p.HoldingLeft, p.HoldingRight = "nonenone", ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMapper()
			m.SetDedupeIdentical(true)
			s := observationSession()
			if tc.before != nil {
				tc.before(s)
			}
			t0 := time.Unix(100, 0)
			m.MapSessionAt(s, t0)
			if duplicate := m.MapSessionAt(s, t0.Add(time.Second)); !duplicate.SkippedDuplicate {
				t.Fatal("identical tick not deduped")
			}
			tc.change(s)
			changed := m.MapSessionAt(s, t0.Add(2*time.Second))
			if changed.SkippedDuplicate || len(changed.Frames) != 2 {
				t.Fatal("observation-only change was suppressed")
			}
		})
	}
}
