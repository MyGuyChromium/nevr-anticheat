package replay

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// SummaryVersion is stamped on every MatchSummary document so a reader can
// tell which builder produced a stored row.
const SummaryVersion = 1

// ThrowGoalWindow is how long after a release a goal is still credited to
// that throw (a long shot from the far end flies for about two seconds).
const ThrowGoalWindow = 6 * time.Second

// releaseSpeedGrace is how many ticks after a release the disc velocity may
// still read zero before the throw keeps a zero speed (the API updates the
// disc a tick or two after the hands let go).
const releaseSpeedGrace = 2

// MatchSummary is the one document that describes a match for people: the
// scoreboard, the goals, the throws and the anticheat verdicts, built once
// from the replay's session snapshots while they stream (see SummaryBuilder)
// and stored as JSON in match_summaries. It is what the desktop page shows
// and what its JSON / CSV downloads carry.
type MatchSummary struct {
	Version   int    `json:"version"`
	MatchID   string `json:"match_id"`
	GameMode  string `json:"game_mode"`
	Map       string `json:"map"`
	IsPrivate bool   `json:"is_private"`
	// StartTime is the recording's first sample (RFC 3339, recorder clock as UTC).
	StartTime       time.Time `json:"start_time"`
	DurationSeconds float64   `json:"duration_seconds"`
	// Ticks is the number of session snapshots summarized.
	Ticks int `json:"ticks"`
	// HasScore is false when no snapshot carried a team score.
	HasScore    bool `json:"has_score"`
	BlueScore   int  `json:"blue_score"`
	OrangeScore int  `json:"orange_score"`
	// Teams holds the per-team totals under "blue" and "orange".
	Teams map[string]*TeamSummary `json:"teams"`
	// Players is the roster, blue first then orange, by name: everyone who
	// appeared on a team in any snapshot, with the stats of their last one.
	Players []*PlayerSummary `json:"players"`
	// Goals is the scoring timeline in match order.
	Goals []GoalEvent `json:"goals"`
	// Throws is the throw log in match order (see ThrowSource).
	Throws []ThrowEvent `json:"throws"`
	// ThrowSource says how releases were detected: "holding" (the
	// holding_left / holding_right hand fields dropped the disc), "possession"
	// (the per-player possession boolean cleared with nobody else holding
	// it; used for sources without hand fields) or "" (no snapshot carried
	// either).
	ThrowSource            string  `json:"throw_source"`
	ThrowGoalWindowSeconds float64 `json:"throw_goal_window_seconds"`
}

// PlayerStats are the per-player counters the game reports (teams[].players[].stats).
type PlayerStats struct {
	Points         int     `json:"points"`
	Goals          int     `json:"goals"`
	Assists        int     `json:"assists"`
	Saves          int     `json:"saves"`
	Steals         int     `json:"steals"`
	Stuns          int     `json:"stuns"`
	Passes         int     `json:"passes"`
	Catches        int     `json:"catches"`
	Blocks         int     `json:"blocks"`
	Interceptions  int     `json:"interceptions"`
	PossessionTime float64 `json:"possession_time"`
	ShotsTaken     int     `json:"shots_taken"`
}

func statsOf(s adapter.EchoVRPlayerStats) PlayerStats {
	return PlayerStats{
		Points: s.Points, Goals: s.Goals, Assists: s.Assists, Saves: s.Saves, Steals: s.Steals, Stuns: s.Stuns,
		Passes: s.Passes, Catches: s.Catches, Blocks: s.Blocks, Interceptions: s.Interceptions,
		PossessionTime: s.Possession, ShotsTaken: s.ShotsOnGoal,
	}
}

func (a *PlayerStats) add(b PlayerStats) {
	a.Points += b.Points
	a.Goals += b.Goals
	a.Assists += b.Assists
	a.Saves += b.Saves
	a.Steals += b.Steals
	a.Stuns += b.Stuns
	a.Passes += b.Passes
	a.Catches += b.Catches
	a.Blocks += b.Blocks
	a.Interceptions += b.Interceptions
	a.PossessionTime += b.PossessionTime
	a.ShotsTaken += b.ShotsTaken
}

// TeamSummary is one team's final score and the sum of its players' stats.
type TeamSummary struct {
	Team    string      `json:"team"`
	Score   int         `json:"score"`
	Players int         `json:"players"`
	Stats   PlayerStats `json:"stats"`
	Throws  ThrowStats  `json:"throws"`
}

