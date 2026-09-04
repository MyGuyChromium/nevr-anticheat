package adapter

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const fixtureDir = "../../tests/fixtures/"

func loadFixture(t *testing.T, name string) *EchoVRSessionResponse {
	t.Helper()
	data, err := os.ReadFile(fixtureDir + name)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var session EchoVRSessionResponse
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return &session
}

var properBody = EchoVRBodyHead{Forward: [3]float64{0, 0, 1}, Left: [3]float64{1, 0, 0}, Up: [3]float64{0, 1, 0}}

func properHand(pos [3]float64) EchoVRHand {
	return EchoVRHand{Position: pos, Forward: [3]float64{0, 0, 1}, Left: [3]float64{1, 0, 0}, Up: [3]float64{0, 1, 0}}
}

func testPlayer(name string, uid int64, pos [3]float64) EchoVRPlayer {
	body := properBody
	body.Position = pos
	return EchoVRPlayer{
		Name:   name,
		UserID: uid,
		Body:   body,
		LHand:  properHand([3]float64{pos[0] - 0.3, pos[1] + 0.3, pos[2] + 0.2}),
		RHand:  properHand([3]float64{pos[0] + 0.3, pos[1] + 0.3, pos[2] - 0.2}),
		Ping:   40,
	}
}

func twoTeamSession(id string, blue, orange []EchoVRPlayer) *EchoVRSessionResponse {
	return &EchoVRSessionResponse{
		SessionID:  id,
		MatchType:  "Echo_Arena",
		GameStatus: "playing",
		GameClock:  200,
		Disc:       &EchoVRDisc{Position: [3]float64{0, 2, 5}, Velocity: [3]float64{3, 4, 0}},
		Teams: []EchoVRTeam{
			{TeamName: "BLUE TEAM", Players: blue},
			{TeamName: "ORANGE TEAM", Players: orange},
		},
	}
}

func frameByPlayer(frames []model.PlayerTelemetryFrame, pid string) *model.PlayerTelemetryFrame {
	for i := range frames {
		if frames[i].PlayerID == pid {
			return &frames[i]
		}
	}
	return nil
}

// F20/F56: timestamps come from the producer's sample time, dt is per player.
func TestMapper_RealTimestamps(t *testing.T) {
	m := NewMapper()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	b := testPlayer("B", 2, [3]float64{-1, 1.6, 10})
	a.Velocity = [3]float64{2, 0, -1}
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	r0 := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, []EchoVRPlayer{b}), t0)
	if len(r0.Frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(r0.Frames))
	}
	for _, f := range r0.Frames {
		if f.Timestamp != 0 || f.DeltaTime != 0 || f.FrameIndex != 0 {
			t.Errorf("first sample: ts=%v dt=%v idx=%d, want 0/0/0", f.Timestamp, f.DeltaTime, f.FrameIndex)
		}
	}
	if !r0.MatchCtx.StartTime.Equal(t0) {
		t.Errorf("match StartTime = %v, want first sample time %v", r0.MatchCtx.StartTime, t0)
	}
	if f := frameByPlayer(r0.Frames, "echovr:1"); f == nil || f.ReportedVelocity == nil || *f.ReportedVelocity != (model.Vec3{2, 0, -1}) {
		t.Errorf("reported Echo velocity was not mapped: %+v", f)
	}
	if f := frameByPlayer(r0.Frames, "echovr:2"); f == nil || f.ReportedVelocity == nil || *f.ReportedVelocity != (model.Vec3{}) {
		t.Errorf("zero reported velocity must remain present, not missing: %+v", f)
	}

	// 200 ms recorder hiccup: dt must be 0.2, not a fabricated 0.067.
	r1 := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, []EchoVRPlayer{b}), t0.Add(200*time.Millisecond))
	for _, f := range r1.Frames {
		if math.Abs(f.Timestamp-0.2) > 1e-9 || math.Abs(f.DeltaTime-0.2) > 1e-9 || f.FrameIndex != 1 {
			t.Errorf("second sample: ts=%v dt=%v idx=%d, want 0.2/0.2/1", f.Timestamp, f.DeltaTime, f.FrameIndex)
		}
	}

	// Player B misses a tick: A's dt is 0.067, B's dt on return spans two ticks.
	r2 := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), t0.Add(267*time.Millisecond))
	if fa := frameByPlayer(r2.Frames, "echovr:1"); fa == nil || math.Abs(fa.DeltaTime-0.067) > 1e-9 {
		t.Errorf("A dt after 67ms = %+v", fa)
	}
	r3 := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, []EchoVRPlayer{b}), t0.Add(334*time.Millisecond))
	if fb := frameByPlayer(r3.Frames, "echovr:2"); fb == nil || math.Abs(fb.DeltaTime-0.134) > 1e-9 {
		t.Errorf("B dt after skipping a tick = %+v, want 0.134", fb)
	}
	if fa := frameByPlayer(r3.Frames, "echovr:1"); fa == nil || math.Abs(fa.DeltaTime-0.067) > 1e-9 {
		t.Errorf("A dt = %+v, want 0.067", fa)
	}

	// Clock going backwards: dt reported as 0 (unknown) and counted.
	r4 := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), t0.Add(300*time.Millisecond))
	if fa := frameByPlayer(r4.Frames, "echovr:1"); fa == nil || fa.DeltaTime != 0 {
		t.Errorf("non-monotonic dt = %+v, want 0", fa)
	}
	if m.Stats().NonMonotonicSamples != 1 {
		t.Errorf("NonMonotonicSamples = %d, want 1", m.Stats().NonMonotonicSamples)
	}
	if !m.FirstSampleTime().Equal(t0) {
		t.Errorf("FirstSampleTime = %v", m.FirstSampleTime())
	}
}

