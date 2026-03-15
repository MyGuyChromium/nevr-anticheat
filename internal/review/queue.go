// Package review manages the moderator review queue.
package review

import (
	"context"
	"log/slog"

	"github.com/nevr-anticheat/nevr-anticheat/internal/evidence"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// Queue manages the moderator review pipeline.
type Queue struct {
	store   *sqlite.Store
	builder *evidence.Builder
	logger  *slog.Logger
}

// NewQueue creates a new review queue.
func NewQueue(store *sqlite.Store, logger *slog.Logger) *Queue {
	return &Queue{
		store:   store,
		builder: evidence.NewBuilder(),
		logger:  logger,
	}
}

// Enqueue creates a review case for a flagged player.
func (q *Queue) Enqueue(
	ctx context.Context,
	playerID string,
	matchCtx *model.MatchContext,
	score model.SuspicionScore,
	events []model.DetectionEvent,
) error {
	rc := q.builder.Build(playerID, matchCtx, score, events)
	if err := q.store.StoreReviewCase(ctx, rc); err != nil {
		return err
	}
	q.logger.Info("review case created",
		"case_id", rc.CaseID,
		"player_id", rc.PlayerID,
		"score", rc.SuspicionScore,
		"severity", rc.Severity,
	)
	return nil
}

// GetPending returns pending review cases.
func (q *Queue) GetPending(ctx context.Context, limit int) ([]model.ReviewCase, error) {
	return q.store.GetPendingReviewCases(ctx, limit)
}

// GetCase retrieves a specific review case.
func (q *Queue) GetCase(ctx context.Context, caseID string) (model.ReviewCase, error) {
	return q.store.GetReviewCase(ctx, caseID)
}