// ThrowStats aggregates a player's (or team's) throws.
type ThrowStats struct {
	Count     int     `json:"count"`
	MeanSpeed float64 `json:"mean_speed"`
	MaxSpeed  float64 `json:"max_speed"`
	Goals     int     `json:"goals"`

	sum float64
}

func (t *ThrowStats) add(speed float64, goal bool) {
	t.Count++
	t.sum += speed
	t.MeanSpeed = t.sum / float64(t.Count)
	if speed > t.MaxSpeed {
		t.MaxSpeed = speed
	}
	if goal {
		t.Goals++
	}
}

// PlayerSummary is one rostered player: identity, presence, ping, the
// final stats, the throw aggregates and the anticheat verdict.
type PlayerSummary struct {
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	Team     string `json:"team"`
	UserID   int64  `json:"user_id,omitempty"`
	Level    int    `json:"level"`
	// Frames is the number of snapshots the player appeared in; FirstSeen /
	// LastSeen are match-relative seconds.
	Frames    int     `json:"frames"`
	FirstSeen float64 `json:"first_seen"`
	LastSeen  float64 `json:"last_seen"`
	PingAvg   float64 `json:"ping_avg"`
	PingMax   int     `json:"ping_max"`
	// Stats are taken from the last snapshot the player appeared in.
	Stats     PlayerStats      `json:"stats"`
	Throws    ThrowStats       `json:"throws"`
	Suspicion *PlayerSuspicion `json:"suspicion,omitempty"`

	pingSum float64
	pingN   int
}

// PlayerSuspicion is the anticheat verdict of one player in the match.
type PlayerSuspicion struct {
	Score            float64  `json:"score"`
	Level            string   `json:"level"`
	Detections       int      `json:"detections"`
	ShadowDetections int      `json:"shadow_detections"`
	TopDetectors     []string `json:"top_detectors"`
}

// GoalEvent is one goal of the scoring timeline: when the team score
// increased, described by the last_score the API reported for it.
type GoalEvent struct {
	Time  float64 `json:"time"`  // match-relative seconds
	Clock string  `json:"clock"` // game_clock_display at the goal
	Team  string  `json:"team"`
	// Points is the score increment (what the goal was worth).
	Points int `json:"points"`
	// Scorer, Assist and the shot facts come from last_score; ScorerID and
	// AssistID resolve the names against the roster ("" when unknown).
	Scorer    string  `json:"scorer"`
	ScorerID  string  `json:"scorer_id"`
	Assist    string  `json:"assist"`
	AssistID  string  `json:"assist_id"`
	GoalType  string  `json:"goal_type"`
	Distance  float64 `json:"distance"`
	DiscSpeed float64 `json:"disc_speed"`
	// Described is false when no last_score could be matched to the goal
	// (the team and points are still known from the score).
	Described bool `json:"described"`
	// BlueScore / OrangeScore are the running score after the goal.
	BlueScore   int `json:"blue_score"`
	OrangeScore int `json:"orange_score"`
	FrameIndex  int `json:"frame_index"`
}

// ThrowEvent is one release of the disc.
type ThrowEvent struct {
	Time       float64 `json:"time"`
	Clock      string  `json:"clock"`
	PlayerID   string  `json:"player_id"`
	Player     string  `json:"player"`
	Team       string  `json:"team"`
	Speed      float64 `json:"speed"` // |disc.velocity| at the release, m/s
	Goal       bool    `json:"goal"`  // a goal by the thrower followed within ThrowGoalWindow
	FrameIndex int     `json:"frame_index"`
}

// SummaryBuilder accumulates a MatchSummary from session snapshots in match
// order; nothing is buffered, so it can run while the replay streams.
type SummaryBuilder struct {
	s       *MatchSummary
	mc      *model.MatchContext
	players map[string]*PlayerSummary
	names   map[string]string // display name -> player id, last seen

	havePrev             bool
	prevBlue, prevOrange int
	// lastScoreKey is the last_score already attached to a goal (or seen
	// before any goal), so a repeated payload is not attached twice.
	lastScoreKey string
	pendingGoal  int // index into s.Goals awaiting its last_score, -1 when none
	pendingUntil float64

	holding    map[string]bool // who held the disc in the previous tick (hand fields)
	possession map[string]bool // who reported possession in the previous tick
	// pendingSpeed is the throw awaiting a non-zero disc speed; graceLeft
	// counts the ticks it may still wait.
	pendingSpeed int
	graceLeft    int
	lastTime     float64
}

