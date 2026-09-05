package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/evidence"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// Browser uploads are streamed straight into the durable recovery queue. A
// Spark recording may be much larger than its compressed on-disk size, so the
// desktop accepts multi-gigabyte files without buffering or making a second
// temporary copy. The request limit still protects the loopback service from
// an accidentally unbounded multipart body.
const (
	maxUploadFileBytes    int64 = 4 << 30
	maxUploadRequestBytes int64 = 8 << 30
)

// historyLimit bounds the History panel and the flagged lists.
const historyLimit = 200

//go:embed index.html
var indexHTML []byte

// server is the loopback HTTP app: one embedded page and a small JSON API,
// every route under a per-run random token so other local pages cannot
// reach it.
type server struct {
	engine       *replay.Engine
	token        string
	mux          *http.ServeMux
	clipDir      string
	launchReplay replayLaunchFunc
	runtime      *desktopRuntime

	analyzeMu sync.Mutex // uploads are analyzed one at a time
	activeMu  sync.Mutex
	activeID  uint64
	activeCtx context.CancelFunc
	quitOnce  sync.Once
	quit      chan struct{}
}

func newServer(engine *replay.Engine, token string) *server {
	applyActiveProfile(engine)
	s := &server{
		engine: engine, token: token, mux: http.NewServeMux(), quit: make(chan struct{}),
		clipDir: defaultReplayClipDir(), launchReplay: launchSparkReplayViewer,
	}
	if dashboard, err := s.buildCalibrationDashboard(context.Background(), nil); err != nil {
		rolledBack := s.rollBackAllPromotions(context.Background())
		if len(rolledBack) > 0 {
			engine.Logger().Warn("calibration could not be verified; active promotions were returned to shadow", "detectors", rolledBack, "error", err)
		}
	} else {
		s.reconcilePromotions(context.Background(), dashboard)
	}
	s.runtime = newDesktopRuntime(engine, s.quit)
	s.runtime.analyzeMu = &s.analyzeMu
	p := "/" + token
	s.mux.HandleFunc("GET "+p+"/{$}", s.handleIndex)
	s.mux.HandleFunc("POST "+p+"/api/analyze", s.handleAnalyze)
	s.mux.HandleFunc("POST "+p+"/api/analyze/cancel", s.handleCancelAnalyze)
	s.mux.HandleFunc("GET "+p+"/api/health", s.handleHealth)
	s.mux.HandleFunc("GET "+p+"/api/calibration", s.handleCalibration)
	s.mux.HandleFunc("GET "+p+"/api/lab/regression", s.handleRegressionLab)
	s.mux.HandleFunc("GET "+p+"/api/lab/thresholds", s.handleThresholdSpecs)
	s.mux.HandleFunc("POST "+p+"/api/lab/thresholds/preview", s.handleThresholdPreview)
	s.mux.HandleFunc("POST "+p+"/api/lab/experiments", s.handleExperimentMatrix)
	s.mux.HandleFunc("GET "+p+"/api/lab/validation", s.handleValidationDashboard)
	s.mux.HandleFunc("GET "+p+"/api/lab/calibration-report", s.handleCalibrationReport)
	s.mux.HandleFunc("GET "+p+"/api/calibration/opportunities", s.handleCalibrationOpportunities)
	s.mux.HandleFunc("POST "+p+"/api/calibration/opportunities", s.handleStoreCalibrationOpportunity)
	s.mux.HandleFunc("DELETE "+p+"/api/calibration/opportunities/{id}", s.handleDeleteCalibrationOpportunity)
	s.mux.HandleFunc("GET "+p+"/api/lab/promotions", s.handlePromotions)
	s.mux.HandleFunc("POST "+p+"/api/lab/candidate", s.handleStoreThresholdCandidate)
	s.mux.HandleFunc("POST "+p+"/api/lab/promotions/{detector}", s.handlePromoteDetector)
	s.mux.HandleFunc("POST "+p+"/api/lab/promotions/{detector}/rollback", s.handleRollbackDetector)
	s.mux.HandleFunc("GET "+p+"/api/lab/synthetic", s.handleSyntheticProbes)
	s.mux.HandleFunc("POST "+p+"/api/maintenance/backup", s.handleBackup)
	s.mux.HandleFunc("POST "+p+"/api/maintenance/checkpoint", s.handleCheckpoint)
	s.mux.HandleFunc("POST "+p+"/api/maintenance/open-data-folder", s.handleOpenDataFolder)
	s.mux.HandleFunc("POST "+p+"/api/maintenance/clear-clips", s.handleClearClips)
	s.mux.HandleFunc("POST "+p+"/api/maintenance/support-bundle", s.handleSupportBundle)
	s.mux.HandleFunc("GET "+p+"/api/settings", s.handleSettings)
	s.mux.HandleFunc("POST "+p+"/api/settings", s.handleSaveSettings)
	s.mux.HandleFunc("POST "+p+"/api/watch/scan", s.handleWatchScan)
	s.mux.HandleFunc("GET "+p+"/api/recovery", s.handleRecovery)
	s.mux.HandleFunc("POST "+p+"/api/recovery/resume", s.handleRecoveryResume)
	s.mux.HandleFunc("POST "+p+"/api/recovery/discard", s.handleRecoveryDiscard)
	s.mux.HandleFunc("GET "+p+"/api/update", s.handleUpdateCheck)
	s.mux.HandleFunc("GET "+p+"/api/queue", s.handleQueue)
	s.mux.HandleFunc("POST "+p+"/api/update/open", s.handleOpenUpdate)
	s.mux.HandleFunc("GET "+p+"/api/flagged", s.handleFlagged)
	s.mux.HandleFunc("GET "+p+"/api/observations", s.handleObservations)
	s.mux.HandleFunc("GET "+p+"/api/matches", s.handleMatches)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}", s.handleMatch)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/comparison", s.handleAnalysisComparison)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/investigation", s.handleInvestigation)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/report", s.handleCaseReport)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/notes", s.handleListNotes)
	s.mux.HandleFunc("POST "+p+"/api/match/{id}/notes", s.handleStoreNote)
	s.mux.HandleFunc("DELETE "+p+"/api/note/{id}", s.handleDeleteNote)
	s.mux.HandleFunc("GET "+p+"/api/player/{id}/history", s.handlePlayerHistory)
	s.mux.HandleFunc("GET "+p+"/api/players/compare", s.handlePlayerComparison)
	s.mux.HandleFunc("POST "+p+"/api/match/{id}/label", s.handleMatchLabel)
	s.mux.HandleFunc("POST "+p+"/api/event/{id}/review", s.handleEventReview)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/physics/frame/{frame}", s.handlePhysicsFrame)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/physics/event/{event}", s.handlePhysicsEvent)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/diagnostic/frame/{frame}", s.handleDiagnosticFrame)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/diagnostic/event/{event}", s.handleDiagnosticEvent)
	s.mux.HandleFunc("POST "+p+"/api/match/{id}/archive", s.handleArchiveMatch)
	s.mux.HandleFunc("POST "+p+"/api/match/{id}/restore-raw", s.handleRestoreMatchRaw)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/summary.json", s.handleSummaryJSON)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/export.csv", s.handleExportCSV)
	s.mux.HandleFunc("POST "+p+"/api/replay-viewer", s.handleReplayViewer)
	s.mux.HandleFunc("POST "+p+"/api/match/{id}/replay/frame/{frame}", s.handleReplayFrame)
	s.mux.HandleFunc("POST "+p+"/api/match/{id}/replay/{event}", s.handleReplayClip)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}/evidence/{player}", s.handleMatchEvidence)
	s.mux.HandleFunc("GET "+p+"/api/case/{id}/evidence", s.handleCaseEvidence)
	s.mux.HandleFunc("GET "+p+"/api/profiles", s.handleProfiles)
	s.mux.HandleFunc("POST "+p+"/api/profiles", s.handleStoreProfile)
	s.mux.HandleFunc("POST "+p+"/api/profiles/{name}/activate", s.handleActivateProfile)
	s.mux.HandleFunc("DELETE "+p+"/api/profiles/{name}", s.handleDeleteProfile)
	s.mux.HandleFunc("GET "+p+"/api/filters", s.handleFilters)
	s.mux.HandleFunc("POST "+p+"/api/filters", s.handleStoreFilter)
	s.mux.HandleFunc("DELETE "+p+"/api/filters/{name}", s.handleDeleteFilter)
	s.mux.HandleFunc("GET "+p+"/api/library", s.handleExportLibrary)
	s.mux.HandleFunc("POST "+p+"/api/library", s.handleImportLibrary)
	s.mux.HandleFunc("GET "+p+"/api/setup", s.handleSetupDiagnostics)
	s.mux.HandleFunc("GET "+p+"/api/maintenance/backups", s.handleListBackups)
	s.mux.HandleFunc("POST "+p+"/api/maintenance/restore", s.handleScheduleRestore)
	s.mux.HandleFunc(p+"/quit", s.handleQuit)
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	return s
}

