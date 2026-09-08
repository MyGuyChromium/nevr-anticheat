package sqlite

import (
	"context"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// StoreHistoryProvider adapts the Store to the pattern.HistoryProvider interface,
// enabling PAT_003 (Cross-Match Consistency) to query historical detection events
// from the database rather than requiring in-memory state.
//
// History is non-shadow events only, excluding the meta-detectors PAT_003 and
// PAT_004 so a meta-detector never feeds on its own (or another meta-detector's)
// output. Cross-match aggregation shares this independent-evidence filter;
// latest human-invalidated and zero-contribution observations are excluded.
// The limit is match-aware: the most recent matchLimit matches are selected
// first and every event of those matches is returned, so a match is never cut
// in half by a row cap.
type StoreHistoryProvider struct {
	store *Store
}

// NewStoreHistoryProvider creates a HistoryProvider backed by SQLite.
func NewStoreHistoryProvider(store *Store) *StoreHistoryProvider {
	return &StoreHistoryProvider{store: store}
}

// GetPlayerDetections returns the player's non-shadow, non-meta detection
// events from their matchLimit most recent matches.
func (hp *StoreHistoryProvider) GetPlayerDetections(playerID string, matchLimit int) ([]model.DetectionEvent, error) {
	return hp.store.GetPlayerHistoryEvents(context.Background(), playerID, matchLimit)
}