// NewSummaryBuilder starts a summary for the match mc describes.
func NewSummaryBuilder(mc *model.MatchContext) *SummaryBuilder {
	s := &MatchSummary{
		Version:                SummaryVersion,
		Teams:                  map[string]*TeamSummary{"blue": {Team: "blue"}, "orange": {Team: "orange"}},
		Players:                []*PlayerSummary{},
		Goals:                  []GoalEvent{},
		Throws:                 []ThrowEvent{},
		ThrowGoalWindowSeconds: ThrowGoalWindow.Seconds(),
	}
	if mc != nil {
		s.MatchID, s.GameMode, s.Map, s.IsPrivate, s.StartTime = mc.MatchID, mc.GameMode, mc.Map, mc.IsPrivate, mc.StartTime
	}
	return &SummaryBuilder{
		s: s, mc: mc,
		players: make(map[string]*PlayerSummary), names: make(map[string]string),
		pendingGoal: -1, pendingSpeed: -1,
		holding: make(map[string]bool), possession: make(map[string]bool),
	}
}

// activeStatus reports whether a game_status is live play, the only time a
// release is a throw (between rounds the disc is teleported).
func activeStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "playing", "round", "overtime", "sudden_death", "":
		return true
	}
	return false
}

// lastScoreKey identifies a last_score payload by the shot facts, not the
// names: the API blanks a name to "[INVALID]" when the player leaves, which
// must not read as a new goal.
func lastScoreKey(ls *adapter.EchoVRLastScore) string {
	if ls == nil {
		return ""
	}
	return fmt.Sprintf("%s|%d|%s|%.6f|%.6f", ls.Team, ls.PointAmount, ls.GoalType, ls.DiscSpeed, ls.DistanceThrown)
}

func (b *SummaryBuilder) player(pid string) *PlayerSummary {
	p := b.players[pid]
	if p == nil {
		p = &PlayerSummary{PlayerID: pid}
		if b.mc != nil {
			p.Team = b.mc.TeamAssignments[pid]
			p.Name = b.mc.PlayerNames[pid]
		}
		b.players[pid] = p
		b.s.Players = append(b.s.Players, p)
	}
	return p
}