// Handler is the routed handler.
func (s *server) Handler() http.Handler { return s.mux }

// Done is closed when /quit was requested.
func (s *server) Done() <-chan struct{} { return s.quit }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"error": fmt.Sprintf(format, args...)})
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}

func (s *server) handleQuit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "NEVR-Anticheat desktop has stopped. You can close this tab.\n")
	s.quitOnce.Do(func() { close(s.quit) })
}

// ---- views -----------------------------------------------------------------

type playerView struct {
	PlayerID         string   `json:"player_id"`
	Name             string   `json:"name"`
	Team             string   `json:"team"`
	Frames           int      `json:"frames"`
	Score            float64  `json:"score"`
	Level            string   `json:"level"`
	Detections       int      `json:"detections"`
	ShadowDetections int      `json:"shadow_detections"`
	TopDetectors     []string `json:"top_detectors"`
}

type eventView struct {
	EventID         string  `json:"event_id"`
	DetectorID      string  `json:"detector_id"`
	DetectorName    string  `json:"detector_name"`
	DetectorVersion string  `json:"detector_version"`
	PlayerID        string  `json:"player_id"`
	PlayerName      string  `json:"player_name"`
	FrameIndex      int     `json:"frame_index"`
	FrameRangeStart int     `json:"frame_range_start"`
	FrameRangeEnd   int     `json:"frame_range_end"`
	Timestamp       float64 `json:"timestamp"`
	Severity        float64 `json:"severity"`
	SeverityLabel   string  `json:"severity_label"`
	Confidence      float64 `json:"confidence"`
	ObservedValue   string  `json:"observed_value"`
	ExpectedRange   string  `json:"expected_range"`
	Explanation     string  `json:"explanation"`
	IsShadow        bool    `json:"is_shadow"`
	MergedCount     int     `json:"merged_count"`
	ReviewVerdict   string  `json:"review_verdict,omitempty"`
	ReviewComment   string  `json:"review_comment,omitempty"`
	BlindReview     bool    `json:"blind_review,omitempty"`
	ReviewedAt      string  `json:"reviewed_at,omitempty"`
}

type caseView struct {
	CaseID            string  `json:"case_id"`
	PlayerID          string  `json:"player_id"`
	PlayerName        string  `json:"player_name"`
	MatchID           string  `json:"match_id"`
	Level             string  `json:"level"`
	Severity          string  `json:"severity"`
	SuspicionScore    float64 `json:"suspicion_score"`
	RecommendedAction string  `json:"recommended_action"`
	Status            string  `json:"status"`
	Explanation       string  `json:"explanation"`
	CreatedAt         string  `json:"created_at"`
}

type telemetryView struct {
	FramesInserted int `json:"frames_inserted"`
	FramesIgnored  int `json:"frames_ignored"`
	TicksInserted  int `json:"ticks_inserted"`
	TicksIgnored   int `json:"ticks_ignored"`
}

type basisView struct {
	Proper              int `json:"proper"`
	Reflected           int `json:"reflected"`
	NonOrthonormal      int `json:"non_orthonormal"`
	Degenerate          int `json:"degenerate"`
	HandTrackingLost    int `json:"hand_tracking_lost"`
	NonMonotonicSamples int `json:"non_monotonic_samples"`
	ClockSteps          int `json:"clock_steps"`
}

type diagView struct {
	Snapshots               int            `json:"snapshots"`
	LinesRejected           int            `json:"lines_rejected"`
	SessionChanges          int            `json:"session_changes"`
	PlayerEntriesSeen       int            `json:"player_entries_seen"`
	SpectatorEntriesDropped int            `json:"spectator_entries_dropped"`
	FramesMapped            int            `json:"frames_mapped"`
	PlayerFramesRejected    int            `json:"player_frames_rejected"`
	RejectionsByField       map[string]int `json:"rejections_by_field"`
	PlayersSeen             int            `json:"players_seen"`
	Basis                   *basisView     `json:"basis,omitempty"`
	Report                  string         `json:"report"`
}

type telemetryHealthView struct {
	Status          string                              `json:"status"`
	Snapshots       int                                 `json:"snapshots"`
	PresenceTracked bool                                `json:"presence_tracked"`
	Warnings        []adapter.TelemetryWarning          `json:"warnings"`
	UnknownFields   map[string]int                      `json:"unknown_fields"`
	FieldPresence   map[string]*adapter.FieldDiagnostic `json:"field_presence"`
	Compatibility   string                              `json:"compatibility"`
}

type matchView struct {
	MatchID               string               `json:"match_id"`
	Levels                model.LevelTable     `json:"levels"`
	DiscSpeedCap          float64              `json:"disc_speed_cap"`
	SourceFile            string               `json:"source_file"`
	StartTime             string               `json:"start_time"`
	DurationSeconds       float64              `json:"duration_seconds"`
	GameMode              string               `json:"game_mode"`
	Map                   string               `json:"map"`
	IsPrivate             bool                 `json:"is_private"`
	Source                string               `json:"source"`
	HasScore              bool                 `json:"has_score"`
	BlueScore             int                  `json:"blue_score"`
	OrangeScore           int                  `json:"orange_score"`
	FramesProcessed       int                  `json:"frames_processed"`
	InvalidFrames         int                  `json:"invalid_frames"`
	PlayerFrames          int                  `json:"player_frames"`
	AnalyzedAt            string               `json:"analyzed_at"`
	Replaced              bool                 `json:"replaced"`
	ClearedEvents         int64                `json:"cleared_events"`
	ClearedScores         int64                `json:"cleared_scores"`
	Telemetry             *telemetryView       `json:"telemetry,omitempty"`
	Players               []playerView         `json:"players"`
	Events                []eventView          `json:"events"`
	Cases                 []caseView           `json:"cases"`
	Diagnostics           *diagView            `json:"diagnostics,omitempty"`
	TelemetryHealth       *telemetryHealthView `json:"telemetry_health,omitempty"`
	CalibrationLabel      string               `json:"calibration_label,omitempty"`
	CalibrationComment    string               `json:"calibration_comment,omitempty"`
	CalibrationReviewedAt string               `json:"calibration_reviewed_at,omitempty"`
	Storage               *sqlite.StorageStats `json:"storage,omitempty"`
	Warnings              []string             `json:"warnings"`
	Summary               *replay.MatchSummary `json:"summary,omitempty"`
}

