package adapter

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// MappingResult contains the converted frames plus any warnings/errors.
type MappingResult struct {
	Frames   []model.PlayerTelemetryFrame
	MatchCtx *model.MatchContext
	Warnings []MappingWarning
	Errors   []MappingError

	// SkippedDuplicate is true when duplicate detection is enabled and the
	// snapshot was byte-for-byte the same game state as the previous one
	// (same game_clock, disc and player poses). No frames are produced and
	// the frame index is not advanced.
	SkippedDuplicate bool
	// SpectatorsDropped is the number of player entries on non-blue/orange
	// teams (SPECTATORS, unknown) that were excluded from this snapshot.
	SpectatorsDropped int
}

// MappingWarning is a non-fatal issue where a safe default was used.
type MappingWarning struct {
	Field   string
	Message string
}

// MappingError is a fatal issue where the frame was rejected.
type MappingError struct {
	PlayerName string
	Field      string
	Message    string
}

// FieldConfidence indicates how reliable a field mapping is.
type FieldConfidence int

const (
	Confirmed FieldConfidence = iota // verified against real Echo VR data
	Likely                           // probably correct based on community docs
	Inferred                         // computed from other fields
	Unknown                          // unconfirmed; using safe default
	Absent                           // known to not exist in Echo VR
)

// MapperStats are per-session counters the mapper keeps so operators can see
// data-quality signals that individual warnings (emitted once) cannot convey.
type MapperStats struct {
	// Snapshots is the number of session payloads mapped (frame ticks).
	Snapshots int `json:"snapshots"`
	// FramesMapped is the number of per-player frames produced.
	FramesMapped int `json:"frames_mapped"`
	// FramesRejected is the number of per-player entries rejected (zero/NaN position).
	FramesRejected int `json:"frames_rejected"`
	// SpectatorsDropped counts player entries on SPECTATORS/unknown teams.
	SpectatorsDropped int `json:"spectators_dropped"`
	// DuplicatesSkipped counts snapshots skipped by change detection.
	DuplicatesSkipped int `json:"duplicates_skipped"`
	// NonMonotonicSamples counts frames of snapshots whose sample time went
	// backwards by less than clockStepThreshold. Timestamp is clamped to the
	// previous snapshot's Timestamp for them (never decreases), so a player
	// present in both snapshots reports DeltaTime 0 (unknown).
	NonMonotonicSamples int `json:"non_monotonic_samples"`
	// ClockSteps counts snapshots whose sample time went backwards by at
	// least clockStepThreshold (recorder clock adjusted: NTP step, DST
	// fall-back). A new local source epoch is created with unknown boundary
	// duration; no nominal cadence is presented as measured elapsed time.
	ClockSteps int `json:"clock_steps"`
	// PossessionConflicts counts ticks where more than one player reported possession.
	PossessionConflicts int `json:"possession_conflicts"`

	// Direction-vector basis quality, counted per converted pose (body + both hands).
	BasesProper         int `json:"bases_proper"`
	BasesReflected      int `json:"bases_reflected"`
	BasesNonOrthonormal int `json:"bases_non_orthonormal"`
	BasesDegenerate     int `json:"bases_degenerate"`
	// HandTrackingLost counts hand poses emitted as the zero quaternion because
	// one or more direction vectors were missing.
	HandTrackingLost int `json:"hand_tracking_lost"`
}

// Mapper converts raw Echo VR API responses into NEVR-Anticheat telemetry frames.
//
// Time base: the producer supplies the real sample time of every snapshot
// (MapSessionAt). Timestamp is seconds since the first sample this mapper saw
// (or since NewMatch) and DeltaTime is the per-player gap to that player's
// previous frame (0 on a player's first frame). Nothing assumes a fixed poll
// cadence. Timestamp never decreases: a sample time that goes backwards by
// less than clockStepThreshold is clamped to the previous snapshot's
// Timestamp (MapperStats.NonMonotonicSamples); a larger backward jump is a
// recorder clock step. Its local source epoch changes, DeltaTime is unknown
// (zero), and the ordering timestamp advances by only one floating-point ULP.
// Subsequent samples use measured spacing within that new epoch. Rebased
// timestamps are explicitly marked in Observation.TimeBasis.
//
// Frame identity: FrameIndex is a 0-based counter per Mapper (per match after
// NewMatch). Producers must preserve their match-relative index/time across
// reconnects: the ingest server never converts retries into fresh frames.
type Mapper struct {
	frameIndex int

	haveFirstSample bool
	firstSampleTime time.Time
	// clockOffset (seconds) is added to every sample time after a backward
	// clock step solely to keep an ordered storage coordinate.
	clockOffset float64
	// lastTickTimestamp is the Timestamp of the previous sampled snapshot.
	haveLastTick      bool
	lastTickTimestamp float64
	// prevTimestamp is the last Timestamp emitted for each player.
	prevTimestamp map[string]float64

	physics model.PhysicsConstants

	dedupe          bool
	lastFingerprint uint64
	haveFingerprint bool

	lastThrow                                                    EchoVRLastThrow
	haveLastThrow                                                bool
	throwClientID                                                string
	throwSessionID                                               string
	observationSource, observationTimeBasis, observationSourceID string
	observationEpoch                                             uint64

	stats MapperStats

	// Track which fields have been warned about (warn once per field)
	warnedFields map[string]bool
}