// Add folds one snapshot in: frameIndex is the tick's frame index and t its
// match-relative time in seconds (the mapper's Timestamp).
func (b *SummaryBuilder) Add(session *adapter.EchoVRSessionResponse, frameIndex int, t float64) {
	if session == nil {
		return
	}
	s := b.s
	s.Ticks++
	b.lastTime = t
	if s.Ticks == 1 {
		b.lastScoreKey = lastScoreKey(session.LastScore)
	}

	// Roster, presence, ping, final stats; who holds the disc this tick.
	curHold := make(map[string]bool, len(b.holding))
	curPoss := make(map[string]bool, len(b.possession))
	haveHands, anyHolds, anyPoss := false, false, false
	for teamIdx, team := range session.Teams {
		teamName, ok := adapter.MappedTeamName(team.TeamName, teamIdx)
		if !ok {
			continue
		}
		for i := range team.Players {
			p := &team.Players[i]
			pid := adapter.PlayerIDOf(p)
			ps := b.player(pid)
			if ps.Frames == 0 {
				ps.FirstSeen = t
			}
			ps.Frames++
			ps.LastSeen = t
			ps.Team = teamName
			if p.Name != "" {
				ps.Name = p.Name
				b.names[p.Name] = pid
			}
			ps.UserID = p.UserID
			if p.Level > 0 {
				ps.Level = p.Level
			}
			if p.Ping > 0 {
				ps.pingSum += float64(p.Ping)
				ps.pingN++
				ps.PingAvg = ps.pingSum / float64(ps.pingN)
				if p.Ping > ps.PingMax {
					ps.PingMax = p.Ping
				}
			}
			ps.Stats = statsOf(p.Stats)
			if p.HasHoldingFields() {
				haveHands = true
			}
			if p.HoldsDisc() {
				curHold[pid], anyHolds = true, true
			}
			if p.Possession {
				curPoss[pid], anyPoss = true, true
			}
		}
	}

	// Disc speed this tick; a throw that read zero at its release takes the
	// first non-zero reading within the grace.
	speed := 0.0
	if session.Disc != nil {
		speed = model.Vec3(session.Disc.Velocity).Magnitude()
	}
	if b.pendingSpeed >= 0 {
		if speed > 0 {
			th := &s.Throws[b.pendingSpeed]
			th.Speed = speed
			b.pendingSpeed = -1
		} else if b.graceLeft--; b.graceLeft <= 0 {
			b.pendingSpeed = -1
		}
	}

	// Releases: the hand fields when the source has them (they drop to
	// "none" at the release), else the possession boolean (it stays with
	// the last carrier, so only a clear with nobody else holding counts).
	// A release while another player already holds the disc is a steal or
	// a hand-off, not a throw.
	if haveHands {
		s.ThrowSource = "holding"
		for pid, held := range b.holding {
			if held && !curHold[pid] && !anyHolds {
				b.release(pid, session, frameIndex, t, speed)
			}
		}
	} else if s.ThrowSource != "holding" {
		if len(curPoss) > 0 || len(b.possession) > 0 {
			s.ThrowSource = "possession"
		}
		for pid, had := range b.possession {
			if had && !curPoss[pid] && !anyPoss {
				b.release(pid, session, frameIndex, t, speed)
			}
		}
	}
	b.holding, b.possession = curHold, curPoss

	// Score and goals.
	blue, orange := session.BluePoints, session.OrangePoints
	if blue != 0 || orange != 0 {
		s.HasScore = true
	}
	s.BlueScore, s.OrangeScore = blue, orange
	if b.havePrev {
		if d := blue - b.prevBlue; d > 0 {
			b.goal("blue", d, session, frameIndex, t)
		}
		if d := orange - b.prevOrange; d > 0 {
			b.goal("orange", d, session, frameIndex, t)
		}
	}
	b.prevBlue, b.prevOrange, b.havePrev = blue, orange, true
	if b.pendingGoal >= 0 {
		if key := lastScoreKey(session.LastScore); key != b.lastScoreKey && session.LastScore != nil {
			b.describe(&s.Goals[b.pendingGoal], session.LastScore, t)
			b.lastScoreKey = key
			b.pendingGoal = -1
		} else if t > b.pendingUntil {
			b.linkThrow(&s.Goals[b.pendingGoal])
			b.pendingGoal = -1
		}
	}
}

func (b *SummaryBuilder) release(pid string, session *adapter.EchoVRSessionResponse, frameIndex int, t, speed float64) {
	if !activeStatus(session.GameStatus) {
		return
	}
	ps := b.player(pid)
	b.s.Throws = append(b.s.Throws, ThrowEvent{
		Time: t, Clock: session.GameClockDisplay, PlayerID: pid, Player: ps.Name, Team: ps.Team,
		Speed: speed, FrameIndex: frameIndex,
	})
	if speed == 0 {
		b.pendingSpeed, b.graceLeft = len(b.s.Throws)-1, releaseSpeedGrace
	} else {
		b.pendingSpeed = -1
	}
}

// goal records a score increment. The last_score that describes it usually
// changes on the same tick or a couple of ticks before; when it has not
// changed yet the goal waits for it (pendingGoal) up to three seconds.
func (b *SummaryBuilder) goal(team string, points int, session *adapter.EchoVRSessionResponse, frameIndex int, t float64) {
	s := b.s
	if b.pendingGoal >= 0 {
		b.linkThrow(&s.Goals[b.pendingGoal])
		b.pendingGoal = -1
	}
	s.Goals = append(s.Goals, GoalEvent{
		Time: t, Clock: session.GameClockDisplay, Team: team, Points: points,
		BlueScore: session.BluePoints, OrangeScore: session.OrangePoints, FrameIndex: frameIndex,
	})
	g := &s.Goals[len(s.Goals)-1]
	if key := lastScoreKey(session.LastScore); session.LastScore != nil && key != b.lastScoreKey {
		b.describe(g, session.LastScore, t)
		b.lastScoreKey = key
		return
	}
	b.pendingGoal, b.pendingUntil = len(s.Goals)-1, t+3
}