// matchData is what a match view is built from, whether the match was just
// analyzed or loaded back from the store.
type matchData struct {
	ctx             *model.MatchContext
	sourceFile      string
	analyzedAt      time.Time
	framesProcessed int
	invalidFrames   int
	framesByPlayer  map[string]int
	scores          map[string]model.SuspicionScore
	events          []model.DetectionEvent
	eventReviews    map[string]sqlite.EventReview
	cases           []model.ReviewCase
	hasScore        bool
	blue, orange    int
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func nameOf(mc *model.MatchContext, pid string) string {
	if mc != nil {
		if n := mc.PlayerNames[pid]; n != "" {
			return n
		}
	}
	return ""
}

func teamRank(team string) int {
	switch team {
	case "blue":
		return 0
	case "orange":
		return 1
	default:
		return 2
	}
}

func (s *server) buildMatchView(d matchData) matchView {
	mc := d.ctx
	levels := s.engine.Levels()
	v := matchView{
		MatchID:         mc.MatchID,
		Levels:          levels,
		DiscSpeedCap:    mc.Physics.DiscSpeedCap,
		SourceFile:      d.sourceFile,
		StartTime:       fmtTime(mc.StartTime),
		DurationSeconds: mc.Duration.Seconds(),
		GameMode:        mc.GameMode,
		Map:             mc.Map,
		IsPrivate:       mc.IsPrivate,
		Source:          mc.Source,
		HasScore:        d.hasScore,
		BlueScore:       d.blue,
		OrangeScore:     d.orange,
		FramesProcessed: d.framesProcessed,
		InvalidFrames:   d.invalidFrames,
		AnalyzedAt:      fmtTime(d.analyzedAt),
		Players:         []playerView{},
		Events:          []eventView{},
		Cases:           []caseView{},
		Warnings:        []string{},
	}

	// Roster: the context's players plus anyone only seen in frames or events.
	seen := make(map[string]bool)
	var pids []string
	add := func(pid string) {
		if pid != "" && !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	for _, pid := range mc.PlayerIDs {
		add(pid)
	}
	for pid := range d.framesByPlayer {
		add(pid)
	}
	for _, ev := range d.events {
		add(ev.PlayerID)
	}

	type detCount struct {
		id string
		n  int
	}
	byPlayer := make(map[string]map[string]int)
	detections := make(map[string]int)
	shadow := make(map[string]int)
	for _, ev := range d.events {
		if ev.IsShadow {
			shadow[ev.PlayerID]++
		} else {
			detections[ev.PlayerID]++
		}
		if byPlayer[ev.PlayerID] == nil {
			byPlayer[ev.PlayerID] = make(map[string]int)
		}
		byPlayer[ev.PlayerID][ev.DetectorID]++
	}
	for _, pid := range pids {
		pv := playerView{
			PlayerID:         pid,
			Name:             nameOf(mc, pid),
			Team:             mc.TeamAssignments[pid],
			Frames:           d.framesByPlayer[pid],
			Detections:       detections[pid],
			ShadowDetections: shadow[pid],
			TopDetectors:     []string{},
		}
		if sc, ok := d.scores[pid]; ok {
			pv.Score = sc.TotalScore
		}
		pv.Level = string(levels.LevelFor(pv.Score))
		var counts []detCount
		for id, n := range byPlayer[pid] {
			counts = append(counts, detCount{id, n})
		}
		sort.Slice(counts, func(i, j int) bool {
			if counts[i].n != counts[j].n {
				return counts[i].n > counts[j].n
			}
			return counts[i].id < counts[j].id
		})
		for i, c := range counts {
			if i == 3 {
				break
			}
			pv.TopDetectors = append(pv.TopDetectors, fmt.Sprintf("%s (%d)", c.id, c.n))
		}
		v.Players = append(v.Players, pv)
		v.PlayerFrames += pv.Frames
	}
	sort.SliceStable(v.Players, func(i, j int) bool {
		a, b := v.Players[i], v.Players[j]
		if ra, rb := teamRank(a.Team), teamRank(b.Team); ra != rb {
			return ra < rb
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.PlayerID < b.PlayerID
	})

	events := append([]model.DetectionEvent(nil), d.events...)
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.FrameIndex != b.FrameIndex {
			return a.FrameIndex < b.FrameIndex
		}
		if a.PlayerID != b.PlayerID {
			return a.PlayerID < b.PlayerID
		}
		return a.DetectorID < b.DetectorID
	})
	for _, ev := range events {
		detectorName := ev.DetectorID
		if spec, ok := config.DetectorSpecFor(ev.DetectorID); ok {
			detectorName = spec.Name
		}
		evView := eventView{
			EventID:         ev.EventID,
			DetectorID:      ev.DetectorID,
			DetectorName:    detectorName,
			DetectorVersion: ev.DetectorVersion,
			PlayerID:        ev.PlayerID,
			PlayerName:      nameOf(mc, ev.PlayerID),
			FrameIndex:      ev.FrameIndex,
			FrameRangeStart: ev.FrameRangeStart,
			FrameRangeEnd:   ev.FrameRangeEnd,
			Timestamp:       ev.Timestamp,
			Severity:        ev.Severity,
			SeverityLabel:   model.EventSeverityLabel(ev.Severity),
			Confidence:      ev.Confidence,
			ObservedValue:   ev.ObservedValue,
			ExpectedRange:   ev.ExpectedRange,
			Explanation:     explainEvent(ev),
			IsShadow:        ev.IsShadow,
			MergedCount:     ev.MergedCount,
		}
		if review, ok := d.eventReviews[ev.EventID]; ok {
			evView.ReviewVerdict = review.Verdict
			evView.ReviewComment = review.Comment
			evView.BlindReview = review.BlindReview
			evView.ReviewedAt = fmtTime(review.ReviewedAt)
		}
		v.Events = append(v.Events, evView)
	}
	for _, rc := range d.cases {
		v.Cases = append(v.Cases, s.caseView(rc, mc))
	}
	return v
}

func (s *server) caseView(rc model.ReviewCase, mc *model.MatchContext) caseView {
	level := rc.Level
	if level == "" {
		level = string(s.engine.Levels().LevelFor(rc.SuspicionScore))
	}
	return caseView{
		CaseID:            rc.CaseID,
		PlayerID:          rc.PlayerID,
		PlayerName:        nameOf(mc, rc.PlayerID),
		MatchID:           rc.MatchID,
		Level:             level,
		Severity:          rc.Severity,
		SuspicionScore:    rc.SuspicionScore,
		RecommendedAction: rc.RecommendedAction,
		Status:            rc.Status,
		Explanation:       rc.Explanation,
		CreatedAt:         fmtTime(rc.CreatedAt),
	}
}

func diagnosticsView(diag *adapter.DiagnosticReport) *diagView {
	if diag == nil {
		return nil
	}
	report := diag.FormatReport() // takes the report's lock; read fields after
	v := &diagView{
		Snapshots:               diag.Snapshots,
		LinesRejected:           diag.FramesRejected,
		SessionChanges:          diag.SessionChanges,
		PlayerEntriesSeen:       diag.PlayerEntriesSeen,
		SpectatorEntriesDropped: diag.SpectatorEntriesDropped,
		FramesMapped:            diag.FramesMapped,
		PlayerFramesRejected:    diag.PlayerFramesRejected,
		RejectionsByField:       diag.RejectionsByField,
		PlayersSeen:             diag.PlayerCount,
		Report:                  report,
	}
	if v.RejectionsByField == nil {
		v.RejectionsByField = map[string]int{}
	}
	if ms := diag.MapperStats; ms != nil {
		v.Basis = &basisView{
			Proper:              ms.BasesProper,
			Reflected:           ms.BasesReflected,
			NonOrthonormal:      ms.BasesNonOrthonormal,
			Degenerate:          ms.BasesDegenerate,
			HandTrackingLost:    ms.HandTrackingLost,
			NonMonotonicSamples: ms.NonMonotonicSamples,
			ClockSteps:          ms.ClockSteps,
		}
	}
	return v
}

func telemetryHealth(diag *adapter.DiagnosticReport) *telemetryHealthView {
	if diag == nil {
		return nil
	}
	warnings := diag.HealthWarnings()
	status := "healthy"
	for _, warning := range warnings {
		if warning.Level == "error" {
			status = "incompatible"
			break
		}
		if warning.Level == "warning" {
			status = "warning"
		}
	}
	return &telemetryHealthView{
		Status: status, Snapshots: diag.Snapshots, PresenceTracked: diag.PresenceTracked,
		Warnings: warnings, UnknownFields: diag.UnknownFields, FieldPresence: diag.FieldPresence,
		Compatibility: diag.CompatibilityReport(),
	}
}

// freshMatchView renders what AnalyzeFileAll just produced for one match.
func (s *server) freshMatchView(ctx context.Context, res *replay.AnalyzeResult, sourceFile string) matchView {
	v := s.buildMatchView(matchData{
		ctx:             res.MatchCtx,
		sourceFile:      sourceFile,
		analyzedAt:      time.Now(),
		framesProcessed: res.Result.FramesProcessed,
		invalidFrames:   res.Result.InvalidFrames,
		framesByPlayer:  res.Summary.FramesByPlayer,
		scores:          res.Result.PlayerScores,
		events:          res.Result.DetectionEvents,
		cases:           res.Result.ReviewCases,
		hasScore:        res.Summary.HasScore,
		blue:            res.Summary.BlueScore,
		orange:          res.Summary.OrangeScore,
	})
	v.Replaced, v.ClearedEvents, v.ClearedScores = res.Replaced, res.ClearedEvents, res.ClearedScores
	v.Telemetry = &telemetryView{
		FramesInserted: res.Telemetry.Inserted,
		FramesIgnored:  res.Telemetry.Ignored,
		TicksInserted:  res.Telemetry.TicksInserted,
		TicksIgnored:   res.Telemetry.TicksIgnored,
	}
	v.Diagnostics = diagnosticsView(res.Diagnostics)
	v.TelemetryHealth = telemetryHealth(res.Diagnostics)
	s.decorateMatchMetadata(ctx, &v)
	if w := res.Warnings(); len(w) > 0 {
		v.Warnings = w
	}
	v.Summary = res.MatchSummary
	return v
}

// storedMatchView rebuilds the view of a match from the store.
func (s *server) storedMatchView(ctx context.Context, matchID string) (matchView, error) {
	store := s.engine.Store()
	sm, err := store.GetStoredMatch(ctx, matchID)
	if err != nil {
		return matchView{}, err
	}
	events, err := store.GetMatchEvents(ctx, matchID)
	if err != nil {
		return matchView{}, fmt.Errorf("loading events: %w", err)
	}
	reviews, err := store.GetEventReviewsByMatch(ctx, matchID)
	if err != nil {
		return matchView{}, fmt.Errorf("loading event reviews: %w", err)
	}
	scores, err := store.GetMatchScores(ctx, matchID)
	if err != nil {
		return matchView{}, fmt.Errorf("loading scores: %w", err)
	}
	cases, err := store.GetReviewCasesByMatch(ctx, matchID)
	if err != nil {
		return matchView{}, fmt.Errorf("loading cases: %w", err)
	}
	counts, err := store.GetMatchPlayerFrameCounts(ctx, matchID)
	if err != nil {
		return matchView{}, fmt.Errorf("loading frame counts: %w", err)
	}
	blue, orange, hasScore, err := store.GetMatchFinalScore(ctx, matchID)
	if err != nil {
		return matchView{}, fmt.Errorf("loading final score: %w", err)
	}
	maxIdx, err := store.GetMaxFrameIndex(ctx, matchID)
	if err != nil {
		return matchView{}, err
	}
	mv := s.buildMatchView(matchData{
		ctx:             sm.Context,
		sourceFile:      filepath.Base(sm.Context.ReplayFile),
		analyzedAt:      sm.IngestedAt,
		framesProcessed: maxIdx + 1,
		framesByPlayer:  counts,
		scores:          scores,
		events:          events,
		eventReviews:    reviews,
		cases:           cases,
		hasScore:        hasScore,
		blue:            blue,
		orange:          orange,
	})
	mv.Summary = s.loadSummary(ctx, sm.Context, scores, events)
	diag, diagErr := storedTelemetryDiagnostics(ctx, store, matchID)
	if diagErr == nil {
		mv.TelemetryHealth = telemetryHealth(diag)
	}
	s.decorateMatchMetadata(ctx, &mv)
	return mv, nil
}

// ---- handlers --------------------------------------------------------------

// analyzeEntry is the outcome of one uploaded file. Matches lists every
// match the file holds, in file order (a recording holds two after a
// rematch in the same lobby); OK, MatchID and Match mirror the first match
// that was analyzed, or, when none was, Error, AlreadyStored and MatchID
// mirror the first match, so a client that reads one match per file sees
// the one that matters. A failure that stopped the file part-way is in
// Error, after the matches finished before it; a file that yielded no match
// at all carries the parse failure and a Diagnostic of what was uploaded.
type analyzeEntry struct {
	File          string          `json:"file"`
	OK            bool            `json:"ok"`
	Error         string          `json:"error,omitempty"`
	AlreadyStored bool            `json:"already_stored,omitempty"`
	MatchID       string          `json:"match_id,omitempty"`
	Match         *matchView      `json:"match,omitempty"`
	Diagnostic    *fileDiagnostic `json:"diagnostic,omitempty"`
	Matches       []matchEntry    `json:"matches,omitempty"`
}

// matchEntry is the outcome of one match of an uploaded file: analyzed
// (Match), or refused because it is already stored.
type matchEntry struct {
	OK            bool       `json:"ok"`
	Error         string     `json:"error,omitempty"`
	AlreadyStored bool       `json:"already_stored,omitempty"`
	MatchID       string     `json:"match_id"`
	Match         *matchView `json:"match,omitempty"`
}

// mirrorFirst fills the single-match fields from Matches (see analyzeEntry).
func (e *analyzeEntry) mirrorFirst() {
	for i := range e.Matches {
		if m := &e.Matches[i]; m.OK {
			e.OK, e.MatchID, e.Match = true, m.MatchID, m.Match
			return
		}
	}
	if len(e.Matches) > 0 {
		m := e.Matches[0]
		e.AlreadyStored, e.MatchID, e.Error = m.AlreadyStored, m.MatchID, m.Error
	}
}

// matchEntry renders one AnalyzeFileAll result.
func (s *server) matchEntry(ctx context.Context, res *replay.AnalyzeResult, sourceFile string) matchEntry {
	if res.AlreadyStored {
		id := res.MatchCtx.MatchID
		return matchEntry{AlreadyStored: true, MatchID: id,
			Error: fmt.Sprintf("match %s is already stored; tick \"Re-analyze\" to replace its detection events and scores", id)}
	}
	mv := s.freshMatchView(ctx, res, sourceFile)
	return matchEntry{OK: true, MatchID: mv.MatchID, Match: &mv}
}

// fileDiagnostic describes an upload that could not be parsed, in enough
// detail to paste to a developer: the container, the size, how many lines
// the file holds, what its first bytes look like and what was expected.
type fileDiagnostic struct {
	// Container is "zip" (PK signature), "text" or "empty".
	Container string `json:"container"`
	SizeBytes int64  `json:"size_bytes"`
	// Lines is the number of lines read: the file's own for text, the replay
	// entry's for a ZIP.
	Lines int `json:"lines"`
	// HeadText is the first diagHeadBytes bytes with non-printables escaped
	// (\t \n \r \\ and \xNN); HeadHex is the same bytes as hex.
	HeadText string `json:"head_text"`
	HeadHex  string `json:"head_hex"`
	// ZipEntries lists the archive's entries ("name (size)"), ZIPs only.
	ZipEntries []string `json:"zip_entries,omitempty"`
	// Findings are plain-language observations about the first line.
	Findings []string `json:"findings"`
	// Hint describes the expected layout.
	Hint string `json:"hint"`
}

const (
	diagHeadBytes = 160
	// diagProbeBytes bounds how much of the first line the probe reads.
	diagProbeBytes = 1 << 20
	// diagLineScanBytes bounds how much of a file (or ZIP entry) is read to
	// count its lines.
	diagLineScanBytes = adapter.DefaultMaxReplayBytes

	expectedLayoutHint = "Expected layout: one snapshot per line, `YYYY/MM/DD HH:MM:SS.mmm<TAB>{json}` " +
		"(a UTF-8 text file with one Echo VR session JSON per line, each prefixed by the recorder's " +
		"timestamp and a tab); or a ZIP archive containing that file."
)

// diagnoseUpload inspects a file the parser refused. It never fails: what it
// cannot read is reported as a finding.
func diagnoseUpload(path string) *fileDiagnostic {
	d := &fileDiagnostic{Findings: []string{}, Hint: expectedLayoutHint}
	f, err := os.Open(path)
	if err != nil {
		d.Container = "unreadable"
		d.Findings = append(d.Findings, "The uploaded file could not be opened: "+err.Error())
		return d
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil {
		d.SizeBytes = st.Size()
	}
	head := make([]byte, diagHeadBytes)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	d.HeadText, d.HeadHex = printableBytes(head), hexBytes(head)

	switch {
	case n == 0:
		d.Container = "empty"
		d.Findings = append(d.Findings, "The file is empty.")
		return d
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		d.Container = "zip"
		d.diagnoseZip(path)
		return d
	default:
		d.Container = "text"
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		d.Findings = append(d.Findings, "Could not re-read the file: "+err.Error())
		return d
	}
	d.diagnoseText(f)
	return d
}

// diagnoseText counts lines and checks the first non-empty line against
// the timestamp<TAB>json layout.
func (d *fileDiagnostic) diagnoseText(r io.Reader) {
	br := bufio.NewReaderSize(io.LimitReader(r, diagLineScanBytes), 64<<10)
	var first []byte
	firstDone := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			d.Lines++
			if !firstDone {
				first = append(first, chunk...)
				if len(first) > diagProbeBytes {
					first = first[:diagProbeBytes]
				}
				if err != bufio.ErrBufferFull {
					firstDone = true
				}
			}
		}
		if err == bufio.ErrBufferFull {
			d.Lines-- // the line continues in the next slice
			continue
		}
		if err != nil {
			break
		}
	}
	line := bytes.TrimRight(bytes.TrimPrefix(first, []byte("\xef\xbb\xbf")), "\r\n")
	if len(bytes.TrimSpace(line)) == 0 {
		d.Findings = append(d.Findings, "The first line is blank.")
		return
	}
	if c := line[0]; c == '{' || c == '[' {
		d.Findings = append(d.Findings, "The file starts with JSON instead of a timestamp prefix; an .echoreplay line begins with the recorder's clock.")
	}
	tab := bytes.IndexByte(line, '\t')
	if tab < 0 {
		d.Findings = append(d.Findings, "The first line has no TAB between the timestamp and the JSON.")
		return
	}
	prefix := string(line[:tab])
	if _, err := adapter.ParseReplayLineTime(prefix); err != nil {
		d.Findings = append(d.Findings, fmt.Sprintf("The first line's timestamp prefix %q does not parse as YYYY/MM/DD HH:MM:SS.mmm.", truncate(prefix, 40)))
	}
	payload := line[tab+1:]
	if !json.Valid(payload) {
		d.Findings = append(d.Findings, "The text after the TAB on the first line is not valid JSON.")
		return
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(payload, &top); err != nil {
		d.Findings = append(d.Findings, "The JSON on the first line is not an object.")
		return
	}
	for _, key := range []string{"sessionid", "teams"} {
		if _, ok := top[key]; !ok {
			d.Findings = append(d.Findings, fmt.Sprintf("The first snapshot has no %q key.", key))
		}
	}
}

// diagnoseZip lists the archive and counts the lines of its first file.
func (d *fileDiagnostic) diagnoseZip(path string) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		d.Findings = append(d.Findings, "The file starts with the ZIP signature but could not be opened as an archive: "+err.Error())
		return
	}
	defer zr.Close()
	var entry *zip.File
	for i, zf := range zr.File {
		if i < 10 {
			d.ZipEntries = append(d.ZipEntries, fmt.Sprintf("%s (%d bytes uncompressed)", zf.Name, zf.UncompressedSize64))
		}
		if entry == nil && !zf.FileInfo().IsDir() {
			entry = zf
		}
	}
	if len(zr.File) > 10 {
		d.ZipEntries = append(d.ZipEntries, fmt.Sprintf("… %d more", len(zr.File)-10))
	}
	if entry == nil {
		d.Findings = append(d.Findings, fmt.Sprintf("The archive holds no file (%d entries).", len(zr.File)))
		return
	}
	rc, err := entry.Open()
	if err != nil {
		d.Findings = append(d.Findings, fmt.Sprintf("The archive entry %q could not be opened: %v", entry.Name, err))
		return
	}
	defer rc.Close()
	d.Findings = append(d.Findings, fmt.Sprintf("Inspected the archive entry %q.", entry.Name))
	d.diagnoseText(rc)
}

