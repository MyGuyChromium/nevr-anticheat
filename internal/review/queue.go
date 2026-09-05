// Package review manages the moderator review queue: creating single-match
// review cases from pipeline results, assigning them, and recording
// moderator decisions so per-detector precision can be measured.
//
// BuildCasesFromResult is the one mechanism that turns a match result into
// single-match review cases. CreateCasesFromResult stores those cases for the
// live path; replay stores them with events and scores in one transaction.
// Both use the scorer's level table and deterministic ids
// (CaseID: "RC-<match>-<player>", refreshed on reprocess).
// The lifecycle methods (Assign, Start, Decide) require a store implementing
// LifecycleStore (*sqlite.Store does); a store without them returns
// ErrLifecycleUnsupported rather than silently doing nothing.
package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/nevr-anticheat/nevr-anticheat/internal/evidence"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

// CaseStore is the minimal persistence surface for creating and reading
// cases. *sqlite.Store satisfies it.
type CaseStore interface {
	StoreReviewCase(ctx context.Context, rc model.ReviewCase) error
	GetReviewCase(ctx context.Context, caseID string) (model.ReviewCase, error)
	GetPendingReviewCases(ctx context.Context, limit int) ([]model.ReviewCase, error)
}

// LifecycleStore is the persistence surface for the moderator loop. The
// sqlite store must provide:
//
//	UpdateReviewCaseStatus(ctx, caseID, status, assignedTo string) error
//	    UPDATE review_cases SET status=?, assigned_to=?, updated_at=<UTC RFC3339> WHERE case_id=?
//	    (error when no row matched)
//	StoreModeratorDecision(ctx, model.ModeratorDecision) error
//	    INSERT INTO moderator_decisions (decision_id, case_id, moderator_id, verdict,
//	    action_taken, notes, decided_at, detector_feedback) — detector_feedback is the
//	    JSON-encoded DetectorFeedback slice.
type LifecycleStore interface {
	UpdateReviewCaseStatus(ctx context.Context, caseID, status, assignedTo string) error
	StoreModeratorDecision(ctx context.Context, decision model.ModeratorDecision) error
}

// ErrLifecycleUnsupported is returned by Assign/Start/Decide when the
// configured store does not implement LifecycleStore.
var ErrLifecycleUnsupported = errors.New("review: store does not support case lifecycle updates")

// ErrInvalidTransition is returned when a status change is not allowed.
var ErrInvalidTransition = errors.New("review: invalid case status transition")

// Queue manages the moderator review pipeline.
type Queue struct {
	store   CaseStore
	builder *evidence.Builder
	logger  *slog.Logger
	now     func() time.Time
}

// NewQueue creates a new review queue classifying with the default level
// table. Use SetLevels to align it with the scorer.
func NewQueue(store CaseStore, logger *slog.Logger) *Queue {
	if logger == nil {
		logger = slog.Default()
	}
	return &Queue{
		store:   store,
		builder: evidence.NewBuilder(),
		logger:  logger,
		now:     time.Now,
	}
}

// SetLevels sets the tier table used to classify cases.
func (q *Queue) SetLevels(t model.LevelTable) {
	q.builder.SetLevels(t)
}

// SetDetectorNames supplies human-readable detector names for case evidence.
func (q *Queue) SetDetectorNames(names map[string]string) {
	q.builder.SetDetectorNames(names)
}

// SetClock overrides the wall clock (tests).
func (q *Queue) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	q.now = now
	q.builder.SetClock(now)
}

// Builder exposes the evidence builder for callers that want to inspect cases
// before storing them.
func (q *Queue) Builder() *evidence.Builder { return q.builder }

// CaseID is the deterministic id of the single-match review case for
// (match, player): "RC-<match>-<player>". Re-analysis of the same match
// refreshes the existing case through the store's upsert instead of creating
// a second one, and the CLI `report`/`verdict` commands address cases by it.
func CaseID(matchID, playerID string) string {
	return fmt.Sprintf("RC-%s-%s", matchID, playerID)
}

// Enqueue creates and stores a review case for a flagged player and returns it.
// With a match context the case gets the deterministic CaseID for the
// (match, player) pair; without one the builder's random id is kept.
func (q *Queue) Enqueue(
	ctx context.Context,
	playerID string,
	matchCtx *model.MatchContext,
	score model.SuspicionScore,
	events []model.DetectionEvent,
) (model.ReviewCase, error) {
	rc := q.builder.Build(playerID, matchCtx, score, events)
	if matchCtx != nil && matchCtx.MatchID != "" {
		rc.CaseID = CaseID(matchCtx.MatchID, playerID)
	}
	if err := q.store.StoreReviewCase(ctx, rc); err != nil {
		return rc, err
	}
	q.logCreated(rc)
	return rc, nil
}