// describe fills a goal in from last_score and credits the throw.
func (b *SummaryBuilder) describe(g *GoalEvent, ls *adapter.EchoVRLastScore, t float64) {
	g.Scorer, g.Assist = ls.Scorer(), ls.Assist()
	g.ScorerID, g.AssistID = b.names[g.Scorer], b.names[g.Assist]
	g.GoalType, g.Distance, g.DiscSpeed, g.Described = ls.GoalType, ls.DistanceThrown, ls.DiscSpeed, true
	if ls.Team != "" && strings.EqualFold(ls.Team, g.Team) && ls.PointAmount > 0 {
		g.Points = ls.PointAmount
	}
	b.linkThrow(g)
}

// linkThrow marks the throw that scored: the scorer's latest release within
// ThrowGoalWindow before the goal, or, when the scorer is unknown, the
// latest release by the scoring team.
func (b *SummaryBuilder) linkThrow(g *GoalEvent) {
	window := ThrowGoalWindow.Seconds()
	for i := len(b.s.Throws) - 1; i >= 0; i-- {
		th := &b.s.Throws[i]
		if g.Time-th.Time > window {
			return
		}
		if th.Time > g.Time || th.Goal {
			continue
		}
		if g.ScorerID != "" && th.PlayerID != g.ScorerID {
			continue
		}
		if g.ScorerID == "" && th.Team != g.Team {
			continue
		}
		th.Goal = true
		return
	}
}

func teamRank(team string) int {
	switch team {
	case "blue":
		return 0
	case "orange":
		return 1
	}
	return 2
}

// Finish closes the summary: a goal still waiting for its description is
// linked as it is, players are ordered blue then orange by name, throw and
// team aggregates are computed. The builder must not be used afterwards.
func (b *SummaryBuilder) Finish() *MatchSummary {
	s := b.s
	if b.pendingGoal >= 0 {
		b.linkThrow(&s.Goals[b.pendingGoal])
		b.pendingGoal = -1
	}
	if b.mc != nil && b.mc.Duration > 0 {
		s.DurationSeconds = b.mc.Duration.Seconds()
	} else {
		s.DurationSeconds = b.lastTime
	}
	for i := range s.Throws {
		th := &s.Throws[i]
		if p := b.players[th.PlayerID]; p != nil {
			th.Player, th.Team = p.Name, p.Team
		}
	}
	for _, p := range s.Players {
		p.Throws = ThrowStats{}
	}
	for _, th := range s.Throws {
		if p := b.players[th.PlayerID]; p != nil {
			p.Throws.add(th.Speed, th.Goal)
		}
		if tm := s.Teams[th.Team]; tm != nil {
			tm.Throws.add(th.Speed, th.Goal)
		}
	}
	sort.SliceStable(s.Players, func(i, j int) bool {
		a, c := s.Players[i], s.Players[j]
		if ra, rc := teamRank(a.Team), teamRank(c.Team); ra != rc {
			return ra < rc
		}
		if a.Name != c.Name {
			return strings.ToLower(a.Name) < strings.ToLower(c.Name)
		}
		return a.PlayerID < c.PlayerID
	})
	for _, p := range s.Players {
		if tm := s.Teams[p.Team]; tm != nil {
			tm.Players++
			tm.Stats.add(p.Stats)
		}
	}
	s.Teams["blue"].Score, s.Teams["orange"].Score = s.BlueScore, s.OrangeScore
	return s
}

