package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// StoreMatchViewCache replaces the cached derived document of one kind for a
// match. key records the state the document was built from (for example the
// latest analysis run and the config fingerprint); readers compare it.
func (s *Store) StoreMatchViewCache(ctx context.Context, matchID, kind, key string, doc []byte) error {
	if matchID == "" || kind == "" {
		return fmt.Errorf("view cache: match id and kind are required")
	}
	if len(doc) == 0 || !json.Valid(doc) {
		return fmt.Errorf("view cache %s of match %s: document is not valid JSON", kind, matchID)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO match_view_cache (match_id, kind, cache_key, doc_json, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(match_id, kind) DO UPDATE SET cache_key = excluded.cache_key,
			doc_json = excluded.doc_json, created_at = excluded.created_at`,
		matchID, kind, key, string(doc), fmtDBTime(nowUTC()))
	return err
}

// GetMatchViewCache returns the cached document of one kind and the key it
// was built under, or ErrNotFound.
func (s *Store) GetMatchViewCache(ctx context.Context, matchID, kind string) (key string, doc []byte, err error) {
	var raw string
	err = s.db.QueryRowContext(ctx,
		`SELECT cache_key, doc_json FROM match_view_cache WHERE match_id = ? AND kind = ?`, matchID, kind).Scan(&key, &raw)
	if err == sql.ErrNoRows {
		return "", nil, fmt.Errorf("view cache %s of match %s: %w", kind, matchID, ErrNotFound)
	}
	if err != nil {
		return "", nil, err
	}
	return key, []byte(raw), nil
}
