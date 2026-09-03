package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Errors returned by Exporter.
var (
	ErrNoEvents = errors.New("evidence: review case has no non-shadow detection events for its match")
	ErrNoFrames = errors.New("evidence: no telemetry frames available for the case window")
)

// ExportStore is the persistence surface the exporter reads from.
// *sqlite.Store satisfies it.
type ExportStore interface {
	GetReviewCase(ctx context.Context, caseID string) (model.ReviewCase, error)
	GetMatchEvents(ctx context.Context, matchID string) ([]model.DetectionEvent, error)
	GetMatchFrames(ctx context.Context, matchID string) ([]model.PlayerTelemetryFrame, error)
	GetPlayerScore(ctx context.Context, playerID string) (model.SuspicionScore, error)
}

// MatchContextStore is optionally implemented by an ExportStore (as by
// *sqlite.Store). When available, the exporter attaches the stored match
// context to the bundle so it carries the match's provenance (server_id,
// source, map/mode) alongside the clips.
type MatchContextStore interface {
	GetMatchContext(ctx context.Context, matchID string) (*model.MatchContext, error)
}

// EventClip is the telemetry window around one detection event. It contains
// frames for EVERY player in the match (so a reviewer can see the disc and
// opponents), sorted by frame index then player ID.
type EventClip struct {
	EventID     string                       `json:"event_id"`
	DetectorID  string                       `json:"detector_id"`
	FrameIndex  int                          `json:"frame_index"`
	Timestamp   float64                      `json:"timestamp"`
	WindowStart int                          `json:"window_start"`
	WindowEnd   int                          `json:"window_end"`
	Frames      []model.PlayerTelemetryFrame `json:"frames"`
}

// ReplayBundle is an exportable JSON bundle containing frames around each
// detection event of a review case.
type ReplayBundle struct {
	ExportID        string                 `json:"export_id"`
	ExportedAt      time.Time              `json:"exported_at"`
	CaseID          string                 `json:"case_id,omitempty"`
	MatchID         string                 `json:"match_id"`
	PlayerID        string                 `json:"player_id"`
	DetectionEvents []model.DetectionEvent `json:"detection_events"`
	// Clips holds one +/-N frame window per event (all players).
	Clips []EventClip `json:"clips"`
	// Frames is the de-duplicated union of all clip frames, for consumers
	// that want a single timeline.
	Frames         []model.PlayerTelemetryFrame `json:"frames"`
	PlayerState    *model.PlayerState           `json:"player_state,omitempty"`
	MatchContext   *model.MatchContext          `json:"match_context,omitempty"`
	SuspicionScore *model.SuspicionScore        `json:"suspicion_score,omitempty"`
	Metadata       map[string]string            `json:"metadata,omitempty"`
}

// Exporter creates replay bundles for moderator review.
type Exporter struct {
	store        ExportStore
	framesBefore int // frames to include before each event (default 45 = ~3 sec at 15 fps)
	framesAfter  int // frames to include after each event (default 45)
	now          func() time.Time
}

// NewExporter creates a replay exporter with a +/-45 frame window.
func NewExporter(store ExportStore) *Exporter {
	return &Exporter{store: store, framesBefore: 45, framesAfter: 45, now: time.Now}
}

// SetWindow changes the number of frames kept before and after each event.
func (e *Exporter) SetWindow(before, after int) {
	if before >= 0 {
		e.framesBefore = before
	}
	if after >= 0 {
		e.framesAfter = after
	}
}

// SetClock overrides the wall clock (tests).
func (e *Exporter) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	e.now = now
}

