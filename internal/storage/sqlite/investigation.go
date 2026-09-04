package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// InvestigationNote is a local bookmark or free-form investigator note.
type InvestigationNote struct {
	NoteID     string    `json:"note_id"`
	MatchID    string    `json:"match_id"`
	PlayerID   string    `json:"player_id,omitempty"`
	FrameIndex int       `json:"frame_index"`
	Kind       string    `json:"kind"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const investigationNoteColumns = `note_id, match_id, player_id, frame_index, kind, body, created_at, updated_at`

func scanInvestigationNote(row rowScanner) (InvestigationNote, error) {
	var out InvestigationNote
	var created, updated string
	err := row.Scan(&out.NoteID, &out.MatchID, &out.PlayerID, &out.FrameIndex, &out.Kind, &out.Body, &created, &updated)
	out.CreatedAt, out.UpdatedAt = parseDBTimeLenient(created), parseDBTimeLenient(updated)
	return out, err
}

// StoreInvestigationNote creates or updates a note. An empty note id creates
// a UUID; imported IDs are retained so libraries merge idempotently.
func (s *Store) StoreInvestigationNote(ctx context.Context, note InvestigationNote) (InvestigationNote, error) {
	note.NoteID, note.MatchID = strings.TrimSpace(note.NoteID), strings.TrimSpace(note.MatchID)
	note.PlayerID, note.Kind, note.Body = strings.TrimSpace(note.PlayerID), strings.ToLower(strings.TrimSpace(note.Kind)), strings.TrimSpace(note.Body)
	if note.MatchID == "" {
		return note, fmt.Errorf("match id is required")
	}
	if note.Kind != "note" && note.Kind != "bookmark" {
		return note, fmt.Errorf("kind must be note or bookmark")
	}
	if note.FrameIndex < -1 {
		return note, fmt.Errorf("frame index must be -1 or greater")
	}
	if len(note.Body) > 8000 {
		return note, fmt.Errorf("note exceeds 8000 characters")
	}
	if note.Kind == "note" && note.Body == "" {
		return note, fmt.Errorf("note body is required")
	}
	if note.NoteID == "" {
		note.NoteID = uuid.NewString()
	}
	now := nowUTC()
	if note.CreatedAt.IsZero() {
		note.CreatedAt = now
	}
	note.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO investigation_notes
		(note_id, match_id, player_id, frame_index, kind, body, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(note_id) DO UPDATE SET match_id=excluded.match_id, player_id=excluded.player_id,
		frame_index=excluded.frame_index, kind=excluded.kind, body=excluded.body, updated_at=excluded.updated_at`,
		note.NoteID, note.MatchID, note.PlayerID, note.FrameIndex, note.Kind, note.Body,
		fmtDBTime(note.CreatedAt), fmtDBTime(note.UpdatedAt))
	if err != nil {
		return note, fmt.Errorf("storing investigation note: %w", err)
	}
	return note, nil
}

func (s *Store) ListInvestigationNotes(ctx context.Context, matchID string) ([]InvestigationNote, error) {
	query := `SELECT ` + investigationNoteColumns + ` FROM investigation_notes`
	var args []any
	if strings.TrimSpace(matchID) != "" {
		query += ` WHERE match_id = ?`
		args = append(args, strings.TrimSpace(matchID))
	}
	query += ` ORDER BY match_id, frame_index, created_at, note_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvestigationNote
	for rows.Next() {
		note, err := scanInvestigationNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, note)
	}
	return out, rows.Err()
}

func (s *Store) DeleteInvestigationNote(ctx context.Context, noteID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM investigation_notes WHERE note_id = ?`, strings.TrimSpace(noteID))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AnalysisRun records detector/config provenance and coarse performance for
// one match analysis. It contains no raw telemetry.
type AnalysisRun struct {
	RunID                  int64     `json:"run_id"`
	MatchID                string    `json:"match_id"`
	Source                 string    `json:"source"`
	AppVersion             string    `json:"app_version"`
	BuildCommit            string    `json:"build_commit"`
	ConfigFingerprint      string    `json:"config_fingerprint"`
	CalibrationFingerprint string    `json:"calibration_fingerprint"`
	ProfileName            string    `json:"profile_name,omitempty"`
	TelemetryQuality       float64   `json:"telemetry_quality"`
	QualityGrade           string    `json:"quality_grade"`
	QualityGated           bool      `json:"quality_gated"`
	WallMilliseconds       int64     `json:"wall_milliseconds"`
	PipelineMilliseconds   int64     `json:"pipeline_milliseconds"`
	FramesProcessed        int       `json:"frames_processed"`
	EventsProduced         int       `json:"events_produced"`
	CreatedAt              time.Time `json:"created_at"`
}

