// Package evidence builds and formats review cases for moderator review.
package evidence

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Builder constructs ReviewCase objects from detection events.
type Builder struct{}

// NewBuilder creates a new evidence builder.
func NewBuilder() *Builder {
	return &Builder{}
}

// Build creates a ReviewCase for a flagged player.
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

	// Build triggered detectors list
	var triggered []model.TriggeredDetector
	for _, ev := range playerEvents {
		sevStr := "low"
		if ev.Severity >= 0.8 {
			sevStr = "critical"
		} else if ev.Severity >= 0.6 {
			sevStr = "high"
		} else if ev.Severity >= 0.3 {
			sevStr = "medium"
		}
		triggered = append(triggered, model.TriggeredDetector{
			DetectorID: ev.DetectorID,
			Severity:   sevStr,
			Confidence: ev.Confidence,
			Details:    ev.ObservedValue,
		})
	}

	// Determine severity and recommended action
	severity := "low"
	action := "review_only"
	if score.TotalScore >= 95 {
		severity = "critical"
		action = "temp_restrict"
	} else if score.TotalScore >= 80 {
		severity = "critical"
		action = "temp_restrict"
	} else if score.TotalScore >= 60 {
		severity = "high"
		action = "review_only"
	} else if score.TotalScore >= 40 {
		severity = "medium"
		action = "enhanced_monitoring"
	}

	// Build explanation
	explanation := fmt.Sprintf(
		"Player %s scored %.1f (level: %s). %d detection(s) from %d detector(s).",
		playerID, score.TotalScore, score.Level(), len(playerEvents), len(triggered),
	)

	now := time.Now()
	return model.ReviewCase{
		CaseID:             fmt.Sprintf("RC-%s-%s", now.Format("20060102"), uuid.New().String()[:8]),
		PlayerID:           playerID,
		MatchID:            matchCtx.MatchID,
		TimestampStart:     matchCtx.StartTime,
		TimestampEnd:       matchCtx.StartTime.Add(matchCtx.Duration),
		DetectorsTriggered: triggered,
		Severity:           severity,
		SuspicionScore:     score.TotalScore,
		RecommendedAction:  action,
		Explanation:        explanation,
		Status:             "pending",
		CreatedAt:          now,
	}
}
