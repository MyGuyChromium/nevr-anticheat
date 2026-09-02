package tests

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func loadSession(t *testing.T, path string) *adapter.EchoVRSessionResponse {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var session adapter.EchoVRSessionResponse
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return &session
}

// TestMapper_NormalSession verifies correct mapping of a standard Echo VR session.
func TestMapper_NormalSession(t *testing.T) {
	session := loadSession(t, "fixtures/echovr_session_normal.json")
	mapper := adapter.NewMapper()
	result := mapper.MapSession(session)

	// Should produce 2 frames (1 per player)
	if len(result.Frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(result.Frames))
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}

	// Check player 1
	p1 := result.Frames[0]
	if p1.PlayerID != "echovr:1234567890" {
		t.Errorf("player1 ID = %q, want echovr:1234567890", p1.PlayerID)
	}
	if p1.Position[0] != 3.5 || p1.Position[1] != 1.6 || p1.Position[2] != -2.0 {
		t.Errorf("player1 position = %v, want [3.5, 1.6, -2.0]", p1.Position)
	}
	if p1.IsStunned {
		t.Error("player1 should not be stunned")
	}
	if p1.BlueScore != 4 {
		t.Errorf("blue score = %d, want 4", p1.BlueScore)
	}
	if p1.Goals != 1 {
		t.Errorf("player1 goals = %d, want 1", p1.Goals)
	}
	if p1.GamePhase != "playing" {
		t.Errorf("game phase = %q, want playing", p1.GamePhase)
	}

	// Check hand positions mapped correctly
	if p1.LeftHandPosition[0] != 3.2 {
		t.Errorf("left hand X = %f, want 3.2", p1.LeftHandPosition[0])
	}
	if p1.RightHandPosition[0] != 3.8 {
		t.Errorf("right hand X = %f, want 3.8", p1.RightHandPosition[0])
	}

	// Check rotation is a valid quaternion (converted from direction vectors)
	if !p1.Rotation.IsUnit() {
		t.Errorf("player1 rotation is not unit: magnitude=%f", p1.Rotation.Magnitude())
	}

	// Check disc state
	if p1.Disc == nil {
		t.Fatal("disc should not be nil")
	}
	if p1.Disc.Position[0] != 5.0 {
		t.Errorf("disc X = %f, want 5.0", p1.Disc.Position[0])
	}
	expectedDiscSpeed := math.Sqrt(12.5*12.5 + 1.0*1.0 + 0.5*0.5)
	if math.Abs(p1.Disc.Speed-expectedDiscSpeed) > 0.01 {
		t.Errorf("disc speed = %f, want %f", p1.Disc.Speed, expectedDiscSpeed)
	}

	// Check player 2 is on orange team
	p2 := result.Frames[1]
	if p2.PlayerID != "echovr:9876543210" {
		t.Errorf("player2 ID = %q, want echovr:9876543210", p2.PlayerID)
	}

	// Match context
	if result.MatchCtx == nil {
		t.Fatal("match context should not be nil")
	}
	if result.MatchCtx.MatchID != "MTX-20260315-173022-EU2" {
		t.Errorf("match ID = %q", result.MatchCtx.MatchID)
	}
	if result.MatchCtx.GameMode != "Echo_Arena" {
		t.Errorf("game mode = %q", result.MatchCtx.GameMode)
	}
	if len(result.MatchCtx.PlayerIDs) != 2 {
		t.Errorf("expected 2 player IDs, got %d", len(result.MatchCtx.PlayerIDs))
	}
}

// TestMapper_MalformedSession verifies that malformed data is rejected gracefully.
func TestMapper_MalformedSession(t *testing.T) {
	session := loadSession(t, "fixtures/echovr_session_malformed.json")
	mapper := adapter.NewMapper()
	result := mapper.MapSession(session)

	// ZeroPositionPlayer should be rejected
	if len(result.Errors) < 1 {
		t.Error("expected at least 1 error for zero-position player")
	}
	foundZeroPosError := false
	for _, e := range result.Errors {
		if e.PlayerName == "ZeroPositionPlayer" && e.Field == "body.position" {
			foundZeroPosError = true
		}
	}
	if !foundZeroPosError {
		t.Error("expected rejection of ZeroPositionPlayer for zero position")
	}

	// ValidPlayer should be accepted (stunned=true is valid)
	if len(result.Frames) < 1 {
		t.Fatal("expected at least 1 valid frame")
	}
	validFrame := result.Frames[0]
	if validFrame.PlayerID != "echovr:222" {
		t.Errorf("valid player ID = %q, want echovr:222", validFrame.PlayerID)
	}
	if !validFrame.IsStunned {
		t.Error("ValidPlayer should be stunned")
	}
	if validFrame.Goals != 2 {
		t.Errorf("goals = %d, want 2", validFrame.Goals)
	}
	if validFrame.Stuns != 5 {
		t.Errorf("stuns = %d, want 5", validFrame.Stuns)
	}
}