// NewMapper creates a new Echo VR telemetry mapper.
func NewMapper() *Mapper {
	return &Mapper{
		prevTimestamp:     make(map[string]float64),
		warnedFields:      make(map[string]bool),
		physics:           model.DefaultPhysics(),
		observationSource: "echovr_session", observationTimeBasis: "supplied_sample_time",
	}
}

const (
	// clockStepThreshold is the backward jump of the sample time (seconds)
	// from which a snapshot is treated as a recorder clock step and re-based
	// rather than clamped.
	clockStepThreshold = 1.0
)

// NewMatch resets the per-match state: the time base (Timestamp restarts at
// 0 on the next snapshot, which becomes MatchContext.StartTime), the frame
// index, the per-player timestamp history and the duplicate fingerprint.
// Counters (Stats) and warn-once state persist. The replay parser calls it
// when a recording's session id changes so the next match starts clean.
func (m *Mapper) NewMatch() {
	m.frameIndex = 0
	m.haveFirstSample = false
	m.firstSampleTime = time.Time{}
	m.clockOffset = 0
	m.haveLastTick = false
	m.lastTickTimestamp = 0
	m.observationEpoch = 0
	m.prevTimestamp = make(map[string]float64)
	m.haveFingerprint = false
	m.lastFingerprint = 0
	m.lastThrow = EchoVRLastThrow{}
	m.haveLastThrow = false
	m.throwClientID, m.throwSessionID = "", ""
}

// NewFile resets both per-match state and file-level diagnostics. NewMatch
// intentionally keeps counters across sessions in one recording; callers
// starting another recording should use NewFile instead.
func (m *Mapper) NewFile() {
	m.NewMatch()
	m.stats = MapperStats{}
	m.warnedFields = make(map[string]bool)
}

// SetPhysics sets the physics constants copied into every MatchContext this
// mapper produces (default: model.DefaultPhysics()). Callers that load a
// [physics] config section should pass it here so detectors see it.
func (m *Mapper) SetPhysics(p model.PhysicsConstants) { m.physics = p }

// SetDedupeIdentical enables change detection: a snapshot whose game state
// (game_clock, game_status, disc pose, every player's body/hand positions and
// possession/stun flags) is identical to the previous snapshot is skipped and
// reported via MappingResult.SkippedDuplicate. Intended for the live poll path,
// where a broadcaster that updates slower than the poll interval would
// otherwise produce zero-velocity frames followed by double-distance frames.
func (m *Mapper) SetDedupeIdentical(enabled bool) { m.dedupe = enabled }

// Stats returns a snapshot of the mapper's counters.
func (m *Mapper) Stats() MapperStats { return m.stats }

// FirstSampleTime returns the wall-clock time of the first snapshot this
// mapper mapped (the origin of Timestamp), or the zero time if none yet.
func (m *Mapper) FirstSampleTime() time.Time { return m.firstSampleTime }

// MapSession converts a raw Echo VR /session response into telemetry frames
// using the current wall-clock time as the sample time. Live producers that
// know when the payload was received should prefer MapSessionAt.
func (m *Mapper) MapSession(raw *EchoVRSessionResponse) *MappingResult {
	return m.MapSessionAt(raw, time.Now())
}

