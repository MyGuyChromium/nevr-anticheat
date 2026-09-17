package adapter

import (
	"testing"
	"time"

	capture "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// phaseParityStep is one game state as each source spells it: the /session
// API string and the native capture enum. The unnamed state is "" on one
// path and UNSPECIFIED on the other.
type phaseParityStep struct {
	session string
	native  capture.GameStatus
	want    string
}

func replayPhases(t *testing.T, steps []phaseParityStep) []string {
	t.Helper()
	m := NewMapper()
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var out []string
	for i, step := range steps {
		s := twoTeamSession("phase-parity", []EchoVRPlayer{testPlayer("Blue", 1, [3]float64{1, 1.6, -10})},
			[]EchoVRPlayer{testPlayer("Orange", 2, [3]float64{-1, 1.6, 10})})
		s.GameStatus = step.session
		s.GameClock = 200 - float64(i) // keep snapshots distinct
		res := m.MapSessionAt(s, start.Add(time.Duration(i)*50*time.Millisecond))
		if len(res.Frames) != 2 {
			t.Fatalf("replay step %d mapped %d frames", i, len(res.Frames))
		}
		if res.Frames[0].GamePhase != res.Frames[1].GamePhase {
			t.Fatalf("replay step %d: players of one tick disagree on the phase: %q vs %q", i, res.Frames[0].GamePhase, res.Frames[1].GamePhase)
		}
		out = append(out, res.Frames[0].GamePhase)
	}
	return out
}

func tapePhases(t *testing.T, steps []phaseParityStep) []string {
	t.Helper()
	d := NewTapeRawDecoder()
	h := tapeTestHeader()
	var out []string
	for i, step := range steps {
		f := tapeTestFrame(uint32(i))
		f.GetEchoArena().GameStatus = step.native
		if i == 0 {
			f.GetEchoArena().Events = []*capture.EchoEvent{tapeTestGrab(1, "none", "none")}
		}
		tick, err := d.Decode(tapeTestRaw(t, h, f))
		if err != nil {
			t.Fatalf("tape step %d: %v", i, err)
		}
		if len(tick.Frames) != 1 {
			t.Fatalf("tape step %d mapped %d frames", i, len(tick.Frames))
		}
		out = append(out, tick.Frames[0].GamePhase)
	}
	return out
}

// The same game state must be active or inactive no matter which ingest path
// recorded it. Before the shared normaliser the replay path read the unnamed
// post-goal state as live play and the tape path read every UNSPECIFIED as
// inactive.
func TestUnnamedGameStatusHasOnePhaseOnReplayAndTape(t *testing.T) {
	const (
		unnamed = capture.GameStatus_GAME_STATUS_UNSPECIFIED
		playing = capture.GameStatus_GAME_STATUS_PLAYING
	)
	steps := []phaseParityStep{
		{"pre_match", capture.GameStatus_GAME_STATUS_PRE_MATCH, "pre_match"},
		{"", unnamed, model.PhasePreRoundGap},
		{"round_start", capture.GameStatus_GAME_STATUS_ROUND_START, "round_start"},
		{"playing", playing, "playing"},
		{"", unnamed, "playing"},
		{"score", capture.GameStatus_GAME_STATUS_SCORE, "round_over"},
		{"", unnamed, model.PhasePostScoreGap},
		{"", unnamed, model.PhasePostScoreGap},
		{"round_start", capture.GameStatus_GAME_STATUS_ROUND_START, "round_start"},
		{"playing", playing, "playing"},
		{"sudden_death", capture.GameStatus_GAME_STATUS_SUDDEN_DEATH, "sudden_death"},
		{"post_match", capture.GameStatus_GAME_STATUS_POST_MATCH, "post_match"},
	}
	replay, tape := replayPhases(t, steps), tapePhases(t, steps)
	mc := &model.MatchContext{}
	for i, step := range steps {
		if replay[i] != step.want || tape[i] != step.want {
			t.Errorf("step %d (%q): replay %q, tape %q, want %q", i, step.session, replay[i], tape[i], step.want)
		}
		if mc.IsActivePhase(replay[i]) != mc.IsActivePhase(tape[i]) {
			t.Errorf("step %d: replay and tape disagree on whether %q is live play", i, step.session)
		}
	}
	for _, i := range []int{1, 6, 7} {
		if mc.IsActivePhase(replay[i]) || mc.IsActivePhase(tape[i]) {
			t.Errorf("step %d: the unnamed goal-cycle gap must not be live play", i)
		}
	}
}

// A recording that never names a status keeps the active fallback on both
// paths, so a source without game_status is not blinded.
func TestNeverNamedGameStatusStaysActiveOnReplayAndTape(t *testing.T) {
	steps := make([]phaseParityStep, 40)
	for i := range steps {
		steps[i] = phaseParityStep{"", capture.GameStatus_GAME_STATUS_UNSPECIFIED, "playing"}
	}
	replay, tape := replayPhases(t, steps), tapePhases(t, steps)
	mc := &model.MatchContext{}
	for i := range steps {
		if replay[i] != "playing" || tape[i] != "playing" || !mc.IsActivePhase(replay[i]) || !mc.IsActivePhase(tape[i]) {
			t.Fatalf("tick %d: replay %q tape %q must both stay the active fallback", i, replay[i], tape[i])
		}
	}
}

// NewMatch restarts the mapper's time base; the normaliser must not carry a
// "score" from the previous match into the next one.
func TestPhaseDoesNotLeakAcrossMapperMatches(t *testing.T) {
	m := NewMapper()
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	session := func(id, status string, clock float64) *EchoVRSessionResponse {
		s := twoTeamSession(id, []EchoVRPlayer{testPlayer("Blue", 1, [3]float64{1, 1.6, -10})}, nil)
		s.GameStatus, s.GameClock = status, clock
		return s
	}
	m.MapSessionAt(session("first", "playing", 100), start)
	m.MapSessionAt(session("first", "score", 99), start.Add(time.Second))
	m.NewMatch()
	res := m.MapSessionAt(session("second", "", 300), start.Add(2*time.Second))
	if len(res.Frames) != 1 || res.Frames[0].GamePhase != "playing" {
		t.Fatalf("second match inherited the first match's status: %+v", res.Frames)
	}
}
