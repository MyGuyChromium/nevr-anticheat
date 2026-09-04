package testutil

import (
	"fmt"
	"sort"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

// ===================================================================
// 5. Composition helpers and multi-player matches
// ===================================================================

// Concat joins frame segments of ONE player into a continuous stream:
// frame indices and timestamps continue from the previous segment (at the
// segment's own sample spacing) and every later segment is translated so
// its first body position is where the previous segment ended, so the
// join itself is never a teleport. Hands and the disc move with the body.
func Concat(segments ...[]model.PlayerTelemetryFrame) []model.PlayerTelemetryFrame {
	var out []model.PlayerTelemetryFrame
	for _, seg := range segments {
		if len(seg) == 0 {
			continue
		}
		if len(out) == 0 {
			out = append(out, seg...)
			continue
		}
		last := out[len(out)-1]
		dt := 1.0 / DefaultTickRate
		if len(seg) > 1 && seg[1].Timestamp > seg[0].Timestamp {
			dt = seg[1].Timestamp - seg[0].Timestamp
		}
		shift := last.Position.Sub(seg[0].Position)
		baseIdx := last.FrameIndex + 1
		baseTS := last.Timestamp + dt
		for i, f := range seg {
			f.FrameIndex = baseIdx + i
			f.Timestamp = baseTS + (seg[i].Timestamp - seg[0].Timestamp)
			if i == 0 {
				f.DeltaTime = dt
			}
			f.Position = f.Position.Add(shift)
			if !f.LeftHandPosition.IsZero() {
				f.LeftHandPosition = f.LeftHandPosition.Add(shift)
			}
			if !f.RightHandPosition.IsZero() {
				f.RightHandPosition = f.RightHandPosition.Add(shift)
			}
			if f.Disc != nil {
				d := *f.Disc
				d.Position = d.Position.Add(shift)
				if d.ReleasePosition != nil {
					rp := d.ReleasePosition.Add(shift)
					d.ReleasePosition = &rp
				}
				f.Disc = &d
			}
			out = append(out, f)
		}
	}
	return out
}

// ShiftFrom displaces the body and hands of every frame from atFrame on by
// delta (a round-reset style jump that persists), keeping the result inside
// the arena.
func ShiftFrom(frames []model.PlayerTelemetryFrame, atFrame int, delta model.Vec3) {
	for i := atFrame; i < len(frames); i++ {
		p := ClampToArena(frames[i].Position.Add(delta))
		d := p.Sub(frames[i].Position)
		frames[i].Position = p
		frames[i].LeftHandPosition = frames[i].LeftHandPosition.Add(d)
		frames[i].RightHandPosition = frames[i].RightHandPosition.Add(d)
	}
}

// MatchBuilder assembles a multi-player match from per-player frame
// streams: a MatchContext with roster and team assignments, the merged
// frame list in (frame index, player id) order, and optionally one shared
// disc state per tick (as the adapter emits: every player's frame carries
// the same DiscState).
type MatchBuilder struct {
	matchID   string
	players   map[string][]model.PlayerTelemetryFrame
	teams     map[string]string
	discOwner string
}

// NewMatchBuilder creates an empty match.
func NewMatchBuilder(matchID string) *MatchBuilder {
	return &MatchBuilder{
		matchID: matchID,
		players: make(map[string][]model.PlayerTelemetryFrame),
		teams:   make(map[string]string),
	}
}

// AddPlayer adds one player's frames (PlayerID taken from the frames) on
// the given team ("blue"/"orange"). Team is stamped on every frame as the
// producers do.
func (mb *MatchBuilder) AddPlayer(team string, frames []model.PlayerTelemetryFrame) *MatchBuilder {
	if len(frames) == 0 {
		return mb
	}
	pid := frames[0].PlayerID
	for i := range frames {
		frames[i].Team = team
	}
	mb.players[pid] = frames
	mb.teams[pid] = team
	return mb
}

// WithSharedDisc makes every frame of a tick carry the disc state from
// playerID's frame at that index (the thrower), as a real session does.
func (mb *MatchBuilder) WithSharedDisc(playerID string) *MatchBuilder {
	mb.discOwner = playerID
	return mb
}

// PlayerIDs returns the roster in sorted order.
func (mb *MatchBuilder) PlayerIDs() []string {
	ids := make([]string, 0, len(mb.players))
	for pid := range mb.players {
		ids = append(ids, pid)
	}
	sort.Strings(ids)
	return ids
}

// Build returns the match context and the merged, ordered frames.
func (mb *MatchBuilder) Build() (*model.MatchContext, []model.PlayerTelemetryFrame) {
	mc := NewMatchContext()
	mc.MatchID = mb.matchID
	mc.PlayerIDs = mb.PlayerIDs()
	mc.TeamAssignments = make(map[string]string, len(mb.teams))
	for pid, team := range mb.teams {
		mc.TeamAssignments[pid] = team
	}

	var discByIdx map[int]*model.DiscState
	if owner, ok := mb.players[mb.discOwner]; ok {
		discByIdx = make(map[int]*model.DiscState, len(owner))
		for i := range owner {
			discByIdx[owner[i].FrameIndex] = owner[i].Disc
		}
	}

	var all []model.PlayerTelemetryFrame
	maxIdx := 0
	for _, pid := range mc.PlayerIDs {
		for _, f := range mb.players[pid] {
			if discByIdx != nil {
				if d, ok := discByIdx[f.FrameIndex]; ok && d != nil {
					dc := *d
					f.Disc = &dc
				}
			}
			if f.FrameIndex > maxIdx {
				maxIdx = f.FrameIndex
			}
			all = append(all, f)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].FrameIndex != all[j].FrameIndex {
			return all[i].FrameIndex < all[j].FrameIndex
		}
		return all[i].PlayerID < all[j].PlayerID
	})
	mc.Duration = time.Duration(float64(maxIdx+1) / mc.TickRate * float64(time.Second))
	return mc, all
}