// MapSessionAt converts a raw Echo VR /session response sampled at sampleTime
// into telemetry frames. Returns one frame per blue/orange player in the
// response; spectators and players with invalid positions are dropped.
func (m *Mapper) MapSessionAt(raw *EchoVRSessionResponse, sampleTime time.Time) *MappingResult {
	result := &MappingResult{}

	if raw == nil {
		result.Errors = append(result.Errors, MappingError{Field: "session", Message: "nil session response"})
		return result
	}

	if !m.haveFirstSample {
		m.haveFirstSample = true
		m.firstSampleTime = sampleTime
	}

	result.MatchCtx = m.mapMatchContext(raw, result)

	clientID := uniqueClientPlayer(raw)
	if clientID != m.throwClientID || raw.SessionID != m.throwSessionID {
		m.haveLastThrow, m.haveFingerprint = false, false
		m.throwClientID, m.throwSessionID = clientID, raw.SessionID
	}
	// Detect a clock discontinuity even when the payload is unchanged.
	// It invalidates deduplication and local throw baselines before mapping.
	timestamp, backwards := m.tickTimestamp(sampleTime, result)
	if m.dedupe {
		fp := sessionFingerprint(raw)
		if m.haveFingerprint && fp == m.lastFingerprint {
			result.SkippedDuplicate = true
			m.stats.DuplicatesSkipped++
			return result
		}
		m.lastFingerprint = fp
		m.haveFingerprint = true
	}

	reportedThrow := m.changedLastThrow(raw.LastThrow)
	if clientID == "" {
		reportedThrow = nil
	}

	// Build ONE disc state per tick. Explicit hand-held item fields are the
	// preferred sampled attachment signal; legacy booleans remain separately
	// compatible, while the explicit Attachment state never uses that fallback.
	// The disc is held by whichever mapped player reports it. Every frame
	// of this tick gets the same values so detectors that look at "the disc"
	// through any player's frame agree on whether it is held and by whom.
	var tickDisc *model.DiscState
	if raw.Disc.hasPosition() && raw.Disc.hasVelocity() {
		vel := model.Vec3(raw.Disc.Velocity)
		tickDisc = &model.DiscState{
			Position: model.Vec3(raw.Disc.Position),
			Velocity: vel,
			Speed:    vel.Magnitude(),
		}
		if raw.Disc.BounceCount != nil && *raw.Disc.BounceCount >= 0 {
			count := *raw.Disc.BounceCount
			tickDisc.BounceCount = &count
		}
		tickDisc.Attachment = observedDiscAttachment(raw)
	} else if raw.Disc != nil {
		m.warnOnce(result, "disc", "disc_motion_unavailable", "disc position or velocity is absent/null/incomplete; disc observation omitted rather than manufacturing a zero vector")
	}
	holders := 0
	sampledPlayers := 0
	allHoldingKnown := true
	for teamIdx, team := range raw.Teams {
		if _, ok := MappedSessionTeamName(raw, teamIdx); !ok {
			continue
		}
		for i := range team.Players {
			sampledPlayers++
			p := &team.Players[i]
			// Both fields must be explicit. HasDisc keeps its established
			// one-field/legacy fallback semantics for existing detectors.
			allHoldingKnown = allHoldingKnown && heldItemKnown(p.HoldingLeft) && heldItemKnown(p.HoldingRight)
			if team.Players[i].HasDisc() {
				holders++
				if tickDisc != nil && !tickDisc.IsHeld {
					tickDisc.IsHeld = true
					tickDisc.PossessorID = playerID(team.Players[i])
				}
			}
		}
	}
	if tickDisc != nil {
		tickDisc.SampledPlayerCount = sampledPlayers
		tickDisc.PossessionKnown = sampledPlayers > 0 && allHoldingKnown
		tickDisc.PossessionConflict = holders > 1
	}
	if holders > 1 {
		m.stats.PossessionConflicts++
		m.warnOnce(result, "possession", "possession_conflict",
			fmt.Sprintf("%d players report possession in the same tick; the first in team order is used as holder", holders))
	}

	// Map each player
	for teamIdx, team := range raw.Teams {
		teamName, ok := MappedSessionTeamName(raw, teamIdx)
		if !ok {
			result.SpectatorsDropped += len(team.Players)
			m.stats.SpectatorsDropped += len(team.Players)
			if len(team.Players) > 0 {
				m.warnOnce(result, "teams", "spectators",
					fmt.Sprintf("team %q is not blue/orange; its players are excluded from telemetry", team.TeamName))
			}
			continue
		}
		if team.TeamName == "" {
			m.warnOnce(result, "teams", "unnamed_team",
				"team entry has no name; team assigned by array index (0=blue, 1=orange)")
		} else if _, named := mappedTeamName(team.TeamName, teamIdx); !named {
			m.warnOnce(result, "teams", "custom_team_names",
				"custom team display names use the standard three-entry Echo layout (0=blue, 1=orange, 2=SPECTATORS); names do not establish team colour")
		}

		for i := range team.Players {
			player := &team.Players[i]
			pid := playerID(*player)

			dt := 0.0
			if prev, seen := m.prevTimestamp[pid]; seen {
				dt = timestamp - prev
				if dt < 0 {
					dt = 0 // cannot happen: Timestamp is monotonic per mapper
				}
			}
			if backwards {
				m.stats.NonMonotonicSamples++
			}

			frame, warnings, err := m.mapPlayer(player, raw, teamName, timestamp, dt, m.frameIndex, tickDisc, reportedThrow)
			if err != nil {
				result.Errors = append(result.Errors, *err)
				m.stats.FramesRejected++
				continue
			}
			m.prevTimestamp[pid] = timestamp
			result.Frames = append(result.Frames, *frame)
			result.Warnings = append(result.Warnings, warnings...)
			m.stats.FramesMapped++
		}
	}

	m.stats.Snapshots++
	m.frameIndex++
	return result
}

// changedLastThrow returns an engine throw record only on the snapshot where
// it changes. The first value establishes a baseline because a replay can
// begin after the local player has already thrown. Missing values do not
// erase that baseline; all-zero reset records do, but are never emitted.
func (m *Mapper) changedLastThrow(raw *EchoVRLastThrow) *model.GameThrowDetails {
	if raw == nil {
		return nil
	}
	changed := m.haveLastThrow && *raw != m.lastThrow
	m.lastThrow = *raw
	m.haveLastThrow = true
	if !changed {
		return nil
	}
	details := model.GameThrowDetails{
		ArmSpeed: raw.ArmSpeed, TotalSpeed: raw.TotalSpeed,
		OffAxisSpinDeg: raw.OffAxisSpinDeg, WristThrowPenalty: raw.WristThrowPenalty,
		RotPerSec: raw.RotPerSec, PotentialSpeedFromRot: raw.PotentialSpeedFromRot,
		SpeedFromArm: raw.SpeedFromArm, SpeedFromMovement: raw.SpeedFromMovement,
		SpeedFromWrist: raw.SpeedFromWrist, WristAlignToThrowDeg: raw.WristAlignToThrowDeg,
		ThrowAlignToMovementDeg: raw.ThrowAlignToMovementDeg,
		OffAxisPenalty:          raw.OffAxisPenalty, ThrowMovePenalty: raw.ThrowMovePenalty,
	}
	if !details.Valid() {
		return nil
	}
	return &details
}