func printableBytes(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == '\t':
			sb.WriteString(`\t`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\\':
			sb.WriteString(`\\`)
		case c >= 0x20 && c < 0x7f:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, `\x%02x`, c)
		}
	}
	return sb.String()
}

// hexBytes renders b as space-separated hex, 16 bytes per line.
func hexBytes(b []byte) string {
	var sb strings.Builder
	for i, c := range b {
		if i > 0 {
			if i%16 == 0 {
				sb.WriteByte('\n')
			} else {
				sb.WriteByte(' ')
			}
		}
		fmt.Fprintf(&sb, "%02x", c)
	}
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type analyzeResponse struct {
	Force   bool           `json:"force"`
	Results []analyzeEntry `json:"results"`
}

// uploadName reduces a client file name to a safe base name that keeps its
// extension (the parser picks the format from it).
func uploadName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "upload"
	}
	return name
}

var errUploadTooLarge = errors.New("replay exceeds the 4 GB desktop limit")

func saveUploadPart(dir string, idx int, part *multipart.Part) (path string, err error) {
	return saveUploadPartLimited(dir, idx, part, maxUploadFileBytes)
}

func saveUploadPartLimited(dir string, idx int, part *multipart.Part, maxBytes int64) (path string, err error) {
	sub := filepath.Join(dir, fmt.Sprint(idx))
	if err := os.MkdirAll(sub, 0o700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(sub)
	if err != nil {
		_ = os.Remove(sub)
		return "", err
	}
	defer func() {
		if closeErr := root.Close(); err == nil {
			err = closeErr
		}
		// The file cleanup defer below runs first. Once the root handle is
		// closed, remove the now-empty numbered directory as well.
		if err != nil {
			_ = os.Remove(sub)
		}
	}()
	name := uploadName(part.FileName())
	path = filepath.Join(sub, name)
	dst, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := dst.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = root.Remove(name)
		}
	}()
	written, err := io.Copy(dst, io.LimitReader(part, maxBytes+1))
	if err != nil {
		return "", err
	}
	if written > maxBytes {
		return "", errUploadTooLarge
	}
	if err := dst.Sync(); err != nil {
		return "", err
	}
	return path, nil
}

