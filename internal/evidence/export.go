package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// ReplayBundle is an exportable JSON bundle containing frames around a detection event.
type ReplayBundle struct {
	ExportID        string                       `json:"export_id"`
	ExportedAt      time.Time                    `json:"exported_at"`
	CaseID          string                       `json:"case_id,omitempty"`
	MatchID         string                       `json:"match_id"`
	PlayerID        string                       `json:"player_id"`
	DetectionEvents []model.DetectionEvent        `json:"detection_events"`
	FramesBefore    []model.PlayerTelemetryFrame `json:"frames_before"`
	FramesAfter     []model.PlayerTelemetryFrame `json:"frames_after"`
	EventFrames     []model.PlayerTelemetryFrame `json:"event_frames"`
	PlayerState     *model.PlayerState           `json:"player_state,omitempty"`
	MatchContext    *model.MatchContext           `json:"match_context,omitempty"`
	SuspicionScore  *model.SuspicionScore        `json:"suspicion_score,omitempty"`
	Metadata        map[string]string            `json:"metadata,omitempty"`
}

// Exporter creates replay bundles for moderator review.
type Exporter struct {
	store        *sqlite.Store
	framesBefore int // frames to include before event (default 45 = ~3 sec)
	framesAfter  int // frames to include after event (default 45)
}

// NewExporter creates a replay exporter.
func NewExporter(store *sqlite.Store) *Exporter {
	return &Exporter{
		store:        store,
		framesBefore: 45,
		framesAfter:  45,
	}
}

// ExportForCase creates a replay bundle for a review case.
func (e *Exporter) ExportForCase(
	ctx context.Context,
	caseID string,
	allFrames []model.PlayerTelemetryFrame,
) (*ReplayBundle, error) {
	rc, err := e.store.GetReviewCase(ctx, caseID)
	if err != nil {
		return nil, fmt.Errorf("getting review case: %w", err)
	}

	events, err := e.store.GetPlayerEvents(ctx, rc.PlayerID, 100, 0)
	if err != nil {
		return nil, fmt.Errorf("getting events: %w", err)
	}

	// Filter events to this match
	var matchEvents []model.DetectionEvent
	for _, ev := range events {
		if ev.MatchID == rc.MatchID {
			matchEvents = append(matchEvents, ev)
		}
	}

	// Find frame ranges around events
	minFrame := int(^uint(0) >> 1) // max int
	maxFrame := 0
	for _, ev := range matchEvents {
		if ev.FrameRangeStart < minFrame {
			minFrame = ev.FrameRangeStart
		}
		if ev.FrameRangeEnd > maxFrame {
			maxFrame = ev.FrameRangeEnd
		}
	}

	// Expand window
	startFrame := minFrame - e.framesBefore
	if startFrame < 0 {
		startFrame = 0
	}
	endFrame := maxFrame + e.framesAfter

	// Extract relevant frames
	var before, during, after []model.PlayerTelemetryFrame
	for _, f := range allFrames {
		if f.PlayerID != rc.PlayerID {
			continue
		}
		switch {
		case f.FrameIndex >= startFrame && f.FrameIndex < minFrame:
			before = append(before, f)
		case f.FrameIndex >= minFrame && f.FrameIndex <= maxFrame:
			during = append(during, f)
		case f.FrameIndex > maxFrame && f.FrameIndex <= endFrame:
			after = append(after, f)
		}
	}

	score, _ := e.store.GetPlayerScore(ctx, rc.PlayerID)

	bundle := &ReplayBundle{
		ExportID:        fmt.Sprintf("EXP-%s", caseID),
		ExportedAt:      time.Now(),
		CaseID:          caseID,
		MatchID:         rc.MatchID,
		PlayerID:        rc.PlayerID,
		DetectionEvents: matchEvents,
		FramesBefore:    before,
		EventFrames:     during,
		FramesAfter:     after,
		SuspicionScore:  &score,
		Metadata: map[string]string{
			"severity":           rc.Severity,
			"recommended_action": rc.RecommendedAction,
			"frame_window":       fmt.Sprintf("%d-%d", startFrame, endFrame),
		},
	}

	return bundle, nil
}

// MarshalBundle serializes a replay bundle to JSON.
func MarshalBundle(bundle *ReplayBundle) ([]byte, error) {
	return json.MarshalIndent(bundle, "", "  ")
}