// TwoByFourStart returns the spawn position of roster slot i (0-7): blue
// slots 0-3 on the -Z half, orange slots 4-7 on the +Z half, spread on X.
func TwoByFourStart(i int) model.Vec3 {
	x := []float64{-4, -1.5, 1.5, 4}[i%4]
	z := -25.0 - 8.0*float64(i%4)
	if i >= 4 {
		z = 25.0 + 8.0*float64(i%4)
	}
	return model.Vec3{x, headHeight, z}
}

// TwoByFourPlayerID returns the player id of roster slot i (0-7).
func TwoByFourPlayerID(i int) string {
	if i < 4 {
		return fmt.Sprintf("blue%d", i+1)
	}
	return fmt.Sprintf("orange%d", i-3)
}

// TwoByFourTeam returns the team of roster slot i (0-7).
func TwoByFourTeam(i int) string {
	if i < 4 {
		return "blue"
	}
	return "orange"
}

// TwoByFour builds a 4v4 match of nFrames in which every player flies
// legitimately at 3-6 m/s from their spawn, except roster slot cheaterSlot
// whose frames come from cheater(fb) (nil cheater: everyone is legit). The
// cheater's disc is shared across all frames of a tick.
func TwoByFour(matchID string, nFrames int, cheaterSlot int, cheater func(fb *FrameBuilder) []model.PlayerTelemetryFrame) (*model.MatchContext, []model.PlayerTelemetryFrame) {
	mb := NewMatchBuilder(matchID)
	for i := 0; i < 8; i++ {
		fb := NewFrameBuilder(TwoByFourPlayerID(i)).WithStartPos(TwoByFourStart(i)).WithTeam(TwoByFourTeam(i))
		var frames []model.PlayerTelemetryFrame
		if cheater != nil && i == cheaterSlot {
			frames = cheater(fb)
			mb.WithSharedDisc(fb.PlayerID())
		} else {
			frames = fb.NormalMovingPlayer(nFrames, 3.0+float64(i%4))
		}
		mb.AddPlayer(TwoByFourTeam(i), frames)
	}
	return mb.Build()
}

// PunchScenario generates a blue puncher and an orange victim standing
// `distance` metres apart on Z. Every 100 frames (from frame 60) the
// puncher's stun stat increments and, on the same frame, the victim is
// stunned for 45 frames. STATE_007 (when enabled) attributes each punch to
// the victim and, with distance > 10 m (+ closing credit), fires from the
// min_incidents-th (3rd) punch on. The returned context carries both teams.
func PunchScenario(nFrames int, distance float64) (*model.MatchContext, []model.PlayerTelemetryFrame) {
	puncher := NewFrameBuilder("puncher").WithStartPos(model.Vec3{1, headHeight, -5}).WithTeam("blue").NormalIdlePlayer(nFrames)
	victim := NewFrameBuilder("victim").WithStartPos(model.Vec3{1, headHeight, -5 + distance}).WithTeam("orange").NormalIdlePlayer(nFrames)
	stuns := 0
	for i := range puncher {
		if i >= 60 && (i-60)%100 == 0 {
			stuns++
		}
		puncher[i].Stuns = stuns
		c := (i - 60) % 100
		victim[i].IsStunned = i >= 60 && c < 45
	}
	return NewMatchBuilder("punch-match").AddPlayer("blue", puncher).AddPlayer("orange", victim).Build()
}

// ===================================================================
// 6. Scenario registry and validation
// ===================================================================

// Scenario is one named generator output with the number of frames the
// validator is expected to reject (0 for every scenario that models a
// producer honouring the telemetry contract).
type Scenario struct {
	Name          string
	Frames        []model.PlayerTelemetryFrame
	ExpectInvalid int
}

