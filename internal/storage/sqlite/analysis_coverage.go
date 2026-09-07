package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// GetMatchAnalysisCoverage returns nil for legacy analyses. It deliberately
// does not recompute from today's config: those are not the detectors that
// produced the stored events. Re-analysis refreshes both together.
func (s *Store) GetMatchAnalysisCoverage(ctx context.Context, matchID string) (map[string]*model.PlayerCoverage, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, `SELECT coverage_json FROM match_analysis_coverage WHERE match_id = ?`, matchID).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var coverage map[string]*model.PlayerCoverage
	if err := json.Unmarshal([]byte(doc), &coverage); err != nil {
		return nil, fmt.Errorf("decode analysis coverage: %w", err)
	}
	return coverage, nil
}
