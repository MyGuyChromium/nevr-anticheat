package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

const BlindArtifactMaxBytes = 32 << 20
const BlindArtifactTotalBytes = 256 << 20
const BlindArtifactMaxCount = 128
const BlindSessionMaxCount = 10000

// These are workflow integrity records, not authenticated identities or proof
// that the local operator has never seen the detector's output elsewhere.
type BlindArtifact struct {
	SHA256    string `json:"sha256"`
	Filename  string `json:"filename"`
	SizeBytes int    `json:"size_bytes"`
}
type BlindArtifactInventory struct {
	Artifacts    []BlindArtifact `json:"artifacts"`
	UsedBytes    int64           `json:"used_bytes"`
	MaxBytes     int64           `json:"max_bytes"`
	MaxFileBytes int64           `json:"max_file_bytes"`
	MaxCount     int             `json:"max_count"`
}
type BlindReviewBinding struct {
	MatchID        string `json:"match_id"`
	PlayerID       string `json:"player_id"`
	DetectorID     string `json:"detector_id"`
	Kind           string `json:"opportunity_kind"`
	BehaviorType   string `json:"behavior_type"`
	FrameStart     int    `json:"frame_start"`
	FrameEnd       int    `json:"frame_end"`
	EvidenceMethod string `json:"evidence_method"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	LegalContext   string `json:"legal_context"`
}

type BlindReviewPlayer struct {
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	MinFrame int    `json:"min_frame"`
	MaxFrame int    `json:"max_frame"`
}
type BlindReviewMatch struct {
	MatchID    string              `json:"match_id"`
	SourceFile string              `json:"source_file"`
	Players    []BlindReviewPlayer `json:"players"`
	MaxFrame   int                 `json:"max_frame"`
}

// ListBlindReviewMatches deliberately never queries labels, events, or scores.
func (s *Store) ListBlindReviewMatches(ctx context.Context) ([]BlindReviewMatch, error) {
	matches, err := s.ListMatches(ctx, 200)
	if err != nil {
		return nil, err
	}
	out := make([]BlindReviewMatch, 0, len(matches))
	for _, m := range matches {
		if m.Context == nil {
			continue
		}
		x := BlindReviewMatch{MatchID: m.Context.MatchID, Players: []BlindReviewPlayer{}}
		if m.Context.ReplayFile != "" {
			x.SourceFile = path.Base(strings.ReplaceAll(m.Context.ReplayFile, "\\", "/"))
		}
		rows, err := s.db.QueryContext(ctx, `SELECT player_id,MIN(frame_index),MAX(frame_index) FROM telemetry_frames WHERE match_id=? GROUP BY player_id ORDER BY player_id`, x.MatchID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p BlindReviewPlayer
			if err := rows.Scan(&p.PlayerID, &p.MinFrame, &p.MaxFrame); err != nil {
				rows.Close()
				return nil, err
			}
			p.Name = m.Context.PlayerNames[p.PlayerID]
			if p.Name == "" {
				p.Name = p.PlayerID
			}
			x.Players = append(x.Players, p)
			x.MaxFrame = max(x.MaxFrame, p.MaxFrame)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

type BlindReviewBallot struct {
	ReviewerID           string    `json:"reviewer_id"`
	GroundTruth          string    `json:"ground_truth"`
	Comment              string    `json:"comment"`
	PreRevealAttestation bool      `json:"pre_reveal_attestation"`
	SubmittedAt          time.Time `json:"submitted_at"`
}
type BlindReviewSession struct {
	SessionID            string              `json:"session_id"`
	Binding              BlindReviewBinding  `json:"binding"`
	CandidateFingerprint string              `json:"candidate_fingerprint"`
	WindowSHA256         string              `json:"window_sha256"`
	BallotCount          int                 `json:"ballot_count"`
	Revealed             bool                `json:"revealed"`
	Consensus            string              `json:"consensus,omitempty"`
	Ballots              []BlindReviewBallot `json:"ballots,omitempty"`
	CreatedAt            time.Time           `json:"created_at"`
}

func validSHA256(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size && strings.ToLower(value) == value
}
func digestBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (s *Store) StoreBlindArtifact(ctx context.Context, filename string, payload []byte) (BlindArtifact, error) {
	// The name is display-only and is never interpreted as a disk path.
	filename = strings.TrimSpace(filename)
	if filename == "" || len(filename) > 160 || strings.ContainsAny(filename, "/\\\x00\r\n") {
		return BlindArtifact{}, errors.New("choose a simple evidence filename without a path")
	}
	if len(payload) == 0 || len(payload) > BlindArtifactMaxBytes {
		return BlindArtifact{}, errors.New("evidence must contain 1 byte to 32 MiB; use a short clip")
	}
	out := BlindArtifact{SHA256: digestBytes(payload), Filename: filename, SizeBytes: len(payload)}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT filename FROM blind_evidence_artifacts WHERE sha256=?`, out.SHA256).Scan(&existing)
	if err == nil {
		out.Filename = existing
		return out, nil
	}
	if err != sql.ErrNoRows {
		return out, err
	}
	var total, count int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(payload)),0),COUNT(*) FROM blind_evidence_artifacts`).Scan(&total, &count); err != nil {
		return out, err
	}
	if total+int64(len(payload)) > BlindArtifactTotalBytes || count >= BlindArtifactMaxCount {
		return out, errors.New("local evidence capacity reached (256 MiB / 128 files); retain your backup and use a separate review library")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blind_evidence_artifacts(sha256,filename,payload) VALUES(?,?,?)`, out.SHA256, filename, payload); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) ListBlindArtifacts(ctx context.Context) (BlindArtifactInventory, error) {
	out := BlindArtifactInventory{Artifacts: []BlindArtifact{}, MaxBytes: BlindArtifactTotalBytes, MaxFileBytes: BlindArtifactMaxBytes, MaxCount: BlindArtifactMaxCount}
	rows, err := s.db.QueryContext(ctx, `SELECT sha256,filename,length(payload) FROM blind_evidence_artifacts ORDER BY created_at,sha256 LIMIT 129`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var a BlindArtifact
		if err := rows.Scan(&a.SHA256, &a.Filename, &a.SizeBytes); err != nil {
			return out, err
		}
		out.Artifacts = append(out.Artifacts, a)
		out.UsedBytes += int64(a.SizeBytes)
	}
	return out, rows.Err()
}
func (s *Store) GetBlindArtifact(ctx context.Context, hash string) (BlindArtifact, []byte, error) {
	if !validSHA256(hash) {
		return BlindArtifact{}, nil, errors.New("invalid evidence SHA-256")
	}
	var a BlindArtifact
	var payload []byte
	a.SHA256 = hash
	err := s.db.QueryRowContext(ctx, `SELECT filename,payload FROM blind_evidence_artifacts WHERE sha256=? AND length(payload)<=?`, hash, BlindArtifactMaxBytes).Scan(&a.Filename, &payload)
	if err == sql.ErrNoRows {
		return a, nil, ErrNotFound
	}
	if err != nil {
		return a, nil, err
	}
	if digestBytes(payload) != hash {
		return a, nil, errors.New("evidence content hash mismatch")
	}
	a.SizeBytes = len(payload)
	return a, payload, nil
}

type blindQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func blindWindowHash(ctx context.Context, q blindQuerier, b BlindReviewBinding) (string, error) {
	if b.FrameStart < 0 || b.FrameEnd < b.FrameStart || b.FrameEnd-b.FrameStart > 9000 {
		return "", errors.New("review window must be ordered and at most 9001 frames")
	}
	rows, err := q.QueryContext(ctx, `SELECT frame_index,CASE WHEN length(frame_json)<=33554432 THEN frame_json ELSE NULL END FROM telemetry_frames WHERE match_id=? AND player_id=? AND frame_index BETWEEN ? AND ? ORDER BY frame_index LIMIT 9002`, b.MatchID, b.PlayerID, b.FrameStart, b.FrameEnd)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	n, total := 0, 0
	for rows.Next() {
		var idx int
		var storedFrame sql.NullString
		if err := rows.Scan(&idx, &storedFrame); err != nil {
			return "", err
		}
		if !storedFrame.Valid {
			return "", errors.New("review window contains an oversized telemetry frame")
		}
		frame := storedFrame.String
		total += len(frame)
		if total > 32<<20 {
			return "", errors.New("review window telemetry exceeds 32 MiB; select a shorter window")
		}
		fmt.Fprintf(h, "%d:%d:", idx, len(frame))
		h.Write([]byte(frame))
		n++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if n == 0 {
		return "", errors.New("selected player has no telemetry in this window")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func normalizeBlindBinding(b BlindReviewBinding) (BlindReviewBinding, error) {
	x, err := normalizeOpportunity(CalibrationOpportunity{MatchID: b.MatchID, PlayerID: b.PlayerID, DetectorID: b.DetectorID, Kind: b.Kind, BehaviorType: b.BehaviorType, FrameStart: b.FrameStart, FrameEnd: b.FrameEnd, GroundTruth: GroundTruthUncertain, EvidenceMethod: b.EvidenceMethod})
	if err != nil {
		return b, err
	}
	b.MatchID, b.PlayerID, b.DetectorID, b.Kind, b.BehaviorType, b.EvidenceMethod = x.MatchID, x.PlayerID, x.DetectorID, x.Kind, x.BehaviorType, x.EvidenceMethod
	if !validIndependentEvidenceMethod(b.EvidenceMethod) || !validSHA256(b.ArtifactSHA256) {
		return b, errors.New("independent evidence method and an uploaded evidence SHA-256 are required")
	}
	b.LegalContext = strings.ToLower(strings.TrimSpace(b.LegalContext))
	switch b.LegalContext {
	case "normal", "lean", "stack", "block_push", "slap", "headbutt", "transition", "other", "unknown":
	default:
		return b, errors.New("invalid legal_context")
	}
	return b, nil
}
func (s *Store) CreateBlindReviewSession(ctx context.Context, b BlindReviewBinding, candidate string) (BlindReviewSession, error) {
	var out BlindReviewSession
	var err error
	b, err = normalizeBlindBinding(b)
	if err != nil {
		return out, err
	}
	if !validSHA256(candidate) {
		return out, errors.New("an exact candidate fingerprint is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var payload []byte
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM blind_evidence_artifacts WHERE sha256=? AND length(payload)<=?`, b.ArtifactSHA256, BlindArtifactMaxBytes).Scan(&payload); err != nil {
		return out, errors.New("upload the actual evidence artifact first")
	}
	if digestBytes(payload) != b.ArtifactSHA256 {
		return out, errors.New("evidence content hash mismatch")
	}
	window, err := blindWindowHash(ctx, tx, b)
	if err != nil {
		return out, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blind_review_sessions`).Scan(&count); err != nil {
		return out, err
	}
	if count >= BlindSessionMaxCount {
		return out, errors.New("review-session capacity reached; use a separate review library")
	}
	raw, _ := json.Marshal(b)
	out = BlindReviewSession{SessionID: uuid.NewString(), Binding: b, CandidateFingerprint: candidate, WindowSHA256: window, CreatedAt: nowUTC()}
	_, err = tx.ExecContext(ctx, `INSERT INTO blind_review_sessions(session_id,binding_json,artifact_sha256,candidate_fingerprint,window_sha256,created_at) VALUES(?,?,?,?,?,?)`, out.SessionID, string(raw), b.ArtifactSHA256, candidate, window, fmtDBTime(out.CreatedAt))
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func readBlindSession(ctx context.Context, q blindQuerier, id string) (BlindReviewSession, error) {
	var out BlindReviewSession
	var binding, revealed, created string
	err := q.QueryRowContext(ctx, `SELECT session_id,binding_json,candidate_fingerprint,window_sha256,revealed_at,created_at FROM blind_review_sessions WHERE session_id=?`, id).Scan(&out.SessionID, &binding, &out.CandidateFingerprint, &out.WindowSHA256, &revealed, &created)
	if err == sql.ErrNoRows {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(binding), &out.Binding); err != nil {
		return out, err
	}
	out.CreatedAt = parseDBTimeLenient(created)
	out.Revealed = revealed != ""
	rows, err := q.QueryContext(ctx, `SELECT ballot_json FROM blind_review_ballots WHERE session_id=? ORDER BY reviewer_key`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var ballot BlindReviewBallot
		if err := rows.Scan(&raw); err != nil {
			return out, err
		}
		if err := json.Unmarshal([]byte(raw), &ballot); err != nil {
			return out, err
		}
		out.Ballots = append(out.Ballots, ballot)
	}
	out.BallotCount = len(out.Ballots)
	if out.Revealed && len(out.Ballots) == 2 {
		out.Consensus = GroundTruthUncertain
		if out.Ballots[0].GroundTruth == out.Ballots[1].GroundTruth {
			out.Consensus = out.Ballots[0].GroundTruth
		}
	}
	return out, rows.Err()
}
func redactBlindSession(in BlindReviewSession) BlindReviewSession {
	if !in.Revealed {
		in.Ballots = nil
		in.Consensus = ""
	}
	return in
}
func (s *Store) ListBlindReviewSessions(ctx context.Context) ([]BlindReviewSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id FROM blind_review_sessions ORDER BY created_at DESC,session_id LIMIT 500`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]BlindReviewSession, 0, len(ids))
	for _, id := range ids {
		x, err := readBlindSession(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, redactBlindSession(x))
	}
	return out, nil
}
func validateBlindBinding(ctx context.Context, q blindQuerier, x BlindReviewSession, candidate string, verifiedArtifacts ...map[string]bool) error {
	if candidate != x.CandidateFingerprint {
		return errors.New("candidate changed; start a new review session")
	}
	h, err := blindWindowHash(ctx, q, x.Binding)
	if err != nil {
		return err
	}
	if h != x.WindowSHA256 {
		return errors.New("source player window changed; the previous review is no longer measurable")
	}
	if len(verifiedArtifacts) > 0 && verifiedArtifacts[0][x.Binding.ArtifactSHA256] {
		return nil
	}
	var payload []byte
	if err := q.QueryRowContext(ctx, `SELECT payload FROM blind_evidence_artifacts WHERE sha256=? AND length(payload)<=?`, x.Binding.ArtifactSHA256, BlindArtifactMaxBytes).Scan(&payload); err != nil {
		return err
	}
	if digestBytes(payload) != x.Binding.ArtifactSHA256 {
		return errors.New("evidence content hash mismatch")
	}
	if len(verifiedArtifacts) > 0 {
		verifiedArtifacts[0][x.Binding.ArtifactSHA256] = true
	}
	return nil
}
func (s *Store) SubmitBlindReviewBallot(ctx context.Context, id, candidate string, b BlindReviewBallot) (BlindReviewSession, error) {
	b.ReviewerID = strings.TrimSpace(b.ReviewerID)
	b.GroundTruth = strings.ToLower(strings.TrimSpace(b.GroundTruth))
	b.Comment = strings.TrimSpace(b.Comment)
	if !b.PreRevealAttestation || len(b.ReviewerID) < 2 || len(b.ReviewerID) > 120 || strings.EqualFold(b.ReviewerID, "local-owner") || !validGroundTruth(b.GroundTruth) || len(b.Comment) > 2000 {
		return BlindReviewSession{}, errors.New("a named reviewer, pre-reveal attestation, valid ground truth and comment of at most 2000 characters are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BlindReviewSession{}, err
	}
	defer tx.Rollback()
	x, err := readBlindSession(ctx, tx, id)
	if err != nil {
		return x, err
	}
	if x.Revealed || x.BallotCount >= 2 {
		return redactBlindSession(x), errors.New("ballots are already locked; they cannot be replaced")
	}
	if err := validateBlindBinding(ctx, tx, x, candidate); err != nil {
		return redactBlindSession(x), err
	}
	b.SubmittedAt = nowUTC()
	raw, _ := json.Marshal(b)
	if _, err := tx.ExecContext(ctx, `INSERT INTO blind_review_ballots(session_id,reviewer_key,ballot_json,created_at) VALUES(?,?,?,?)`, id, strings.ToLower(b.ReviewerID), string(raw), fmtDBTime(b.SubmittedAt)); err != nil {
		return redactBlindSession(x), errors.New("this reviewer already has an immutable ballot")
	}
	x.BallotCount++
	if err := tx.Commit(); err != nil {
		return BlindReviewSession{}, err
	}
	return redactBlindSession(x), nil
}

func (s *Store) RevealBlindReview(ctx context.Context, id, candidate string) (BlindReviewSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BlindReviewSession{}, err
	}
	defer tx.Rollback()
	x, err := readBlindSession(ctx, tx, id)
	if err != nil {
		return x, err
	}
	if x.BallotCount != 2 {
		return redactBlindSession(x), errors.New("two distinct immutable ballots are required before reveal")
	}
	if err := validateBlindBinding(ctx, tx, x, candidate); err != nil {
		return redactBlindSession(x), err
	}
	if x.Revealed {
		return x, nil
	}
	b := x.Binding
	var overlap int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM calibration_opportunities WHERE match_id=? AND player_id=? AND detector_id=? AND frame_end>=? AND frame_start<=?`, b.MatchID, b.PlayerID, b.DetectorID, b.FrameStart, b.FrameEnd).Scan(&overlap); err != nil {
		return BlindReviewSession{}, err
	}
	if overlap > 0 {
		return redactBlindSession(x), errors.New("an existing ground-truth window overlaps; retain/export its notes and remove that annotation before revealing this session")
	}
	consensus := GroundTruthUncertain
	if x.Ballots[0].GroundTruth == x.Ballots[1].GroundTruth {
		consensus = x.Ballots[0].GroundTruth
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO calibration_opportunities(opportunity_id,match_id,player_id,detector_id,behavior_type,opportunity_kind,frame_start,frame_end,ground_truth,reviewer_id,blind_review,reviewed_at,verifier_id,verified_ground_truth,evidence_method,evidence_reference,review_session_id) VALUES(?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,?)`, id, b.MatchID, b.PlayerID, b.DetectorID, b.BehaviorType, b.Kind, b.FrameStart, b.FrameEnd, consensus, x.Ballots[0].ReviewerID, fmtDBTime(nowUTC()), x.Ballots[1].ReviewerID, consensus, b.EvidenceMethod, "sha256:"+b.ArtifactSHA256, id)
	if err != nil {
		return BlindReviewSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE blind_review_sessions SET revealed_at=? WHERE session_id=? AND revealed_at=''`, fmtDBTime(nowUTC()), id); err != nil {
		return BlindReviewSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return redactBlindSession(x), err
	}
	x.Revealed, x.Consensus = true, consensus
	return x, nil
}

// VerifiedBlindOpportunity verifies actual stored bytes and current bindings.
// It does not authenticate people, prove independence, or certify cheating.
func (s *Store) VerifiedBlindOpportunity(ctx context.Context, o CalibrationOpportunity, candidate string) (bool, string, error) {
	return s.verifiedBlindOpportunity(ctx, o, candidate, make(map[string]bool))
}

type BlindOpportunityProof struct {
	Verified     bool
	LegalContext string
}

// Batch validation rehashes each actual artifact once per dashboard, bounding
// attachment I/O by the 256 MiB library cap instead of once per opportunity.
func (s *Store) VerifiedBlindOpportunities(ctx context.Context, opportunities []CalibrationOpportunity, candidate string) (map[string]BlindOpportunityProof, error) {
	out := make(map[string]BlindOpportunityProof)
	verifiedArtifacts := make(map[string]bool)
	for _, o := range opportunities {
		ok, legal, err := s.verifiedBlindOpportunity(ctx, o, candidate, verifiedArtifacts)
		if err != nil {
			return nil, err
		}
		out[o.OpportunityID] = BlindOpportunityProof{Verified: ok, LegalContext: legal}
	}
	return out, nil
}

func (s *Store) verifiedBlindOpportunity(ctx context.Context, o CalibrationOpportunity, candidate string, verifiedArtifacts map[string]bool) (bool, string, error) {
	if o.ReviewSessionID == "" {
		return false, "unknown", nil
	}
	x, err := readBlindSession(ctx, s.db, o.ReviewSessionID)
	if err == ErrNotFound {
		return false, "unknown", nil
	}
	if err != nil {
		return false, "unknown", err
	}
	b := x.Binding
	if !x.Revealed || x.BallotCount != 2 || !x.Ballots[0].PreRevealAttestation || !x.Ballots[1].PreRevealAttestation || x.Consensus == GroundTruthUncertain || x.Consensus != o.GroundTruth || o.OpportunityID != x.SessionID || o.MatchID != b.MatchID || o.PlayerID != b.PlayerID || o.DetectorID != b.DetectorID || o.FrameStart != b.FrameStart || o.FrameEnd != b.FrameEnd {
		return false, b.LegalContext, nil
	}
	if err := validateBlindBinding(ctx, s.db, x, candidate, verifiedArtifacts); err != nil {
		return false, b.LegalContext, nil
	}
	return true, b.LegalContext, nil
}