func (q *Queue) logCreated(rc model.ReviewCase) {
	q.logger.Info("review case created",
		"case_id", rc.CaseID,
		"player_id", rc.PlayerID,
		"match_id", rc.MatchID,
		"score", rc.SuspicionScore,
		"level", rc.Level,
		"severity", rc.Severity,
		"detections", len(rc.DetectorsTriggered),
	)
}

// GetPending returns pending review cases.
func (q *Queue) GetPending(ctx context.Context, limit int) ([]model.ReviewCase, error) {
	return q.store.GetPendingReviewCases(ctx, limit)
}

// GetCase retrieves a specific review case.
func (q *Queue) GetCase(ctx context.Context, caseID string) (model.ReviewCase, error) {
	return q.store.GetReviewCase(ctx, caseID)
}

// ValidTransition reports whether a case may move from one status to another.
func ValidTransition(from, to string) bool {
	switch from {
	case model.CaseStatusPending:
		return to == model.CaseStatusAssigned || to == model.CaseStatusInReview || to == model.CaseStatusDecided || to == model.CaseStatusClosed
	case model.CaseStatusAssigned:
		return to == model.CaseStatusInReview || to == model.CaseStatusDecided || to == model.CaseStatusPending || to == model.CaseStatusClosed
	case model.CaseStatusInReview:
		return to == model.CaseStatusDecided || to == model.CaseStatusAssigned || to == model.CaseStatusClosed
	case model.CaseStatusDecided:
		return to == model.CaseStatusAppealed || to == model.CaseStatusClosed
	case model.CaseStatusAppealed:
		return to == model.CaseStatusDecided || to == model.CaseStatusClosed
	default:
		return false
	}
}

func (q *Queue) lifecycle() (LifecycleStore, bool) {
	ls, ok := q.store.(LifecycleStore)
	return ls, ok
}

func (q *Queue) transition(ctx context.Context, caseID, to, assignedTo string) (model.ReviewCase, error) {
	ls, ok := q.lifecycle()
	if !ok {
		return model.ReviewCase{}, ErrLifecycleUnsupported
	}
	rc, err := q.store.GetReviewCase(ctx, caseID)
	if err != nil {
		return rc, fmt.Errorf("review: loading case %s: %w", caseID, err)
	}
	if !ValidTransition(rc.Status, to) {
		return rc, fmt.Errorf("%w: %s -> %s (case %s)", ErrInvalidTransition, rc.Status, to, caseID)
	}
	if assignedTo == "" {
		assignedTo = rc.AssignedTo
	}
	if err := ls.UpdateReviewCaseStatus(ctx, caseID, to, assignedTo); err != nil {
		return rc, err
	}
	rc.Status = to
	rc.AssignedTo = assignedTo
	rc.UpdatedAt = q.now()
	return rc, nil
}

// Assign moves a case to "assigned" for the given moderator.
func (q *Queue) Assign(ctx context.Context, caseID, moderatorID string) (model.ReviewCase, error) {
	if moderatorID == "" {
		return model.ReviewCase{}, errors.New("review: moderator id required")
	}
	return q.transition(ctx, caseID, model.CaseStatusAssigned, moderatorID)
}

// Start moves a case to "in_review".
func (q *Queue) Start(ctx context.Context, caseID, moderatorID string) (model.ReviewCase, error) {
	return q.transition(ctx, caseID, model.CaseStatusInReview, moderatorID)
}

// Decide records a moderator decision and marks the case decided. Missing
// DecisionID/DecidedAt are filled in.
func (q *Queue) Decide(ctx context.Context, decision model.ModeratorDecision) (model.ReviewCase, error) {
	if err := decision.Validate(); err != nil {
		return model.ReviewCase{}, err
	}
	ls, ok := q.lifecycle()
	if !ok {
		return model.ReviewCase{}, ErrLifecycleUnsupported
	}
	rc, err := q.store.GetReviewCase(ctx, decision.CaseID)
	if err != nil {
		return rc, fmt.Errorf("review: loading case %s: %w", decision.CaseID, err)
	}
	if !ValidTransition(rc.Status, model.CaseStatusDecided) {
		return rc, fmt.Errorf("%w: %s -> %s (case %s)", ErrInvalidTransition, rc.Status, model.CaseStatusDecided, rc.CaseID)
	}
	if decision.DecisionID == "" {
		decision.DecisionID = uuid.New().String()
	}
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = q.now()
	}
	if err := ls.StoreModeratorDecision(ctx, decision); err != nil {
		return rc, fmt.Errorf("review: storing decision: %w", err)
	}
	assigned := rc.AssignedTo
	if assigned == "" {
		assigned = decision.ModeratorID
	}
	if err := ls.UpdateReviewCaseStatus(ctx, rc.CaseID, model.CaseStatusDecided, assigned); err != nil {
		return rc, err
	}
	rc.Status = model.CaseStatusDecided
	rc.AssignedTo = assigned
	rc.UpdatedAt = q.now()
	q.logger.Info("review case decided",
		"case_id", rc.CaseID, "moderator", decision.ModeratorID, "verdict", decision.Verdict,
		"detector_feedback", len(decision.DetectorFeedback))
	return rc, nil
}