func (s *Store) StoreAnalysisRun(ctx context.Context, run AnalysisRun) (AnalysisRun, error) {
	if strings.TrimSpace(run.MatchID) == "" {
		return run, fmt.Errorf("match id is required")
	}
	run.CreatedAt = nowUTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO analysis_runs
		(match_id, source, app_version, build_commit, config_fingerprint, calibration_fingerprint, profile_name,
		 telemetry_quality, quality_grade, quality_gated, wall_milliseconds,
		 pipeline_milliseconds, frames_processed, events_produced, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.MatchID, run.Source, run.AppVersion, run.BuildCommit, run.ConfigFingerprint, run.CalibrationFingerprint, run.ProfileName,
		run.TelemetryQuality, run.QualityGrade, run.QualityGated, run.WallMilliseconds,
		run.PipelineMilliseconds, run.FramesProcessed, run.EventsProduced, fmtDBTime(run.CreatedAt))
	if err != nil {
		return run, fmt.Errorf("storing analysis provenance: %w", err)
	}
	run.RunID, _ = res.LastInsertId()
	return run, nil
}

const analysisRunColumns = `run_id, match_id, source, app_version, build_commit, config_fingerprint, calibration_fingerprint,
	profile_name, telemetry_quality, quality_grade, quality_gated, wall_milliseconds,
	pipeline_milliseconds, frames_processed, events_produced, created_at`

func scanAnalysisRun(row rowScanner) (AnalysisRun, error) {
	var out AnalysisRun
	var gated int
	var created string
	err := row.Scan(&out.RunID, &out.MatchID, &out.Source, &out.AppVersion, &out.BuildCommit,
		&out.ConfigFingerprint, &out.CalibrationFingerprint, &out.ProfileName, &out.TelemetryQuality, &out.QualityGrade,
		&gated, &out.WallMilliseconds, &out.PipelineMilliseconds, &out.FramesProcessed,
		&out.EventsProduced, &created)
	out.QualityGated, out.CreatedAt = gated != 0, parseDBTimeLenient(created)
	return out, err
}

func (s *Store) ListAnalysisRuns(ctx context.Context, matchID string, limit int) ([]AnalysisRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + analysisRunColumns + ` FROM analysis_runs`
	var args []any
	if strings.TrimSpace(matchID) != "" {
		query += ` WHERE match_id = ?`
		args = append(args, strings.TrimSpace(matchID))
	}
	query += ` ORDER BY run_id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AnalysisRun
	for rows.Next() {
		run, err := scanAnalysisRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// ConfigProfile is a named, shadow-safe detector configuration. Activation is
// applied during the next desktop start, before any worker or handler runs.
type ConfigProfile struct {
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	DetectorsJSON string    `json:"detectors_json"`
	Active        bool      `json:"active"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *Store) StoreConfigProfile(ctx context.Context, p ConfigProfile) (ConfigProfile, error) {
	p.Name, p.Description = strings.TrimSpace(p.Name), strings.TrimSpace(p.Description)
	if p.Name == "" || len(p.Name) > 80 {
		return p, fmt.Errorf("profile name must contain 1-80 characters")
	}
	if len(p.Description) > 500 {
		return p, fmt.Errorf("profile description exceeds 500 characters")
	}
	if !json.Valid([]byte(p.DetectorsJSON)) {
		return p, fmt.Errorf("profile detector configuration is not valid JSON")
	}
	now := nowUTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO config_profiles
		(name, detectors_json, description, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET detectors_json=excluded.detectors_json,
		description=excluded.description, updated_at=excluded.updated_at`,
		p.Name, p.DetectorsJSON, p.Description, p.Active, fmtDBTime(p.CreatedAt), fmtDBTime(p.UpdatedAt))
	return p, err
}

func scanConfigProfile(row rowScanner) (ConfigProfile, error) {
	var out ConfigProfile
	var active int
	var created, updated string
	err := row.Scan(&out.Name, &out.DetectorsJSON, &out.Description, &active, &created, &updated)
	out.Active, out.CreatedAt, out.UpdatedAt = active != 0, parseDBTimeLenient(created), parseDBTimeLenient(updated)
	return out, err
}

func (s *Store) ListConfigProfiles(ctx context.Context) ([]ConfigProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, detectors_json, description, active, created_at, updated_at FROM config_profiles ORDER BY active DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigProfile
	for rows.Next() {
		p, err := scanConfigProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetActiveConfigProfile(ctx context.Context) (ConfigProfile, bool, error) {
	p, err := scanConfigProfile(s.db.QueryRowContext(ctx, `SELECT name, detectors_json, description, active, created_at, updated_at FROM config_profiles WHERE active = 1 LIMIT 1`))
	if err == sql.ErrNoRows {
		return ConfigProfile{}, false, nil
	}
	return p, err == nil, err
}

func (s *Store) ActivateConfigProfile(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_profiles WHERE name = ?`, strings.TrimSpace(name)).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE config_profiles SET active = CASE WHEN name = ? THEN 1 ELSE 0 END`, strings.TrimSpace(name)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteConfigProfile(ctx context.Context, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM config_profiles WHERE name = ? AND active = 0`, strings.TrimSpace(name))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SavedFilter persists named investigation views in the evidence database so
// they can be included in portable library exports.
type SavedFilter struct {
	Name       string    `json:"name"`
	FilterJSON string    `json:"filter_json"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (s *Store) StoreSavedFilter(ctx context.Context, f SavedFilter) (SavedFilter, error) {
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" || len(f.Name) > 80 || !json.Valid([]byte(f.FilterJSON)) {
		return f, fmt.Errorf("filter requires a 1-80 character name and valid JSON")
	}
	now := nowUTC()
	if f.CreatedAt.IsZero() {
		f.CreatedAt = now
	}
	f.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO saved_filters (name, filter_json, created_at, updated_at)
		VALUES (?, ?, ?, ?) ON CONFLICT(name) DO UPDATE SET filter_json=excluded.filter_json, updated_at=excluded.updated_at`,
		f.Name, f.FilterJSON, fmtDBTime(f.CreatedAt), fmtDBTime(f.UpdatedAt))
	return f, err
}