func (s *server) beginAnalysis() (context.Context, uint64) {
	ctx, cancel := context.WithCancel(context.Background())
	s.activeMu.Lock()
	s.activeID++
	id := s.activeID
	s.activeCtx = cancel
	s.activeMu.Unlock()
	return ctx, id
}

func (s *server) finishAnalysis(id uint64) {
	s.activeMu.Lock()
	if s.activeID == id {
		if s.activeCtx != nil {
			s.activeCtx()
		}
		s.activeCtx = nil
	}
	s.activeMu.Unlock()
}

func (s *server) cancelAnalysis() bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.activeCtx == nil {
		return false
	}
	s.activeCtx()
	return true
}

func (s *server) analysisActive() bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	return s.activeCtx != nil
}

func (s *server) handleCancelAnalyze(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": s.cancelAnalysis()})
}

func (s *server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadRequestBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad multipart upload: %v", err)
		return
	}
	// Desktop intake is deliberately idempotent from the user's point of view:
	// uploading a replay that is already in the database refreshes its derived
	// analysis instead of surfacing an "already stored" refusal. The CLI keeps
	// its explicit --force safety switch for automation and scripting.
	const force = true

	// Upload spooling, analysis and cleanup are one serialized transaction.
	// Otherwise the background crash-recovery worker could discover a large
	// file while the request is still writing it and queue the partial copy.
	s.analyzeMu.Lock()
	defer s.analyzeMu.Unlock()

	// Uploads are spooled beside the database before analysis. If the process
	// or laptop exits after upload, the next launch can resume these files
	// instead of asking the user to upload them again.
	tmp, err := os.MkdirTemp(s.runtime.pendingDir, "upload-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "creating durable upload queue: %v", err)
		return
	}
	cleanupAll := true
	defer func() {
		if cleanupAll {
			_ = os.RemoveAll(tmp)
		}
	}()

	type pendingUpload struct {
		entry analyzeEntry
		path  string
	}
	var uploads []pendingUpload
	for {
		part, nextErr := mr.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(nextErr, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "upload request exceeds 8 GB")
			} else {
				writeError(w, http.StatusBadRequest, "reading upload: %v", nextErr)
			}
			return
		}
		if part.FormName() != "files" || part.FileName() == "" {
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
			continue
		}
		entry := analyzeEntry{File: uploadName(part.FileName())}
		switch strings.ToLower(filepath.Ext(entry.File)) {
		case ".echoreplay", ".json":
			path, saveErr := saveUploadPart(tmp, len(uploads), part)
			if saveErr != nil {
				if errors.Is(saveErr, errUploadTooLarge) {
					entry.Error = errUploadTooLarge.Error()
				} else {
					var tooLarge *http.MaxBytesError
					if errors.As(saveErr, &tooLarge) {
						writeError(w, http.StatusRequestEntityTooLarge, "upload request exceeds 8 GB")
						_ = part.Close()
						return
					}
					entry.Error = "saving upload: " + saveErr.Error()
				}
			}
			uploads = append(uploads, pendingUpload{entry: entry, path: path})
		default:
			_, _ = io.Copy(io.Discard, part)
			entry.Error = "unsupported file type (expected .echoreplay or a legacy .json replay)"
			uploads = append(uploads, pendingUpload{entry: entry})
		}
		_ = part.Close()
	}
	if len(uploads) == 0 {
		writeError(w, http.StatusBadRequest, "no files uploaded (field \"files\")")
		return
	}
	// From here on, keep any file whose analysis does not finish. The recovery
	// worker can retry it after a power loss or explicit cancellation.
	cleanupAll = false

	// Analyses run one at a time; the engine's pipelines are per call and the
	// results page stays in upload order.
	analysisCtx, analysisID := s.beginAnalysis()
	defer s.finishAnalysis(analysisID)

	resp := analyzeResponse{Force: force, Results: make([]analyzeEntry, 0, len(uploads))}
	for _, upload := range uploads {
		entry, path := upload.entry, upload.path
		if entry.Error != "" || path == "" {
			resp.Results = append(resp.Results, entry)
			continue
		}
		if analysisCtx.Err() != nil {
			entry.Error = "analysis cancelled"
			resp.Results = append(resp.Results, entry)
			continue
		}
		// The analysis remains independent of a disconnected upload request,
		// but the explicit Cancel action can stop it at the next replay tick.
		queueID := s.runtime.queueStart(entry.File, "upload")
		started := time.Now()
		results, err := s.engine.AnalyzeFileAll(analysisCtx, path, force)
		recordAnalysisResults(analysisCtx, s.engine, results, "upload", time.Since(started))
		queueErr := err
		if queueErr == nil {
			for _, result := range results {
				if result != nil && result.PersistError() != nil {
					queueErr = result.PersistError()
					break
				}
			}
		}
		s.runtime.queueFinish(queueID, len(results), queueErr)
		for _, res := range results {
			entry.Matches = append(entry.Matches, s.matchEntry(analysisCtx, res, entry.File))
		}
		entry.mirrorFirst()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				entry.Error = "analysis cancelled"
			} else {
				entry.Error = err.Error()
			}
			if len(results) == 0 {
				entry.Diagnostic = diagnoseUpload(path)
			}
		}
		if queueErr == nil {
			_ = os.Remove(path)
			_ = os.Remove(filepath.Dir(path))
		}
		resp.Results = append(resp.Results, entry)
	}
	if entries, readErr := os.ReadDir(tmp); readErr == nil && len(entries) == 0 {
		_ = os.Remove(tmp)
	}
	writeJSON(w, http.StatusOK, resp)
}