// AllScenarios returns every FrameBuilder generator at a representative
// length, for validity sweeps.
func AllScenarios() []Scenario {
	fb := func() *FrameBuilder { return NewFrameBuilder("player1").WithStartPos(model.Vec3{2, headHeight, 0}) }
	return []Scenario{
		{Name: "NormalIdlePlayer", Frames: fb().NormalIdlePlayer(300)},
		{Name: "NormalMovingPlayer", Frames: fb().NormalMovingPlayer(600, 5.0)},
		{Name: "NormalMovingPlayer/fast", Frames: fb().NormalMovingPlayer(600, 30.0)},
		{Name: "NormalThrowSequence", Frames: fb().NormalThrowSequence(6)},
		{Name: "EliteThrowSequence", Frames: fb().EliteThrowSequence(10)},
		{Name: "NormalBoostSequence", Frames: fb().NormalBoostSequence(4)},
		{Name: "NormalStunCycle", Frames: fb().NormalStunCycle(450, 45)},
		{Name: "NormalShieldCycle", Frames: fb().NormalShieldCycle(4)},
		{Name: "RespawnImmunity", Frames: fb().RespawnImmunity(200, 100)},
		{Name: "JitteryTelemetry", Frames: fb().JitteryTelemetry(300, 0.05)},
		{Name: "PacketLossFrames", Frames: fb().PacketLossFrames(300, 0.3)},
		{Name: "LargeFrameGap", Frames: fb().LargeFrameGap(200, 100, 2.0)},
		{Name: "InterpolationArtifact", Frames: fb().InterpolationArtifact(300)},
		{Name: "HighPingPlayer", Frames: fb().HighPingPlayer(300, 200)},
		{Name: "PingSpikeSequence", Frames: fb().PingSpikeSequence(300, 100, 400)},
		{Name: "RegrabStackingBurst", Frames: fb().RegrabStackingBurst(150)},
		{Name: "FastWristFlick", Frames: fb().FastWristFlick(150)},
		{Name: "SteadyHandPlayer", Frames: fb().SteadyHandPlayer(300)},
		{Name: "SpeedHackFrames", Frames: fb().SpeedHackFrames(150, 80)},
		{Name: "OscillatingSpeedHack", Frames: fb().OscillatingSpeedHack(300)},
		{Name: "TeleportCheat/30", Frames: fb().TeleportCheat(300, 30, 10)},
		{Name: "TeleportCheat/70", Frames: fb().TeleportCheat(600, 70, 11)},
		{Name: "ZeroInertiaReversals", Frames: fb().ZeroInertiaReversals(300)},
		{Name: "BoostCapViolation", Frames: fb().BoostCapViolation(300)},
		{Name: "InfiniteBoost", Frames: fb().InfiniteBoost(300)},
		{Name: "StunBypass", Frames: fb().StunBypass(200)},
		{Name: "GodMode", Frames: fb().GodMode(600)},
		{Name: "ShieldAbuse", Frames: fb().ShieldAbuse(700)},
		{Name: "CooldownBypass", Frames: fb().CooldownBypass(20)},
		{Name: "ScoreJump", Frames: fb().ScoreJump(50, 7)},
		{Name: "BotBehavior", Frames: fb().BotBehavior(600)},
		{Name: "FrozenAim", Frames: fb().FrozenAim(600)},
		{Name: "HandSpeedHack", Frames: fb().HandSpeedHack(100)},
		{Name: "WristSpin", Frames: fb().WristSpin(100, 3.0)},
		{Name: "ExtendedReach", Frames: fb().ExtendedReach(100)},
		{Name: "AimbotThrows", Frames: fb().AimbotThrows(8)},
		{Name: "ArtifactThrows", Frames: fb().ArtifactThrows(3)},
		{Name: "NearCapThrows", Frames: fb().NearCapThrows(10)},
		{Name: "PrecisionAimbot", Frames: fb().PrecisionAimbot(10)},
		{Name: "MagnetismCheat", Frames: fb().MagnetismCheat(4)},
		{Name: "AcceleratingDisc", Frames: fb().AcceleratingDisc(3)},
		{Name: "ReverseHandThrows", Frames: fb().ReverseHandThrows(4)},
		{Name: "MacroThrows", Frames: fb().MacroThrows(14)},
		{Name: "ImpossibleGrabs", Frames: fb().ImpossibleGrabs(4)},
		{Name: "CompositeCheater", Frames: fb().CompositeCheater()},
	}
}

// ValidateFrames runs the production FrameValidator (DefaultConfig,
// DefaultPhysics) over copies of the frames and returns the rejected count
// and the rejection reasons.
func ValidateFrames(frames []model.PlayerTelemetryFrame) (invalid int, reasons map[string]int) {
	v := pipeline.NewFrameValidator(config.DefaultConfig())
	mc := NewMatchContext()
	reasons = make(map[string]int)
	for i := range frames {
		f := frames[i]
		if _, err := v.Validate(&f, mc); err != nil {
			invalid++
			reasons[err.Error()]++
		}
	}
	return invalid, reasons
}
