package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const (
	replayClipBefore = 45
	replayClipAfter  = 45
)

var errRawReplayUnavailable = errors.New("raw replay ticks unavailable")

type replayLaunchFunc func(string) (string, error)

type replayClip struct {
	Path       string
	FrameStart int
	FrameEnd   int
	Frames     int
}

func defaultReplayClipDir() string {
	root, err := os.UserCacheDir()
	if err != nil || root == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "NEVR-Anticheat", "replay-clips")
}

// buildSparkReplayClip reconstructs the exact raw incident window as an
// uncompressed .echoreplay. Spark Replay Viewer accepts this native
// timestamp-tab-JSON format directly and starts at the first frame, so the
// reviewer lands immediately before the detection instead of opening an HTML
// page in a browser.
func buildSparkReplayClip(ctx context.Context, store *sqlite.Store, clipDir string, event model.DetectionEvent) (*replayClip, error) {
	start, end := event.FrameRangeStart, event.FrameRangeEnd
	if start < 0 || end < start || (start == 0 && end == 0 && event.FrameIndex != 0) {
		start, end = event.FrameIndex, event.FrameIndex
	}
	if event.FrameIndex < start {
		start = event.FrameIndex
	}
	if event.FrameIndex > end {
		end = event.FrameIndex
	}
	start -= replayClipBefore
	if start < 0 {
		start = 0
	}
	end += replayClipAfter

	rawTicks, err := store.GetMatchRawTicks(ctx, event.MatchID, start, end)
	if err != nil {
		return nil, fmt.Errorf("load raw replay ticks: %w", err)
	}
	if len(rawTicks) == 0 {
		return nil, fmt.Errorf("%w for match %s", errRawReplayUnavailable, event.MatchID)
	}
	timestamps, err := store.GetMatchTickTimestamps(ctx, event.MatchID)
	if err != nil {
		return nil, fmt.Errorf("load replay timestamps: %w", err)
	}
	mc, err := store.GetMatchContext(ctx, event.MatchID)
	if err != nil {
		return nil, fmt.Errorf("load match context: %w", err)
	}
	base := mc.StartTime
	if base.IsZero() {
		base = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	}

	indices := make([]int, 0, len(rawTicks))
	for idx := range rawTicks {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	if err := os.MkdirAll(clipDir, 0o755); err != nil {
		return nil, fmt.Errorf("create replay clip directory: %w", err)
	}
	pattern := fmt.Sprintf("nevr-%s-%s-frame-%d-*.echoreplay",
		safeClipName(event.MatchID), safeClipName(event.DetectorID), event.FrameIndex)
	f, err := os.CreateTemp(clipDir, pattern)
	if err != nil {
		return nil, fmt.Errorf("create replay clip: %w", err)
	}
	path := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()

	written := 0
	actualStart, actualEnd := 0, 0
	for _, idx := range indices {
		raw := []byte(rawTicks[idx])
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return nil, fmt.Errorf("raw replay tick %d is invalid JSON: %w", idx, err)
		}
		rel, found := timestamps[idx]
		if !found {
			// This is only a compatibility fallback for an old raw tick without
			// a corresponding normalized frame. Keep ordering and normal 15 Hz
			// playback; current ingestion always takes the exact branch above.
			rel = event.Timestamp + float64(idx-event.FrameIndex)/15.0
			if rel < 0 {
				rel = 0
			}
		}
		stamp := base.Add(time.Duration(rel * float64(time.Second))).Format("2006/01/02 15:04:05.000")
		if _, err := fmt.Fprintf(f, "%s\t%s\n", stamp, compact.Bytes()); err != nil {
			return nil, fmt.Errorf("write replay clip: %w", err)
		}
		if written == 0 {
			actualStart = idx
		}
		actualEnd = idx
		written++
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("flush replay clip: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close replay clip: %w", err)
	}
	ok = true
	return &replayClip{Path: path, FrameStart: actualStart, FrameEnd: actualEnd, Frames: written}, nil
}

func safeClipName(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	if b.Len() == 0 {
		return "event"
	}
	return b.String()
}

// replayViewerCandidates includes Spark's own install target first. The env
// override supports portable/custom installations without hard-coding them.
func replayViewerCandidates(goos string, getenv func(string) string, executableDir string) []string {
	var out []string
	add := func(path string) {
		path = strings.Trim(strings.TrimSpace(path), `"`)
		if path == "" {
			return
		}
		key := strings.ToLower(filepath.Clean(path))
		for _, old := range out {
			if strings.ToLower(filepath.Clean(old)) == key {
				return
			}
		}
		out = append(out, path)
	}
	add(getenv("NEVR_REPLAY_VIEWER"))
	switch goos {
	case "windows":
		for _, root := range []string{getenv("USERPROFILE"), getenv("OneDrive"), getenv("OneDriveConsumer")} {
			if root != "" {
				add(filepath.Join(root, "Documents", "Replay Viewer", "Replay Viewer.exe"))
			}
		}
		if executableDir != "" {
			add(filepath.Join(executableDir, "Replay Viewer", "Replay Viewer.exe"))
			add(filepath.Join(executableDir, "Replay Viewer.exe"))
		}
		if root := getenv("ProgramFiles"); root != "" {
			add(filepath.Join(root, "Oculus", "Software", "Software", "franzco-echodata", "Replay Viewer.exe"))
		}
		add("Replay Viewer.exe")
		add("ReplayViewer.exe")
	case "darwin":
		add("Replay Viewer")
	default:
		add("Replay Viewer")
		add("ReplayViewer")
	}
	return out
}

func resolvedReplayViewers() []string {
	executableDir := ""
	if executable, err := os.Executable(); err == nil {
		executableDir = filepath.Dir(executable)
	}
	var out []string
	for _, candidate := range replayViewerCandidates(runtime.GOOS, os.Getenv, executableDir) {
		command := candidate
		if filepath.IsAbs(candidate) {
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				continue
			}
		} else {
			resolved, err := exec.LookPath(candidate)
			if err != nil {
				continue
			}
			command = resolved
		}
		out = append(out, command)
	}
	return out
}

// findSparkReplayViewer resolves the preferred viewer without launching it.
func findSparkReplayViewer() (string, error) {
	if commands := resolvedReplayViewers(); len(commands) > 0 {
		return commands[0], nil
	}
	message := "replay viewer unavailable: open Replay Viewer in Spark once so it installs to Documents\\Replay Viewer, or set NEVR_REPLAY_VIEWER to Replay Viewer.exe"
	return "", errors.New(message)
}

func launchSparkReplayViewer(clipPath string) (string, error) {
	var lastErr error
	for _, command := range resolvedReplayViewers() {
		var args []string
		if strings.TrimSpace(clipPath) != "" {
			args = append(args, clipPath)
		}
		cmd := exec.Command(command, args...)
		if filepath.IsAbs(command) {
			cmd.Dir = filepath.Dir(command)
		}
		if err := cmd.Start(); err == nil {
			return command, nil
		} else {
			lastErr = err
		}
	}
	message := "replay viewer unavailable: open Replay Viewer in Spark once so it installs to Documents\\Replay Viewer, or set NEVR_REPLAY_VIEWER to Replay Viewer.exe"
	if lastErr != nil {
		return "", fmt.Errorf("%s: %w", message, lastErr)
	}
	return "", errors.New(message)
}