// F21: spectators and unknown teams are dropped; Team is populated per frame.
func TestMapper_ThreeTeamsDropsSpectators(t *testing.T) {
	session := loadFixture(t, "echovr_session_three_teams.json")
	if len(session.Teams) != 3 {
		t.Fatalf("fixture should have 3 teams, got %d", len(session.Teams))
	}
	m := NewMapper()
	r := m.MapSession(session)

	if len(r.Frames) != 2 {
		t.Fatalf("expected 2 frames (spectator dropped), got %d", len(r.Frames))
	}
	if r.SpectatorsDropped != 1 || m.Stats().SpectatorsDropped != 1 {
		t.Errorf("SpectatorsDropped = %d / %d, want 1", r.SpectatorsDropped, m.Stats().SpectatorsDropped)
	}
	if f := frameByPlayer(r.Frames, "echovr:9001"); f != nil {
		t.Errorf("spectator reached the frames: %+v", f)
	}
	if f := frameByPlayer(r.Frames, "echovr:1001"); f == nil || f.Team != "blue" {
		t.Errorf("blue player frame = %+v", f)
	}
	if f := frameByPlayer(r.Frames, "echovr:2001"); f == nil || f.Team != "orange" {
		t.Errorf("orange player frame = %+v", f)
	}

	ctx := r.MatchCtx
	if len(ctx.PlayerIDs) != 2 {
		t.Errorf("roster = %v, want the 2 playing players", ctx.PlayerIDs)
	}
	if _, ok := ctx.TeamAssignments["echovr:9001"]; ok {
		t.Error("spectator present in TeamAssignments")
	}
	if ctx.TeamAssignments["echovr:1001"] != "blue" || ctx.TeamAssignments["echovr:2001"] != "orange" {
		t.Errorf("team assignments = %v", ctx.TeamAssignments)
	}

	warned := false
	for _, w := range r.Warnings {
		if w.Field == "teams" {
			warned = true
		}
	}
	if !warned {
		t.Error("expected a one-time warning about the excluded team")
	}
}

