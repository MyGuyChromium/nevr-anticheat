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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// maxUploadBytes bounds one /api/analyze request (all files together).
const maxUploadBytes = 200 << 20

// historyLimit bounds the History panel and the flagged lists.
const historyLimit = 200

//go:embed index.html
var indexHTML []byte

// server is the loopback HTTP app: one embedded page and a small JSON API,
// every route under a per-run random token so other local pages cannot
// reach it.
type server struct {
	engine *replay.Engine
	token  string
	mux    *http.ServeMux

	analyzeMu sync.Mutex // uploads are analyzed one at a time
	quitOnce  sync.Once
	quit      chan struct{}
}

func newServer(engine *replay.Engine, token string) *server {
	s := &server{engine: engine, token: token, mux: http.NewServeMux(), quit: make(chan struct{})}
	p := "/" + token
	s.mux.HandleFunc("GET "+p+"/{$}", s.handleIndex)
	s.mux.HandleFunc("POST "+p+"/api/analyze", s.handleAnalyze)
	s.mux.HandleFunc("GET "+p+"/api/flagged", s.handleFlagged)
	s.mux.HandleFunc("GET "+p+"/api/matches", s.handleMatches)
	s.mux.HandleFunc("GET "+p+"/api/match/{id}", s.handleMatch)
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
	IsShadow        bool    `json:"is_shadow"`
	MergedCount     int     `json:"merged_count"`
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

type matchView struct {
	MatchID         string           `json:"match_id"`
	Levels          model.LevelTable `json:"levels"`
	SourceFile      string           `json:"source_file"`
	StartTime       string           `json:"start_time"`
	DurationSeconds float64          `json:"duration_seconds"`
	GameMode        string           `json:"game_mode"`
	Map             string           `json:"map"`
	IsPrivate       bool             `json:"is_private"`
	Source          string           `json:"source"`
	HasScore        bool             `json:"has_score"`
	BlueScore       int              `json:"blue_score"`
	OrangeScore     int              `json:"orange_score"`
	FramesProcessed int              `json:"frames_processed"`
	InvalidFrames   int              `json:"invalid_frames"`
	PlayerFrames    int              `json:"player_frames"`
	AnalyzedAt      string           `json:"analyzed_at"`
	Replaced        bool             `json:"replaced"`
	ClearedEvents   int64            `json:"cleared_events"`
	ClearedScores   int64            `json:"cleared_scores"`
	Telemetry       *telemetryView   `json:"telemetry,omitempty"`
	Players         []playerView     `json:"players"`
	Events          []eventView      `json:"events"`
	Cases           []caseView       `json:"cases"`
	Diagnostics     *diagView        `json:"diagnostics,omitempty"`
	Warnings        []string         `json:"warnings"`
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
		v.Events = append(v.Events, eventView{
			EventID:         ev.EventID,
			DetectorID:      ev.DetectorID,
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
			IsShadow:        ev.IsShadow,
			MergedCount:     ev.MergedCount,
		})
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

// freshMatchView renders what AnalyzeFileAll just produced for one match.
func (s *server) freshMatchView(res *replay.AnalyzeResult, sourceFile string) matchView {
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
	if w := res.Warnings(); len(w) > 0 {
		v.Warnings = w
	}
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
	return s.buildMatchView(matchData{
		ctx:             sm.Context,
		sourceFile:      filepath.Base(sm.Context.ReplayFile),
		analyzedAt:      sm.IngestedAt,
		framesProcessed: maxIdx + 1,
		framesByPlayer:  counts,
		scores:          scores,
		events:          events,
		cases:           cases,
		hasScore:        hasScore,
		blue:            blue,
		orange:          orange,
	}), nil
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
func (s *server) matchEntry(res *replay.AnalyzeResult, sourceFile string) matchEntry {
	if res.AlreadyStored {
		id := res.MatchCtx.MatchID
		return matchEntry{AlreadyStored: true, MatchID: id,
			Error: fmt.Sprintf("match %s is already stored; tick \"Re-analyze\" to replace its detection events and scores", id)}
	}
	mv := s.freshMatchView(res, sourceFile)
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
			d.ZipEntries = append(d.ZipEntries, fmt.Sprintf("%s (%s)", zf.Name, fmtBytes(int64(zf.UncompressedSize64))))
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

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
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

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
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

func saveUpload(dir string, idx int, fh *multipart.FileHeader) (string, error) {
	src, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	sub := filepath.Join(dir, fmt.Sprint(idx))
	if err := os.MkdirAll(sub, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(sub, uploadName(fh.Filename))
	dst, err := os.Create(path)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return "", err
	}
	return path, dst.Close()
}

func (s *server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "upload exceeds %d MB", maxUploadBytes>>20)
			return
		}
		writeError(w, http.StatusBadRequest, "bad upload: %v", err)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "no files uploaded (field \"files\")")
		return
	}
	force := isTrue(r.FormValue("force"))

	tmp, err := os.MkdirTemp("", "nevr-desktop-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "creating temp dir: %v", err)
		return
	}
	defer os.RemoveAll(tmp)

	// Analyses run one at a time; the engine's pipelines are per call but
	// the results page is easier to read when files finish in upload order.
	s.analyzeMu.Lock()
	defer s.analyzeMu.Unlock()

	resp := analyzeResponse{Force: force, Results: make([]analyzeEntry, 0, len(files))}
	for i, fh := range files {
		entry := analyzeEntry{File: uploadName(fh.Filename)}
		switch strings.ToLower(filepath.Ext(entry.File)) {
		case ".echoreplay", ".json":
		default:
			entry.Error = "unsupported file type (expected .echoreplay or a legacy .json replay)"
			resp.Results = append(resp.Results, entry)
			continue
		}
		path, err := saveUpload(tmp, i, fh)
		if err != nil {
			entry.Error = "saving upload: " + err.Error()
			resp.Results = append(resp.Results, entry)
			continue
		}
		// The analysis is not tied to the request: a closed tab must not
		// leave a half-written match behind. Every match the file holds is
		// analyzed and reported (a recording holds two after a rematch).
		results, err := s.engine.AnalyzeFileAll(context.Background(), path, force)
		for _, res := range results {
			entry.Matches = append(entry.Matches, s.matchEntry(res, entry.File))
		}
		entry.mirrorFirst()
		if err != nil {
			entry.Error = err.Error()
			if len(results) == 0 {
				entry.Diagnostic = diagnoseUpload(path)
			}
		}
		resp.Results = append(resp.Results, entry)
	}
	writeJSON(w, http.StatusOK, resp)
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

type rosterEntry struct {
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	Team     string `json:"team"`
}

type matchListEntry struct {
	MatchID         string        `json:"match_id"`
	SourceFile      string        `json:"source_file"`
	StartTime       string        `json:"start_time"`
	DurationSeconds float64       `json:"duration_seconds"`
	GameMode        string        `json:"game_mode"`
	Map             string        `json:"map"`
	Source          string        `json:"source"`
	FrameCount      int           `json:"frame_count"`
	AnalyzedAt      string        `json:"analyzed_at"`
	Players         []rosterEntry `json:"players"`
}

func (s *server) handleMatches(w http.ResponseWriter, r *http.Request) {
	list, err := s.engine.Store().ListMatches(r.Context(), historyLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading matches: %v", err)
		return
	}
	out := make([]matchListEntry, 0, len(list))
	for _, sm := range list {
		mc := sm.Context
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
