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

func TestAnalyzeReplayPersistsUnscoredDirectDiscTransfer(t *testing.T) {
	for _, team := range []string{"BLUE TEAM", "ORANGE TEAM"} {
		t.Run(team, func(t *testing.T) {
			// Synthetic nearby same-team handoff / opponent ownership change.
			// These are regression scenarios, not independently labelled recordings.
			var lines strings.Builder
			start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
			for frame := 0; frame < 12; frame++ {
				var session adapter.EchoVRSessionResponse
				if err := json.Unmarshal([]byte(remapSession(t, frame, "disc")), &session); err != nil {
					t.Fatal(err)
				}
				session.GameClock = 100 - float64(frame)*.067
				alice := session.Teams[0].Players[0]
				bob := alice
				bob.Name, bob.UserID = "Synthetic recipient", 202
				bob.HoldingRight, bob.Possession = "none", false
				if frame >= 2 {
					alice.HoldingRight, alice.Possession = "none", false
					bob.HoldingRight, bob.Possession = "disc", true
				}
				session.Teams[0].Players[0] = alice
				if team == "BLUE TEAM" {
					session.Teams[0].Players = append(session.Teams[0].Players, bob)
				} else {
					session.Teams = append(session.Teams, adapter.EchoVRTeam{TeamName: team, Players: []adapter.EchoVRPlayer{bob}})
				}
				encoded, err := json.Marshal(session)
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&lines, "%s\t%s\n", start.Add(time.Duration(frame)*67*time.Millisecond).Format("2006/01/02 15:04:05.000"), encoded)
			}
			path := filepath.Join(t.TempDir(), "synthetic-transfer.echoreplay")
			if err := os.WriteFile(path, []byte(lines.String()), 0600); err != nil {
				t.Fatal(err)
			}
			engine := newTestEngine(t)
			for _, force := range []bool{false, true} {
				result, err := engine.AnalyzeFile(context.Background(), path, force)
				if err != nil || result.PersistError() != nil {
					t.Fatalf("analyze force=%v: result=%+v err=%v", force, result, err)
				}
				for _, event := range result.Result.DetectionEvents {
					if event.DetectorID == "STATE_001" {
						t.Fatal("legal/ambiguous ownership change became a scored event")
					}
				}
				coverage, err := engine.Store().GetMatchAnalysisCoverage(context.Background(), result.MatchCtx.MatchID)
				if err != nil || coverage["echovr:202"] == nil {
					t.Fatalf("missing persisted coverage: %v", err)
				}
				found := false
				for _, detector := range coverage["echovr:202"].Detectors {
					if detector.DetectorID != "STATE_001" {
						continue
					}
					log := detector.MechanicsReview
					if log == nil || log.Total != 1 || log.Inconclusive != 1 || log.Anomaly != 0 || log.ValidatedViolation != 0 || len(log.Records) != 1 {
						t.Fatalf("missing/duplicate/misleading stored transfer: %+v", log)
					}
					record := log.Records[0]
					if record.Result != model.MechanicsInconclusive || record.Reason != "grab_transfer_without_free_sample" || record.FrameIndex != 2 || !strings.Contains(record.ReasonDescription, "legal handoff or steal") || record.RawSamples[0].Attachment != "held" {
						t.Fatalf("stored evidence misrepresented: %+v", record)
					}
					found = true
				}
				if !found {
					t.Fatal("STATE_001 not enabled in actual replay engine")
				}
			}
		})
	}
}
