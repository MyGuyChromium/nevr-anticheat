package model

import "testing"

type phaseStep struct {
	status string
	t      float64
	want   string
	active bool
}

func runPhaseSteps(t *testing.T, steps []phaseStep) {
	t.Helper()
	var n PhaseNormalizer
	for i, s := range steps {
		got := n.Normalize("session-a", s.status, s.t)
		// The mapper repeats the call for every player of a tick.
		if again := n.Normalize("session-a", s.status, s.t); again != got {
			t.Fatalf("step %d: repeated call changed the phase: %q then %q", i, got, again)
		}
		if got != s.want {
			t.Fatalf("step %d (%q at %.2fs): phase %q, want %q", i, s.status, s.t, got, s.want)
		}
		if IsActiveGamePhase(got) != s.active {
			t.Fatalf("step %d (%q): active=%v, want %v", i, s.status, !s.active, s.active)
		}
	}
}

// The goal cycle the client really reports: playing -> score -> (unnamed for
// a few seconds) -> round_start -> playing, plus the same unnamed state
// between pre_match and round_start.
func TestPhaseNormalizerGoalCycleGapIsNotPlay(t *testing.T) {
	runPhaseSteps(t, []phaseStep{
		{"pre_match", 0, "pre_match", false},
		{"", 1, PhasePreRoundGap, false},
		{"round_start", 2, "round_start", false},
		{"playing", 3, "playing", true},
		{"score", 4, "round_over", false},
		{"", 5, PhasePostScoreGap, false},
		{"  ", 9.5, PhasePostScoreGap, false},
		{"round_start", 10, "round_start", false},
		{"playing", 11, "playing", true},
		{"sudden_death", 12, "sudden_death", true},
		{"post_sudden_death", 13, "post_sudden_death", false},
	})
}

// A source that never fills game_status keeps the active fallback: the
// normaliser must not blind detectors for such a recording.
func TestPhaseNormalizerNeverNamedStaysActive(t *testing.T) {
	var n PhaseNormalizer
	for i := 0; i < 600; i++ {
		for _, status := range []string{"", "unknown"} {
			if got := n.Normalize("", status, float64(i)/20); got != "playing" || !IsActiveGamePhase(got) {
				t.Fatalf("tick %d status %q: phase %q must stay the active fallback", i, status, got)
			}
		}
	}
}

// An unnamed status during live play stays live play (unchanged behaviour),
// and an unnamed status that outlives any real gap falls back to active.
func TestPhaseNormalizerUnnamedDuringPlayAndOverlongGap(t *testing.T) {
	runPhaseSteps(t, []phaseStep{
		{"playing", 0, "playing", true},
		{"", 1, "playing", true},
		{"score", 2, "round_over", false},
		{"", 3, PhasePostScoreGap, false},
		{"", 3 + MaxUnnamedGapSeconds, PhasePostScoreGap, false},
		{"", 3.1 + MaxUnnamedGapSeconds, "playing", true},
		{"", 500, "playing", true},
		{"score", 501, "round_over", false},
		{"", 502, PhasePostScoreGap, false},
	})
}

func TestPhaseNormalizerRestartsPerRecording(t *testing.T) {
	var n PhaseNormalizer
	n.Normalize("a", "score", 10)
	if got := n.Normalize("", "", 11); got != PhasePostScoreGap {
		t.Fatalf("a sample without a session id belongs to the current recording, got %q", got)
	}
	if got := n.Normalize("b", "", 12); got != "playing" {
		t.Fatalf("a new session id must not inherit the previous recording's status, got %q", got)
	}
	n.Normalize("b", "score", 13)
	if got := n.Normalize("b", "", 1); got != "playing" {
		t.Fatalf("a time-base restart must not inherit earlier statuses, got %q", got)
	}
}

func TestNamedStatusesAndNativeUnspecified(t *testing.T) {
	for status, want := range map[string]string{
		"playing": "playing", "PLAYING": "playing", " Round_Start ": "round_start", "score": "round_over",
		"round_over": "round_over", "pre_match": "pre_match", "post_match": "post_match",
		"pre_sudden_death": "pre_sudden_death", "some_future_state": "some_future_state",
	} {
		var n PhaseNormalizer
		if got := n.Normalize("s", status, 0); got != want {
			t.Errorf("%q: got %q want %q", status, got, want)
		}
	}
	for _, status := range []string{"", " ", "unknown", "UNSPECIFIED", "game_status_unspecified"} {
		if !IsUnnamedGameStatus(status) {
			t.Errorf("%q must read as unnamed", status)
		}
	}
	if IsUnnamedGameStatus("playing") || IsUnnamedGameStatus("score") {
		t.Error("named statuses must not read as unnamed")
	}
	mc := &MatchContext{}
	for _, phase := range []string{PhasePostScoreGap, PhasePreRoundGap, "round_over", "unknown"} {
		if mc.IsActivePhase(phase) {
			t.Errorf("%q must be inactive", phase)
		}
	}
	for _, phase := range []string{"playing", "sudden_death", "overtime", "round", ""} {
		if !mc.IsActivePhase(phase) {
			t.Errorf("%q must be active", phase)
		}
	}
}