// tickTimestamp converts a snapshot's sample time into the monotonic
// Timestamp of this tick. backwards reports that the raw sample time went
// backwards (by less than clockStepThreshold) and was clamped.
func (m *Mapper) tickTimestamp(sampleTime time.Time, result *MappingResult) (timestamp float64, backwards bool) {
	timestamp = sampleTime.Sub(m.firstSampleTime).Seconds() + m.clockOffset
	if m.haveLastTick {
		switch {
		case timestamp <= m.lastTickTimestamp-clockStepThreshold:
			// Only maintain storage ordering. The ULP is not a measurement
			// of elapsed time; every player gets dt=0 and a new source epoch.
			want := math.Nextafter(m.lastTickTimestamp, math.Inf(1))
			m.clockOffset += want - timestamp
			m.observationEpoch++
			m.prevTimestamp = make(map[string]float64)
			m.haveLastThrow, m.haveFingerprint = false, false
			m.stats.ClockSteps++
			m.warnOnce(result, "sample_time", "clock_step",
				fmt.Sprintf("sample time stepped back %.3f s; local source epoch changed, boundary duration unknown, timestamps use a rebased ordering coordinate (not continuous capture time)",
					m.lastTickTimestamp-timestamp))
			timestamp = want
		case timestamp < m.lastTickTimestamp:
			timestamp = m.lastTickTimestamp
			backwards = true
		}
	}
	m.haveLastTick = true
	m.lastTickTimestamp = timestamp
	return timestamp, backwards
}

// warnOnce appends a warning to the result the first time key is seen.
func (m *Mapper) warnOnce(result *MappingResult, field, key, message string) {
	if m.warnedFields[key] {
		return
	}
	m.warnedFields[key] = true
	result.Warnings = append(result.Warnings, MappingWarning{Field: field, Message: message})
}

// mappedTeamName resolves an Echo VR team entry to "blue"/"orange".
// Only exact canonical labels establish a role, independently of array order.
// Colour words inside display names do not. Entries without a name fall back
// to array index (0=blue, 1=orange); other labels need the snapshot-level layout
// validation in MappedSessionTeamName, or are excluded.
func mappedTeamName(name string, idx int) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "blue", "blue team":
		return "blue", true
	case "orange", "orange team":
		return "orange", true
	case "":
		switch idx {
		case 0:
			return "blue", true
		case 1:
			return "orange", true
		}
	}
	return "", false
}

// MappedSessionTeamName resolves a team consistently for telemetry, evidence,
// diagnostics and summaries. A standard Echo snapshot has exactly three slots:
// blue, orange, spectators. Only that complete layout, with an explicit final
// SPECTATORS label and no conflicting canonical labels, permits custom names
// in the first two slots. A two-team or unfamiliar layout cannot establish the
// roles of arbitrary display names and retains the stricter name-based rule.
// Sources with explicit roles (for example native tape) should project those
// roles to canonical labels rather than interpreting header display names.
func MappedSessionTeamName(raw *EchoVRSessionResponse, idx int) (string, bool) {
	if raw == nil || idx < 0 || idx >= len(raw.Teams) {
		return "", false
	}
	if standardEchoTeamLayout(raw.Teams) && idx < 2 {
		return mappedTeamName("", idx)
	}
	return mappedTeamName(raw.Teams[idx].TeamName, idx)
}

func standardEchoTeamLayout(teams []EchoVRTeam) bool {
	if len(teams) != 3 || !strings.EqualFold(strings.TrimSpace(teams[2].TeamName), "spectators") {
		return false
	}
	for idx := 0; idx < 2; idx++ {
		name := strings.ToLower(strings.TrimSpace(teams[idx].TeamName))
		if strings.Contains(name, "spectator") {
			return false
		}
		if named, ok := mappedTeamName(name, idx); ok {
			byIndex, _ := mappedTeamName("", idx)
			if named != byIndex {
				return false
			}
		}
	}
	return true
}

