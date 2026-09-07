package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

var blindExecutableHash struct {
	sync.Once
	value string
	err   error
}

var blindArtifactTransfers = make(chan struct{}, 2)

func acquireBlindTransfer(w http.ResponseWriter) bool {
	select {
	case blindArtifactTransfers <- struct{}{}:
		return true
	default:
		writeError(w, 429, "another evidence transfer is running; retry shortly")
		return false
	}
}

// runningExecutableSHA256 is shared by analysis provenance and review binding.
// A dirty build's linked commit/version alone cannot identify executed code.
func runningExecutableSHA256() (string, error) {
	blindExecutableHash.Do(func() {
		path, err := os.Executable()
		if err != nil {
			blindExecutableHash.err = err
			return
		}
		f, err := os.Open(path)
		if err != nil {
			blindExecutableHash.err = err
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			blindExecutableHash.err = err
			return
		}
		blindExecutableHash.value = hex.EncodeToString(h.Sum(nil))
	})
	if blindExecutableHash.err != nil {
		return "", fmt.Errorf("identify running executable SHA-256: %w", blindExecutableHash.err)
	}
	return blindExecutableHash.value, nil
}

// Bind even a local development review to the actual running executable. This
// hash adds identity, not publisher trust; dirty/unknown holdout stays blocked.
func (s *server) blindCandidateFingerprint() (string, error) {
	executableHash, err := runningExecutableSHA256()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(appVersion + "\x00" + analysisBuildRevision() + "\x00" + calibrationFingerprint(s.engine.Config()) + "\x00" + executableHash))
	return hex.EncodeToString(sum[:]), nil
}

func decodeBlindJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		writeError(w, 400, "invalid review request: %v", err)
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		writeError(w, 400, "one JSON object is required")
		return false
	}
	return true
}

func (s *server) handleUploadBlindArtifact(w http.ResponseWriter, r *http.Request) {
	if !acquireBlindTransfer(w) {
		return
	}
	defer func() { <-blindArtifactTransfers }()
	r.Body = http.MaxBytesReader(w, r.Body, sqlite.BlindArtifactMaxBytes+(64<<10))
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, 400, "choose a local evidence file")
		return
	}
	var payload []byte
	var filename string
	consent := false
	parts := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, 413, "evidence upload is invalid or too large (32 MiB maximum)")
			return
		}
		parts++
		if parts > 2 {
			part.Close()
			writeError(w, 400, "provide one file and consent=true")
			return
		}
		switch part.FormName() {
		case "consent":
			data, err := io.ReadAll(io.LimitReader(part, 16))
			if err != nil || string(data) != "true" {
				part.Close()
				writeError(w, 400, "explicit local evidence consent is required")
				return
			}
			consent = true
		case "file":
			if payload != nil || part.FileName() == "" {
				part.Close()
				writeError(w, 400, "provide exactly one evidence file")
				return
			}
			filename = part.FileName()
			payload, err = io.ReadAll(io.LimitReader(part, sqlite.BlindArtifactMaxBytes+1))
			if err != nil || len(payload) > sqlite.BlindArtifactMaxBytes {
				part.Close()
				writeError(w, 413, "evidence exceeds 32 MiB; select a short clip")
				return
			}
		default:
			part.Close()
			writeError(w, 400, "only a local file and consent are accepted; paths and URLs are not supported")
			return
		}
		part.Close()
	}
	if !consent {
		writeError(w, 400, "explicit local evidence consent is required")
		return
	}
	a, err := s.engine.Store().StoreBlindArtifact(r.Context(), filename, payload)
	if err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	writeJSON(w, 201, a)
}

