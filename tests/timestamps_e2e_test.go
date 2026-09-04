package tests

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// replaySnapshot describes one .echoreplay line of a two-player session.
type replaySnapshot struct {
	at         time.Time
	pos        model.Vec3 // player one body position
	possession bool
	discPos    model.Vec3
	discVel    model.Vec3
}

// replayLine renders an .echoreplay line ("YYYY/MM/DD HH:MM:SS.mmm\t{json}")
// in the confirmed Echo VR /session layout. Player one moves; player two is
// static so the roster has both teams.
func replayLine(s replaySnapshot) string {
	vec := func(v model.Vec3) string { return fmt.Sprintf("[%g,%g,%g]", v[0], v[1], v[2]) }
	basis := `"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]`
	hand := func(p model.Vec3, dx float64) string {
		return fmt.Sprintf(`{"pos":%s,%s}`, vec(model.Vec3{p[0] + dx, p[1] + 0.3, p[2]}), basis)
	}
	player := func(name string, userID int64, p model.Vec3, possession bool) string {
		return fmt.Sprintf(`{"name":%q,"userid":%d,"playerid":0,"body":{"position":%s,%s},"head":{"position":%s,%s},`+
			`"velocity":[0,0,0],"lhand":%s,"rhand":%s,"stunned":false,"invulnerable":false,"possession":%v,"blocking":false,"ping":40,"stats":{}}`,
			name, userID, vec(p), basis, vec(p), basis, hand(p, -0.3), hand(p, 0.3), possession)
	}
	json := fmt.Sprintf(`{"sessionid":"TS-E2E","match_type":"Echo_Arena","map_name":"mpl_arena_a","game_status":"playing","game_clock":200.0,`+
		`"disc":{"position":%s,"velocity":%s},"blue_points":0,"orange_points":0,`+
		`"teams":[{"team":"BLUE TEAM","players":[%s]},{"team":"ORANGE TEAM","players":[%s]}]}`,
		vec(s.discPos), vec(s.discVel),
		player("Mover", 1001, s.pos, s.possession),
		player("Static", 1002, model.Vec3{-3, 1.6, 10}, false))
	return s.at.UTC().Format("2006/01/02 15:04:05.000") + "\t" + json
}

// TestTimestamps_ReplayIntervalsDriveFrameDtAndVelocity: an .echoreplay whose
// lines are 50 / 67 / 200 ms apart must yield exactly those FrameDt values
// per frame and velocities computed from the REAL interval, with no fixed
// tick-rate assumption anywhere between the file and the feature extractor.
func TestTimestamps_ReplayIntervalsDriveFrameDtAndVelocity(t *testing.T) {
	intervals := []time.Duration{50 * time.Millisecond, 67 * time.Millisecond, 200 * time.Millisecond}
	const speedZ = 3.0 // m/s along Z
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	var lines []string
	var wantDt []float64
	at := start
	pos := model.Vec3{2, 1.6, 0}
	for i := 0; i < 30; i++ {
		lines = append(lines, replayLine(replaySnapshot{at: at, pos: pos, discPos: model.Vec3{0, 2, 20}}))
		iv := intervals[i%len(intervals)]
		wantDt = append(wantDt, iv.Seconds())
		at = at.Add(iv)
		pos[2] += speedZ * iv.Seconds()
	}
	path := writeNDJSON(t, t.TempDir(), "intervals.echoreplay", lines)

	parser := adapter.NewEchoReplayParser()
	matchCtx, frames, diag, err := parser.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diag.FramesRejected != 0 || len(frames) != 60 {
		t.Fatalf("parsed %d frames, %d rejected", len(frames), diag.FramesRejected)
	}
	if !matchCtx.StartTime.Equal(start) {
		t.Errorf("MatchContext.StartTime = %v, want first sample %v", matchCtx.StartTime, start)
	}

	fe := pipeline.NewFeatureExtractor(30)
	ps := &model.PlayerState{PlayerID: "echovr:1001"}
	n := 0
	for i := range frames {
		f := &frames[i]
		if f.PlayerID != "echovr:1001" {
			continue
		}
		prevPos := ps.Position
		fe.UpdatePlayerState(ps, f, matchCtx)
		if n == 0 {
			if ps.FrameDt != 0 || f.DeltaTime != 0 || f.Timestamp != 0 {
				t.Errorf("first frame: dt must be unknown, got FrameDt=%v DeltaTime=%v ts=%v", ps.FrameDt, f.DeltaTime, f.Timestamp)
			}
		} else {
			want := wantDt[n-1]
			if math.Abs(ps.FrameDt-want) > 1e-6 || math.Abs(f.DeltaTime-want) > 1e-6 {
				t.Errorf("frame %d: FrameDt=%.4f DeltaTime=%.4f, want %.3f", n, ps.FrameDt, f.DeltaTime, want)
			}
			wantVel := f.Position.Sub(prevPos).Scale(1 / want)
			if math.Abs(ps.Velocity[2]-speedZ) > 1e-6 || ps.Velocity.Sub(wantVel).Magnitude() > 1e-6 {
				t.Errorf("frame %d (dt %.3f): velocity %v, want %v (%.1f m/s on Z)", n, want, ps.Velocity, wantVel, speedZ)
			}
		}
		n++
	}
	if n != 30 {
		t.Fatalf("player one frames = %d", n)
	}
}

