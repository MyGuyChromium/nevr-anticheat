package adapter

import (
	"reflect"
	"testing"
	"time"
)

func TestObservedAttachmentExplicitState(t *testing.T) {
	for _, tc := range []struct {
		name, left, right, otherLeft, state, holder string
		hands                                       []string
	}{
		{"free", "none", "none", "none", "free", "", nil},
		{"left", "disc", "none", "none", "held", "echovr:1", []string{"left"}},
		{"right", "none", "disc", "none", "held", "echovr:1", []string{"right"}},
		{"both", "disc", "disc", "none", "held", "echovr:1", []string{"left", "right"}},
		{"geometry_and_player_grab", "geo", "12", "none", "free", "", nil},
		{"partial", "disc", "", "none", "unknown", "", nil},
		{"missing_other", "disc", "none", "", "unknown", "", nil},
		{"unsupported", "none", "mystery", "none", "unknown", "", nil},
		{"conflict", "disc", "none", "disc", "unknown", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := testPlayer("A", 1, [3]float64{1, 2, 1}), testPlayer("B", 2, [3]float64{2, 2, 2})
			a.HoldingLeft, a.HoldingRight, a.Possession = tc.left, tc.right, true // sticky possession must not override explicit free
			b.HoldingLeft, b.HoldingRight = tc.otherLeft, "none"
			r := NewMapper().MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, []EchoVRPlayer{b}), time.Unix(100, 0))
			f := frameByPlayer(r.Frames, "echovr:1")
			if f == nil || f.Disc == nil {
				t.Fatal("fixture missing")
			}
			got := f.Disc.Attachment
			if got.State != tc.state || got.HolderID != tc.holder || !reflect.DeepEqual(got.HandCandidates, tc.hands) {
				t.Fatalf("attachment=%+v", got)
			}
			if tc.left != "" && (f.HeldItems.Left == nil || *f.HeldItems.Left != tc.left) {
				t.Fatal("raw label lost")
			}
			if tc.right == "" && f.HeldItems.Right != nil {
				t.Fatal("absent hand label manufactured")
			}
			if f.IsBoostingKnown == nil || *f.IsBoostingKnown {
				t.Fatal("unavailable boost promoted to known")
			}
			if tc.state == "held" {
				other := frameByPlayer(r.Frames, "echovr:2")
				f.Disc.Attachment.HandCandidates[0] = "mutated"
				if other.Disc.Attachment.HandCandidates[0] == "mutated" {
					t.Fatal("shared mutable attachment")
				}
			}
		})
	}
}

func TestMapperLocalReportBindingAndSourceReset(t *testing.T) {
	a, b := testPlayer("Local", 1, [3]float64{1, 2, 1}), testPlayer("Remote", 2, [3]float64{2, 2, 2})
	raw := twoTeamSession("m", []EchoVRPlayer{a}, []EchoVRPlayer{b})
	raw.ClientName = "Local"
	raw.LastThrow = &EchoVRLastThrow{}
	m := NewMapper()
	m.SetDedupeIdentical(true)
	m.SetObservationSource("http", "body_received", "endpoint-a")
	tm := time.Unix(1000, 0)
	m.MapSessionAt(raw, tm)
	raw.LastThrow = &EchoVRLastThrow{TotalSpeed: 19, SpeedFromArm: 15, SpeedFromMovement: 4}
	res := m.MapSessionAt(raw, tm.Add(time.Second))
	local := frameByPlayer(res.Frames, "echovr:1")
	if local == nil || local.GameLastThrow == nil || !local.GameLastThrowProvenance.BoundLocalThrow(local.PlayerID, local.FrameIndex, local.Timestamp) || local.Observation.TimeBasis != "body_received" || local.Observation.Authority != "client_reported" {
		t.Fatalf("missing bound report: %+v", local)
	}
	if frameByPlayer(res.Frames, "echovr:2").GameLastThrow != nil {
		t.Fatal("remote received local report")
	}
	// Endpoint changes baseline a preexisting report; no old value is a new throw.
	m.SetObservationSource("http", "body_received", "endpoint-b")
	raw.LastThrow.TotalSpeed = 21
	res = m.MapSessionAt(raw, tm.Add(2*time.Second))
	if frameByPlayer(res.Frames, "echovr:1").GameLastThrow != nil {
		t.Fatal("source change emitted old local record")
	}
	// A same-named second player makes binding ambiguous even without motion.
	raw.Teams[1].Players[0].Name = "Local"
	raw.LastThrow.TotalSpeed = 22
	res = m.MapSessionAt(raw, tm.Add(3*time.Second))
	for _, f := range res.Frames {
		if f.GameLastThrow != nil || f.Observation.SourcePlayerID != "" {
			t.Fatal("name collision picked a client")
		}
	}
	// Restoring unique identity also establishes a baseline, not an event.
	raw.Teams[1].Players[0].Name = "Remote"
	raw.LastThrow.TotalSpeed = 23
	res = m.MapSessionAt(raw, tm.Add(4*time.Second))
	if frameByPlayer(res.Frames, "echovr:1").GameLastThrow != nil {
		t.Fatal("binding reset emitted stale value")
	}
	m.SetDedupeIdentical(false)
	res = m.MapSessionAt(raw, tm.Add(5*time.Second))
	if frameByPlayer(res.Frames, "echovr:1").GameLastThrow != nil {
		t.Fatal("identical record falsely certified a new throw")
	}
	if local.GameLastThrowProvenance.SourceID != "endpoint-a" {
		t.Fatal("returned provenance mutated on source switch")
	}
}

func TestUniqueClientRequiresPersistentUnambiguousIdentity(t *testing.T) {
	a := testPlayer("Local", 0, [3]float64{1, 2, 1})
	raw := twoTeamSession("m", []EchoVRPlayer{a}, nil)
	raw.ClientName = "Local"
	if uniqueClientPlayer(raw) != "" {
		t.Fatal("missing persistent identity accepted")
	}
	raw.Teams[0].Players[0].UserID = 1
	if uniqueClientPlayer(raw) != "echovr:1" {
		t.Fatal("unique persistent identity rejected")
	}
	raw.Teams = append(raw.Teams, EchoVRTeam{TeamName: "SPECTATORS", Players: []EchoVRPlayer{testPlayer("Local", 2, [3]float64{})}})
	if uniqueClientPlayer(raw) != "" {
		t.Fatal("spectator name collision accepted")
	}
}