// ApplySuspicion attaches the anticheat verdicts: per player the match
// score and level, the counts of real and shadow detections and the three
// detectors that fired most. A player only known from the detections is
// added to the roster with the context's team.
func (s *MatchSummary) ApplySuspicion(mc *model.MatchContext, scores map[string]model.SuspicionScore, events []model.DetectionEvent, levels model.LevelTable) {
	byID := make(map[string]*PlayerSummary, len(s.Players))
	for _, p := range s.Players {
		byID[p.PlayerID] = p
	}
	get := func(pid string) *PlayerSummary {
		if p := byID[pid]; p != nil {
			return p
		}
		p := &PlayerSummary{PlayerID: pid}
		if mc != nil {
			p.Team, p.Name = mc.TeamAssignments[pid], mc.PlayerNames[pid]
		}
		byID[pid] = p
		s.Players = append(s.Players, p)
		return p
	}
	type detCount struct {
		id string
		n  int
	}
	perDetector := make(map[string]map[string]int)
	for _, p := range s.Players {
		p.Suspicion = &PlayerSuspicion{Level: string(levels.LevelFor(0)), TopDetectors: []string{}}
	}
	for _, ev := range events {
		if ev.PlayerID == "" {
			continue
		}
		p := get(ev.PlayerID)
		if p.Suspicion == nil {
			p.Suspicion = &PlayerSuspicion{Level: string(levels.LevelFor(0)), TopDetectors: []string{}}
		}
		if ev.IsShadow {
			p.Suspicion.ShadowDetections++
		} else {
			p.Suspicion.Detections++
		}
		if perDetector[ev.PlayerID] == nil {
			perDetector[ev.PlayerID] = make(map[string]int)
		}
		perDetector[ev.PlayerID][ev.DetectorID]++
	}
	for pid, sc := range scores {
		p := get(pid)
		if p.Suspicion == nil {
			p.Suspicion = &PlayerSuspicion{TopDetectors: []string{}}
		}
		p.Suspicion.Score = sc.TotalScore
	}
	for _, p := range s.Players {
		su := p.Suspicion
		su.Level = string(levels.LevelFor(su.Score))
		var counts []detCount
		for id, n := range perDetector[p.PlayerID] {
			counts = append(counts, detCount{id, n})
		}
		sort.Slice(counts, func(i, j int) bool {
			if counts[i].n != counts[j].n {
				return counts[i].n > counts[j].n
			}
			return counts[i].id < counts[j].id
		})
		su.TopDetectors = []string{}
		for i, c := range counts {
			if i == 3 {
				break
			}
			su.TopDetectors = append(su.TopDetectors, fmt.Sprintf("%s (%d)", c.id, c.n))
		}
	}
}

// FlaggedPlayers lists the players whose level is above informational, sorted.
func (s *MatchSummary) FlaggedPlayers() []string {
	var out []string
	for _, p := range s.Players {
		if p.Suspicion != nil && p.Suspicion.Level != string(model.LevelClean) && p.Suspicion.Level != string(model.LevelInformational) {
			out = append(out, p.PlayerID)
		}
	}
	sort.Strings(out)
	return out
}

// TotalDetections counts the real (non-shadow) detections of every player.
func (s *MatchSummary) TotalDetections() int {
	n := 0
	for _, p := range s.Players {
		if p.Suspicion != nil {
			n += p.Suspicion.Detections
		}
	}
	return n
}

// Meta is the legacy match_summaries row the document is stored with.
func (s *MatchSummary) Meta() sqlite.MatchSummaryMeta {
	return sqlite.MatchSummaryMeta{
		MatchID: s.MatchID, Map: s.Map, GameMode: s.GameMode, StartTime: s.StartTime,
		Duration: time.Duration(s.DurationSeconds * float64(time.Second)), FrameCount: s.Ticks,
		TotalDetections: s.TotalDetections(), FlaggedPlayers: s.FlaggedPlayers(),
	}
}

// csvColumns is the players export, one row per rostered player.
var csvColumns = []string{
	"player_id", "name", "team", "level", "frames", "first_seen_s", "last_seen_s", "ping_avg_ms", "ping_max_ms",
	"points", "goals", "assists", "saves", "steals", "stuns", "passes", "catches", "blocks", "interceptions",
	"possession_time_s", "shots_taken", "throws", "throw_mean_speed_mps", "throw_max_speed_mps", "throw_goals",
	"suspicion_score", "suspicion_level", "detections", "shadow_detections", "top_detectors",
}