type healthResponse struct {
	Version            string  `json:"version"`
	SchemaVersion      int     `json:"schema_version"`
	DatabasePath       string  `json:"database_path"`
	DatabaseBytes      int64   `json:"database_bytes"`
	DatabaseMainBytes  int64   `json:"database_main_bytes"`
	DatabaseWALBytes   int64   `json:"database_wal_bytes"`
	DatabaseSHMBytes   int64   `json:"database_shm_bytes"`
	StoredMatches      int     `json:"stored_matches"`
	ClipDirectory      string  `json:"clip_directory"`
	ClipFiles          int     `json:"clip_files"`
	ClipBytes          int64   `json:"clip_bytes"`
	SparkInstalled     bool    `json:"spark_installed"`
	SparkPath          string  `json:"spark_path,omitempty"`
	DiscSpeedCap       float64 `json:"disc_speed_cap"`
	AnalysisActive     bool    `json:"analysis_active"`
	DirectLabelCount   int     `json:"direct_label_count"`
	CalibrationMatches int     `json:"calibration_matches"`
	RawTicks           int     `json:"raw_ticks"`
	RawTickBytes       int64   `json:"raw_tick_bytes"`
	NormalizedFrames   int     `json:"normalized_frames"`
	NormalizedBytes    int64   `json:"normalized_bytes"`
	ArchiveDirectory   string  `json:"archive_directory"`
	ArchiveFiles       int     `json:"archive_files"`
	ArchiveBytes       int64   `json:"archive_bytes"`
}

func directoryStats(path string) (files int, bytes int64) {
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, statErr := entry.Info(); statErr == nil {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes
}

func (s *server) databasePath() string {
	path := s.engine.Store().Path()
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func (s *server) schemaVersion() int { return sqlite.SchemaVersion() }

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	path := s.databasePath()
	dbMainBytes := fileSize(path)
	dbWALBytes := fileSize(path + "-wal")
	dbSHMBytes := fileSize(path + "-shm")
	dbBytes := dbMainBytes + dbWALBytes + dbSHMBytes
	matches, err := s.engine.Store().GetStoredMatchCount(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "counting stored matches: %v", err)
		return
	}
	labelCount, err := s.engine.Store().GetEventReviewCount(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "counting detector labels: %v", err)
		return
	}
	labelCounts, err := s.engine.Store().MatchLabelCounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "counting calibration matches: %v", err)
		return
	}
	calibrationMatches := 0
	for _, count := range labelCounts {
		calibrationMatches += count
	}
	storageStats, err := s.engine.Store().GetStorageStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "measuring telemetry storage: %v", err)
		return
	}
	clipDir, _ := filepath.Abs(s.clipDir)
	clipFiles, clipBytes := directoryStats(clipDir)
	archiveDir, _ := filepath.Abs(s.archiveDir())
	archiveFiles, archiveBytes := s.rawArchiveStats()
	sparkPath, sparkErr := findSparkReplayViewer()
	writeJSON(w, http.StatusOK, healthResponse{
		Version: appVersion, SchemaVersion: sqlite.SchemaVersion(), DatabasePath: path,
		DatabaseBytes: dbBytes, DatabaseMainBytes: dbMainBytes, DatabaseWALBytes: dbWALBytes,
		DatabaseSHMBytes: dbSHMBytes, StoredMatches: matches, ClipDirectory: clipDir,
		ClipFiles: clipFiles, ClipBytes: clipBytes, SparkInstalled: sparkErr == nil,
		SparkPath: sparkPath, DiscSpeedCap: s.engine.Config().Physics.Constants().DiscSpeedCap,
		AnalysisActive: s.analysisActive(), DirectLabelCount: labelCount, CalibrationMatches: calibrationMatches,
		RawTicks: storageStats.RawTicks, RawTickBytes: storageStats.RawTickBytes,
		NormalizedFrames: storageStats.NormalizedFrames, NormalizedBytes: storageStats.NormalizedBytes,
		ArchiveDirectory: archiveDir, ArchiveFiles: archiveFiles, ArchiveBytes: archiveBytes,
	})
}

