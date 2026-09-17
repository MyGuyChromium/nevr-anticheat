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

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
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
	Path              string
	FrameStart        int
	FrameEnd          int
	Frames            int
	DerivedFromNative bool
}

func defaultReplayClipDir() string {
	root, err := os.UserCacheDir()
	if err != nil || root == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "NEVR-Anticheat", "replay-clips")
}

// buildSparkReplayClip reconstructs the selected stored incident window as an
// uncompressed .echoreplay. For a native tape source, its session JSON is a
// derived compatibility view, not the original capture or complete event history.
// Spark Replay Viewer accepts this
// timestamp-tab-JSON format directly and starts at the first frame, so the
// reviewer lands immediately before the detection instead of opening an HTML
// page in a browser.
func buildSparkReplayClip(ctx context.Context, store *sqlite.Store, clipDir string, event model.DetectionEvent) (*replayClip, error) {
	if event.FrameIndex < 0 {
		return nil, fmt.Errorf("%w: invalid incident frame", errRawReplayUnavailable)
	}
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
	end = min(end, int(^uint(0)>>1)-replayClipAfter) + replayClipAfter

	rawTicks, err := store.GetMatchRawTicks(ctx, event.MatchID, start, end)
	if err != nil {
		return nil, fmt.Errorf("load raw replay ticks: %w", err)
	}
	if len(rawTicks) == 0 {
		return nil, fmt.Errorf("%w for match %s", errRawReplayUnavailable, event.MatchID)
	}
	if _, ok := rawTicks[event.FrameIndex]; !ok {
		return nil, fmt.Errorf("%w for incident frame %d in match %s", errRawReplayUnavailable, event.FrameIndex, event.MatchID)
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

	if err := os.MkdirAll(clipDir, 0o700); err != nil {
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw := []byte(rawTicks[idx])
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return nil, fmt.Errorf("raw replay tick %d is invalid JSON: %w", idx, err)
		}
		var stamp string
		if mc.Source == "tape" {
			// Native event-only ticks have no normalized player timestamp. The
			// original header time and offset are authoritative for recording
			// timing, including sub-millisecond header precision. Never invent
			// a playback cadence or use a stale match-cache start time here.
			sample, err := adapter.NativeTapeSampleTime(rawTicks[idx])
			if err != nil {
				return nil, fmt.Errorf("native replay tick %d timestamp unavailable: %w", idx, err)
			}
			stamp = sample.Format(time.RFC3339Nano)
		} else {
			rel, found := timestamps[idx]
			if !found {
				// Compatibility fallback for legacy raw ticks without a
				// corresponding normalized frame. Native captures never use it.
				rel = event.Timestamp + float64(idx-event.FrameIndex)/15.0
				if rel < 0 {
					rel = 0
				}
			}
			stamp = base.Add(time.Duration(rel * float64(time.Second))).Format("2006/01/02 15:04:05.000")
		}
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
	return &replayClip{Path: path, FrameStart: actualStart, FrameEnd: actualEnd, Frames: written, DerivedFromNative: mc.Source == "tape"}, nil
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
//
// The folder nevr-desktop.exe runs from is deliberately not a candidate. The
// portable build is saved wherever the moderator likes, typically Desktop or
// Downloads, where any web page can drop a file named "Replay Viewer.exe"
// without a prompt; "Open in Spark" would then run it. A viewer kept in an
// unusual place is named with NEVR_REPLAY_VIEWER, which only the user can set.
func replayViewerCandidates(goos string, getenv func(string) string) []string {
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
	var untrusted []string
	if executable, err := os.Executable(); err == nil {
		untrusted = append(untrusted, filepath.Dir(executable))
	}
	if cwd, err := os.Getwd(); err == nil {
		untrusted = append(untrusted, cwd)
	}
	return resolveReplayViewers(replayViewerCandidates(runtime.GOOS, os.Getenv), os.Getenv("NEVR_REPLAY_VIEWER"), untrusted, exec.LookPath)
}

// resolveReplayViewers keeps the candidates that exist. A name found through
// PATH is refused when it resolves into one of the untrusted folders (the
// app's own folder and the working directory, which the app sets to its own
// folder): PATH must not bring the planted-binary problem back. The explicit
// override is the user's own choice and is exempt.
func resolveReplayViewers(candidates []string, override string, untrustedDirs []string, lookPath func(string) (string, error)) []string {
	override = strings.Trim(strings.TrimSpace(override), `"`)
	var out []string
	for _, candidate := range candidates {
		command := candidate
		if filepath.IsAbs(candidate) {
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				continue
			}
		} else {
			resolved, err := lookPath(candidate)
			if err != nil {
				continue
			}
			if absolute, err := filepath.Abs(resolved); err == nil {
				resolved = absolute
			}
			command = resolved
		}
		if candidate != override && insideAnyDir(command, untrustedDirs) {
			continue
		}
		out = append(out, command)
	}
	return out
}

func insideAnyDir(path string, dirs []string) bool {
	path = strings.ToLower(filepath.Clean(path))
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		dir = strings.ToLower(filepath.Clean(dir))
		if path == dir || strings.HasPrefix(path, strings.TrimRight(dir, `\/`)+string(filepath.Separator)) {
			return true
		}
	}
	return false
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
	return launchSparkReplayViewerWith(clipPath, resolvedReplayViewers(), func(cmd *exec.Cmd) error {
		if err := cmd.Start(); err != nil {
			return err
		}
		// The viewer outlives this HTTP request. Reap it on exit so repeated
		// launches do not retain process handles (or zombies on Unix).
		go func() { _ = cmd.Wait() }()
		return nil
	})
}

func launchSparkReplayViewerWith(clipPath string, commands []string, start func(*exec.Cmd) error) (string, error) {
	if strings.TrimSpace(clipPath) != "" {
		absolute, err := filepath.Abs(clipPath)
		if err != nil {
			return "", fmt.Errorf("resolving replay clip: %w", err)
		}
		clipPath = absolute
	}
	var lastErr error
	for _, command := range commands {
		var args []string
		if strings.TrimSpace(clipPath) != "" {
			args = append(args, clipPath)
		}
		cmd := exec.Command(command, args...)
		if filepath.IsAbs(command) {
			cmd.Dir = filepath.Dir(command)
		}
		if err := start(cmd); err == nil {
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