// PlayersCSV renders the roster as CSV (UTF-8, CRLF, header row), one row
// per player in roster order.
func (s *MatchSummary) PlayersCSV() []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	_ = w.Write(csvColumns)
	f := func(v float64, prec int) string { return strconv.FormatFloat(v, 'f', prec, 64) }
	for _, p := range s.Players {
		su := p.Suspicion
		if su == nil {
			su = &PlayerSuspicion{}
		}
		st := p.Stats
		_ = w.Write([]string{
			p.PlayerID, p.Name, p.Team, strconv.Itoa(p.Level), strconv.Itoa(p.Frames), f(p.FirstSeen, 2), f(p.LastSeen, 2),
			f(p.PingAvg, 1), strconv.Itoa(p.PingMax),
			strconv.Itoa(st.Points), strconv.Itoa(st.Goals), strconv.Itoa(st.Assists), strconv.Itoa(st.Saves), strconv.Itoa(st.Steals),
			strconv.Itoa(st.Stuns), strconv.Itoa(st.Passes), strconv.Itoa(st.Catches), strconv.Itoa(st.Blocks), strconv.Itoa(st.Interceptions),
			f(st.PossessionTime, 2), strconv.Itoa(st.ShotsTaken),
			strconv.Itoa(p.Throws.Count), f(p.Throws.MeanSpeed, 2), f(p.Throws.MaxSpeed, 2), strconv.Itoa(p.Throws.Goals),
			f(su.Score, 2), su.Level, strconv.Itoa(su.Detections), strconv.Itoa(su.ShadowDetections), strings.Join(su.TopDetectors, "; "),
		})
	}
	w.Flush()
	return buf.Bytes()
}

// ErrNoRawTicks is returned by RebuildSummary for a match whose source
// snapshots are not in the store (a legacy JSON replay, or a live match).
var ErrNoRawTicks = errors.New("no raw ticks stored for the match")

// RebuildSummary builds the summary of a stored match from its raw ticks
// (match_ticks), for matches analyzed before summaries existed. The ticks'
// match-relative times come from the telemetry frames; a tick without one
// is placed a nominal tick after the previous. Suspicion is not applied.
func RebuildSummary(ctx context.Context, store *sqlite.Store, mc *model.MatchContext) (*MatchSummary, error) {
	if mc == nil || mc.MatchID == "" {
		return nil, errors.New("rebuild summary: no match context")
	}
	times, err := store.GetMatchTickTimestamps(ctx, mc.MatchID)
	if err != nil {
		return nil, fmt.Errorf("loading tick times: %w", err)
	}
	nominal := 1.0 / 30
	if mc.TickRate > 0 {
		nominal = 1 / mc.TickRate
	}
	b := NewSummaryBuilder(mc)
	last, haveLast := 0.0, false
	n, err := store.ForEachMatchTick(ctx, mc.MatchID, func(idx int, raw string) error {
		var session adapter.EchoVRSessionResponse
		if err := json.Unmarshal([]byte(raw), &session); err != nil {
			return nil // a payload that cannot be decoded is skipped, as the parser would have
		}
		t, ok := times[idx]
		if !ok {
			if haveLast {
				t = last + nominal
			}
		}
		if haveLast && t < last {
			t = last
		}
		last, haveLast = t, true
		b.Add(&session, idx, t)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading raw ticks: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("match %s: %w", mc.MatchID, ErrNoRawTicks)
	}
	s := b.Finish()
	if math.IsNaN(s.DurationSeconds) || s.DurationSeconds < 0 {
		s.DurationSeconds = 0
	}
	return s, nil
}

// LoadMatchSummary returns the stored summary of a match, rebuilding and
// storing it from the raw ticks when the match was analyzed before
// summaries existed. The suspicion verdicts are (re)applied from the given
// scores and events either way, so the document reflects the analysis
// currently stored. ErrNoRawTicks when neither exists.
func (e *Engine) LoadMatchSummary(ctx context.Context, mc *model.MatchContext, scores map[string]model.SuspicionScore, events []model.DetectionEvent) (*MatchSummary, error) {
	if mc == nil {
		return nil, errors.New("no match context")
	}
	var s *MatchSummary
	if doc, err := e.store.GetMatchSummaryJSON(ctx, mc.MatchID); err == nil {
		s = &MatchSummary{}
		if err := json.Unmarshal(doc, s); err != nil {
			s = nil
		}
	} else if !errors.Is(err, sqlite.ErrNotFound) {
		return nil, err
	}
	rebuilt := false
	if s == nil {
		var err error
		if s, err = RebuildSummary(ctx, e.store, mc); err != nil {
			return nil, err
		}
		rebuilt = true
	}
	s.ApplySuspicion(mc, scores, events, e.Levels())
	if rebuilt {
		if doc, err := json.Marshal(s); err == nil {
			_ = e.store.StoreMatchSummaryJSON(ctx, s.Meta(), doc) // a cache miss next time is harmless
		}
	}
	return s, nil
}
