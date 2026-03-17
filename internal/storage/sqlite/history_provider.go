package sqlite

import (
	"context"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// StoreHistoryProvider adapts the Store to the pattern.HistoryProvider interface,
// enabling PAT_003 (Cross-Match Consistency) to query historical detection events
// from the database rather than requiring in-memory state.
type StoreHistoryProvider struct {
	store *Store
}

// NewStoreHistoryProvider creates a HistoryProvider backed by SQLite.
func NewStoreHistoryProvider(store *Store) *StoreHistoryProvider {
	return &StoreHistoryProvider{store: store}
}

// GetPlayerDetections returns up to matchLimit recent matches' worth of
// detection events for a player.
func (hp *StoreHistoryProvider) GetPlayerDetections(playerID string, matchLimit int) ([]model.DetectionEvent, error) {
	// Query enough events to cover matchLimit matches.
	// Each match may have multiple events; fetch a generous limit and trim.
	events, err := hp.store.GetPlayerEvents(context.Background(), playerID, matchLimit*50, 0)
	if err != nil {
		return nil, err
	}

	// Trim to events from at most matchLimit distinct matches.
	seen := make(map[string]bool)
	var result []model.DetectionEvent
	for _, ev := range events {
		if ev.MatchID == "" {
			continue
		}
		seen[ev.MatchID] = true
		if len(seen) > matchLimit {
			break
		}
		result = append(result, ev)
	}
	return result, nil
}