func TestMappedTeamName(t *testing.T) {
	cases := []struct {
		name string
		idx  int
		want string
		ok   bool
	}{
		{"BLUE TEAM", 0, "blue", true},
		{"orange team", 0, "orange", true},
		{"Blue", 1, "blue", true},
		{"SPECTATORS", 2, "", false},
		{"SPECTATORS", 0, "", false},
		{"", 0, "blue", true},
		{"", 1, "orange", true},
		{"", 2, "", false},
		{"RED TEAM", 0, "", false},
	}
	for _, c := range cases {
		got, ok := mappedTeamName(c.name, c.idx)
		if got != c.want || ok != c.ok {
			t.Errorf("mappedTeamName(%q,%d) = %q,%v want %q,%v", c.name, c.idx, got, ok, c.want, c.ok)
		}
	}
}

// F23/F43: one disc state per tick, identical on every player's frame.
func TestMapper_DiscSharedAcrossFrames(t *testing.T) {
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	b := testPlayer("B", 2, [3]float64{-1, 1.6, 10})
	b.Possession = true
	c := testPlayer("C", 3, [3]float64{2, 1.6, 12})
	r := NewMapper().MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, []EchoVRPlayer{b, c}), time.Unix(0, 0))
	if len(r.Frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(r.Frames))
	}
	for _, f := range r.Frames {
		if f.Disc == nil {
			t.Fatalf("%s: nil disc", f.PlayerID)
		}
		if !f.Disc.IsHeld || f.Disc.PossessorID != "echovr:2" {
			t.Errorf("%s: disc = held:%v possessor:%q, want held by echovr:2", f.PlayerID, f.Disc.IsHeld, f.Disc.PossessorID)
		}
		if math.Abs(f.Disc.Speed-5) > 1e-12 {
			t.Errorf("%s: disc speed = %v, want 5", f.PlayerID, f.Disc.Speed)
		}
	}
	// HasPossession stays per player.
	if frameByPlayer(r.Frames, "echovr:1").HasPossession || !frameByPlayer(r.Frames, "echovr:2").HasPossession {
		t.Error("HasPossession should reflect the individual player's flag")
	}
	// Frames hold independent copies so a consumer mutating one does not leak.
	r.Frames[0].Disc.FramesSinceRelease = 99
	if r.Frames[1].Disc.FramesSinceRelease != 0 {
		t.Error("disc state aliased between frames")
	}

	// Nobody holds it: free on every frame.
	r = NewMapper().MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, []EchoVRPlayer{c}), time.Unix(0, 0))
	for _, f := range r.Frames {
		if f.Disc.IsHeld || f.Disc.PossessorID != "" {
			t.Errorf("%s: free disc marked held", f.PlayerID)
		}
	}

	// A spectator "holding" the disc does not count; two real holders are a conflict.
	spec := testPlayer("S", 9, [3]float64{0, 5, 0})
	spec.Possession = true
	s := twoTeamSession("m", []EchoVRPlayer{a}, []EchoVRPlayer{c})
	s.Teams = append(s.Teams, EchoVRTeam{TeamName: "SPECTATORS", Players: []EchoVRPlayer{spec}})
	r = NewMapper().MapSessionAt(s, time.Unix(0, 0))
	for _, f := range r.Frames {
		if f.Disc.IsHeld {
			t.Errorf("%s: spectator possession leaked into disc state", f.PlayerID)
		}
	}
	a2 := a
	a2.Possession = true
	b2 := b
	m := NewMapper()
	r = m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a2}, []EchoVRPlayer{b2}), time.Unix(0, 0))
	if m.Stats().PossessionConflicts != 1 {
		t.Errorf("PossessionConflicts = %d, want 1", m.Stats().PossessionConflicts)
	}
	if r.Frames[0].Disc.PossessorID != "echovr:1" || r.Frames[1].Disc.PossessorID != "echovr:1" {
		t.Errorf("conflict resolution not deterministic: %q / %q", r.Frames[0].Disc.PossessorID, r.Frames[1].Disc.PossessorID)
	}
}

