package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func (s *server) decorateMatchMetadata(ctx context.Context, view *matchView) {
	if view == nil || view.MatchID == "" {
		return
	}
	if label, ok, err := s.engine.Store().GetMatchLabel(ctx, view.MatchID); err == nil && ok {
		view.CalibrationLabel = label.Label
		view.CalibrationComment = label.Comment
		view.CalibrationReviewedAt = fmtTime(label.ReviewedAt)
	}
	if stats, err := s.engine.Store().GetMatchStorageStats(ctx, view.MatchID); err == nil {
		view.Storage = &stats
	}
}

type calibrationDetectorView struct {
	DetectorID     string  `json:"detector_id"`
	Confirmed      int     `json:"confirmed"`
	FalsePositive  int     `json:"false_positive"`
	Inconclusive   int     `json:"inconclusive"`
	NeedsMoreData  int     `json:"needs_more_data"`
	EventsReviewed int     `json:"events_reviewed"`
	DirectLabels   int     `json:"direct_labels"`
	Precision      float64 `json:"precision,omitempty"`
	HasPrecision   bool    `json:"has_precision"`
}

func (s *server) handleCalibration(w http.ResponseWriter, r *http.Request) {
	store := s.engine.Store()
	counts, err := store.MatchLabelCounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading match labels: %v", err)
		return
	}
	stored, err := store.GetStoredMatchCount(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "counting matches: %v", err)
		return
	}
	rows, err := store.ComputeCalibration(r.Context(), time.Time{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "computing calibration: %v", err)
		return
	}
	detectors := make([]calibrationDetectorView, 0, len(rows))
	for _, row := range rows {
		precision, ok := row.Precision()
		detectors = append(detectors, calibrationDetectorView{
			DetectorID: row.DetectorID, Confirmed: row.Confirmed, FalsePositive: row.FalsePositive,
			Inconclusive: row.Inconclusive, NeedsMoreData: row.NeedsMoreData,
			EventsReviewed: row.EventsReviewed, DirectLabels: row.DirectLabels,
			Precision: precision, HasPrecision: ok,
		})
	}
	labeled := counts[sqlite.MatchLabelKnownClean] + counts[sqlite.MatchLabelSuspected] + counts[sqlite.MatchLabelConfirmedCheat]
	writeJSON(w, http.StatusOK, map[string]any{
		"match_labels": counts, "labeled_matches": labeled, "unlabeled_matches": maxInt(0, stored-labeled),
		"stored_matches": stored, "detectors": detectors,
		"notice": "Replay labels organize the calibration library. Detector labels provide detector-specific ground truth; neither changes enforcement.",
	})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *server) handleMatchLabel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label      string `json:"label"`
		Comment    string `json:"comment"`
		ReviewerID string `json:"reviewer_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid match label: %v", err)
		return
	}
	label, err := s.engine.Store().StoreMatchLabel(r.Context(), r.PathValue("id"), body.Label, body.Comment, body.ReviewerID, appVersion, s.configFingerprint())
	if errors.Is(err, sqlite.ErrNotFound) {
		writeError(w, http.StatusNotFound, "%v", err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, label)
}

func parseFramePath(r *http.Request) (int, error) {
	frame, err := strconv.Atoi(r.PathValue("frame"))
	if err != nil || frame < 0 {
		return 0, fmt.Errorf("invalid frame %q", r.PathValue("frame"))
	}
	return frame, nil
}

func (s *server) handlePhysicsFrame(w http.ResponseWriter, r *http.Request) {
	frame, err := parseFramePath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	inspection, err := s.buildPhysicsInspection(r.Context(), r.PathValue("id"), r.URL.Query().Get("player"), frame, nil)
	if err != nil {
		writeInspectorError(w, err)
		return
	}
	attachInspectionHealth(r.Context(), s, inspection)
	writeJSON(w, http.StatusOK, inspection)
}

func (s *server) handlePhysicsEvent(w http.ResponseWriter, r *http.Request) {
	event, err := findMatchEvent(r.Context(), s.engine.Store(), r.PathValue("id"), r.PathValue("event"))
	if err != nil {
		writeInspectorError(w, err)
		return
	}
	inspection, err := s.buildPhysicsInspection(r.Context(), r.PathValue("id"), event.PlayerID, event.FrameIndex, event)
	if err != nil {
		writeInspectorError(w, err)
		return
	}
	attachInspectionHealth(r.Context(), s, inspection)
	writeJSON(w, http.StatusOK, inspection)
}

func attachInspectionHealth(ctx context.Context, s *server, inspection *physicsInspection) {
	if diag, err := storedTelemetryDiagnostics(ctx, s.engine.Store(), inspection.MatchID); err == nil {
		inspection.Telemetry = telemetryHealth(diag)
	}
}

func writeInspectorError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if isInspectorNotFound(err) {
		status = http.StatusNotFound
	} else if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "invalid") {
		status = http.StatusBadRequest
	}
	writeError(w, status, "building physics inspection: %v", err)
}

func (s *server) handleDiagnosticEvent(w http.ResponseWriter, r *http.Request) {
	event, err := findMatchEvent(r.Context(), s.engine.Store(), r.PathValue("id"), r.PathValue("event"))
	if err != nil {
		writeInspectorError(w, err)
		return
	}
	inspection, err := s.buildPhysicsInspection(r.Context(), r.PathValue("id"), event.PlayerID, event.FrameIndex, event)
	if err != nil {
		writeInspectorError(w, err)
		return
	}
	s.writeDiagnosticBundle(w, r, inspection, event.DetectorID+"-frame-"+strconv.Itoa(event.FrameIndex))
}

func (s *server) handleDiagnosticFrame(w http.ResponseWriter, r *http.Request) {
	frame, err := parseFramePath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	inspection, err := s.buildPhysicsInspection(r.Context(), r.PathValue("id"), r.URL.Query().Get("player"), frame, nil)
	if err != nil {
		writeInspectorError(w, err)
		return
	}
	s.writeDiagnosticBundle(w, r, inspection, "frame-"+strconv.Itoa(frame))
}

func (s *server) writeDiagnosticBundle(w http.ResponseWriter, r *http.Request, inspection *physicsInspection, suffix string) {
	attachInspectionHealth(r.Context(), s, inspection)
	bundle, err := s.buildDiagnosticBundle(inspection)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "building diagnostic bundle: %v", err)
		return
	}
	name := "nevr-diagnostic-" + safeClipName(suffix) + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bundle)
}

func (s *server) handleArchiveMatch(w http.ResponseWriter, r *http.Request) {
	matchID := r.PathValue("id")
	var body struct {
		PruneRaw     bool   `json:"prune_raw"`
		Confirmation string `json:"confirmation"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid archive request: %v", err)
			return
		}
	}
	if body.PruneRaw && body.Confirmation != matchID {
		writeError(w, http.StatusBadRequest, "pruning requires the exact match id as confirmation")
		return
	}
	stats, err := s.engine.Store().GetMatchStorageStats(r.Context(), matchID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "measuring match storage: %v", err)
		return
	}
	path, manifest, err := s.createRawArchive(r.Context(), matchID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "archiving raw telemetry: %v", err)
		return
	}
	result := rawArchiveResult{OK: true, Path: path, Bytes: fileSize(path), TicksArchived: manifest.TickCount, Message: "Raw telemetry archived and verified; database source remains intact."}
	if body.PruneRaw {
		removed, err := s.engine.Store().DeleteMatchRawTicks(r.Context(), matchID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "archive was saved to %s, but pruning failed: %v", path, err)
			return
		}
		result.TicksPruned = removed
		result.SpaceReusable = stats.RawTickBytes
		result.Message = "Raw telemetry archived, verified, and removed from the active database. Normalized evidence and analysis remain available; restore is available from the archive."
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) handleRestoreMatchRaw(w http.ResponseWriter, r *http.Request) {
	matchID := r.PathValue("id")
	path, err := s.latestRawArchive(matchID)
	if archiveNotFound(err) {
		writeError(w, http.StatusNotFound, "no raw archive found for match %s", matchID)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "finding archive: %v", err)
		return
	}
	manifest, ticks, err := readAndVerifyRawArchive(path, matchID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "verifying archive: %v", err)
		return
	}
	inserted, present, err := s.engine.Store().RestoreMatchRawTicks(r.Context(), matchID, ticks)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "restoring archive: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "path": path, "ticks_in_archive": manifest.TickCount,
		"ticks_restored": inserted, "ticks_already_present": present,
		"message": fmt.Sprintf("Restored %d raw tick(s); %d were already present.", inserted, present),
	})
}