// TestMapper_NilSession verifies nil session handling.
func TestMapper_NilSession(t *testing.T) {
	mapper := adapter.NewMapper()
	result := mapper.MapSession(nil)
	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error for nil session, got %d", len(result.Errors))
	}
}

// TestMapper_EmptyTeams verifies empty teams handling.
func TestMapper_EmptyTeams(t *testing.T) {
	session := &adapter.EchoVRSessionResponse{
		SessionID:  "empty-test",
		MatchType:  "Echo_Arena",
		GameStatus: "pre_match",
	}
	mapper := adapter.NewMapper()
	result := mapper.MapSession(session)
	if len(result.Frames) != 0 {
		t.Errorf("expected 0 frames for empty teams, got %d", len(result.Frames))
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d", len(result.Errors))
	}
}

// TestMapper_DirectionVectorToQuat verifies quaternion conversion for a proper
// (det +1) basis and for the reflected (det -1) basis Echo VR fixtures use.
func TestMapper_DirectionVectorToQuat(t *testing.T) {
	// Proper identity basis: forward=[0,0,1], left = up x forward = [1,0,0], up=[0,1,0]
	q, quality := model.QuatFromDirectionVectorsChecked(
		model.Vec3{0, 0, 1}, // forward
		model.Vec3{1, 0, 0}, // left (right-handed)
		model.Vec3{0, 1, 0}, // up
	)
	if quality != model.BasisProper {
		t.Errorf("proper basis flagged as %v", quality)
	}
	if !q.IsUnit() || math.Abs(math.Abs(q.W())-1.0) > 1e-9 {
		t.Errorf("expected identity quaternion, got %v", q)
	}

	// Reflected identity basis (left = -(up x forward)): must be flagged and
	// must still convert to the identity rotation, not to garbage.
	qr, qualityR := model.QuatFromDirectionVectorsChecked(
		model.Vec3{0, 0, 1},
		model.Vec3{-1, 0, 0},
		model.Vec3{0, 1, 0},
	)
	if !qualityR.Reflected() {
		t.Errorf("reflected basis not flagged: %v", qualityR)
	}
	if math.Abs(math.Abs(qr.W())-1.0) > 1e-9 {
		t.Errorf("reflected identity basis should give identity, got %v", qr)
	}

	// 90-degree rotation around +Y: forward=[1,0,0], up=[0,1,0].
	// Proper left = up x forward = [0,0,-1]; reflected left = [0,0,1].
	for _, left := range []model.Vec3{{0, 0, -1}, {0, 0, 1}} {
		q2 := model.QuatFromDirectionVectors(model.Vec3{1, 0, 0}, left, model.Vec3{0, 1, 0})
		if !q2.IsUnit() {
			t.Errorf("left=%v: quaternion not unit: magnitude=%f", left, q2.Magnitude())
		}
		if angleDist := q.AngularDistance(q2); math.Abs(angleDist-math.Pi/2) > 1e-9 {
			t.Errorf("left=%v: expected pi/2 angular distance, got %f", left, angleDist)
		}
		// Applying the rotation to +Z must give the forward vector (handedness).
		if got := q2.Rotate(model.Vec3{0, 0, 1}); got.Sub(model.Vec3{1, 0, 0}).Magnitude() > 1e-9 {
			t.Errorf("left=%v: q*Z = %v, want [1 0 0]", left, got)
		}
	}
}