func (s *server) handleDownloadBlindArtifact(w http.ResponseWriter, r *http.Request) {
	if !acquireBlindTransfer(w) {
		return
	}
	defer func() { <-blindArtifactTransfers }()
	a, payload, err := s.engine.Store().GetBlindArtifact(r.Context(), r.PathValue("sha"))
	if err != nil {
		writeError(w, 404, "evidence is unavailable or failed integrity verification")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-NEVR-Evidence-SHA256", a.SHA256)
	w.Write(payload) // #nosec G705 -- Deliberate byte-for-byte evidence download: octet-stream, attachment disposition, and nosniff are set above and asserted by API tests; content is never rendered inline.
}

func (s *server) handleListBlindReviews(w http.ResponseWriter, r *http.Request) {
	x, err := s.engine.Store().ListBlindReviewSessions(r.Context())
	if err != nil {
		writeError(w, 500, "loading blind reviews: %v", err)
		return
	}
	artifacts, err := s.engine.Store().ListBlindArtifacts(r.Context())
	if err != nil {
		writeError(w, 500, "loading evidence inventory: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"sessions": x, "inventory": artifacts, "sealed_holdout": false, "authenticated_reviewers": false, "notice": "Two immutable local ballots precede reveal in this workflow. The operator can inspect other views; identities are attested, not authenticated. Evidence remains private and is included in database backups."})
}

func (s *server) handleBlindReviewMatches(w http.ResponseWriter, r *http.Request) {
	matches, err := s.engine.Store().ListBlindReviewMatches(r.Context())
	if err != nil {
		writeError(w, 500, "review match list is unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"matches": matches})
}

func (s *server) handleCreateBlindReview(w http.ResponseWriter, r *http.Request) {
	var b sqlite.BlindReviewBinding
	if !decodeBlindJSON(w, r, &b) {
		return
	}
	if !s.analyzeMu.TryLock() {
		writeError(w, 409, "analysis is running; retry when it finishes")
		return
	}
	defer s.analyzeMu.Unlock()
	known := false
	for _, d := range s.engine.Config().EffectiveTable() {
		if d.ID == strings.ToUpper(strings.TrimSpace(b.DetectorID)) {
			known = true
			break
		}
	}
	if !known {
		writeError(w, 400, "unknown detector")
		return
	}
	executableHash, hashErr := runningExecutableSHA256()
	if hashErr != nil {
		s.engine.Logger().Error("cannot bind review without executable identity", "error", hashErr)
		writeError(w, 500, "running executable integrity could not be verified; bound review is unavailable")
		return
	}
	runs, err := s.engine.Store().ListAnalysisRuns(r.Context(), b.MatchID, 1)
	if err != nil || len(runs) != 1 || runs[0].AppVersion != appVersion || runs[0].BuildCommit != analysisBuildRevision() || runs[0].CalibrationFingerprint != calibrationFingerprint(s.engine.Config()) || runs[0].ExecutableSHA256 == "" || runs[0].ExecutableSHA256 != executableHash {
		writeError(w, 409, "re-analyze this match with the current executable/configuration before creating a bound review; legacy, missing or different executable SHA-256 cannot establish the analyzed candidate")
		return
	}
	candidate, err := s.blindCandidateFingerprint()
	if err != nil {
		writeError(w, 500, "%v", err)
		return
	}
	x, err := s.engine.Store().CreateBlindReviewSession(r.Context(), b, candidate)
	if err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	writeJSON(w, 201, x)
}

func (s *server) handleBlindReviewBallot(w http.ResponseWriter, r *http.Request) {
	var b sqlite.BlindReviewBallot
	if !decodeBlindJSON(w, r, &b) {
		return
	}
	if !s.analyzeMu.TryLock() {
		writeError(w, 409, "analysis is running; retry when it finishes")
		return
	}
	defer s.analyzeMu.Unlock()
	candidate, err := s.blindCandidateFingerprint()
	if err != nil {
		writeError(w, 500, "%v", err)
		return
	}
	x, err := s.engine.Store().SubmitBlindReviewBallot(r.Context(), r.PathValue("id"), candidate, b)
	if err != nil {
		writeError(w, 409, "%v", err)
		return
	}
	writeJSON(w, 201, x)
}

func (s *server) handleRevealBlindReview(w http.ResponseWriter, r *http.Request) {
	if !s.analyzeMu.TryLock() {
		writeError(w, 409, "analysis is running; retry when it finishes")
		return
	}
	candidate, err := s.blindCandidateFingerprint()
	if err != nil {
		s.analyzeMu.Unlock()
		writeError(w, 500, "%v", err)
		return
	}
	x, err := s.engine.Store().RevealBlindReview(r.Context(), r.PathValue("id"), candidate)
	s.analyzeMu.Unlock()
	if err != nil {
		writeError(w, 409, "%v", err)
		return
	}
	s.reconcileCalibrationChange(r.Context())
	events, err := s.engine.Store().GetMatchEvents(r.Context(), x.Binding.MatchID)
	if err != nil {
		writeError(w, 500, "loading revealed observations: %v", err)
		return
	}
	matched := make([]any, 0)
	sample := calibrationSample{PlayerID: x.Binding.PlayerID, DetectorID: x.Binding.DetectorID, FrameStart: x.Binding.FrameStart, FrameEnd: x.Binding.FrameEnd}
	for _, event := range events {
		if samplePredicted(sample, []model.DetectionEvent{event}) {
			matched = append(matched, event)
		}
	}
	writeJSON(w, 200, map[string]any{"session": x, "observations": matched, "automatic_enforcement": false})
}