func (s *server) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	// Keep maintenance and replay replacement mutually exclusive. The SQLite
	// store also has one connection, but taking the analysis lock gives the UI
	// a predictable all-or-nothing maintenance result.
	s.analyzeMu.Lock()
	defer s.analyzeMu.Unlock()
	path := s.databasePath()
	before := fileSize(path) + fileSize(path+"-wal") + fileSize(path+"-shm")
	result, err := s.engine.Store().CheckAndCheckpoint(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "checking database: %v", err)
		return
	}
	after := fileSize(path) + fileSize(path+"-wal") + fileSize(path+"-shm")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "integrity": result.Integrity, "wal_frames": result.WALFrames,
		"checkpointed_frames": result.Checkpointed, "bytes_before": before, "bytes_after": after,
		"bytes_reclaimed": max(int64(0), before-after),
		"message":         "Database integrity is OK and committed WAL pages were checkpointed. Replay evidence was not removed.",
	})
}

func (s *server) handleBackup(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(filepath.Dir(s.databasePath()), "backups")
	name := "nevr-anticheat-" + time.Now().UTC().Format("20060102-150405.000000000") + ".db"
	path := filepath.Join(dir, name)
	if err := s.engine.Store().Backup(r.Context(), path); err != nil {
		writeError(w, http.StatusInternalServerError, "creating backup: %v", err)
		return
	}
	var bytes int64
	if info, err := os.Stat(path); err == nil {
		bytes = info.Size()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path, "bytes": bytes})
}

func openDirectory(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer.exe", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

func (s *server) handleOpenDataFolder(w http.ResponseWriter, _ *http.Request) {
	dir := filepath.Dir(s.databasePath())
	if err := openDirectory(dir); err != nil {
		writeError(w, http.StatusInternalServerError, "opening data folder: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": dir})
}

func (s *server) handleClearClips(w http.ResponseWriter, _ *http.Request) {
	abs, err := filepath.Abs(strings.TrimSpace(s.clipDir))
	if err != nil || strings.TrimSpace(s.clipDir) == "" {
		writeError(w, http.StatusInternalServerError, "invalid replay clip directory")
		return
	}
	entries, err := os.ReadDir(abs)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": 0})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reading replay clips: %v", err)
		return
	}
	removed := 0
	for _, entry := range entries {
		target := filepath.Join(abs, entry.Name())
		rel, relErr := filepath.Rel(abs, target)
		if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			writeError(w, http.StatusInternalServerError, "unsafe replay clip path")
			return
		}
		if err := os.RemoveAll(target); err != nil {
			writeError(w, http.StatusInternalServerError, "removing replay clip %s: %v", entry.Name(), err)
			return
		}
		removed++
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

