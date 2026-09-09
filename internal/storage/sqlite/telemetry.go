package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// TelemetryFrameRow pairs a normalized frame with optional raw profiler JSON.
//
// Storage contract:
//
//	telemetry_frames.frame_json: Always populated. Contains the normalized
//	    PlayerTelemetryFrame as JSON, one row per (match, player, frame_index).
//	    This is what the detection pipeline consumes during reprocessing.
//
//	match_ticks.raw_json: The original profiler/API session payload, or for
//	    native tape a derived session projection plus the original protobuf
//	    header/frame bytes in its reserved _nevr_tape wrapper,
//	    stored ONCE per (match_id, frame_index) — never once per player row.
//	    Populated when the source provides richer data than the normalized frame
//	    (.echoreplay ingestion and current bridge live ingestion:
//	    EchoVRSessionResponse JSON with stats, goal events, player level, ...).
//	    Legacy JSON and older/custom WebSocket producers may omit it. Absent
//	    means "not available from this source", never "bug / not wired".
//
// RawJSON on a row is the raw payload for that row's frame_index; rows of the
// same tick may all carry it (it is de-duplicated on write) or only one may.
type TelemetryFrameRow struct {
	Frame   model.PlayerTelemetryFrame
	RawJSON string // source JSON or native protobuf wrapper for this tick, empty if unavailable
}

// TelemetryStoreResult reports what a telemetry write actually did.
type TelemetryStoreResult struct {
	Inserted      int // player-frame rows actually inserted
	Ignored       int // rows skipped because (match_id, player_id, frame_index) already existed
	TicksInserted int // match_ticks rows inserted (raw payloads)
	TicksIgnored  int // match_ticks rows that already existed
}

// StoreTelemetryFrames persists normalized telemetry frames for a match in bulk.
// Returns the number of rows ACTUALLY inserted; rows whose
// (match_id, player_id, frame_index) already existed are ignored and not
// counted. Callers should log/metric len(frames)-inserted as ignored duplicates.
func (s *Store) StoreTelemetryFrames(ctx context.Context, matchID string, frames []model.PlayerTelemetryFrame) (int, error) {
	res, err := s.StoreTelemetryFramesWithRaw(ctx, matchID, frames, nil)
	return res.Inserted, err
}

// StoreTelemetryFrameRows persists frame rows that may include raw profiler
// JSON. Raw payloads are written once per frame_index to match_ticks.
// Returns the number of player-frame rows actually inserted.
func (s *Store) StoreTelemetryFrameRows(ctx context.Context, matchID string, rows []TelemetryFrameRow) (int, error) {
	frames := make([]model.PlayerTelemetryFrame, len(rows))
	var rawByFrame map[int]string
	for i, r := range rows {
		frames[i] = r.Frame
		if r.RawJSON != "" {
			if rawByFrame == nil {
				rawByFrame = make(map[int]string)
			}
			if _, seen := rawByFrame[r.Frame.FrameIndex]; !seen {
				rawByFrame[r.Frame.FrameIndex] = r.RawJSON
			}
		}
	}
	res, err := s.StoreTelemetryFramesWithRaw(ctx, matchID, frames, rawByFrame)
	return res.Inserted, err
}