func TestMapper_HoldingFieldsOverrideStalePossession(t *testing.T) {
	p := testPlayer("Thrower", 1, [3]float64{1, 1.6, -10})
	p.Possession = true
	p.HoldingLeft, p.HoldingRight = "none", "none"
	r := NewMapper().MapSessionAt(twoTeamSession("m", []EchoVRPlayer{p}, nil), time.Unix(0, 0))
	if len(r.Frames) != 1 {
		t.Fatalf("frames=%d, want 1", len(r.Frames))
	}
	if r.Frames[0].HasPossession || r.Frames[0].Disc.IsHeld {
		t.Fatalf("explicit empty hands must override stale possession: %+v", r.Frames[0])
	}

	p.HoldingRight = "disc"
	r = NewMapper().MapSessionAt(twoTeamSession("m", []EchoVRPlayer{p}, nil), time.Unix(0, 0))
	if !r.Frames[0].HasPossession || !r.Frames[0].Disc.IsHeld || r.Frames[0].Disc.PossessorID != "echovr:1" {
		t.Fatalf("explicit disc hand was not mapped as possession: %+v", r.Frames[0])
	}
}

func TestMapper_GameLastThrowChangeGoesOnlyToLocalPlayer(t *testing.T) {
	a := testPlayer("Local", 1, [3]float64{1, 1.6, -10})
	b := testPlayer("Remote", 2, [3]float64{-1, 1.6, 10})
	m := NewMapper()
	m.SetDedupeIdentical(true)
	t0 := time.Unix(1000, 0)

	baseline := twoTeamSession("m", []EchoVRPlayer{a}, []EchoVRPlayer{b})
	baseline.ClientName = "Local"
	baseline.LastThrow = &EchoVRLastThrow{}
	r0 := m.MapSessionAt(baseline, t0)
	if frameByPlayer(r0.Frames, "echovr:1").GameLastThrow != nil {
		t.Fatal("the first last_throw value is a baseline, not a new throw")
	}

	changed := twoTeamSession("m", []EchoVRPlayer{a}, []EchoVRPlayer{b})
	changed.ClientName = "Local"
	changed.LastThrow = &EchoVRLastThrow{
		ArmSpeed: 12.4, TotalSpeed: 19.91, OffAxisSpinDeg: 3.2,
		WristThrowPenalty: 0.4, RotPerSec: 8.1, PotentialSpeedFromRot: 2.5,
		SpeedFromArm: 12, SpeedFromMovement: 4.2, SpeedFromWrist: 3.71,
		WristAlignToThrowDeg: 4.5, ThrowAlignToMovementDeg: 7.5,
		OffAxisPenalty: 0.2, ThrowMovePenalty: 0.1,
	}
	r1 := m.MapSessionAt(changed, t0.Add(67*time.Millisecond))
	if r1.SkippedDuplicate {
		t.Fatal("a last_throw change must survive live snapshot deduplication")
	}
	local := frameByPlayer(r1.Frames, "echovr:1")
	remote := frameByPlayer(r1.Frames, "echovr:2")
	if local == nil || local.GameLastThrow == nil || local.GameLastThrow.TotalSpeed != 19.91 || local.GameLastThrow.SpeedFromMovement != 4.2 {
		t.Fatalf("local engine throw was not mapped: %+v", local)
	}
	if remote == nil || remote.GameLastThrow != nil {
		t.Fatalf("local-only last_throw leaked to remote player: %+v", remote)
	}

	// With dedupe disabled, a repeated engine record maps a normal frame but
	// does not pulse again and cannot be attached to a later release.
	m.SetDedupeIdentical(false)
	r2 := m.MapSessionAt(changed, t0.Add(134*time.Millisecond))
	if frameByPlayer(r2.Frames, "echovr:1").GameLastThrow != nil {
		t.Fatal("an unchanged last_throw record must not be emitted twice")
	}
}