// sessionFingerprint hashes the parts of a snapshot that change every tick
// while the game is live. Two consecutive equal fingerprints mean the
// broadcaster served the same state twice.
func sessionFingerprint(raw *EchoVRSessionResponse) uint64 {
	h := fnv.New64a()
	writeI := func(v uint64) {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], v)
		h.Write(buf[:])
	}
	writeF := func(v float64) { writeI(math.Float64bits(v)) }
	writeSigned := func(v int64) {
		// Self-delimiting signed encoding preserves the complete int64 domain
		// without float rounding or a signed-to-unsigned conversion.
		var buf [binary.MaxVarintLen64]byte
		n := binary.PutVarint(buf[:], v)
		h.Write(buf[:n])
	}
	writeS := func(v string) { writeI(uint64(len(v))); h.Write([]byte(v)) }
	writeV := func(v [3]float64) { writeF(v[0]); writeF(v[1]); writeF(v[2]) }
	writeB := func(v bool) {
		if v {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	}
	writeS(raw.GameStatus)
	writeS(raw.ClientName)
	writeS(raw.SessionID)
	writeS(raw.MatchType)
	writeS(raw.MapName)
	writeB(raw.PrivateMatch)
	writeF(raw.GameClock)
	writeB(raw.Disc != nil)
	if raw.Disc != nil {
		writeB(raw.Disc.hasPosition())
		writeB(raw.Disc.hasVelocity())
		writeV(raw.Disc.Position)
		writeV(raw.Disc.Velocity)
		writeB(raw.Disc.BounceCount != nil)
		if raw.Disc.BounceCount != nil {
			// Preserve signed raw values (including invalid negatives) in a
			// self-delimiting encoding without unsigned conversion overflow.
			writeSigned(int64(*raw.Disc.BounceCount))
		}
	}
	writeSigned(int64(raw.BluePoints))
	writeSigned(int64(raw.OrangePoints))
	writeB(raw.LastThrow != nil)
	if raw.LastThrow != nil {
		writeF(raw.LastThrow.ArmSpeed)
		writeF(raw.LastThrow.TotalSpeed)
		writeF(raw.LastThrow.OffAxisSpinDeg)
		writeF(raw.LastThrow.WristThrowPenalty)
		writeF(raw.LastThrow.RotPerSec)
		writeF(raw.LastThrow.PotentialSpeedFromRot)
		writeF(raw.LastThrow.SpeedFromArm)
		writeF(raw.LastThrow.SpeedFromMovement)
		writeF(raw.LastThrow.SpeedFromWrist)
		writeF(raw.LastThrow.WristAlignToThrowDeg)
		writeF(raw.LastThrow.ThrowAlignToMovementDeg)
		writeF(raw.LastThrow.OffAxisPenalty)
		writeF(raw.LastThrow.ThrowMovePenalty)
	}
	writeI(uint64(len(raw.Teams)))
	for _, team := range raw.Teams {
		writeS(team.TeamName)
		writeI(uint64(len(team.Players)))
		for i := range team.Players {
			p := &team.Players[i]
			// User IDs are exact int64 values. Conversion through float64
			// aliases adjacent IDs above 2^53 and can drop a roster change.
			writeSigned(p.UserID)
			writeS(p.Name)
			writeB(p.hasVelocity())
			writeV(p.Velocity)
			writeV(p.Body.Position)
			writeV(p.Head.Position)
			writeV(p.Body.Forward)
			writeV(p.Body.Left)
			writeV(p.Body.Up)
			writeV(p.LHand.Position)
			writeV(p.RHand.Position)
			writeV(p.LHand.Forward)
			writeV(p.LHand.Left)
			writeV(p.LHand.Up)
			writeV(p.RHand.Forward)
			writeV(p.RHand.Left)
			writeV(p.RHand.Up)
			writeB(p.Possession)
			writeS(p.HoldingLeft)
			writeS(p.HoldingRight)
			writeB(p.Stunned)
			writeB(p.Blocking)
			writeB(p.Invulnerable)
			writeSigned(int64(p.Ping))
			writeSigned(int64(p.Stats.Goals))
			writeSigned(int64(p.Stats.Stuns))
		}
	}
	return h.Sum64()
}

// mapMatchContext extracts match-level metadata.
func (m *Mapper) mapMatchContext(raw *EchoVRSessionResponse, result *MappingResult) *model.MatchContext {
	mc := &model.MatchContext{
		MatchID:         raw.SessionID, // CONFIRMED
		GameMode:        raw.MatchType, // CONFIRMED
		Map:             raw.MapName,   // CONFIRMED
		IsPrivate:       raw.PrivateMatch,
		Physics:         m.physics,
		Source:          "echovr_api",
		StartTime:       m.firstSampleTime,
		TeamAssignments: make(map[string]string),
	}

	// Map game phase
	// CONFIRMED: game_status values are "playing", "round_start", "round_over", "pre_match", "post_match", "score"
	// Our system expects the same strings (with "score" treated as non-active)

	// Collect player IDs of the two playing teams only.
	for teamIdx, team := range raw.Teams {
		teamName, ok := MappedSessionTeamName(raw, teamIdx)
		if !ok {
			continue
		}
		for _, p := range team.Players {
			pid := playerID(p)
			if _, dup := mc.TeamAssignments[pid]; !dup {
				mc.PlayerIDs = append(mc.PlayerIDs, pid)
			}
			mc.TeamAssignments[pid] = teamName
			if p.Name != "" {
				if mc.PlayerNames == nil {
					mc.PlayerNames = make(map[string]string)
				}
				mc.PlayerNames[pid] = p.Name
			}
		}
	}

	return mc
}

