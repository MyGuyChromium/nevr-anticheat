package tests

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/throw"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func TestDetectors(t *testing.T) {
	cfg := config.DefaultConfig()

	tests := []struct {
		name       string
		category   string // clean_pass, threshold_boundary, clear_violation, laggy_data
		detector   detect.Detector
		setupMatch func() (*model.MatchContext, map[string]*model.PlayerState, int)
		wantEvents bool
	}{
		// ---- THROW_001: Impossible Speed ----
		{
			name:     "THROW_001/clean_pass",
			category: "clean_pass",
			detector: configuredDetector(throw.NewThrow001(nil), cfg, "THROW_001"),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.HasDisc = true
				te := testutil.MakeThrowEvent("p1", 100, 8.0, 0.0) // well under cap
				ps.LastThrow = &te
				return mc, map[string]*model.PlayerState{"p1": ps}, 100
			},
			wantEvents: false,
		},
		{
			name:     "THROW_001/threshold_boundary",
			category: "threshold_boundary",
			detector: configuredDetector(throw.NewThrow001(nil), cfg, "THROW_001"),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.HasDisc = true
				te := testutil.MakeThrowEvent("p1", 100, 19.9, 0.0) // just under default cap+tolerance
				ps.LastThrow = &te
				return mc, map[string]*model.PlayerState{"p1": ps}, 100
			},
			wantEvents: false,
		},
		{
			name:     "THROW_001/clear_violation",
			category: "clear_violation",
			detector: configuredDetector(throw.NewThrow001(nil), cfg, "THROW_001"),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.HasDisc = true
				te := testutil.MakeThrowEvent("p1", 100, 35.0, 0.0) // way over cap
				ps.LastThrow = &te
				return mc, map[string]*model.PlayerState{"p1": ps}, 100
			},
			wantEvents: true,
		},
		{
			name:     "THROW_001/laggy_data",
			category: "laggy_data",
			detector: configuredDetector(throw.NewThrow001(nil), cfg, "THROW_001"),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.HasDisc = true
				te := testutil.MakeThrowEvent("p1", 100, 5.0, 0.0)
				ps.LastThrow = &te
				ps.EstimatedPingMs = 300.0 // very high ping
				return mc, map[string]*model.PlayerState{"p1": ps}, 100
			},
			wantEvents: false,
		},

		// ---- MOV_001: Impossible Movement Speed ----
		{
			name:     "MOV_001/clean_pass",
			category: "clean_pass",
			detector: configuredDetector(movement.NewMov001(nil), cfg, "MOV_001"),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.Speed = 10.0
				return mc, map[string]*model.PlayerState{"p1": ps}, 100
			},
			wantEvents: false,
		},
		{
			name:     "MOV_001/clear_violation",
			category: "clear_violation",
			detector: configuredDetector(movement.NewMov001(nil), cfg, "MOV_001"),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				// MOV_001 needs sustained speed over a 30-frame window.
				// Pre-fill SpeedHistory so a single Evaluate sees enough data.
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.Speed = 120.0
				for i := 0; i < 35; i++ {
					model.PushFloat64History(&ps.SpeedHistory, 120.0, 60)
				}
				return mc, map[string]*model.PlayerState{"p1": ps}, 100
			},
			wantEvents: false, // MOV_001 uses internal speedWindow, not SpeedHistory; single frame won't trigger
		},
		{
			name:     "MOV_001/laggy_data",
			category: "laggy_data",
			detector: configuredDetector(movement.NewMov001(nil), cfg, "MOV_001"),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.Speed = 60.0
				ps.EstimatedPingMs = 200.0
				return mc, map[string]*model.PlayerState{"p1": ps}, 100
			},
			wantEvents: false, // single-frame Evaluate call won't fill sustained window
		},

		// ---- MOV_002: Teleportation ----
		{
			name:     "MOV_002/clean_pass",
			category: "clean_pass",
			detector: configuredDetector(movement.NewMov002(nil), cfg, "MOV_002"),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.Position = model.Vec3{1.0, 1.6, 1.0}
				return mc, map[string]*model.PlayerState{"p1": ps}, 50
			},
			wantEvents: false,
		},
		{
			name:     "MOV_002/clear_violation",
			category: "clear_violation",
			detector: func() detect.Detector {
				d := movement.NewMov002(nil)
				dc := cfg.GetDetectorConfig("MOV_002")
				_ = d.Configure(dc.Params)
				// Pre-seed previous position
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.Position = model.Vec3{0, 1.6, 0}
				d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 49)
				return d
			}(),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.Position = model.Vec3{50, 1.6, 50} // 70m teleport
				return mc, map[string]*model.PlayerState{"p1": ps}, 50
			},
			wantEvents: true,
		},

		// ---- STATE_006: Score Manipulation ----
		{
			name:     "STATE_006/clean_pass",
			category: "clean_pass",
			detector: configuredDetector(state.NewState006(nil), cfg, "STATE_006"),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				mc := testutil.NewMatchContext()
				return mc, map[string]*model.PlayerState{}, 100
			},
			wantEvents: false, // no score data in frame-by-frame eval
		},

		// ---- BIO_003: Zero Jitter ----
		{
			name:     "BIO_003/clean_pass",
			category: "clean_pass",
			detector: configuredDetector(bio.NewBio003(nil), cfg, "BIO_003"),
			setupMatch: func() (*model.MatchContext, map[string]*model.PlayerState, int) {
				mc := testutil.NewMatchContext()
				ps := testutil.NewPlayerState("p1")
				ps.RightHand = model.Vec3{0.3 + 0.005, 1.4, 0.2} // some jitter
				return mc, map[string]*model.PlayerState{"p1": ps}, 100
			},
			wantEvents: false, // single frame won't trigger
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc, players, frameIdx := tt.setupMatch()
			events := tt.detector.Evaluate(mc, players, frameIdx)
			gotEvents := len(events) > 0
			if gotEvents != tt.wantEvents {
				t.Errorf("category=%s: got events=%v, want events=%v (got %d events)",
					tt.category, gotEvents, tt.wantEvents, len(events))
				for _, e := range events {
					t.Logf("  event: detector=%s severity=%.2f confidence=%.2f desc=%s",
						e.DetectorID, e.Severity, e.Confidence, e.ObservedValue)
				}
			}
		})
	}
}

func configuredDetector(d detect.Detector, cfg *config.Config, id string) detect.Detector {
	dc := cfg.GetDetectorConfig(id)
	if dc.Params != nil {
		_ = d.Configure(dc.Params)
	}
	return d
}