// ExportForCase creates a replay bundle for a review case. frames may be nil,
// in which case the match's frames are loaded from the store. The bundle
// contains one clip per non-shadow event of the case's player in the case's
// match; it is an error for the case to have no such events or for the
// window to contain no frames.
func (e *Exporter) ExportForCase(
	ctx context.Context,
	caseID string,
	frames []model.PlayerTelemetryFrame,
) (*ReplayBundle, error) {
	rc, err := e.store.GetReviewCase(ctx, caseID)
	if err != nil {
		return nil, fmt.Errorf("getting review case: %w", err)
	}
	if rc.MatchID == "" {
		return nil, fmt.Errorf("evidence: review case %s has no match_id", caseID)
	}

	all, err := e.store.GetMatchEvents(ctx, rc.MatchID)
	if err != nil {
		return nil, fmt.Errorf("getting match events: %w", err)
	}
	var events []model.DetectionEvent
	for _, ev := range all {
		if ev.PlayerID == rc.PlayerID && !ev.IsShadow {
			events = append(events, ev)
		}
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("%w (case %s, player %s, match %s)", ErrNoEvents, caseID, rc.PlayerID, rc.MatchID)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].FrameIndex != events[j].FrameIndex {
			return events[i].FrameIndex < events[j].FrameIndex
		}
		return events[i].DetectorID < events[j].DetectorID
	})

	if frames == nil {
		frames, err = e.store.GetMatchFrames(ctx, rc.MatchID)
		if err != nil {
			return nil, fmt.Errorf("getting match frames: %w", err)
		}
	}
	sorted := make([]model.PlayerTelemetryFrame, len(frames))
	copy(sorted, frames)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].FrameIndex != sorted[j].FrameIndex {
			return sorted[i].FrameIndex < sorted[j].FrameIndex
		}
		return sorted[i].PlayerID < sorted[j].PlayerID
	})

	clips := make([]EventClip, 0, len(events))
	type frameKey struct {
		idx int
		pid string
	}
	union := make(map[frameKey]model.PlayerTelemetryFrame)
	for _, ev := range events {
		lo, hi := ev.FrameRangeStart, ev.FrameRangeEnd
		if hi < lo {
			lo, hi = ev.FrameIndex, ev.FrameIndex
		}
		start := lo - e.framesBefore
		if start < 0 {
			start = 0
		}
		end := hi + e.framesAfter
		clip := EventClip{
			EventID:     ev.EventID,
			DetectorID:  ev.DetectorID,
			FrameIndex:  ev.FrameIndex,
			Timestamp:   ev.Timestamp,
			WindowStart: start,
			WindowEnd:   end,
		}
		// sorted is ordered by FrameIndex; find the first frame >= start.
		i := sort.Search(len(sorted), func(k int) bool { return sorted[k].FrameIndex >= start })
		for ; i < len(sorted) && sorted[i].FrameIndex <= end; i++ {
			clip.Frames = append(clip.Frames, sorted[i])
			union[frameKey{sorted[i].FrameIndex, sorted[i].PlayerID}] = sorted[i]
		}
		clips = append(clips, clip)
	}
	if len(union) == 0 {
		return nil, fmt.Errorf("%w (case %s, match %s, %d events)", ErrNoFrames, caseID, rc.MatchID, len(events))
	}
	// The bundle's frame window is the actual span of all clips, not the
	// first clip's start and the last clip's end: clips are in event order,
	// and an event with a long frame range can extend past later events.
	windowStart, windowEnd := clips[0].WindowStart, clips[0].WindowEnd
	for _, c := range clips[1:] {
		if c.WindowStart < windowStart {
			windowStart = c.WindowStart
		}
		if c.WindowEnd > windowEnd {
			windowEnd = c.WindowEnd
		}
	}
	unionFrames := make([]model.PlayerTelemetryFrame, 0, len(union))
	for _, f := range union {
		unionFrames = append(unionFrames, f)
	}
	sort.SliceStable(unionFrames, func(i, j int) bool {
		if unionFrames[i].FrameIndex != unionFrames[j].FrameIndex {
			return unionFrames[i].FrameIndex < unionFrames[j].FrameIndex
		}
		return unionFrames[i].PlayerID < unionFrames[j].PlayerID
	})

	bundle := &ReplayBundle{
		ExportID:        fmt.Sprintf("EXP-%s", caseID),
		ExportedAt:      e.now(),
		CaseID:          caseID,
		MatchID:         rc.MatchID,
		PlayerID:        rc.PlayerID,
		DetectionEvents: events,
		Clips:           clips,
		Frames:          unionFrames,
		Metadata: map[string]string{
			"severity":           rc.Severity,
			"recommended_action": rc.RecommendedAction,
			"level":              rc.Level,
			"threshold_version":  rc.ThresholdVersion,
			"frame_window":       fmt.Sprintf("%d-%d", windowStart, windowEnd),
			"clip_count":         fmt.Sprintf("%d", len(clips)),
			"frames_before":      fmt.Sprintf("%d", e.framesBefore),
			"frames_after":       fmt.Sprintf("%d", e.framesAfter),
		},
	}

	if mcs, ok := e.store.(MatchContextStore); ok {
		if mc, err := mcs.GetMatchContext(ctx, rc.MatchID); err == nil && mc != nil {
			bundle.MatchContext = mc
			if sid := MatchServerID(mc); sid != "" {
				bundle.Metadata["server_id"] = sid
			}
		}
	}

	// The stored score is a display-only snapshot; omit it rather than embed
	// a zero value when none exists.
	if score, err := e.store.GetPlayerScore(ctx, rc.PlayerID); err == nil {
		bundle.SuspicionScore = &score
	} else {
		bundle.Metadata["suspicion_score"] = "unavailable"
	}

	return bundle, nil
}

// MarshalBundle serializes a replay bundle to JSON.
func MarshalBundle(bundle *ReplayBundle) ([]byte, error) {
	return json.MarshalIndent(bundle, "", "  ")
}