func (s *server) handleEventReview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Verdict     string `json:"verdict"`
		Comment     string `json:"comment"`
		ReviewerID  string `json:"reviewer_id"`
		BlindReview bool   `json:"blind_review"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid review: %v", err)
		return
	}
	review, err := s.engine.Store().StoreEventReviewWithBlind(r.Context(), r.PathValue("id"), body.Verdict, body.Comment, body.ReviewerID, body.BlindReview)
	if errors.Is(err, sqlite.ErrNotFound) {
		writeError(w, http.StatusNotFound, "%v", err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	s.reconcileCalibrationChange(r.Context())
	writeJSON(w, http.StatusOK, review)
}

type crossMatchCaseView struct {
	CaseID          string         `json:"case_id"`
	PlayerID        string         `json:"player_id"`
	PlayerName      string         `json:"player_name"`
	MatchCount      int            `json:"match_count"`
	MatchIDs        []string       `json:"match_ids"`
	Severity        string         `json:"severity"`
	CumulativeScore float64        `json:"cumulative_score"`
	DecayedScore    float64        `json:"decayed_score"`
	Detectors       map[string]int `json:"detectors"`
	Status          string         `json:"status"`
	CreatedAt       string         `json:"created_at"`
}

type flaggedResponse struct {
	SingleMatch []caseView           `json:"single_match"`
	CrossMatch  []crossMatchCaseView `json:"cross_match"`
}

// nameResolver looks up display names through stored match contexts,
// caching one context per match for the request.
type nameResolver struct {
	ctx   context.Context
	store *sqlite.Store
	cache map[string]*model.MatchContext
}

func (n *nameResolver) context(matchID string) *model.MatchContext {
	if mc, ok := n.cache[matchID]; ok {
		return mc
	}
	var mc *model.MatchContext
	if sm, err := n.store.GetStoredMatch(n.ctx, matchID); err == nil {
		mc = sm.Context
	}
	n.cache[matchID] = mc
	return mc
}

func (n *nameResolver) name(matchIDs []string, pid string) string {
	for _, id := range matchIDs {
		if name := nameOf(n.context(id), pid); name != "" {
			return name
		}
	}
	return ""
}

func (s *server) handleFlagged(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	store := s.engine.Store()
	cases, err := store.GetPendingReviewCases(ctx, historyLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading cases: %v", err)
		return
	}
	xm, err := store.GetPendingCrossMatchReviewCases(ctx, historyLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading cross-match cases: %v", err)
		return
	}
	names := &nameResolver{ctx: ctx, store: store, cache: make(map[string]*model.MatchContext)}
	resp := flaggedResponse{SingleMatch: []caseView{}, CrossMatch: []crossMatchCaseView{}}
	for _, rc := range cases {
		resp.SingleMatch = append(resp.SingleMatch, s.caseView(rc, names.context(rc.MatchID)))
	}
	for _, rc := range xm {
		dets := rc.Detectors
		if dets == nil {
			dets = map[string]int{}
		}
		resp.CrossMatch = append(resp.CrossMatch, crossMatchCaseView{
			CaseID:          rc.CaseID,
			PlayerID:        rc.PlayerID,
			PlayerName:      names.name(rc.MatchIDs, rc.PlayerID),
			MatchCount:      rc.MatchCount,
			MatchIDs:        rc.MatchIDs,
			Severity:        rc.Severity,
			CumulativeScore: rc.CumulativeScore,
			DecayedScore:    rc.DecayedScore,
			Detectors:       dets,
			Status:          rc.Status,
			CreatedAt:       fmtTime(rc.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleObservations(w http.ResponseWriter, r *http.Request) {
	stats, err := s.engine.Store().ComputeObservationStats(r.Context(), time.Time{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading detector observations: %v", err)
		return
	}
	if stats == nil {
		stats = []sqlite.DetectorObservationStats{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stats":  stats,
		"notice": "Observation counts are not detector validation; thresholds require labeled real telemetry and moderator verdicts.",
	})
}

type rosterEntry struct {
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	Team     string `json:"team"`
}

type matchListEntry struct {
	MatchID            string        `json:"match_id"`
	SourceFile         string        `json:"source_file"`
	StartTime          string        `json:"start_time"`
	DurationSeconds    float64       `json:"duration_seconds"`
	GameMode           string        `json:"game_mode"`
	Map                string        `json:"map"`
	Source             string        `json:"source"`
	FrameCount         int           `json:"frame_count"`
	AnalyzedAt         string        `json:"analyzed_at"`
	Players            []rosterEntry `json:"players"`
	EventCount         int           `json:"event_count"`
	DetectorIDs        []string      `json:"detector_ids"`
	Flagged            bool          `json:"flagged"`
	CalibrationLabel   string        `json:"calibration_label,omitempty"`
	CalibrationComment string        `json:"calibration_comment,omitempty"`
}

func (s *server) handleMatches(w http.ResponseWriter, r *http.Request) {
	list, err := s.engine.Store().ListMatches(r.Context(), historyLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading matches: %v", err)
		return
	}
	matchIDs := make([]string, 0, len(list))
	for _, sm := range list {
		matchIDs = append(matchIDs, sm.Context.MatchID)
	}
	signals, err := s.engine.Store().GetMatchReviewSignals(r.Context(), matchIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading match review signals: %v", err)
		return
	}
	labels, err := s.engine.Store().GetMatchLabels(r.Context(), matchIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading match calibration labels: %v", err)
		return
	}
	out := make([]matchListEntry, 0, len(list))
	for _, sm := range list {
		mc := sm.Context
		signal := signals[mc.MatchID]
		e := matchListEntry{
			MatchID:         mc.MatchID,
			SourceFile:      filepath.Base(mc.ReplayFile),
			StartTime:       fmtTime(mc.StartTime),
			DurationSeconds: mc.Duration.Seconds(),
			GameMode:        mc.GameMode,
			Map:             mc.Map,
			Source:          mc.Source,
			FrameCount:      sm.FrameCount,
			AnalyzedAt:      fmtTime(sm.IngestedAt),
			Players:         []rosterEntry{},
			EventCount:      signal.EventCount,
			DetectorIDs:     signal.DetectorIDs,
			Flagged:         signal.Flagged,
		}
		if label, ok := labels[mc.MatchID]; ok {
			e.CalibrationLabel, e.CalibrationComment = label.Label, label.Comment
		}
		if mc.ReplayFile == "" {
			e.SourceFile = ""
		}
		for _, pid := range mc.PlayerIDs {
			e.Players = append(e.Players, rosterEntry{PlayerID: pid, Name: nameOf(mc, pid), Team: mc.TeamAssignments[pid]})
		}
		sort.SliceStable(e.Players, func(i, j int) bool {
			if ra, rb := teamRank(e.Players[i].Team), teamRank(e.Players[j].Team); ra != rb {
				return ra < rb
			}
			return e.Players[i].PlayerID < e.Players[j].PlayerID
		})
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": out})
}

func (s *server) handleMatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mv, err := s.storedMatchView(r.Context(), id)
	if errors.Is(err, sqlite.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no stored match %q", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading match %s: %v", id, err)
		return
	}
	writeJSON(w, http.StatusOK, mv)
}

// loadSummary returns the stored (or lazily rebuilt) match summary with the
// current suspicion verdicts applied, or nil when none can be produced.
func (s *server) loadSummary(ctx context.Context, mc *model.MatchContext, scores map[string]model.SuspicionScore, events []model.DetectionEvent) *replay.MatchSummary {
	sum, err := s.engine.LoadMatchSummary(ctx, mc, scores, events)
	if err != nil {
		return nil
	}
	return sum
}

// summaryFor loads the match view and returns its summary, writing the
// error response itself when there is none.
func (s *server) summaryFor(w http.ResponseWriter, r *http.Request) (*replay.MatchSummary, string, bool) {
	id := r.PathValue("id")
	mv, err := s.storedMatchView(r.Context(), id)
	if errors.Is(err, sqlite.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no stored match %q", id)
		return nil, id, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading match %s: %v", id, err)
		return nil, id, false
	}
	if mv.Summary == nil {
		writeError(w, http.StatusNotFound, "no summary for match %q (no raw ticks stored)", id)
		return nil, id, false
	}
	return mv.Summary, id, true
}

func (s *server) handleSummaryJSON(w http.ResponseWriter, r *http.Request) {
	sum, id, ok := s.summaryFor(w, r)
	if !ok {
		return
	}
	doc, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding summary: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+"-summary.json"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

func (s *server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	sum, id, ok := s.summaryFor(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+"-players.csv"))
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- csv.Writer produced an attachment, not executable HTML.
	_, _ = w.Write(sum.PlayersCSV())
}

func (s *server) handleReplayViewer(w http.ResponseWriter, _ *http.Request) {
	viewer, err := s.launchReplay("")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "Opened Spark Replay Viewer",
		"viewer":  viewer,
	})
}

func (s *server) handleReplayClip(w http.ResponseWriter, r *http.Request) {
	matchID, eventID := r.PathValue("id"), r.PathValue("event")
	events, err := s.engine.Store().GetMatchEvents(r.Context(), matchID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading match events: %v", err)
		return
	}
	var event *model.DetectionEvent
	for i := range events {
		if events[i].EventID == eventID {
			event = &events[i]
			break
		}
	}
	if event == nil {
		writeError(w, http.StatusNotFound, "no detection event %q in match %q", eventID, matchID)
		return
	}
	s.openReplayClip(w, r, *event)
}

func (s *server) handleReplayFrame(w http.ResponseWriter, r *http.Request) {
	frame, err := strconv.Atoi(r.PathValue("frame"))
	if err != nil || frame < 0 {
		writeError(w, http.StatusBadRequest, "invalid replay frame %q", r.PathValue("frame"))
		return
	}
	s.openReplayClip(w, r, model.DetectionEvent{
		MatchID: r.PathValue("id"), DetectorID: "THROW_LOG", FrameIndex: frame,
		FrameRangeStart: frame, FrameRangeEnd: frame,
	})
}

func (s *server) openReplayClip(w http.ResponseWriter, r *http.Request, event model.DetectionEvent) {
	clip, err := buildSparkReplayClip(r.Context(), s.engine.Store(), s.clipDir, event)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errRawReplayUnavailable) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, "building Spark replay clip: %v", err)
		return
	}
	viewer, err := s.launchReplay(clip.Path)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "%v (the incident clip was still saved to %s)", err, clip.Path)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"message":     fmt.Sprintf("Opened frames %d–%d in Spark Replay Viewer", clip.FrameStart, clip.FrameEnd),
		"clip_file":   clip.Path,
		"viewer":      viewer,
		"frame_start": clip.FrameStart,
		"frame_end":   clip.FrameEnd,
		"frames":      clip.Frames,
	})
}

func (s *server) handleMatchEvidence(w http.ResponseWriter, r *http.Request) {
	exporter := evidence.NewExporter(s.engine.Store())
	bundle, err := exporter.ExportForMatchPlayer(r.Context(), r.PathValue("id"), r.PathValue("player"), true, nil)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, evidence.ErrNoEvents) || errors.Is(err, evidence.ErrNoFrames) || errors.Is(err, sqlite.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, "building evidence review: %v", err)
		return
	}
	s.writeEvidenceHTML(w, bundle)
}

func (s *server) handleCaseEvidence(w http.ResponseWriter, r *http.Request) {
	exporter := evidence.NewExporter(s.engine.Store())
	bundle, err := exporter.ExportForCase(r.Context(), r.PathValue("id"), nil)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, evidence.ErrNoEvents) || errors.Is(err, evidence.ErrNoFrames) || errors.Is(err, sqlite.ErrNotFound) || strings.Contains(err.Error(), "no rows") {
			status = http.StatusNotFound
		}
		writeError(w, status, "building evidence review: %v", err)
		return
	}
	s.writeEvidenceHTML(w, bundle)
}

func (s *server) writeEvidenceHTML(w http.ResponseWriter, bundle *evidence.ReplayBundle) {
	doc, err := evidence.MarshalHTML(bundle)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding evidence review: %v", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="nevr-evidence.html"`)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- MarshalHTML escapes embedded evidence and CSP blocks all external content.
	_, _ = w.Write(doc)
}