// StoreTelemetryFramesWithRaw is the canonical telemetry write: normalized
// frames go to telemetry_frames (one row per player per tick) and rawByFrame
// (frame_index -> raw session JSON) goes to match_ticks (one row per tick).
//
// The whole batch is one transaction. Any per-row failure other than a
// primary-key conflict (I/O error, SQLITE_FULL, context cancellation, a frame
// that cannot be serialized) aborts the transaction and is returned; nothing
// is partially committed and nothing is silently skipped. Primary-key
// conflicts are counted in Ignored, never in Inserted.
func (s *Store) StoreTelemetryFramesWithRaw(ctx context.Context, matchID string, frames []model.PlayerTelemetryFrame, rawByFrame map[int]string) (TelemetryStoreResult, error) {
	var res TelemetryStoreResult
	if len(frames) == 0 && len(rawByFrame) == 0 {
		return res, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	ingestedAt := fmtDBTime(time.Now())

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO telemetry_frames (match_id, player_id, frame_index, timestamp, frame_json, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return res, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, f := range frames {
		frameJSON, err := json.Marshal(f)
		if err != nil {
			return TelemetryStoreResult{}, fmt.Errorf("marshal frame match=%s player=%s frame=%d: %w",
				matchID, f.PlayerID, f.FrameIndex, err)
		}
		r, err := stmt.ExecContext(ctx, matchID, f.PlayerID, f.FrameIndex, f.Timestamp, string(frameJSON), ingestedAt)
		if err != nil {
			return TelemetryStoreResult{}, fmt.Errorf("insert frame match=%s player=%s frame=%d: %w",
				matchID, f.PlayerID, f.FrameIndex, err)
		}
		n, err := r.RowsAffected()
		if err != nil {
			return TelemetryStoreResult{}, fmt.Errorf("rows affected: %w", err)
		}
		if n > 0 {
			res.Inserted++
		} else {
			res.Ignored++
		}
	}

	if len(rawByFrame) > 0 {
		tickStmt, err := tx.PrepareContext(ctx,
			`INSERT OR IGNORE INTO match_ticks (match_id, frame_index, raw_json, ingested_at) VALUES (?, ?, ?, ?)`)
		if err != nil {
			return TelemetryStoreResult{}, fmt.Errorf("prepare ticks: %w", err)
		}
		defer tickStmt.Close()
		// Deterministic order so a failure is reproducible.
		idxs := make([]int, 0, len(rawByFrame))
		for idx := range rawByFrame {
			idxs = append(idxs, idx)
		}
		sort.Ints(idxs)
		for _, idx := range idxs {
			raw := rawByFrame[idx]
			if raw == "" {
				continue
			}
			r, err := tickStmt.ExecContext(ctx, matchID, idx, raw, ingestedAt)
			if err != nil {
				return TelemetryStoreResult{}, fmt.Errorf("insert tick match=%s frame=%d: %w", matchID, idx, err)
			}
			n, _ := r.RowsAffected()
			if n > 0 {
				res.TicksInserted++
			} else {
				res.TicksIgnored++
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return TelemetryStoreResult{}, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

// ReplaceMatchTelemetryFrames atomically replaces the normalized player-frame
// cache for one match. The original match_ticks rows are intentionally left
// untouched: they are the immutable source used to regenerate these frames
// after mapper/schema fixes. A serialization or insert failure rolls the
// transaction back, preserving every previously normalized row.
func (s *Store) ReplaceMatchTelemetryFrames(ctx context.Context, matchID string, frames []model.PlayerTelemetryFrame) (int, error) {
	if matchID == "" {
		return 0, errors.New("replace telemetry: empty match id")
	}
	if len(frames) == 0 {
		return 0, errors.New("replace telemetry: no frames")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin replace telemetry: %w", err)
	}
	defer tx.Rollback()
	// Databases from schema v8 can still carry their only raw snapshots in
	// telemetry_frames.raw_json. Promote one exact copy per tick before the
	// cache is deleted, so a mapper refresh also completes the v9 storage
	// migration instead of destroying the source it just consumed.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO match_ticks (match_id, frame_index, raw_json, ingested_at)
		SELECT match_id, frame_index, MIN(raw_json), MIN(ingested_at)
		FROM telemetry_frames
		WHERE match_id = ? AND raw_json IS NOT NULL AND raw_json <> ''
		GROUP BY match_id, frame_index`, matchID); err != nil {
		return 0, fmt.Errorf("preserve legacy raw ticks for match %s: %w", matchID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM telemetry_frames WHERE match_id = ?`, matchID); err != nil {
		return 0, fmt.Errorf("delete old telemetry for match %s: %w", matchID, err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO telemetry_frames (match_id, player_id, frame_index, timestamp, frame_json, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare replacement telemetry: %w", err)
	}
	defer stmt.Close()
	ingestedAt := fmtDBTime(time.Now())
	for _, f := range frames {
		frameJSON, err := json.Marshal(f)
		if err != nil {
			return 0, fmt.Errorf("marshal replacement frame match=%s player=%s frame=%d: %w",
				matchID, f.PlayerID, f.FrameIndex, err)
		}
		if _, err := stmt.ExecContext(ctx, matchID, f.PlayerID, f.FrameIndex, f.Timestamp, string(frameJSON), ingestedAt); err != nil {
			return 0, fmt.Errorf("insert replacement frame match=%s player=%s frame=%d: %w",
				matchID, f.PlayerID, f.FrameIndex, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit replacement telemetry: %w", err)
	}
	return len(frames), nil
}

// matchRawTicksSelect reads both storage generations in one snapshot. A
// canonical tick takes precedence at its own index, without hiding legacy
// ticks elsewhere in the match after an upgrade or a resumed ingest.
const matchRawTicksSelect = `SELECT frame_index, raw_json FROM match_ticks WHERE match_id = ?
	UNION ALL
	SELECT t.frame_index, MIN(t.raw_json) AS raw_json FROM telemetry_frames t
	WHERE t.match_id = ? AND t.raw_json IS NOT NULL AND t.raw_json <> ''
	AND NOT EXISTS (SELECT 1 FROM match_ticks m
		WHERE m.match_id = t.match_id AND m.frame_index = t.frame_index)
	GROUP BY t.frame_index`

// GetMatchRawTicks returns raw session payloads for frame indices in
// [fromIdx, toIdx] (inclusive), keyed by frame_index. Missing canonical ticks
// fall back to legacy telemetry_frames.raw_json at each index. Returns an
// empty map when the source carried no raw payload.
func (s *Store) GetMatchRawTicks(ctx context.Context, matchID string, fromIdx, toIdx int) (map[int]string, error) {
	out := make(map[int]string)
	rows, err := s.db.QueryContext(ctx,
		`SELECT frame_index, raw_json FROM (`+matchRawTicksSelect+`)
		 WHERE frame_index >= ? AND frame_index <= ? ORDER BY frame_index`,
		matchID, matchID, fromIdx, toIdx)
	if err != nil {
		return nil, err
	}
	if err := collectTicks(rows, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetAllMatchRawTicks returns every original source tick for a match. Unlike a
// range based on normalized frame indices, this also retains raw snapshots
// whose player rows were all rejected or absent; archive callers must use it.
func (s *Store) GetAllMatchRawTicks(ctx context.Context, matchID string) (map[int]string, error) {
	out := make(map[int]string)
	rows, err := s.db.QueryContext(ctx,
		matchRawTicksSelect+` ORDER BY frame_index`, matchID, matchID)
	if err != nil {
		return nil, err
	}
	if err := collectTicks(rows, out); err != nil {
		return nil, err
	}
	return out, nil
}

func collectTicks(rows *sql.Rows, out map[int]string) error {
	defer rows.Close()
	for rows.Next() {
		var idx int
		var raw string
		if err := rows.Scan(&idx, &raw); err != nil {
			return err
		}
		out[idx] = raw
	}
	return rows.Err()
}

// GetMatchTickCount returns how many raw ticks are stored for a match.
func (s *Store) GetMatchTickCount(ctx context.Context, matchID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM match_ticks WHERE match_id = ?`, matchID).Scan(&n)
	return n, err
}

// StoreMatchContext persists match metadata so it can be reconstructed for
// reprocessing. The wall-clock start time is also written to an indexed column
// so time-range queries and cross-match decay can use match time rather than
// ingestion time. Replaces any existing row for the match.
func (s *Store) StoreMatchContext(ctx context.Context, matchCtx *model.MatchContext, frameCount int) error {
	ctxJSON, err := json.Marshal(matchCtx)
	if err != nil {
		return fmt.Errorf("marshal match context: %w", err)
	}
	var startPtr *string
	if start := fmtDBTimeOrEmpty(matchCtx.StartTime); start != "" {
		startPtr = &start
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO match_contexts (match_id, context_json, frame_count, ingested_at, match_start_time)
		 VALUES (?, ?, ?, ?, ?)`,
		matchCtx.MatchID, string(ctxJSON), frameCount, fmtDBTime(time.Now()), startPtr,
	)
	return err
}

// HasMatchContext reports whether an explicit match_contexts row exists.
func (s *Store) HasMatchContext(ctx context.Context, matchID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM match_contexts WHERE match_id = ?`, matchID).Scan(&n)
	return n > 0, err
}

// HasMatch reports whether any source data (context or telemetry) exists for the match.
func (s *Store) HasMatch(ctx context.Context, matchID string) (bool, error) {
	ok, err := s.HasMatchContext(ctx, matchID)
	if err != nil || ok {
		return ok, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM telemetry_frames WHERE match_id = ? LIMIT 1`, matchID).Scan(&n)
	return n > 0, err
}

// GetMatchContext loads match metadata from the database.
//
// When no match_contexts row exists but telemetry frames do (a live match whose
// context was never finalized), a context is synthesized from the frames:
// distinct player IDs (sorted), team assignments from the earliest frame per
// player, StartTime = earliest ingestion time, DefaultPhysics, Source
// "live_telemetry" and ReplayFile "" — enough for reprocessing to run. Callers
// that need to know can compare Source or call HasMatchContext.
func (s *Store) GetMatchContext(ctx context.Context, matchID string) (*model.MatchContext, error) {
	var ctxJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT context_json FROM match_contexts WHERE match_id = ?`, matchID,
	).Scan(&ctxJSON)
	switch {
	case err == nil:
		var mc model.MatchContext
		if err := json.Unmarshal([]byte(ctxJSON), &mc); err != nil {
			return nil, fmt.Errorf("unmarshal match context: %w", err)
		}
		return &mc, nil
	case err == sql.ErrNoRows:
		return s.synthesizeMatchContext(ctx, matchID)
	default:
		return nil, err
	}
}

func (s *Store) synthesizeMatchContext(ctx context.Context, matchID string) (*model.MatchContext, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.player_id, t.frame_json, (SELECT MIN(ingested_at) FROM telemetry_frames WHERE match_id = t.match_id)
		 FROM telemetry_frames t
		 WHERE t.match_id = ?
		   AND t.frame_index = (SELECT MIN(frame_index) FROM telemetry_frames
		                        WHERE match_id = t.match_id AND player_id = t.player_id)
		 ORDER BY t.player_id`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mc := &model.MatchContext{
		MatchID:         matchID,
		TeamAssignments: make(map[string]string),
		Source:          "live_telemetry",
		Physics:         model.DefaultPhysics(),
	}
	for rows.Next() {
		var pid, frameJSON, firstIngested string
		if err := rows.Scan(&pid, &frameJSON, &firstIngested); err != nil {
			return nil, err
		}
		mc.PlayerIDs = append(mc.PlayerIDs, pid)
		var f model.PlayerTelemetryFrame
		if err := json.Unmarshal([]byte(frameJSON), &f); err == nil && f.Team != "" {
			mc.TeamAssignments[pid] = f.Team
		}
		if mc.StartTime.IsZero() {
			mc.StartTime = parseDBTimeLenient(firstIngested)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(mc.PlayerIDs) == 0 {
		return nil, fmt.Errorf("match %s: %w", matchID, ErrNotFound)
	}
	sort.Strings(mc.PlayerIDs)
	return mc, nil
}

// GetMatchFrames loads all telemetry frames for a match from the database,
// ordered by frame_index and player_id. A row whose frame_json cannot be
// decoded is an error (naming the row), never silently dropped: reprocessing
// a subset of the source frames would produce different detections with no
// indication why.
func (s *Store) GetMatchFrames(ctx context.Context, matchID string) ([]model.PlayerTelemetryFrame, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT player_id, frame_index, frame_json FROM telemetry_frames
		 WHERE match_id = ? ORDER BY frame_index, player_id`,
		matchID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFrames(rows, matchID)
}

func scanFrames(rows *sql.Rows, matchID string) ([]model.PlayerTelemetryFrame, error) {
	var frames []model.PlayerTelemetryFrame
	for rows.Next() {
		var pid, fJSON string
		var idx int
		if err := rows.Scan(&pid, &idx, &fJSON); err != nil {
			return nil, err
		}
		var f model.PlayerTelemetryFrame
		if err := json.Unmarshal([]byte(fJSON), &f); err != nil {
			return nil, fmt.Errorf("corrupt telemetry row match=%s player=%s frame=%d: %w", matchID, pid, idx, err)
		}
		frames = append(frames, f)
	}
	return frames, rows.Err()
}

// GetPlayerMatchIDs returns up to limit match IDs the player has stored
// telemetry in, most recent match first. "Recent" is the match's wall-clock
// start time when the context records one, else the earliest ingestion time.
func (s *Store) GetPlayerMatchIDs(ctx context.Context, playerID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.match_id
		 FROM telemetry_frames t
		 LEFT JOIN match_contexts mc ON mc.match_id = t.match_id
		 WHERE t.player_id = ?
		 GROUP BY t.match_id
		 ORDER BY COALESCE(mc.match_start_time, MIN(t.ingested_at)) DESC, t.match_id
		 LIMIT ?`, playerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var mid string
		if err := rows.Scan(&mid); err != nil {
			return nil, err
		}
		ids = append(ids, mid)
	}
	return ids, rows.Err()
}

// GetPlayerFrames loads telemetry frames for a specific player across their
// matchLimit most recent matches (see GetPlayerMatchIDs).
func (s *Store) GetPlayerFrames(ctx context.Context, playerID string, matchLimit int) (map[string][]model.PlayerTelemetryFrame, error) {
	matchIDs, err := s.GetPlayerMatchIDs(ctx, playerID, matchLimit)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]model.PlayerTelemetryFrame, len(matchIDs))
	for _, mid := range matchIDs {
		frows, err := s.db.QueryContext(ctx,
			`SELECT player_id, frame_index, frame_json FROM telemetry_frames
			 WHERE match_id = ? AND player_id = ? ORDER BY frame_index`,
			mid, playerID,
		)
		if err != nil {
			return nil, err
		}
		frames, err := scanFrames(frows, mid)
		frows.Close()
		if err != nil {
			return nil, err
		}
		result[mid] = frames
	}
	return result, nil
}

// GetMatchIDsByTimeRange returns match IDs whose match time falls in
// [since, until). Match time is match_contexts.match_start_time when the
// context records one, otherwise the earliest telemetry ingestion time for
// the match. A zero since means no lower bound; a zero until means now.
// Results are ordered by match time, then match ID.
func (s *Store) GetMatchIDsByTimeRange(ctx context.Context, since, until time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.match_id FROM (
		    SELECT t.match_id AS match_id,
		           COALESCE(mc.match_start_time, MIN(t.ingested_at)) AS match_time
		    FROM telemetry_frames t
		    LEFT JOIN match_contexts mc ON mc.match_id = t.match_id
		    GROUP BY t.match_id
		 ) m
		 WHERE m.match_time >= ? AND m.match_time < ?
		 ORDER BY m.match_time, m.match_id`,
		fmtDBTimeSince(since), fmtDBTime(until),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetMatchStartTimes returns the recorded wall-clock start time for each of
// the given matches. Matches without a recorded start time are absent from
// the result.
func (s *Store) GetMatchStartTimes(ctx context.Context, matchIDs []string) (map[string]time.Time, error) {
	out := make(map[string]time.Time, len(matchIDs))
	if len(matchIDs) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(matchIDs)), ",")
	args := make([]any, len(matchIDs))
	for i, id := range matchIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT match_id, match_start_time FROM match_contexts
		 WHERE match_start_time IS NOT NULL AND match_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, ts string
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, err
		}
		if t, err := parseDBTime(ts); err == nil {
			out[id] = t
		}
	}
	return out, rows.Err()
}

// GetStoredMatchCount returns how many matches have stored telemetry.
func (s *Store) GetStoredMatchCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT match_id) FROM telemetry_frames`,
	).Scan(&count)
	return count, err
}

// PruneOldTelemetry removes telemetry frames (and their raw ticks) ingested
// before the given duration ago.
//
// WARNING: Telemetry frames are IMMUTABLE SOURCE DATA — the profiler truth.
// Pruning telemetry permanently destroys the ability to reprocess those matches.
// This method exists for explicit manual maintenance only (e.g., disk space emergency).
// It MUST NOT be called automatically or on a timer. The default retention policy
// is indefinite: "all telemetry ever collected."
func (s *Store) PruneOldTelemetry(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	frames, err := s.pruneBefore(ctx, "telemetry_frames", "ingested_at", cutoff)
	if err != nil {
		return frames, err
	}
	if _, err := s.pruneBefore(ctx, "match_ticks", "ingested_at", cutoff); err != nil {
		return frames, err
	}
	return frames, nil
}
