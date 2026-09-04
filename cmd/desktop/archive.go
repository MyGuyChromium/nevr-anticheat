package main

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const rawArchiveVersion = 1

type rawArchiveManifest struct {
	Version       int    `json:"version"`
	MatchID       string `json:"match_id"`
	CreatedAt     string `json:"created_at"`
	TickCount     int    `json:"tick_count"`
	TicksSHA256   string `json:"ticks_sha256"`
	AppVersion    string `json:"app_version"`
	SchemaVersion int    `json:"schema_version"`
	Purpose       string `json:"purpose"`
}

type archivedRawTick struct {
	FrameIndex int             `json:"frame_index"`
	Raw        json.RawMessage `json:"raw"`
}

type rawArchiveResult struct {
	OK            bool   `json:"ok"`
	Path          string `json:"path"`
	Bytes         int64  `json:"bytes"`
	TicksArchived int    `json:"ticks_archived"`
	TicksPruned   int64  `json:"ticks_pruned"`
	SpaceReusable int64  `json:"space_reusable"`
	Message       string `json:"message"`
}

func (s *server) archiveDir() string {
	return filepath.Join(filepath.Dir(s.databasePath()), "archives")
}

func archivePrefix(matchID string) string {
	return "nevr-raw-" + safeClipName(matchID) + "-"
}

func writeArchiveJSON(zw *zip.Writer, name string, value any) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func writeArchivedTicks(zw *zip.Writer, indices []int, ticks map[int]string, digest hash.Hash) error {
	w, err := zw.Create("raw-ticks.ndjson")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(io.MultiWriter(w, digest))
	for _, idx := range indices {
		raw := ticks[idx]
		if !json.Valid([]byte(raw)) {
			return fmt.Errorf("raw tick %d is not valid JSON", idx)
		}
		if err := enc.Encode(archivedRawTick{FrameIndex: idx, Raw: json.RawMessage(raw)}); err != nil {
			return err
		}
	}
	return nil
}

// createRawArchive writes and verifies a full-fidelity local archive. It does
// not remove database rows; pruning is a separate step after verification.
func (s *server) createRawArchive(ctx context.Context, matchID string) (string, rawArchiveManifest, error) {
	ticks, err := s.engine.Store().GetAllMatchRawTicks(ctx, matchID)
	if err != nil {
		return "", rawArchiveManifest{}, err
	}
	if len(ticks) == 0 {
		return "", rawArchiveManifest{}, fmt.Errorf("match %s has no raw ticks to archive", matchID)
	}
	indices := make([]int, 0, len(ticks))
	for idx := range ticks {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	dir := s.archiveDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", rawArchiveManifest{}, err
	}
	tmp, err := os.CreateTemp(dir, archivePrefix(matchID)+"*.tmp")
	if err != nil {
		return "", rawArchiveManifest{}, err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			// #nosec G703 -- tmpPath was returned by CreateTemp inside archiveDir.
			_ = os.Remove(tmpPath)
		}
	}()
	zw := zip.NewWriter(tmp)
	digest := sha256.New()
	if err := writeArchivedTicks(zw, indices, ticks, digest); err != nil {
		_ = zw.Close()
		return "", rawArchiveManifest{}, err
	}
	manifest := rawArchiveManifest{
		Version: rawArchiveVersion, MatchID: matchID, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		TickCount: len(indices), TicksSHA256: hex.EncodeToString(digest.Sum(nil)), AppVersion: appVersion,
		SchemaVersion: s.schemaVersion(), Purpose: "Restorable full-fidelity archive of raw Echo/Spark source ticks",
	}
	if err := writeArchiveJSON(zw, "manifest.json", manifest); err != nil {
		_ = zw.Close()
		return "", rawArchiveManifest{}, err
	}
	if err := zw.Close(); err != nil {
		return "", rawArchiveManifest{}, err
	}
	if err := tmp.Sync(); err != nil {
		return "", rawArchiveManifest{}, err
	}
	if err := tmp.Close(); err != nil {
		return "", rawArchiveManifest{}, err
	}
	if _, _, err := readAndVerifyRawArchive(tmpPath, matchID); err != nil {
		return "", rawArchiveManifest{}, fmt.Errorf("verifying archive: %w", err)
	}
	finalName := archivePrefix(matchID) + time.Now().UTC().Format("20060102-150405.000000000") + ".raw.zip"
	finalPath := filepath.Join(dir, finalName)
	// #nosec G703 -- both paths are inside archiveDir and finalName uses safeClipName.
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", rawArchiveManifest{}, err
	}
	keep = true
	return finalPath, manifest, nil
}

func readAndVerifyRawArchive(path, expectedMatchID string) (rawArchiveManifest, map[int]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return rawArchiveManifest{}, nil, err
	}
	defer zr.Close()
	var manifest rawArchiveManifest
	ticks := make(map[int]string)
	digest := sha256.New()
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			return manifest, nil, err
		}
		switch file.Name {
		case "manifest.json":
			err = json.NewDecoder(r).Decode(&manifest)
		case "raw-ticks.ndjson":
			err = decodeArchivedTicks(io.TeeReader(r, digest), ticks)
		}
		_ = r.Close()
		if err != nil {
			return manifest, nil, err
		}
	}
	if manifest.Version != rawArchiveVersion || manifest.MatchID == "" || manifest.MatchID != expectedMatchID {
		return manifest, nil, fmt.Errorf("archive manifest does not match %s", expectedMatchID)
	}
	if len(ticks) != manifest.TickCount {
		return manifest, nil, fmt.Errorf("archive tick count is %d, manifest says %d", len(ticks), manifest.TickCount)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(got, manifest.TicksSHA256) {
		return manifest, nil, fmt.Errorf("raw tick checksum mismatch")
	}
	return manifest, ticks, nil
}

func decodeArchivedTicks(r io.Reader, out map[int]string) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	for scanner.Scan() {
		var row archivedRawTick
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return err
		}
		if row.FrameIndex < 0 || len(row.Raw) == 0 || !json.Valid(row.Raw) {
			return fmt.Errorf("invalid archived tick %d", row.FrameIndex)
		}
		if _, duplicate := out[row.FrameIndex]; duplicate {
			return fmt.Errorf("duplicate archived tick %d", row.FrameIndex)
		}
		out[row.FrameIndex] = string(row.Raw)
	}
	return scanner.Err()
}

func (s *server) latestRawArchive(matchID string) (string, error) {
	dir := s.archiveDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	prefix := archivePrefix(matchID)
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".raw.zip") {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	if len(paths) == 0 {
		return "", os.ErrNotExist
	}
	sort.Strings(paths)
	return paths[len(paths)-1], nil
}

func (s *server) rawArchiveStats() (files int, bytes int64) {
	return directoryStats(s.archiveDir())
}

func fileSize(path string) int64 {
	// #nosec G703 -- callers pass paths built by NEVR inside its private data directories.
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}

func archiveNotFound(err error) bool { return errors.Is(err, os.ErrNotExist) }
