package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const githubRepoAPI = "https://api.github.com/repos/MyGuyChromium/nevr-anticheat"

type desktopSettings struct {
	WatchFolder      string            `json:"watch_folder"`
	WatchEnabled     bool              `json:"watch_enabled"`
	AutomaticUpdates bool              `json:"automatic_update_checks"`
	SeenFiles        map[string]string `json:"seen_files,omitempty"`
}

type desktopRuntime struct {
	mu           sync.Mutex
	engine       *replay.Engine
	settingsPath string
	pendingDir   string
	supportDir   string
	settings     desktopSettings
	watchStatus  string
	watchError   string
	watchLast    time.Time
	watchCount   int
	recovering   bool
	recovered    int
	recoveryErr  string
	updateURL    string
	httpClient   *http.Client
	updateClient *http.Client
	updateDir    string
	updateMu     sync.Mutex
	updating     bool
	updateReady  func() (bool, string)
	launchUpdate func(updateLaunchRequest) error
	analyzeMu    *sync.Mutex
	stopped      chan struct{}
	resume       chan struct{}
	queue        []analysisQueueItem
	nextQueueID  int64
}

type analysisQueueItem struct {
	ID           int64  `json:"id"`
	File         string `json:"file"`
	Source       string `json:"source"`
	Status       string `json:"status"`
	Progress     int    `json:"progress"`
	Matches      int    `json:"matches"`
	Error        string `json:"error,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
	DurationMS   int64  `json:"duration_ms,omitempty"`
	startedClock time.Time
}

func (s *server) handleQueue(w http.ResponseWriter, _ *http.Request) {
	items, averageMS := s.runtime.queueSnapshot()
	running, failed := 0, 0
	for _, item := range items {
		switch item.Status {
		case "running":
			running++
		case "failed":
			failed++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "running": running, "failed": failed, "average_duration_ms": averageMS,
	})
}

func newDesktopRuntime(engine *replay.Engine, done <-chan struct{}) *desktopRuntime {
	base := filepath.Dir(engine.Store().Path())
	rt := &desktopRuntime{
		engine: engine, settingsPath: filepath.Join(base, "nevr-desktop-settings.json"),
		pendingDir: filepath.Join(base, ".nevr-pending"), supportDir: filepath.Join(base, "support-bundles"),
		updateURL: githubRepoAPI, httpClient: &http.Client{Timeout: 8 * time.Second},
		updateClient: &http.Client{Timeout: 10 * time.Minute}, updateDir: filepath.Join(base, "updates"),
		updateReady: updateInstallSupport, launchUpdate: launchUpdateHelper,
		settings:    desktopSettings{AutomaticUpdates: true, SeenFiles: make(map[string]string)},
		watchStatus: "idle", analyzeMu: &sync.Mutex{}, stopped: make(chan struct{}), resume: make(chan struct{}, 1),
	}
	_ = os.MkdirAll(rt.pendingDir, 0o700)
	cleanupOldUpdateArtifacts(rt.updateDir)
	_ = rt.loadSettings()
	go rt.loop(done)
	return rt
}

func (rt *desktopRuntime) queueStart(file, source string) int64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.nextQueueID++
	item := analysisQueueItem{ID: rt.nextQueueID, File: filepath.Base(file), Source: source,
		Status: "running", Progress: 10, StartedAt: fmtTime(time.Now()), startedClock: time.Now()}
	rt.queue = append([]analysisQueueItem{item}, rt.queue...)
	if len(rt.queue) > 100 {
		rt.queue = rt.queue[:100]
	}
	return item.ID
}

func (rt *desktopRuntime) queueFinish(id int64, matches int, err error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for i := range rt.queue {
		if rt.queue[i].ID != id {
			continue
		}
		rt.queue[i].Matches = matches
		rt.queue[i].Progress = 100
		rt.queue[i].FinishedAt = fmtTime(time.Now())
		rt.queue[i].DurationMS = time.Since(rt.queue[i].startedClock).Milliseconds()
		if err != nil {
			rt.queue[i].Status, rt.queue[i].Error = "failed", err.Error()
		} else {
			rt.queue[i].Status = "complete"
		}
		return
	}
}

func (rt *desktopRuntime) queueSnapshot() ([]analysisQueueItem, int64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := append([]analysisQueueItem(nil), rt.queue...)
	var total, count int64
	for _, item := range out {
		if item.Status == "complete" && item.DurationMS > 0 {
			total += item.DurationMS
			count++
		}
	}
	if count > 0 {
		return out, total / count
	}
	return out, 0
}

func (rt *desktopRuntime) loadSettings() error {
	doc, err := os.ReadFile(rt.settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings desktopSettings
	if err := json.Unmarshal(doc, &settings); err != nil {
		return err
	}
	if settings.SeenFiles == nil {
		settings.SeenFiles = make(map[string]string)
	}
	rt.settings = settings
	return nil
}

func (rt *desktopRuntime) saveSettingsLocked() error {
	doc, err := json.MarshalIndent(rt.settings, "", "  ")
	if err != nil {
		return err
	}
	tmp := rt.settingsPath + ".tmp"
	if err := os.WriteFile(tmp, doc, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, rt.settingsPath)
}

func (rt *desktopRuntime) loop(done <-chan struct{}) {
	defer close(rt.stopped)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	// Give the HTTP server time to start, then recover files that had already
	// completed upload when a previous process exited.
	timer := time.NewTimer(750 * time.Millisecond)
	select {
	case <-ctx.Done():
		timer.Stop()
		return
	case <-timer.C:
	}
	rt.resumePending(ctx)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rt.resume:
			rt.resumePending(ctx)
		case <-ticker.C:
			rt.mu.Lock()
			enabled := rt.settings.WatchEnabled
			rt.mu.Unlock()
			if enabled {
				rt.scanWatchFolder(ctx)
			}
		}
	}
}

func fileFingerprint(path string, info fs.FileInfo) string {
	h := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(path)) + "\x00" + fmt.Sprint(info.Size()) + "\x00" + info.ModTime().UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(h[:12])
}

func (rt *desktopRuntime) scanWatchFolder(ctx context.Context) (int, error) {
	if rt.updateInProgress() {
		return 0, errors.New("an update is being installed")
	}
	rt.mu.Lock()
	folder := rt.settings.WatchFolder
	if rt.watchStatus == "scanning" {
		rt.mu.Unlock()
		return 0, errors.New("watch scan already running")
	}
	rt.watchStatus, rt.watchError = "scanning", ""
	rt.mu.Unlock()
	finish := func(count int, err error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		rt.watchLast, rt.watchCount = time.Now(), count
		if err != nil {
			rt.watchStatus, rt.watchError = "error", err.Error()
		} else {
			rt.watchStatus = "ready"
		}
	}
	if folder == "" {
		err := errors.New("choose a replay watch folder first")
		finish(0, err)
		return 0, err
	}
	if st, err := os.Stat(folder); err != nil || !st.IsDir() {
		if err == nil {
			err = errors.New("path is not a folder")
		}
		finish(0, err)
		return 0, err
	}
	var files []string
	err := filepath.WalkDir(folder, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != folder && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".echoreplay") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		finish(0, err)
		return 0, err
	}
	sort.Strings(files)
	count := 0
	var failures []string
	rt.analyzeMu.Lock()
	defer rt.analyzeMu.Unlock()
	if rt.updateInProgress() {
		err := errors.New("an update is being installed")
		finish(0, err)
		return 0, err
	}
	for _, path := range files {
		if ctx.Err() != nil {
			finish(count, ctx.Err())
			return count, ctx.Err()
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		// Avoid reading a file while Spark is still writing it.
		if time.Since(info.ModTime()) < 3*time.Second {
			continue
		}
		fp := fileFingerprint(path, info)
		rt.mu.Lock()
		seen := rt.settings.SeenFiles[path] == fp
		rt.mu.Unlock()
		if seen {
			continue
		}
		queueID := rt.queueStart(path, "watch folder")
		started := time.Now()
		results, analyzeErr := rt.engine.AnalyzeFileAll(ctx, path, true)
		recordAnalysisResults(ctx, rt.engine, results, "watch", time.Since(started))
		failure := analyzeErr
		ok := analyzeErr == nil && len(results) > 0
		persistFailed := false
		for _, result := range results {
			if result.PersistError() != nil {
				ok = false
				persistFailed = true
				failure = result.PersistError()
			}
		}
		if !ok {
			if failure == nil {
				failure = errors.New("no match was found in the recording")
			}
			// Remember this exact failed fingerprint so an invalid recording does
			// not consume CPU every five seconds. Replacing or touching the file
			// changes the fingerprint and makes it eligible again.
			// A database write failure or cancellation is not a bad recording.
			// Leave these eligible for the next scan without touching the replay.
			if !persistFailed && ctx.Err() == nil && !errors.Is(failure, context.Canceled) && !errors.Is(failure, context.DeadlineExceeded) {
				rt.mu.Lock()
				rt.settings.SeenFiles[path] = fp
				_ = rt.saveSettingsLocked()
				rt.mu.Unlock()
			}
			failures = append(failures, filepath.Base(path)+": "+failure.Error())
			rt.queueFinish(queueID, len(results), failure)
			continue
		}
		rt.queueFinish(queueID, len(results), nil)
		rt.mu.Lock()
		rt.settings.SeenFiles[path] = fp
		_ = rt.saveSettingsLocked()
		rt.mu.Unlock()
		count++
	}
	if len(failures) > 0 {
		err := fmt.Errorf("%d replay(s) could not be analyzed; first error: %s", len(failures), failures[0])
		finish(count, err)
		return count, err
	}
	finish(count, nil)
	return count, nil
}

func (rt *desktopRuntime) pendingFiles() []string {
	var out []string
	_ = filepath.WalkDir(rt.pendingDir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".echoreplay" || ext == ".json" {
				out = append(out, path)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func (rt *desktopRuntime) resumePending(ctx context.Context) {
	if rt.updateInProgress() {
		return
	}
	rt.mu.Lock()
	if rt.recovering {
		rt.mu.Unlock()
		return
	}
	rt.recovering, rt.recoveryErr = true, ""
	rt.mu.Unlock()
	defer func() { rt.mu.Lock(); rt.recovering = false; rt.mu.Unlock() }()
	rt.analyzeMu.Lock()
	defer rt.analyzeMu.Unlock()
	if ctx.Err() != nil || rt.updateInProgress() {
		return
	}
	for _, path := range rt.pendingFiles() {
		if ctx.Err() != nil {
			return
		}
		queueID := rt.queueStart(path, "crash recovery")
		started := time.Now()
		results, err := rt.engine.AnalyzeFileAll(ctx, path, true)
		recordAnalysisResults(ctx, rt.engine, results, "recovery", time.Since(started))
		ok := err == nil && len(results) > 0
		for _, result := range results {
			if result.PersistError() != nil {
				ok = false
				err = errors.Join(err, result.PersistError())
			}
		}
		if ok {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = fmt.Errorf("analysis saved but pending replay cleanup failed: %w", removeErr)
			} else {
				if filepath.Clean(filepath.Dir(path)) != filepath.Clean(rt.pendingDir) {
					_ = os.Remove(filepath.Dir(path)) // Only empty child job directories, never the queue root.
				}
				rt.queueFinish(queueID, len(results), nil)
				rt.mu.Lock()
				rt.recovered++
				rt.mu.Unlock()
				continue
			}
		}
		if err == nil {
			err = errors.New("recovery did not persist a complete match")
		}
		if err != nil {
			rt.queueFinish(queueID, len(results), err)
			rt.mu.Lock()
			rt.recoveryErr = err.Error()
			rt.mu.Unlock()
		}
	}
}

func suggestedReplayFolders() []string {
	home, _ := os.UserHomeDir()
	local := os.Getenv("LOCALAPPDATA")
	candidates := []string{
		filepath.Join(home, "Documents", "EchoVR", "replays"),
		filepath.Join(home, "Documents", "Ready At Dawn", "Echo VR", "replays"),
		filepath.Join(home, "Documents", "Spark", "replays"),
		filepath.Join(home, "Documents", "Replay Viewer", "replays"),
		filepath.Join(local, "rad", "echovr", "replays"),
	}
	var out []string
	for _, path := range candidates {
		// #nosec G703 -- candidates are fixed replay locations below OS-provided user roots.
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			out = append(out, path)
		}
	}
	return out
}

func (s *server) handleSettings(w http.ResponseWriter, _ *http.Request) {
	rt := s.runtime
	rt.mu.Lock()
	defer rt.mu.Unlock()
	writeJSON(w, 200, map[string]any{"watch_folder": rt.settings.WatchFolder,
		"watch_enabled": rt.settings.WatchEnabled, "automatic_update_checks": rt.settings.AutomaticUpdates,
		"watch_status": rt.watchStatus, "watch_error": rt.watchError, "watch_last_scan": fmtTime(rt.watchLast),
		"watch_last_count": rt.watchCount, "suggested_watch_folders": suggestedReplayFolders()})
}

func (s *server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var req desktopSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, 400, "invalid settings: %v", err)
		return
	}
	req.WatchFolder = strings.TrimSpace(req.WatchFolder)
	if req.WatchEnabled {
		if req.WatchFolder == "" {
			writeError(w, 400, "choose a replay watch folder first")
			return
		}
		abs, err := filepath.Abs(req.WatchFolder)
		if err != nil {
			writeError(w, 400, "invalid watch folder: %v", err)
			return
		}
		st, err := os.Stat(abs)
		if err != nil || !st.IsDir() {
			writeError(w, 400, "watch folder does not exist or is not a directory")
			return
		}
		req.WatchFolder = abs
	}
	s.runtime.mu.Lock()
	previous := s.runtime.settings
	req.SeenFiles = s.runtime.settings.SeenFiles
	s.runtime.settings = req
	err := s.runtime.saveSettingsLocked()
	if err != nil {
		s.runtime.settings = previous
	}
	s.runtime.mu.Unlock()
	if err != nil {
		writeError(w, 500, "saving settings: %v", err)
		return
	}
	s.handleSettings(w, r)
}

func (s *server) handleWatchScan(w http.ResponseWriter, r *http.Request) {
	count, err := s.runtime.scanWatchFolder(r.Context())
	if err != nil {
		writeError(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "analyzed": count, "message": fmt.Sprintf("Watch-folder scan analyzed %d new replay(s).", count)})
}

func (s *server) handleRecovery(w http.ResponseWriter, _ *http.Request) {
	rt := s.runtime
	rt.mu.Lock()
	recovering, recovered, recoveryErr := rt.recovering, rt.recovered, rt.recoveryErr
	rt.mu.Unlock()
	files := rt.pendingFiles()
	writeJSON(w, 200, map[string]any{"recovering": recovering, "recovered": recovered, "pending": len(files), "error": recoveryErr})
}

func (s *server) handleRecoveryResume(w http.ResponseWriter, _ *http.Request) {
	select {
	case s.runtime.resume <- struct{}{}:
		writeJSON(w, 202, map[string]any{"ok": true, "message": "Recovery started. Uploaded replays are reanalyzed from their durable queue."})
	default:
		writeJSON(w, 202, map[string]any{"ok": true, "message": "Recovery is already queued or running."})
	}
}

func (s *server) handleRecoveryDiscard(w http.ResponseWriter, _ *http.Request) {
	rt := s.runtime
	if rt.updateInProgress() {
		writeError(w, http.StatusConflict, "an update is being installed; reopen NEVR after it finishes")
		return
	}
	// Do not remove a durable replay while the recovery worker is reading it.
	rt.analyzeMu.Lock()
	defer rt.analyzeMu.Unlock()
	if rt.updateInProgress() {
		writeError(w, http.StatusConflict, "an update is being installed; reopen NEVR after it finishes")
		return
	}
	base, err := filepath.Abs(rt.pendingDir)
	if err != nil {
		writeError(w, 500, "resolving recovery directory: %v", err)
		return
	}
	files := rt.pendingFiles()
	for _, path := range files {
		resolved, resolveErr := filepath.Abs(path)
		rel, relErr := filepath.Rel(base, resolved)
		if resolveErr != nil || relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			writeError(w, 500, "refusing to remove a file outside the recovery directory")
			return
		}
		if err := os.Remove(resolved); err != nil && !errors.Is(err, os.ErrNotExist) {
			writeError(w, 500, "removing pending upload: %v", err)
			return
		}
	}
	// Job directories contain only the durable copies enumerated above. The
	// resolved containment check keeps this recursive cleanup narrowly scoped.
	entries, _ := os.ReadDir(base)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		target, _ := filepath.Abs(filepath.Join(base, entry.Name()))
		rel, relErr := filepath.Rel(base, target)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			_ = os.RemoveAll(target)
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "removed": len(files), "message": fmt.Sprintf("Discarded %d pending upload(s). Stored matches and evidence were not changed.", len(files))})
}

type updateStatus struct {
	CurrentVersion   string `json:"current_version"`
	CurrentCommit    string `json:"current_commit"`
	BuildTime        string `json:"build_time"`
	LatestCommit     string `json:"latest_commit,omitempty"`
	Available        bool   `json:"available"`
	InstallSupported bool   `json:"install_supported"`
	InstallReason    string `json:"install_reason,omitempty"`
	ReleaseURL       string `json:"release_url"`
	CheckedAt        string `json:"checked_at"`
	Error            string `json:"error,omitempty"`
	LastInstallError string `json:"last_install_error,omitempty"`
}

func (s *server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	status := updateStatus{CurrentVersion: appVersion, CurrentCommit: buildCommit, BuildTime: buildTime,
		ReleaseURL: "https://github.com/MyGuyChromium/nevr-anticheat/releases/tag/windows-latest", CheckedAt: fmtTime(time.Now()),
		LastInstallError: s.runtime.lastUpdateError()}
	status.InstallSupported, status.InstallReason = s.runtime.updateReady()
	// Follow the rolling-release tag instead of master. The workflow only moves
	// this tag after every executable, the installer, and the portable ZIP are ready, so
	// the desktop never advertises an un-downloadable commit as an update.
	commit, err := s.runtime.latestReleaseCommit(r.Context())
	if err != nil {
		status.Error = err.Error()
		writeJSON(w, 200, status)
		return
	}
	status.LatestCommit = commit
	status.Available = buildCommit != "" && buildCommit != "development" && !sameCommit(buildCommit, commit)
	writeJSON(w, 200, status)
}

func (rt *desktopRuntime) beginUpdate() bool {
	rt.updateMu.Lock()
	defer rt.updateMu.Unlock()
	if rt.updating {
		return false
	}
	rt.updating = true
	return true
}

func (rt *desktopRuntime) finishUpdate() {
	rt.updateMu.Lock()
	rt.updating = false
	rt.updateMu.Unlock()
}

func (rt *desktopRuntime) updateInProgress() bool {
	rt.updateMu.Lock()
	defer rt.updateMu.Unlock()
	return rt.updating
}

func (s *server) handleInstallUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.runtime.beginUpdate() {
		writeError(w, http.StatusConflict, "an update is already being prepared")
		return
	}
	launched := false
	defer func() {
		if !launched {
			s.runtime.finishUpdate()
		}
	}()
	// This lock covers uploads, watched-folder analysis, and crash recovery.
	// TryLock makes the update a deliberate retry instead of waiting invisibly
	// behind a long replay, and holding it prevents new work during download.
	if !s.analyzeMu.TryLock() {
		writeError(w, http.StatusConflict, "finish or cancel the active replay analysis before updating")
		return
	}
	defer s.analyzeMu.Unlock()
	if s.analysisActive() {
		writeError(w, http.StatusConflict, "finish or cancel the active replay analysis before updating")
		return
	}
	if ok, reason := s.runtime.updateReady(); !ok {
		writeError(w, http.StatusBadRequest, "%s", reason)
		return
	}
	installer, commit, err := s.runtime.downloadVerifiedUpdate(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "preparing update: %v", err)
		return
	}
	expectedHash, err := stagedInstallerSHA256(installer)
	if err != nil {
		_ = os.Remove(installer)
		writeError(w, http.StatusInternalServerError, "reading staged update identity: %v", err)
		return
	}
	if err := s.runtime.launchUpdate(updateLaunchRequest{InstallerPath: installer, UpdateDir: s.runtime.updateDir, LatestCommit: commit, ExpectedHash: expectedHash}); err != nil {
		_ = os.Remove(installer)
		writeError(w, http.StatusInternalServerError, "starting update helper: %v", err)
		return
	}
	launched = true
	writeJSON(w, http.StatusAccepted, updateInstallResult{LatestCommit: commit,
		Message: "Update verified. NEVR will close, replace every program file, and reopen automatically; your evidence and settings stay in place."})
	go func() {
		time.Sleep(350 * time.Millisecond)
		s.quitOnce.Do(func() { close(s.quit) })
	}()
}

func (s *server) handleOpenUpdate(w http.ResponseWriter, _ *http.Request) {
	url := "https://github.com/MyGuyChromium/nevr-anticheat/releases/download/windows-latest/NEVR-Anticheat-Setup.exe"
	if err := openBrowser(url); err != nil {
		writeError(w, 500, "opening installer download: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "url": url, "message": "Opened the latest verified Windows installer download."})
}

func addZipJSON(zw *zip.Writer, name string, value any) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	doc, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(doc)
	return err
}

func (s *server) createSupportBundle(ctx context.Context) (string, error) {
	if err := os.MkdirAll(s.runtime.supportDir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(s.runtime.supportDir, "nevr-support-"+time.Now().UTC().Format("20060102-150405")+"-*.zip")
	if err != nil {
		return "", err
	}
	path := f.Name()
	zw := zip.NewWriter(f)
	fail := func(err error) (string, error) {
		_ = zw.Close()
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	readme, err := zw.Create("README.txt")
	if err != nil {
		return fail(err)
	}
	if _, err := io.WriteString(readme, "NEVR-Anticheat privacy-redacted support bundle\n\nContains app/schema health, aggregate detector statistics, and pseudonymized recent match metadata. It contains no raw replay ticks, normalized player frames, database file, watch-folder path, or executable.\n"); err != nil {
		return fail(err)
	}
	stats, _ := s.engine.Store().GetStorageStats(ctx)
	calibration, _ := s.engine.Store().ComputeCalibration(ctx, time.Time{})
	observations, _ := s.engine.Store().ComputeObservationStats(ctx, time.Time{})
	matches, _ := s.engine.Store().ListMatches(ctx, 25)
	redactor := &diagnosticRedactor{}
	matchData := make([]any, 0, len(matches))
	for _, sm := range matches {
		players := make([]string, 0, len(sm.Context.PlayerIDs))
		for _, pid := range sm.Context.PlayerIDs {
			players = append(players, redactor.pseudonym(pid))
		}
		matchData = append(matchData, map[string]any{"match": redactor.pseudonym(sm.Context.MatchID), "source": sm.Context.Source,
			"game_mode": sm.Context.GameMode, "map": sm.Context.Map, "start": fmtTime(sm.Context.StartTime), "players": players, "frames": sm.FrameCount})
	}
	if err := addZipJSON(zw, "runtime.json", map[string]any{"app_version": appVersion, "build_commit": buildCommit,
		"build_time": buildTime, "goos": runtime.GOOS, "goarch": runtime.GOARCH, "schema_version": sqlite.SchemaVersion(),
		"storage": stats}); err != nil {
		return fail(err)
	}
	if err := addZipJSON(zw, "calibration.json", calibration); err != nil {
		return fail(err)
	}
	if err := addZipJSON(zw, "observations.json", observations); err != nil {
		return fail(err)
	}
	if err := addZipJSON(zw, "recent_matches_redacted.json", matchData); err != nil {
		return fail(err)
	}
	if err := addZipJSON(zw, "telemetry_contract.json", map[string]any{"disc_speed_cap": s.engine.Physics().DiscSpeedCap,
		"supported_evidence_types": model.EvidenceTypes()}); err != nil {
		return fail(err)
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (s *server) handleSupportBundle(w http.ResponseWriter, r *http.Request) {
	path, err := s.createSupportBundle(r.Context())
	if err != nil {
		writeError(w, 500, "creating support bundle: %v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": path, "message": "Created a privacy-redacted support bundle."})
}