// TestTimestamps_Throw001FiresAt15FpsFromReplay: a 15 fps (67 ms) replay
// with a 35 m/s release must trigger THROW_001 through the real parser,
// extractor and pipeline, with no timestamp or FrameDt override anywhere.
func TestTimestamps_Throw001FiresAt15FpsFromReplay(t *testing.T) {
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	const dt = 67 * time.Millisecond
	pos := model.Vec3{2, 1.6, 0}
	hand := model.Vec3{2.3, 1.9, 0}
	toGoal := model.Vec3{0, 0, 1}
	var lines []string
	at := start
	add := func(s replaySnapshot) {
		s.at = at
		lines = append(lines, replayLine(s))
		at = at.Add(dt)
	}
	for i := 0; i < 20; i++ { // warmup, disc idle far away
		add(replaySnapshot{pos: pos, discPos: model.Vec3{0, 2, 20}})
	}
	for i := 0; i < 10; i++ { // holding
		add(replaySnapshot{pos: pos, possession: true, discPos: hand})
	}
	add(replaySnapshot{pos: pos, discPos: hand.Add(toGoal.Scale(0.5)), discVel: toGoal.Scale(35)}) // release
	for i := 1; i <= 15; i++ {                                                                     // flight
		add(replaySnapshot{pos: pos, discPos: hand.Add(toGoal.Scale(35 * dt.Seconds() * float64(i))), discVel: toGoal.Scale(34)})
	}
	path := writeNDJSON(t, t.TempDir(), "throw15.echoreplay", lines)

	parser := adapter.NewEchoReplayParser()
	matchCtx, frames, _, err := parser.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frames {
		if f.DeltaTime != 0 && math.Abs(f.DeltaTime-dt.Seconds()) > 1e-6 {
			t.Fatalf("replay dt %.4f, want 0.067 (15 fps)", f.DeltaTime)
		}
	}
	cfg := config.DefaultConfig()
	if !cfg.Detectors["THROW_001"].Enabled {
		t.Skip("THROW_001 disabled in the default config")
	}
	hr := testutil.NewHarness(t).WithDetectors("THROW_001").WithMatchContext(matchCtx).Run(t, frames)
	hr.AssertDetectorFired("THROW_001")
	for _, ev := range hr.DetectorEvents("THROW_001") {
		if ev.PlayerID != "echovr:1001" || ev.CausalKey.AnomalyType != "disc_speed" {
			t.Errorf("unexpected THROW_001 event: %+v", ev)
		}
		te, ok := ev.Evidence.(model.ThrowEvidence)
		if !ok || math.Abs(te.ReleaseSpeed-35) > 1e-6 {
			t.Errorf("release speed should be the game-reported 35 m/s: %#v", ev.Evidence)
		}
		if !strings.Contains(ev.ObservedValue, "35.00 m/s") {
			t.Errorf("observed = %q", ev.ObservedValue)
		}
	}
}