// Option customises CreateCasesFromResult.
type Option func(*options)

type options struct {
	names    map[string]string
	logger   *slog.Logger
	minLevel model.ScoringLevel
	now      func() time.Time
}

// WithDetectorNames supplies detector ID -> name for case evidence.
func WithDetectorNames(names map[string]string) Option {
	return func(o *options) { o.names = names }
}

// WithLogger sets the logger used for case creation.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// WithMinLevel changes the tier at which a case is created (default high_risk).
func WithMinLevel(l model.ScoringLevel) Option {
	return func(o *options) { o.minLevel = l }
}

// WithClock overrides the wall clock (tests).
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

func configuredQueue(store CaseStore, table model.LevelTable, opts ...Option) *Queue {
	o := options{minLevel: model.LevelHighRisk, logger: slog.Default()}
	for _, opt := range opts {
		opt(&o)
	}
	if table.IsZero() {
		table = model.DefaultLevelTable()
	}
	q := NewQueue(store, o.logger)
	q.SetLevels(table)
	if o.names != nil {
		q.SetDetectorNames(o.names)
	}
	if o.now != nil {
		q.SetClock(o.now)
	}
	return q
}

// BuildCasesFromResult builds, but does not store, one review case per player
// at or above the configured review level with at least one non-shadow event.
// This pure phase lets SQLite persist events, scores and cases atomically.
func BuildCasesFromResult(
	matchCtx *model.MatchContext,
	result *pipeline.MatchResult,
	table model.LevelTable,
	opts ...Option,
) []model.ReviewCase {
	if result == nil {
		return nil
	}
	o := options{minLevel: model.LevelHighRisk, logger: slog.Default()}
	for _, opt := range opts {
		opt(&o)
	}
	if table.IsZero() {
		table = model.DefaultLevelTable()
	}

	q := configuredQueue(nil, table, opts...)

	// Non-shadow events per player.
	byPlayer := make(map[string][]model.DetectionEvent)
	for _, ev := range result.DetectionEvents {
		if ev.IsShadow {
			continue
		}
		byPlayer[ev.PlayerID] = append(byPlayer[ev.PlayerID], ev)
	}

	ids := make([]string, 0, len(result.PlayerScores))
	for pid := range result.PlayerScores {
		ids = append(ids, pid)
	}
	sort.Strings(ids)

	var cases []model.ReviewCase
	for _, pid := range ids {
		score := result.PlayerScores[pid]
		if len(byPlayer[pid]) == 0 {
			continue
		}
		if !table.LevelFor(score.TotalScore).AtLeast(o.minLevel) {
			continue
		}
		rc := q.builder.Build(pid, matchCtx, score, byPlayer[pid])
		if matchCtx != nil && matchCtx.MatchID != "" {
			rc.CaseID = CaseID(matchCtx.MatchID, pid)
		}
		cases = append(cases, rc)
	}
	return cases
}

// CreateCasesFromResult builds, stores and returns one review case per player.
// Store failures do not stop processing; all errors are joined and returned
// alongside the cases that were successfully stored.
func CreateCasesFromResult(
	ctx context.Context,
	store CaseStore,
	matchCtx *model.MatchContext,
	result *pipeline.MatchResult,
	table model.LevelTable,
	opts ...Option,
) ([]model.ReviewCase, error) {
	q := configuredQueue(store, table, opts...)
	candidates := BuildCasesFromResult(matchCtx, result, table, opts...)
	if candidates == nil {
		return nil, nil
	}
	cases := make([]model.ReviewCase, 0, len(candidates))
	var errs []error
	for _, rc := range candidates {
		if err := store.StoreReviewCase(ctx, rc); err != nil {
			errs = append(errs, fmt.Errorf("review: storing case for player %s: %w", rc.PlayerID, err))
			continue
		}
		q.logCreated(rc)
		cases = append(cases, rc)
	}
	return cases, errors.Join(errs...)
}