func (s *Store) ListSavedFilters(ctx context.Context) ([]SavedFilter, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, filter_json, created_at, updated_at FROM saved_filters ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedFilter
	for rows.Next() {
		var f SavedFilter
		var created, updated string
		if err := rows.Scan(&f.Name, &f.FilterJSON, &created, &updated); err != nil {
			return nil, err
		}
		f.CreatedAt, f.UpdatedAt = parseDBTimeLenient(created), parseDBTimeLenient(updated)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSavedFilter(ctx context.Context, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM saved_filters WHERE name = ?`, strings.TrimSpace(name))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ImportEventReview restores an exported event review even when the current
// analysis no longer has that event id. The copied evidence remains ground
// truth for the regression library.
func (s *Store) ImportEventReview(ctx context.Context, review EventReview) error {
	if strings.TrimSpace(review.EventID) == "" || strings.TrimSpace(review.MatchID) == "" || strings.TrimSpace(review.DetectorID) == "" || !validEventVerdict(review.Verdict) {
		return fmt.Errorf("invalid imported event review")
	}
	if len(review.Comment) > 2000 || (review.EvidenceJSON != "" && !json.Valid([]byte(review.EvidenceJSON))) {
		return fmt.Errorf("invalid imported event review payload")
	}
	if review.ReviewedAt.IsZero() {
		review.ReviewedAt = nowUTC()
	}
	blind := 0
	if review.BlindReview {
		blind = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO event_reviews (`+eventReviewColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id) DO UPDATE SET verdict=excluded.verdict, comment=excluded.comment,
		reviewer_id=excluded.reviewer_id, reviewed_at=excluded.reviewed_at,
		blind_review=excluded.blind_review`,
		review.EventID, review.MatchID, review.PlayerID, review.DetectorID, review.DetectorVersion,
		review.FrameIndex, review.Timestamp, review.Severity, review.Confidence, review.ObservedValue,
		review.ExpectedRange, review.EvidenceType, review.EvidenceJSON, review.Verdict,
		review.Comment, review.ReviewerID, fmtDBTime(review.ReviewedAt), blind)
	return err
}

// ImportMatchLabel restores an exported label without requiring the replay to
// already be present; it becomes visible when that match is later imported.
func (s *Store) ImportMatchLabel(ctx context.Context, label MatchLabel) error {
	if strings.TrimSpace(label.MatchID) == "" || !validMatchLabel(label.Label) || len(label.Comment) > 4000 {
		return fmt.Errorf("invalid imported match label")
	}
	if label.ReviewedAt.IsZero() {
		label.ReviewedAt = nowUTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO match_labels (`+matchLabelColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(match_id) DO UPDATE SET label=excluded.label, comment=excluded.comment,
		reviewer_id=excluded.reviewer_id, app_version=excluded.app_version,
		config_fingerprint=excluded.config_fingerprint, reviewed_at=excluded.reviewed_at`,
		label.MatchID, label.Label, label.Comment, label.ReviewerID, label.AppVersion,
		label.ConfigFingerprint, fmtDBTime(label.ReviewedAt))
	return err
}
