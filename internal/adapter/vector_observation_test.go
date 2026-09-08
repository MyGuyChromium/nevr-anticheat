package adapter

import (
	"encoding/json"
	"testing"
	"time"
)

func vectorSessionJSON(t *testing.T, playerVelocity, discPosition, discVelocity string) []byte {
	t.Helper()
	player := testPlayer("P", 1, [3]float64{1, 2, 3})
	player.HoldingLeft, player.HoldingRight = "none", "none"
	raw := twoTeamSession("m", []EchoVRPlayer{player}, nil)
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(b, &object); err != nil {
		t.Fatal(err)
	}
	p := object["teams"].([]any)[0].(map[string]any)["players"].([]any)[0].(map[string]any)
	d := object["disc"].(map[string]any)
	for _, field := range []struct {
		target     map[string]any
		key, value string
	}{{p, "velocity", playerVelocity}, {d, "position", discPosition}, {d, "velocity", discVelocity}} {
		if field.value == "" {
			delete(field.target, field.key)
		} else {
			field.target[field.key] = json.RawMessage(field.value)
		}
	}
	b, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMapperVectorPresenceDistinguishesExplicitZero(t *testing.T) {
	for _, value := range []string{"", "null", "[0,0]", "[0,0,0,0]", "[0,null,0]", "[0,0,0]", "[1,2,3]"} {
		t.Run(value, func(t *testing.T) {
			known := value == "[0,0,0]" || value == "[1,2,3]"
			for _, field := range []string{"player_velocity", "disc_position", "disc_velocity"} {
				t.Run(field, func(t *testing.T) {
					pv, dp, dv := "[0,0,0]", "[0,0,0]", "[0,0,0]"
					switch field {
					case "player_velocity":
						pv = value
					case "disc_position":
						dp = value
					case "disc_velocity":
						dv = value
					}
					var raw EchoVRSessionResponse
					if err := json.Unmarshal(vectorSessionJSON(t, pv, dp, dv), &raw); err != nil {
						t.Fatal(err)
					}
					for round := 0; round < 2; round++ {
						res := NewMapper().MapSessionAt(&raw, time.Unix(100, 0))
						if len(res.Frames) != 1 {
							t.Fatal("missing player")
						}
						f := res.Frames[0]
						if field == "player_velocity" {
							if (f.ReportedVelocity != nil) != known {
								t.Fatalf("round%d missing velocity became observed: %+v", round, f.ReportedVelocity)
							}
						} else if (f.Disc != nil) != known {
							t.Fatalf("round%d incomplete disc became usable: %+v", round, f.Disc)
						}
						// Reserialization must not upgrade absence/null to observed zero.
						b, err := json.Marshal(&raw)
						if err != nil {
							t.Fatal(err)
						}
						if err = json.Unmarshal(b, &raw); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		})
	}
}

func TestMapperVectorPresenceChangeSurvivesDedupe(t *testing.T) {
	for _, field := range []string{"player", "disc_position", "disc_velocity"} {
		t.Run(field, func(t *testing.T) {
			pv, dp, dv := "[0,0,0]", "[0,0,0]", "[0,0,0]"
			switch field {
			case "player":
				pv = ""
			case "disc_position":
				dp = ""
			case "disc_velocity":
				dv = ""
			}
			m := NewMapper()
			m.SetDedupeIdentical(true)
			var raw EchoVRSessionResponse
			if err := json.Unmarshal(vectorSessionJSON(t, pv, dp, dv), &raw); err != nil {
				t.Fatal(err)
			}
			m.MapSessionAt(&raw, time.Unix(100, 0))
			if err := json.Unmarshal(vectorSessionJSON(t, "[0,0,0]", "[0,0,0]", "[0,0,0]"), &raw); err != nil {
				t.Fatal(err)
			}
			res := m.MapSessionAt(&raw, time.Unix(101, 0))
			if res.SkippedDuplicate || len(res.Frames) != 1 || res.Frames[0].ReportedVelocity == nil || res.Frames[0].Disc == nil {
				t.Fatal("new known-zero observation deduplicated")
			}
		})
	}
}

func TestTypedVectorFixturesRemainExplicit(t *testing.T) {
	p := testPlayer("P", 1, [3]float64{1, 2, 3})
	raw := twoTeamSession("m", []EchoVRPlayer{p}, nil)
	raw.Disc = &EchoVRDisc{}
	res := NewMapper().MapSessionAt(raw, time.Unix(100, 0))
	if res.Frames[0].ReportedVelocity == nil || res.Frames[0].Disc == nil {
		t.Fatal("typed explicit arrays lost compatibility")
	}
}