// MergeMatchContext folds the roster, team assignments and display names of
// src into dst (union of players, latest team and name win) without touching
// dst's identity fields. Replay and live producers use it so late joiners and
// mid-match team changes reach the persisted context instead of only the
// first snapshot.
func MergeMatchContext(dst, src *model.MatchContext) {
	if dst == nil || src == nil {
		return
	}
	if dst.TeamAssignments == nil {
		dst.TeamAssignments = make(map[string]string)
	}
	known := make(map[string]bool, len(dst.PlayerIDs))
	for _, pid := range dst.PlayerIDs {
		known[pid] = true
	}
	for _, pid := range src.PlayerIDs {
		if !known[pid] {
			dst.PlayerIDs = append(dst.PlayerIDs, pid)
			known[pid] = true
		}
		if team, ok := src.TeamAssignments[pid]; ok && team != "" {
			dst.TeamAssignments[pid] = team
		}
		if name, ok := src.PlayerNames[pid]; ok && name != "" {
			if dst.PlayerNames == nil {
				dst.PlayerNames = make(map[string]string)
			}
			dst.PlayerNames[pid] = name
		}
	}
}

// mapPlayer converts a single Echo VR player into a PlayerTelemetryFrame.
func (m *Mapper) mapPlayer(
	p *EchoVRPlayer,
	session *EchoVRSessionResponse,
	team string,
	timestamp float64,
	dt float64,
	frameIdx int,
	tickDisc *model.DiscState,
	reportedThrow *model.GameThrowDetails,
) (*model.PlayerTelemetryFrame, []MappingWarning, *MappingError) {
	var warnings []MappingWarning
	pid := playerID(*p)

	// CONFIRMED from real replay: position is under body.position, NOT top-level.
	pos := model.Vec3(p.Body.Position)
	if pos.IsZero() {
		return nil, nil, &MappingError{PlayerName: p.Name, Field: "body.position", Message: "zero position"}
	}
	if pos.HasNaN() || pos.HasInf() {
		return nil, nil, &MappingError{PlayerName: p.Name, Field: "body.position", Message: "NaN/Inf position"}
	}
	var headPosition *model.Vec3
	if head := model.Vec3(p.Head.Position); !head.IsZero() && !head.HasNaN() && !head.HasInf() {
		headPosition = &head
	}

	// CONFIRMED from real replay: body rotation from body.forward/left/up direction vectors.
	// A degenerate basis (missing vectors) yields the ZERO quaternion, which
	// consumers treat as "no orientation" (IsUnit() == false), never identity.
	bodyRot, bodyQuality := m.convertBasis(p.Body.Forward, p.Body.Left, p.Body.Up)
	if bodyQuality.Degenerate() {
		warnings = m.appendWarningOnce(warnings, "body_rotation",
			"body direction vectors are missing/degenerate; body rotation emitted as zero quaternion")
	}
	if bodyQuality.Reflected() {
		warnings = m.appendWarningOnce(warnings, "basis_reflected",
			"direction vectors form a reflected basis (left = -(up x forward)); converter negates left, counted in MapperStats.BasesReflected")
	}
	if bodyQuality.NonOrthonormal() {
		warnings = m.appendWarningOnce(warnings, "basis_non_orthonormal",
			"direction vectors are not orthonormal; re-orthonormalised, counted in MapperStats.BasesNonOrthonormal")
	}

	// CONFIRMED: hand positions from nested hand objects
	leftHandPos := model.Vec3(p.LHand.Position)
	rightHandPos := model.Vec3(p.RHand.Position)

	// CONFIRMED/LIKELY: hand rotations from direction vectors.
	// Lost tracking (any direction vector zero) is encoded as the ZERO
	// quaternion so the feature extractor's IsUnit guard skips wrist rates and
	// BIO_004 never mixes a fake identity pose into its variance window.
	leftHandRot, leftHandRotationValid := m.convertHand(&p.LHand, &warnings, "left_hand_rotation", "lhand_rot")
	rightHandRot, rightHandRotationValid := m.convertHand(&p.RHand, &warnings, "right_hand_rotation", "rhand_rot")

	// CONFIRMED: disc state is shared by every frame of the tick (see MapSessionAt).
	var disc *model.DiscState
	if tickDisc != nil {
		d := *tickDisc
		d.Attachment = tickDisc.Attachment.Clone()
		if tickDisc.BounceCount != nil {
			count := *tickDisc.BounceCount
			d.BounceCount = &count
		}
		disc = &d
	}

	// Explicit hand-held fields drop on the actual release tick. The legacy
	// Possession boolean can remain true for the last carrier after release.
	hasPossession := p.HasDisc()

	// CONFIRMED: stunned boolean
	isStunned := p.Stunned

	// LIKELY: blocking/shield state
	shieldActive := p.Blocking // LIKELY — field name may differ

	// LIKELY: invulnerable state (post-respawn immunity)
	isImmune := p.Invulnerable

	// ABSENT: boosting — must be inferred from velocity changes
	// We set IsBoosting=false and note the limitation
	isBoosting := false
	warnings = m.appendWarningOnce(warnings, "is_boosting",
		"Echo VR API does not expose boosting state. MOV_004 and MOV_005 will be inactive. ABSENT field.")

	// CONFIRMED: per-player ping exists in Echo VR API
	pingMs := float64(p.Ping) // int ms to float64
	var reportedVelocity *model.Vec3
	if p.hasVelocity() {
		value := model.Vec3(p.Velocity)
		reportedVelocity = &value
	} else {
		warnings = m.appendWarningOnce(warnings, "player_velocity_unavailable", "player velocity is absent/null/incomplete; reported velocity omitted rather than manufacturing stationary motion")
	}

	// Map game phase
	gamePhase := mapGamePhase(session.GameStatus)

	// CONFIRMED: team scores
	blueScore := session.BluePoints
	orangeScore := session.OrangePoints

	// CONFIRMED: per-player stats
	goals := p.Stats.Goals
	stuns := p.Stats.Stuns

	frame := &model.PlayerTelemetryFrame{
		Observation:            m.observation(session, frameIdx, timestamp),
		HeldItems:              handAttachments(p),
		IsBoostingKnown:        new(bool),
		PlayerID:               pid,
		Team:                   team,
		FrameIndex:             frameIdx,
		Timestamp:              timestamp,
		DeltaTime:              dt,
		Position:               pos,
		HeadPosition:           headPosition,
		Rotation:               bodyRot,
		ReportedVelocity:       reportedVelocity,
		LeftHandPosition:       leftHandPos,
		RightHandPosition:      rightHandPos,
		LeftHandRotation:       leftHandRot,
		RightHandRotation:      rightHandRot,
		LeftHandRotationValid:  &leftHandRotationValid,
		RightHandRotationValid: &rightHandRotationValid,
		IsStunned:              isStunned,
		IsBoosting:             isBoosting,
		ShieldActive:           shieldActive,
		IsImmune:               isImmune,
		HasPossession:          hasPossession,
		Disc:                   disc,
		EstimatedPingMs:        pingMs,
		GamePhase:              gamePhase,
		HasScore:               true,
		BlueScore:              blueScore,
		OrangeScore:            orangeScore,
		Goals:                  goals,
		Stuns:                  stuns,
	}
	if reportedThrow != nil && m.throwClientID != "" && pid == m.throwClientID {
		details := *reportedThrow
		frame.GameLastThrow = &details
		frame.GameLastThrowProvenance = frame.Observation.Clone()
		frame.GameLastThrowProvenance.Freshness = "value_change"
	}

	return frame, warnings, nil
}