// F97: lost hand tracking is the zero quaternion, and all three vectors are required.
func TestMapper_LostHandTrackingIsZeroQuat(t *testing.T) {
	p := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	p.LHand.Forward = [3]float64{}
	p.LHand.Up = [3]float64{}
	p.LHand.Left = [3]float64{}
	p.RHand.Left = [3]float64{} // only left missing: still treated as lost
	m := NewMapper()
	r := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{p}, nil), time.Unix(0, 0))
	if len(r.Frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(r.Frames))
	}
	f := r.Frames[0]
	if f.LeftHandRotation != (model.Quat{}) || f.LeftHandRotation.IsUnit() {
		t.Errorf("left hand rotation = %v, want zero quaternion", f.LeftHandRotation)
	}
	if f.RightHandRotation != (model.Quat{}) || f.RightHandRotation.IsUnit() {
		t.Errorf("right hand rotation (missing left) = %v, want zero quaternion", f.RightHandRotation)
	}
	if !f.Rotation.IsUnit() {
		t.Errorf("body rotation should be unit: %v", f.Rotation)
	}
	if m.Stats().HandTrackingLost != 2 {
		t.Errorf("HandTrackingLost = %d, want 2", m.Stats().HandTrackingLost)
	}

	// Body with degenerate basis: zero quaternion, frame still produced.
	p2 := testPlayer("B", 2, [3]float64{1, 1.6, -10})
	p2.Body.Forward = [3]float64{}
	p2.Body.Left = [3]float64{}
	p2.Body.Up = [3]float64{}
	r = NewMapper().MapSessionAt(twoTeamSession("m", []EchoVRPlayer{p2}, nil), time.Unix(0, 0))
	if len(r.Frames) != 1 {
		t.Fatalf("expected 1 frame, got %d (errors %v)", len(r.Frames), r.Errors)
	}
	if r.Frames[0].Rotation.IsUnit() {
		t.Errorf("degenerate body basis should give zero quaternion, got %v", r.Frames[0].Rotation)
	}
}

// F44/F45: basis quality is counted once per pose and warned once per session.
func TestMapper_BasisQualityCounters(t *testing.T) {
	m := NewMapper()
	session := loadFixture(t, "echovr_session_normal.json") // reflected basis (left = -X at forward +Z)
	r := m.MapSession(session)
	if len(r.Frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(r.Frames))
	}
	st := m.Stats()
	if st.BasesReflected != 6 || st.BasesProper != 0 || st.BasesDegenerate != 0 {
		t.Errorf("stats after reflected fixture = %+v, want 6 reflected poses", st)
	}
	warned := 0
	for _, w := range r.Warnings {
		if w.Field == "basis_reflected" {
			warned++
		}
	}
	if warned != 1 {
		t.Errorf("reflected warning emitted %d times, want once", warned)
	}
	// Second snapshot: no new warning, counter keeps growing.
	r2 := m.MapSession(session)
	for _, w := range r2.Warnings {
		if w.Field == "basis_reflected" {
			t.Error("reflected warning repeated on second snapshot")
		}
	}
	if m.Stats().BasesReflected != 12 {
		t.Errorf("BasesReflected = %d, want 12", m.Stats().BasesReflected)
	}

	// Proper basis counts as proper and the two players' rotations are exact.
	m2 := NewMapper()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	r = m2.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), time.Unix(0, 0))
	if m2.Stats().BasesProper != 3 || m2.Stats().BasesReflected != 0 {
		t.Errorf("proper stats = %+v", m2.Stats())
	}
	if !r.Frames[0].Rotation.IsUnit() || math.Abs(math.Abs(r.Frames[0].Rotation.W())-1) > 1e-9 {
		t.Errorf("proper identity basis should map to identity, got %v", r.Frames[0].Rotation)
	}
}