// TestMapper_HandRotationWarning verifies warning on zero hand rotation vectors.
func TestMapper_HandRotationWarning(t *testing.T) {
	session := &adapter.EchoVRSessionResponse{
		SessionID:  "warn-test",
		MatchType:  "Echo_Arena",
		GameStatus: "playing",
		Teams: []adapter.EchoVRTeam{
			{
				TeamName: "BLUE TEAM",
				Players: []adapter.EchoVRPlayer{
					{
						Name:   "NoHandRotation",
						UserID: 333,
						Body: adapter.EchoVRBodyHead{
							Position: [3]float64{5.0, 1.6, 0.0},
							Forward:  [3]float64{0, 0, 1},
							Left:     [3]float64{-1, 0, 0},
							Up:       [3]float64{0, 1, 0},
						},
						LHand: adapter.EchoVRHand{Position: [3]float64{4.7, 1.9, 0.2}},
						// LHand.Forward/Left/Up are all zero
						RHand: adapter.EchoVRHand{Position: [3]float64{5.3, 1.9, -0.2}},
						Stats: adapter.EchoVRPlayerStats{},
					},
				},
			},
			{TeamName: "ORANGE TEAM"},
		},
	}

	mapper := adapter.NewMapper()
	result := mapper.MapSession(session)

	if len(result.Frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(result.Frames))
	}

	// Should have warnings about zero hand rotation vectors
	hasHandRotWarning := false
	for _, w := range result.Warnings {
		if w.Field == "left_hand_rotation" || w.Field == "right_hand_rotation" {
			hasHandRotWarning = true
		}
	}
	if !hasHandRotWarning {
		t.Error("expected warning about zero hand rotation vectors")
	}

	// Lost hand tracking is the ZERO quaternion (IsUnit false), never identity:
	// identity is a real pose and would defeat the extractor's IsUnit guard.
	frame := result.Frames[0]
	if frame.LeftHandRotation != (model.Quat{}) || frame.LeftHandRotation.IsUnit() {
		t.Errorf("expected zero left hand rotation for lost tracking, got %v", frame.LeftHandRotation)
	}
}

// TestMapper_PossessionDetection verifies disc possession inference.
func TestMapper_PossessionDetection(t *testing.T) {
	session := &adapter.EchoVRSessionResponse{
		SessionID:  "poss-test",
		MatchType:  "Echo_Arena",
		GameStatus: "playing",
		Disc: &adapter.EchoVRDisc{
			Position: [3]float64{5.1, 1.9, -0.1}, // very close to rhand
			Velocity: [3]float64{0.1, 0, 0},      // nearly stationary
		},
		Teams: []adapter.EchoVRTeam{
			{
				TeamName: "BLUE TEAM",
				Players: []adapter.EchoVRPlayer{
					{
						Name:       "Holder",
						UserID:     444,
						Possession: true,
						Body: adapter.EchoVRBodyHead{
							Position: [3]float64{5.0, 1.6, 0.0},
							Forward:  [3]float64{0, 0, 1},
							Left:     [3]float64{-1, 0, 0},
							Up:       [3]float64{0, 1, 0},
						},
						LHand: adapter.EchoVRHand{Position: [3]float64{4.7, 1.9, 0.2}},
						RHand: adapter.EchoVRHand{Position: [3]float64{5.1, 1.9, -0.1}}, // matches disc
						Stats: adapter.EchoVRPlayerStats{},
					},
				},
			},
			{TeamName: "ORANGE TEAM"},
		},
	}

	mapper := adapter.NewMapper()
	result := mapper.MapSession(session)

	if len(result.Frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(result.Frames))
	}

	// Player should have possession (per-player boolean from API)
	if !result.Frames[0].HasPossession {
		t.Error("expected Holder to have possession (possession=true)")
	}
	if result.Frames[0].Disc == nil || !result.Frames[0].Disc.IsHeld {
		t.Error("expected disc to be marked as held")
	}
}

// TestMapper_NoPossessionHighSpeed verifies disc not held when moving fast.
func TestMapper_NoPossessionHighSpeed(t *testing.T) {
	session := &adapter.EchoVRSessionResponse{
		SessionID:  "no-poss-test",
		MatchType:  "Echo_Arena",
		GameStatus: "playing",
		Disc: &adapter.EchoVRDisc{
			Position: [3]float64{5.1, 1.9, -0.1},  // close to hand
			Velocity: [3]float64{15.0, 2.0, -1.0}, // but moving fast
		},
		Teams: []adapter.EchoVRTeam{
			{
				TeamName: "BLUE TEAM",
				Players: []adapter.EchoVRPlayer{
					{
						Name:   "NearDisc",
						UserID: 555,
						Body: adapter.EchoVRBodyHead{
							Position: [3]float64{5.0, 1.6, 0.0},
							Forward:  [3]float64{0, 0, 1},
							Left:     [3]float64{-1, 0, 0},
							Up:       [3]float64{0, 1, 0},
						},
						RHand: adapter.EchoVRHand{Position: [3]float64{5.1, 1.9, -0.1}},
						LHand: adapter.EchoVRHand{Position: [3]float64{4.7, 1.9, 0.2}},
						Stats: adapter.EchoVRPlayerStats{},
					},
				},
			},
			{TeamName: "ORANGE TEAM"},
		},
	}

	mapper := adapter.NewMapper()
	result := mapper.MapSession(session)

	// Disc is near hand but moving fast — should NOT be considered held
	if result.Frames[0].HasPossession {
		t.Error("disc near hand but high speed — should not be held")
	}
}

