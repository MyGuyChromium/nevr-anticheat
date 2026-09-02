// Package evidence builds and formats review cases for moderator review.
package evidence

import (
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Builder constructs ReviewCase objects from detection events.
type Builder struct {
	levels model.LevelTable
	names  map[string]string // detector ID -> human name
	now    func() time.Time
}

// NewBuilder creates a new evidence builder using the default level table.
func NewBuilder() *Builder {
	return NewBuilderWithLevels(model.DefaultLevelTable())
}

// NewBuilderWithLevels creates a builder that classifies with the given table.
func NewBuilderWithLevels(t model.LevelTable) *Builder {
	if t.IsZero() {
		t = model.DefaultLevelTable()
	}
	return &Builder{levels: t, names: map[string]string{}, now: time.Now}
}

// SetDetectorNames supplies human-readable detector names (ID -> Name) so
// cases do not read "[THROW_001] THROW_001". Unknown IDs fall back to the ID.
func (b *Builder) SetDetectorNames(names map[string]string) {
	b.names = make(map[string]string, len(names))
	for k, v := range names {
		b.names[k] = v
	}
}

// SetClock overrides the wall clock (tests).
func (b *Builder) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	b.now = now
}

// Levels returns the tier table the builder classifies with.
func (b *Builder) Levels() model.LevelTable { return b.levels }

// SetLevels replaces the tier table (zero tables are ignored).
func (b *Builder) SetLevels(t model.LevelTable) {
	if !t.IsZero() {
		b.levels = t
	}
}

// Build creates a ReviewCase for a flagged player. matchCtx may be nil, in
// which case match fields are taken from the events.
func (b *Builder) Build(
	playerID string,
	matchCtx *model.MatchContext,
	score model.SuspicionScore,
	events []model.DetectionEvent,
) model.ReviewCase {
	// Filter events to this player
	var playerEvents []model.DetectionEvent
	for _, ev := range events {
		if ev.PlayerID == playerID && !ev.IsShadow {
			playerEvents = append(playerEvents, ev)
		}
	}
	sort.SliceStable(playerEvents, func(i, j int) bool {
		if playerEvents[i].FrameIndex != playerEvents[j].FrameIndex {
			return playerEvents[i].FrameIndex < playerEvents[j].FrameIndex
		}
		return playerEvents[i].DetectorID < playerEvents[j].DetectorID
	})

	// Build triggered detectors list
	triggered := make([]model.TriggeredDetector, 0, len(playerEvents))
	distinct := make(map[string]bool)
	versions := make(map[string]string)
	maxTimestamp := 0.0
	for _, ev := range playerEvents {
		distinct[ev.DetectorID] = true
		versions[ev.DetectorID] = ev.DetectorVersion
		if ev.Timestamp > maxTimestamp {
			maxTimestamp = ev.Timestamp
		}
		name := b.names[ev.DetectorID]
		if name == "" {
			name = ev.DetectorID
		}
		triggered = append(triggered, model.TriggeredDetector{
			DetectorID:      ev.DetectorID,
			DetectorName:    name,
			DetectorVersion: ev.DetectorVersion,
			EventID:         ev.EventID,
			Severity:        model.EventSeverityLabel(ev.Severity),
			SeverityValue:   ev.Severity,
			Confidence:      ev.Confidence,
			FrameIndex:      ev.FrameIndex,
			FrameRangeStart: ev.FrameRangeStart,
			FrameRangeEnd:   ev.FrameRangeEnd,
			Timestamp:       ev.Timestamp,
			MetricName:      ev.CausalKey.AnomalyType,
			ExpectedRange:   ev.ExpectedRange,
			Details:         ev.ObservedValue,
		})
	}

	level := b.levels.LevelFor(score.TotalScore)

	matchID := ""
	var start, end time.Time
	if matchCtx != nil {
		matchID = matchCtx.MatchID
		start = matchCtx.StartTime
		if matchCtx.Duration > 0 {
			end = start.Add(matchCtx.Duration)
		} else if !start.IsZero() {
			end = start.Add(time.Duration(maxTimestamp * float64(time.Second)))
		}
	} else if len(playerEvents) > 0 {
		matchID = playerEvents[0].MatchID
	}

	explanation := fmt.Sprintf(
		"Player %s scored %.1f (level: %s). %d detection(s) from %d distinct detector(s).",
		playerID, score.TotalScore, level, len(playerEvents), len(distinct),
	)

	now := b.now()
	return model.ReviewCase{
		CaseID:             fmt.Sprintf("RC-%s-%s", now.UTC().Format("20060102"), uuid.New().String()),
		PlayerID:           playerID,
		MatchID:            matchID,
		TimestampStart:     start,
		TimestampEnd:       end,
		DetectorsTriggered: triggered,
		Severity:           model.CaseSeverityForLevel(level),
		SuspicionScore:     score.TotalScore,
		Level:              string(level),
		RecommendedAction:  model.RecommendedActionForLevel(level),
		Explanation:        explanation,
		Status:             model.CaseStatusPending,
		CreatedAt:          now,
		ThresholdVersion:   ThresholdVersion(b.levels, versions),
	}
}

// ThresholdVersion returns a short stable fingerprint of a level table plus
// the detector versions involved, so cases built under different thresholds
// can be distinguished during calibration.
func ThresholdVersion(levels model.LevelTable, detectorVersions map[string]string) string {
	if levels.IsZero() {
		levels = model.DefaultLevelTable()
	}
	ids := make([]string, 0, len(detectorVersions))
	for id := range detectorVersions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := fnv.New64a()
	fmt.Fprintf(h, "levels:%g/%g/%g/%g/%g;", levels.Informational, levels.Suspicious, levels.HighRisk, levels.Critical, levels.ActionWorthy)
	for _, id := range ids {
		fmt.Fprintf(h, "%s=%s;", id, detectorVersions[id])
	}
	return fmt.Sprintf("tv-%016x", h.Sum64())
}