// F95: change detection on the live path.
func TestMapper_DedupeIdenticalSnapshots(t *testing.T) {
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	m := NewMapper()
	m.SetDedupeIdentical(true)
	t0 := time.Unix(1000, 0)

	r0 := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), t0)
	if r0.SkippedDuplicate || len(r0.Frames) != 1 {
		t.Fatalf("first snapshot should map: %+v", r0)
	}
	r1 := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), t0.Add(67*time.Millisecond))
	if !r1.SkippedDuplicate || len(r1.Frames) != 0 || r1.MatchCtx == nil {
		t.Fatalf("identical snapshot should be skipped with context: %+v", r1)
	}
	if m.Stats().DuplicatesSkipped != 1 {
		t.Errorf("DuplicatesSkipped = %d", m.Stats().DuplicatesSkipped)
	}

	// A moved player is a new frame; its dt spans the skipped poll and the
	// frame index does not skip a number for the duplicate.
	moved := a
	moved.Body.Position[2] += 0.5
	r2 := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{moved}, nil), t0.Add(134*time.Millisecond))
	if r2.SkippedDuplicate || len(r2.Frames) != 1 {
		t.Fatalf("changed snapshot should map: %+v", r2)
	}
	if r2.Frames[0].FrameIndex != 1 || math.Abs(r2.Frames[0].DeltaTime-0.134) > 1e-9 {
		t.Errorf("after duplicate: idx=%d dt=%v, want 1 / 0.134", r2.Frames[0].FrameIndex, r2.Frames[0].DeltaTime)
	}

	// Game clock alone changing is a new sample too.
	s := twoTeamSession("m", []EchoVRPlayer{moved}, nil)
	s.GameClock = 199.9
	if r3 := m.MapSessionAt(s, t0.Add(201*time.Millisecond)); r3.SkippedDuplicate {
		t.Error("game_clock change must not be deduplicated")
	}

	// Default is off: identical snapshots map every time.
	m2 := NewMapper()
	m2.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), t0)
	if r := m2.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), t0.Add(time.Millisecond)); r.SkippedDuplicate {
		t.Error("dedupe should be opt-in")
	}
}

// F96: physics reach the produced MatchContext.
func TestMapper_SetPhysics(t *testing.T) {
	m := NewMapper()
	phys := model.DefaultPhysics()
	phys.DiscSpeedCap = 42
	m.SetPhysics(phys)
	r := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{testPlayer("A", 1, [3]float64{1, 1, 1})}, nil), time.Unix(0, 0))
	if r.MatchCtx.Physics.DiscSpeedCap != 42 {
		t.Errorf("physics not propagated: %v", r.MatchCtx.Physics.DiscSpeedCap)
	}
}

// F98: MergeMatchContext unions rosters and takes the latest team.
func TestMergeMatchContext(t *testing.T) {
	dst := &model.MatchContext{MatchID: "m", PlayerIDs: []string{"a"}, TeamAssignments: map[string]string{"a": "blue"}}
	src := &model.MatchContext{MatchID: "m", PlayerIDs: []string{"a", "b"}, TeamAssignments: map[string]string{"a": "orange", "b": "orange"},
		PlayerNames: map[string]string{"b": "Bee"}}
	MergeMatchContext(dst, src)
	if len(dst.PlayerIDs) != 2 || dst.PlayerIDs[1] != "b" {
		t.Errorf("roster = %v", dst.PlayerIDs)
	}
	if dst.TeamAssignments["a"] != "orange" || dst.TeamAssignments["b"] != "orange" {
		t.Errorf("teams = %v", dst.TeamAssignments)
	}
	if len(dst.PlayerNames) != 1 || dst.PlayerNames["b"] != "Bee" {
		t.Errorf("names = %v", dst.PlayerNames)
	}
	MergeMatchContext(dst, src) // idempotent
	if len(dst.PlayerIDs) != 2 {
		t.Errorf("merge not idempotent: %v", dst.PlayerIDs)
	}
	MergeMatchContext(nil, src)
	MergeMatchContext(dst, nil)
}

func TestMapper_MappedFrameFieldsUnchanged(t *testing.T) {
	// Regression guard for the fixture path: values that were correct before
	// the time-base change stay correct.
	session := loadFixture(t, "echovr_session_normal.json")
	r := NewMapper().MapSession(session)
	if len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	p1 := frameByPlayer(r.Frames, "echovr:1234567890")
	if p1 == nil || p1.Team != "blue" || p1.Goals != 1 || p1.BlueScore != 4 || p1.EstimatedPingMs != 0 {
		t.Errorf("player one frame = %+v", p1)
	}
	p2 := frameByPlayer(r.Frames, "echovr:9876543210")
	if p2 == nil || p2.Team != "orange" {
		t.Errorf("player two frame = %+v", p2)
	}
	if p1.Disc == nil || p2.Disc == nil || *p1.Disc != *p2.Disc {
		t.Errorf("disc differs between frames: %+v vs %+v", p1.Disc, p2.Disc)
	}
}
