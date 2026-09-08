package pattern

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestPat003CannotRecyclePausedPlayspaceHistory(t *testing.T) {
	for _, id := range []string{"MOV_006", "PAT_005"} {
		t.Run(id, func(t *testing.T) {
			for _, trusted := range [][]string{nil, {id}} {
				params := map[string]any{"trusted_detectors": trusted}
				if events := runPat003(params, history(id, false, .99, 6)); len(events) != 0 {
					t.Fatalf("paused history generated aggregate findings with trusted=%v: %+v", trusted, events)
				}
			}
		})
	}
}

func TestPat003PausePreservesUnrelatedHistory(t *testing.T) {
	var recorded []model.DetectionEvent
	for _, id := range []string{"MOV_006", "PAT_005", "THROW_003"} {
		recorded = append(recorded, history(id, false, .9, 3)...)
	}
	for _, trusted := range [][]string{nil, {"MOV_006", "PAT_005", "THROW_003"}} {
		events := runPat003(map[string]any{"trusted_detectors": trusted}, recorded)
		if len(events) != 1 || events[0].CausalKey.AnomalyType != "cross_match_THROW_003" {
			t.Fatalf("pause changed unrelated wrist history or retained playspacing: %+v", events)
		}
	}
}
