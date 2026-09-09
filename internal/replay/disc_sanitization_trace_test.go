package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestAnalyzeReplayKeepsDiscSanitizationTraceAfterHealthRecovers(t *testing.T) {
	// Synthetic malformed held-disc speed must remain unavailable, not be
	// trusted to recover a catch or silently disappear when live health recovers.
	var lines strings.Builder
	start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for frame := 0; frame < 20; frame++ {
		var session adapter.EchoVRSessionResponse
		if err := json.Unmarshal([]byte(remapSession(t, frame, "disc")), &session); err != nil {
			t.Fatal(err)
		}
		session.GameClock = 100 - float64(frame)*.067
		if frame == 2 || frame == 3 {
			session.Disc.Velocity = [3]float64{1000, 0, 0}
		}
		encoded, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&lines, "%s\t%s\n", start.Add(time.Duration(frame)*67*time.Millisecond).Format("2006/01/02 15:04:05.000"), encoded)
	}
	path := filepath.Join(t.TempDir(), "synthetic-sanitized-disc.echoreplay")
	if err := os.WriteFile(path, []byte(lines.String()), 0600); err != nil {
		t.Fatal(err)
	}
	engine := newTestEngine(t)
	result, err := engine.AnalyzeFile(context.Background(), path, false)
	if err != nil || result.PersistError() != nil {
		t.Fatalf("analysis failed: %v", err)
	}
	if result.Result.SanitizedFrames["disc_out_of_range"] != 2 {
		t.Fatalf("sanitizer behavior changed: %+v", result.Result.SanitizedFrames)
	}
	coverage, err := engine.Store().GetMatchAnalysisCoverage(context.Background(), result.MatchCtx.MatchID)
	if err != nil || coverage["echovr:101"] == nil {
		t.Fatalf("read persisted coverage: %v", err)
	}
	player := coverage["echovr:101"]
	if player.DataHealth == nil || player.DataHealth.State != model.HealthHealthy || player.DataHealth.DegradedSamples == 0 {
		t.Fatalf("expected recovered operational health: %+v", player.DataHealth)
	}
	found := false
	for _, detector := range player.Detectors {
		if detector.DetectorID == "STATE_001" && (detector.CandidateFrames != 20 || detector.InputFrames != 18) {
			t.Fatalf("recovered valid disc samples were not analyzed: %+v", detector)
		}
		for _, reason := range detector.DecisionTrace.Reasons {
			if reason.Code != "disc_out_of_range" {
				continue
			}
			if !strings.HasPrefix(detector.DetectorID, "THROW_") && detector.DetectorID != "STATE_001" && detector.DetectorID != "STATE_008" {
				t.Fatalf("disc-only trace incorrectly attached to %s", detector.DetectorID)
			}
			if reason.Count != 2 || reason.FirstFrame != 2 || reason.LastFrame != 3 || !strings.Contains(reason.Description, "not a cheating verdict") {
				t.Fatalf("sanitization history was erased or misrepresented: %+v", reason)
			}
			if detector.DetectorID == "STATE_001" {
				found = true
			}
		}
		if detector.DetectorID == "STATE_001" && detector.MechanicsReview != nil && detector.MechanicsReview.Total != 0 {
			t.Fatal("inferred an acquisition across sanitized unknown disc samples")
		}
	}
	if !found {
		t.Fatal("no persisted disc rejection reason for grab review")
	}
}