// appendWarningOnce appends a warning keyed by field the first time it occurs.
func (m *Mapper) appendWarningOnce(warnings []MappingWarning, field, message string) []MappingWarning {
	if m.warnedFields[field] {
		return warnings
	}
	m.warnedFields[field] = true
	return append(warnings, MappingWarning{Field: field, Message: message})
}

// convertBasis converts a direction-vector triple and updates the quality counters.
func (m *Mapper) convertBasis(forward, left, up [3]float64) (model.Quat, model.BasisQuality) {
	q, quality := model.QuatFromDirectionVectorsChecked(model.Vec3(forward), model.Vec3(left), model.Vec3(up))
	switch {
	case quality.Degenerate():
		m.stats.BasesDegenerate++
	case quality == model.BasisProper:
		m.stats.BasesProper++
	default:
		if quality.Reflected() {
			m.stats.BasesReflected++
		}
		if quality.NonOrthonormal() {
			m.stats.BasesNonOrthonormal++
		}
	}
	return q, quality
}

// convertHand maps a hand's direction vectors to a quaternion. All three
// vectors are required, finite and orthonormal within the conversion tolerance.
// Reflected Echo conventions remain valid; a repaired or missing basis does not
// become an observed wrist orientation merely because it produces a unit value.
func (m *Mapper) convertHand(h *EchoVRHand, warnings *[]MappingWarning, field, warnKey string) (model.Quat, bool) {
	invalid := false
	for _, raw := range [][3]float64{h.Forward, h.Up, h.Left} {
		v := model.Vec3(raw)
		invalid = invalid || v.IsZero() || v.HasNaN() || v.HasInf()
	}
	if invalid {
		m.stats.HandTrackingLost++
		if !m.warnedFields[warnKey] {
			m.warnedFields[warnKey] = true
			*warnings = append(*warnings, MappingWarning{
				Field:   field,
				Message: "hand direction vectors are missing or non-finite; emitting unavailable wrist orientation so BIO_001/BIO_004 skip the frame.",
			})
		}
		return model.Quat{}, false
	}
	q, quality := m.convertBasis(h.Forward, h.Left, h.Up)
	if quality.Degenerate() || quality.NonOrthonormal() || !q.IsUnit() {
		m.stats.HandTrackingLost++
		return model.Quat{}, false
	}
	return q, true
}

// PlayerIDOf is the player id the mapper keys everything by ("echovr:<userid>",
// or "name:<name>" for a player without a user id), for consumers that read
// the decoded session directly (ParsedTick.Session).
func PlayerIDOf(p *EchoVRPlayer) string { return playerID(*p) }