// TestMapper_SequentialFrames verifies frame index incrementing and dt computation.
func TestMapper_SequentialFrames(t *testing.T) {
	session := loadSession(t, "fixtures/echovr_session_normal.json")
	mapper := adapter.NewMapper()

	// Timestamps come from the producer-supplied sample time, so successive
	// samples are given explicitly (67 ms apart).
	t0 := time.Date(2026, 3, 15, 17, 30, 22, 0, time.UTC)
	r1 := mapper.MapSessionAt(session, t0)
	r2 := mapper.MapSessionAt(session, t0.Add(67*time.Millisecond))
	r3 := mapper.MapSessionAt(session, t0.Add(134*time.Millisecond))

	if r1.Frames[0].FrameIndex != 0 {
		t.Errorf("frame 1 index = %d, want 0", r1.Frames[0].FrameIndex)
	}
	if r2.Frames[0].FrameIndex != 1 {
		t.Errorf("frame 2 index = %d, want 1", r2.Frames[0].FrameIndex)
	}
	if r3.Frames[0].FrameIndex != 2 {
		t.Errorf("frame 3 index = %d, want 2", r3.Frames[0].FrameIndex)
	}

	// DeltaTime should be ~0.067 for frames after the first
	if r2.Frames[0].DeltaTime <= 0 {
		t.Errorf("frame 2 dt = %f, want > 0", r2.Frames[0].DeltaTime)
	}
}

// TestMapper_GamePhaseMapping verifies game phase string mapping.
func TestMapper_GamePhaseMapping(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"playing", "playing"},
		{"round_start", "round_start"},
		{"round_over", "round_over"},
		{"pre_match", "pre_match"},
		{"post_match", "post_match"},
		{"score", "round_over"}, // "score" maps to round_over (non-active)
		{"", "playing"},         // empty defaults to playing
		{"PLAYING", "playing"},  // case-insensitive
	}

	for _, tt := range tests {
		session := &adapter.EchoVRSessionResponse{
			SessionID:  "phase-test",
			MatchType:  "Echo_Arena",
			GameStatus: tt.input,
			Teams: []adapter.EchoVRTeam{
				{Players: []adapter.EchoVRPlayer{{
					Name: "P", UserID: 1,
					Body: adapter.EchoVRBodyHead{
						Position: [3]float64{1, 1, 1},
						Forward:  [3]float64{0, 0, 1},
						Left:     [3]float64{-1, 0, 0},
						Up:       [3]float64{0, 1, 0},
					},
					LHand: adapter.EchoVRHand{Position: [3]float64{0.7, 1.3, 1.2}},
					RHand: adapter.EchoVRHand{Position: [3]float64{1.3, 1.3, 0.8}},
				}}},
				{},
			},
		}
		mapper := adapter.NewMapper()
		result := mapper.MapSession(session)
		if len(result.Frames) == 0 {
			t.Errorf("input=%q: no frames", tt.input)
			continue
		}
		if result.Frames[0].GamePhase != tt.expected {
			t.Errorf("input=%q: got %q, want %q", tt.input, result.Frames[0].GamePhase, tt.expected)
		}
	}
}

// TestMapper_FieldMappingDocumentation verifies all mappings are documented.
func TestMapper_FieldMappingDocumentation(t *testing.T) {
	mappings := adapter.DocumentMappings()

	// Verify we have mappings for all critical fields
	requiredFields := []string{
		"PlayerID", "FrameIndex", "Timestamp", "Position",
		"LeftHandPosition", "RightHandPosition",
		"IsStunned", "HasPossession", "Disc.Position", "Disc.Velocity",
	}

	mappedFields := make(map[string]bool)
	for _, m := range mappings {
		mappedFields[m.InternalField] = true
	}

	for _, f := range requiredFields {
		if !mappedFields[f] {
			t.Errorf("missing mapping documentation for critical field: %s", f)
		}
	}

	// Verify no mapping has empty confidence
	for _, m := range mappings {
		if m.EchoVRField == "" {
			t.Errorf("field %s has empty EchoVRField", m.InternalField)
		}
	}

	t.Logf("documented %d field mappings", len(mappings))
}

// TestMapper_BoostingWarning verifies the absent-field warning for boosting.
func TestMapper_BoostingWarning(t *testing.T) {
	session := loadSession(t, "fixtures/echovr_session_normal.json")
	mapper := adapter.NewMapper()
	result := mapper.MapSession(session)

	// Should warn about boosting being absent
	hasBoosting := false
	for _, w := range result.Warnings {
		if w.Field == "is_boosting" {
			hasBoosting = true
		}
	}
	if !hasBoosting {
		t.Error("expected warning about absent boosting field")
	}

	// IsBoosting should always be false
	for _, f := range result.Frames {
		if f.IsBoosting {
			t.Error("IsBoosting should be false (absent field)")
		}
	}
}
