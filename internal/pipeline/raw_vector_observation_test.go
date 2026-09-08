package pipeline

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestRawVelocityPresencePersistsIntoReleaseEvidence(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "explicit_zero"}[present], func(t *testing.T) {
			mapper, extractor := adapter.NewMapper(), NewFeatureExtractor(30)
			ps := &model.PlayerState{PlayerID: "echovr:1"}
			validator := NewFrameValidator(config.DefaultConfig())
			for i := 0; i < 5; i++ {
				item := "disc"
				if i >= 3 {
					item = "none"
				}
				p := map[string]any{"userid": 1, "name": "P", "body": map[string]any{"position": []int{1, 2, 3}}, "lhand": map[string]any{"pos": []float64{.7, 2.3, 3}}, "rhand": map[string]any{"pos": []float64{1.3, 2.3, 3}}, "holding_left": "none", "holding_right": item}
				if present {
					p["velocity"] = []int{0, 0, 0}
				}
				payload := map[string]any{"sessionid": "m", "game_status": "playing", "client_name": "P", "disc": map[string]any{"position": []float64{1.3, 2.3, 3}, "velocity": []int{19, 0, 0}}, "teams": []any{map[string]any{"team": "BLUE TEAM", "players": []any{p}}}}
				b, _ := json.Marshal(payload)
				var raw adapter.EchoVRSessionResponse
				if err := json.Unmarshal(b, &raw); err != nil {
					t.Fatal(err)
				}
				mapped := mapper.MapSessionAt(&raw, time.Unix(100, 0).Add(time.Duration(i)*50*time.Millisecond))
				if len(mapped.Frames) != 1 {
					t.Fatal("fixture did not map")
				}
				f := mapped.Frames[0]
				if _, err := validator.Validate(&f, mapped.MatchCtx); err != nil {
					t.Fatal(err)
				}
				extractor.UpdatePlayerState(ps, &f, mapped.MatchCtx)
			}
			if ps.LastThrow == nil || ps.LastThrow.ReleaseWindow == nil {
				t.Fatal("fixture did not confirm sampled release")
			}
			if ps.HasReportedVelocity != present {
				t.Fatal("extractor invented reported velocity presence")
			}
			for _, sample := range ps.LastThrow.ReleaseWindow.PlayerMovement {
				if (sample.ReportedVelocity != nil) != present {
					t.Fatalf("raw absence was replaced by stationary evidence: %+v", sample)
				}
			}
		})
	}
}

func TestRawMissingDiscMotionCannotInventRelease(t *testing.T) {
	for _, missing := range []string{"position", "velocity"} {
		t.Run(missing, func(t *testing.T) {
			fe, ps := releaseStart(t)
			// The mapper cannot represent an incomplete disc as a complete DiscState.
			var raw adapter.EchoVRSessionResponse
			payload := `{"sessionid":"m","game_status":"playing","disc":{"position":[1,2,3],"velocity":[19,0,0]},"teams":[{"team":"BLUE TEAM","players":[{"userid":1,"body":{"position":[1,2,3]},"holding_left":"none","holding_right":"none"}]}]}`
			var object map[string]any
			if err := json.Unmarshal([]byte(payload), &object); err != nil {
				t.Fatal(err)
			}
			delete(object["disc"].(map[string]any), missing)
			b, _ := json.Marshal(object)
			if err := json.Unmarshal(b, &raw); err != nil {
				t.Fatal(err)
			}
			mapped := adapter.NewMapper().MapSessionAt(&raw, time.Unix(100, 0))
			if len(mapped.Frames) != 1 || mapped.Frames[0].Disc != nil {
				t.Fatal("incomplete disc mapped")
			}
			f := releaseFrame(3, "free")
			f.Disc = mapped.Frames[0].Disc
			fe.UpdatePlayerState(ps, &f, feTestCtx())
			f = releaseFrame(4, "free")
			fe.UpdatePlayerState(ps, &f, feTestCtx())
			if ps.LastThrow != nil || len(fe.pendingReleases) != 0 {
				t.Fatal("missing disc motion manufactured release")
			}
		})
	}
}