// MappedTeamName resolves one canonical team label without snapshot context.
// Consumers with a full snapshot should use MappedSessionTeamName so custom
// display names receive the same layout validation as the mapper.
func MappedTeamName(name string, idx int) (team string, ok bool) { return mappedTeamName(name, idx) }

// playerID creates a stable player identifier from Echo VR player data.
// Uses UserID (numeric Oculus ID) which survives name changes.
func playerID(p EchoVRPlayer) string {
	if p.UserID != 0 {
		return fmt.Sprintf("echovr:%d", p.UserID)
	}
	// Fallback to name if UserID is missing (shouldn't happen)
	return fmt.Sprintf("name:%s", p.Name)
}

// isZeroVec checks if a raw [3]float64 is all zeros.
func isZeroVec(v [3]float64) bool {
	return v[0] == 0 && v[1] == 0 && v[2] == 0
}

// mapGamePhase converts Echo VR game_status to our internal game phase string.
func mapGamePhase(status string) string {
	switch strings.ToLower(status) {
	case "playing":
		return "playing"
	case "round_start":
		return "round_start"
	case "round_over":
		return "round_over"
	case "pre_match":
		return "pre_match"
	case "post_match":
		return "post_match"
	case "score":
		return "round_over" // "score" phase maps to round_over (non-active)
	case "":
		return "playing" // default to active if unknown
	default:
		return status // pass through unknown values
	}
}

// FieldMapping documents a single field mapping for audit purposes.
type FieldMapping struct {
	InternalField string
	EchoVRField   string
	Confidence    FieldConfidence
	DefaultValue  string
	Notes         string
}

// DocumentMappings returns the complete field mapping table for audit.
func DocumentMappings() []FieldMapping {
	return []FieldMapping{
		{"PlayerID", "userid / name", Confirmed, "name:<display_name>", "UserID preferred; falls back to name"},
		{"Team", "teams[].team", Confirmed, "(dropped)", "Exact BLUE/ORANGE labels; custom names use a validated three-slot layout ending in SPECTATORS; spectators and unresolved teams are excluded"},
		{"FrameIndex", "(sequential)", Inferred, "auto-increment", "0-based per Mapper and per match (NewMatch); ingest re-bases per match"},
		{"Timestamp", "(sample time)", Inferred, "0 on first sample", "Seconds since the first sample time supplied to MapSessionAt (replay line prefix / receive time); monotonic: small backward samples are clamped, clock steps re-based"},
		{"DeltaTime", "(computed)", Inferred, "0 on a player's first frame", "Timestamp minus the same player's previous Timestamp; 0 = unknown"},
		{"Position", "body.position", Confirmed, "rejected if zero", "Direct mapping [3]float64"},
		{"Rotation", "body.forward/left/up", Confirmed, "zero quat if degenerate", "QuatFromDirectionVectorsChecked; reflected bases are negated and counted"},
		{"LeftHandPosition", "lhand.pos", Confirmed, "[0,0,0]", "Direct mapping"},
		{"RightHandPosition", "rhand.pos", Confirmed, "[0,0,0]", "Direct mapping"},
		{"LeftHandRotation", "lhand.forward/left/up", Likely, "zero quat", "Converted from direction vectors; any zero vector → zero quaternion (tracking lost)"},
		{"RightHandRotation", "rhand.forward/left/up", Likely, "zero quat", "Same as left hand"},
		{"IsStunned", "stunned", Confirmed, "false", "Direct boolean mapping"},
		{"IsBoosting", "(absent)", Absent, "false", "Echo VR API does not expose boosting state"},
		{"ShieldActive", "blocking", Confirmed, "false", "Per-player blocking boolean"},
		{"IsImmune", "invulnerable", Confirmed, "false", "Post-respawn invulnerability"},
		{"HasPossession", "holding_left/right; possession fallback", Confirmed, "false", "Explicit hand-held item fields mark release; older sources fall back to the sticky possession boolean"},
		{"Disc.Position", "disc.position", Confirmed, "nil disc", "One DiscState per tick, identical on every player's frame"},
		{"Disc.Velocity", "disc.velocity", Confirmed, "nil disc", "Direct mapping; Speed = |velocity|"},
		{"Disc.IsHeld/PossessorID", "players[].holding_left/right; possession fallback", Inferred, "false / empty", "Holder = first mapped player whose hand explicitly holds the disc; older sources fall back to possession"},
		{"EstimatedPingMs", "ping", Confirmed, "0", "Per-player ping in ms; enables lag compensation"},
		{"GamePhase", "game_status", Confirmed, "playing", "Direct string mapping with normalization"},
		{"BlueScore", "blue_points", Confirmed, "0", "Direct mapping"},
		{"OrangeScore", "orange_points", Confirmed, "0", "Direct mapping"},
		{"Goals", "stats.goals", Confirmed, "0", "Per-player stat"},
		{"Stuns", "stats.stuns", Confirmed, "0", "Per-player stat"},
	}
}
