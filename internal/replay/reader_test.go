package replay

import (
	"os"
	"path/filepath"
	"testing"
)

// F210: an excerpt whose first timestamp is not 0 must not report dt = timestamp.
func TestReplayReader_FirstTickDeltaTimeIsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "excerpt.json")
	content := `{
  "header": {"MatchID": "m1", "PlayerIDs": ["p1"], "Teams": {"p1": "blue"}},
  "frames": [
    {"Index": 1200, "Timestamp": 82.3, "GamePhase": "playing",
     "Players": [{"player_id": "p1", "position": [1, 1.6, 2], "rotation": [0,0,0,1]}],
     "Disc": {"position": [0, 2, 0], "velocity": [1, 0, 0], "holder_id": "p1"}},
    {"Index": 1201, "Timestamp": 82.367, "GamePhase": "playing",
     "Players": [{"player_id": "p1", "position": [1, 1.6, 2.1], "rotation": [0,0,0,1]}],
     "Disc": {"position": [0, 2, 0], "velocity": [1, 0, 0], "possessor_id": "p1"}}
  ]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, frames, err := NewReplayReader(path, NewJSONFrameParser()).ReadMatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %d", len(frames))
	}
	if frames[0].DeltaTime != 0 {
		t.Errorf("first tick dt = %v, want 0 (unknown)", frames[0].DeltaTime)
	}
	if d := frames[1].DeltaTime; d < 0.066 || d > 0.068 {
		t.Errorf("second tick dt = %v, want 0.067", d)
	}
	if frames[0].Timestamp != 82.3 {
		t.Errorf("timestamp = %v", frames[0].Timestamp)
	}
	// Disc holder via both spellings.
	if frames[0].Disc == nil || !frames[0].Disc.IsHeld || frames[0].Disc.PossessorID != "p1" {
		t.Errorf("holder_id disc = %+v", frames[0].Disc)
	}
	if frames[1].Disc == nil || !frames[1].Disc.IsHeld || frames[1].Disc.PossessorID != "p1" {
		t.Errorf("possessor_id alias disc = %+v", frames[1].Disc)
	}
}

// F136: a legacy JSON file written with the contract's key names keeps hands and ping.
func TestReplayReader_ContractKeyAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contract.json")
	content := `{
  "header": {"MatchID": "m2", "PlayerIDs": ["PLR-001"], "Teams": {"PLR-001": "blue"}},
  "frames": [
    {"Index": 0, "Timestamp": 0, "GamePhase": "playing",
     "Players": [{
       "player_id": "PLR-001", "position": [12.5, 1.6, -3.2], "rotation": [0, 0.707, 0, 0.707],
       "left_hand_position": [12.2, 1.9, -3.0], "right_hand_position": [12.8, 1.9, -3.4],
       "left_hand_rotation": [0, 0.1, 0, 0.995], "right_hand_rotation": [0, -0.1, 0, 0.995],
       "estimated_ping_ms": 45.0, "has_possession": false
     }],
     "Disc": {"position": [5, 2.1, 0], "velocity": [12.5, 1, -0.5], "holder_id": ""}},
    {"Index": 1, "Timestamp": 0.067, "GamePhase": "playing",
     "Players": [{
       "player_id": "PLR-001", "position": [12.5, 1.6, -3.2], "rotation": [0, 0.707, 0, 0.707],
       "left_hand": [1, 2, 3], "left_hand_position": [9, 9, 9],
       "ping_ms": 30, "estimated_ping_ms": 45.0
     }]}
  ]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, frames, err := NewReplayReader(path, NewJSONFrameParser()).ReadMatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %d", len(frames))
	}
	f := frames[0]
	if f.LeftHandPosition != ([3]float64{12.2, 1.9, -3.0}) || f.RightHandPosition != ([3]float64{12.8, 1.9, -3.4}) {
		t.Errorf("hand positions via aliases = %v / %v", f.LeftHandPosition, f.RightHandPosition)
	}
	if f.LeftHandRotation != ([4]float64{0, 0.1, 0, 0.995}) || f.RightHandRotation != ([4]float64{0, -0.1, 0, 0.995}) {
		t.Errorf("hand rotations via aliases = %v / %v", f.LeftHandRotation, f.RightHandRotation)
	}
	if f.EstimatedPingMs != 45 {
		t.Errorf("ping via alias = %v", f.EstimatedPingMs)
	}
	if f.Disc == nil || f.Disc.IsHeld || f.Disc.Speed == 0 {
		t.Errorf("disc = %+v", f.Disc)
	}
	// Canonical keys win over aliases when both are given.
	g := frames[1]
	if g.LeftHandPosition != ([3]float64{1, 2, 3}) || g.EstimatedPingMs != 30 {
		t.Errorf("canonical keys should win: hand=%v ping=%v", g.LeftHandPosition, g.EstimatedPingMs)
	}
}
