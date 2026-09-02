package adapter

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	// DefaultMaxLineBytes bounds a single NDJSON line. Real session lines are
	// 10-40 KB; 8 MB leaves room for recorders that embed extra payloads.
	DefaultMaxLineBytes = 8 * 1024 * 1024
	// DefaultMaxReplayBytes bounds the uncompressed size read from a ZIP entry.
	DefaultMaxReplayBytes int64 = 500 * 1024 * 1024
)

// replayLineLayouts are the accepted formats of the per-line timestamp prefix.
var replayLineLayouts = []string{
	"2006/01/02 15:04:05.000",
	"2006/01/02 15:04:05",
}

// ParseReplayLineTime parses the 'YYYY/MM/DD HH:MM:SS.mmm' prefix of an
// .echoreplay line. The prefix carries no zone; it is interpreted as UTC, which
// is fine because only differences between lines are used.
func ParseReplayLineTime(prefix string) (time.Time, error) {
	prefix = strings.TrimSpace(prefix)
	var lastErr error
	for _, layout := range replayLineLayouts {
		t, err := time.Parse(layout, prefix)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

// ParsedTick is one mapped replay snapshot delivered by ParseFileStream.
type ParsedTick struct {
	// MatchID is the session id of the snapshot (the match every frame of
	// this tick belongs to), available from the first tick so streaming
	// callers can decide about the match before the file is fully read.
	MatchID string
	// FrameIndex is the mapper's index for this tick (shared by all Frames).
	FrameIndex int
	// SampleTime is the wall-clock time parsed from the line prefix.
	SampleTime time.Time
	// Frames are the mapped player frames of this tick (never empty).
	Frames []model.PlayerTelemetryFrame
	// RawJSON is the original session payload of the line.
	RawJSON string
}

// EchoReplayParser reads .echoreplay files.
// Format: NDJSON where each line is "YYYY/MM/DD HH:MM:SS.mmm\t{json_session_payload}"
// Files may be raw NDJSON or ZIP-compressed (containing the NDJSON file, possibly
// alongside metadata entries).
type EchoReplayParser struct {
	mapper *Mapper

	maxLineBytes   int
	maxReplayBytes int64

	// rawByFrame captures the original session JSON for each frame_index during
	// ParseFile. This preserves per-player stats, goal events, and other profiler
	// fields that the normalized PlayerTelemetryFrame does not carry.
	// ParseFileStream does not populate it; the raw payload travels with the tick.
	rawByFrame map[int]string
}

// NewEchoReplayParser creates a parser for .echoreplay files.
func NewEchoReplayParser() *EchoReplayParser {
	return &EchoReplayParser{
		mapper:         NewMapper(),
		maxLineBytes:   DefaultMaxLineBytes,
		maxReplayBytes: DefaultMaxReplayBytes,
		rawByFrame:     make(map[int]string),
	}
}

// SetPhysics forwards physics constants to the mapper (see Mapper.SetPhysics).
func (p *EchoReplayParser) SetPhysics(phys model.PhysicsConstants) { p.mapper.SetPhysics(phys) }

// SetDedupeIdentical enables the mapper's change detection for replay lines
// (see Mapper.SetDedupeIdentical). Off by default: recorded lines are trusted
// as distinct samples unless the operator opts in.
func (p *EchoReplayParser) SetDedupeIdentical(enabled bool) { p.mapper.SetDedupeIdentical(enabled) }

// SetMaxLineBytes overrides the per-line size cap.
func (p *EchoReplayParser) SetMaxLineBytes(n int) {
	if n > 0 {
		p.maxLineBytes = n
	}
}

// Mapper exposes the underlying mapper (for stats after parsing).
func (p *EchoReplayParser) Mapper() *Mapper { return p.mapper }

// RawSessionByFrame returns a map of frame_index → raw session JSON captured during parsing.
// Only populated after ParseFile has been called. Returns nil for non-.echoreplay sources.
func (p *EchoReplayParser) RawSessionByFrame() map[int]string {
	return p.rawByFrame
}

// ParseFile reads an .echoreplay file, auto-detecting ZIP vs raw NDJSON, and
// accumulates every mapped frame in memory (plus the raw payload per tick,
// see RawSessionByFrame). Callers that only need to stream frames should use
// ParseFileStream, which keeps memory bounded.
// Returns match context, telemetry frames, diagnostic report, and any error.
func (p *EchoReplayParser) ParseFile(path string) (*model.MatchContext, []model.PlayerTelemetryFrame, *DiagnosticReport, error) {
	var allFrames []model.PlayerTelemetryFrame
	matchCtx, diag, err := p.ParseFileStream(path, func(tick *ParsedTick) error {
		if _, exists := p.rawByFrame[tick.FrameIndex]; !exists {
			p.rawByFrame[tick.FrameIndex] = tick.RawJSON
		}
		allFrames = append(allFrames, tick.Frames...)
		return nil
	})
	if err != nil {
		return matchCtx, allFrames, diag, err
	}
	return matchCtx, allFrames, diag, nil
}

// ParseFileStream reads an .echoreplay file and calls fn once per mapped tick
// in file order. The match context returned at the end is the union of every
// snapshot's roster (late joiners and team changes included). Returning an
// error from fn aborts parsing with that error.
func (p *EchoReplayParser) ParseFileStream(path string, fn func(tick *ParsedTick) error) (*model.MatchContext, *DiagnosticReport, error) {
	// Detect ZIP by reading first 4 bytes (PK\x03\x04 magic)
	isZip, err := isZipFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("checking file format: %w", err)
	}

	if isZip {
		return p.parseZipReplay(path, fn)
	}
	return p.parseNDJSON(path, fn)
}

// parseZipReplay extracts the NDJSON entry from a ZIP archive and parses it.
func (p *EchoReplayParser) parseZipReplay(path string, fn func(*ParsedTick) error) (*model.MatchContext, *DiagnosticReport, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening zip: %w", err)
	}
	defer zr.Close()

	entry := selectZipEntry(zr.File)
	if entry == nil {
		return nil, nil, fmt.Errorf("zip archive contains no replay entry (%d entries)", len(zr.File))
	}

	// The central-directory size is advisory (a crafted file can understate
	// it), so the stream itself is bounded as well.
	if int64(entry.UncompressedSize64) > p.maxReplayBytes {
		return nil, nil, fmt.Errorf("replay too large: %d bytes (max %d)", entry.UncompressedSize64, p.maxReplayBytes)
	}

	rc, err := entry.Open()
	if err != nil {
		return nil, nil, fmt.Errorf("opening zip entry %s: %w", entry.Name, err)
	}
	defer rc.Close()

	limited := &boundedReader{r: rc, limit: p.maxReplayBytes}
	matchCtx, diag, err := p.parseReader(limited, filepath.Base(path), fn)
	if err != nil && errors.Is(err, errReplayTooLarge) {
		return matchCtx, diag, fmt.Errorf("replay entry %s exceeds %d bytes uncompressed", entry.Name, p.maxReplayBytes)
	}
	return matchCtx, diag, err
}

var errReplayTooLarge = errors.New("replay exceeds size limit")

// boundedReader fails once more than limit bytes have been read.
type boundedReader struct {
	r     io.Reader
	limit int64
	read  int64
}

func (b *boundedReader) Read(buf []byte) (int, error) {
	n, err := b.r.Read(buf)
	b.read += int64(n)
	if b.read > b.limit {
		return n, errReplayTooLarge
	}
	return n, err
}

// replayEntryExtensions are the entry names an .echoreplay ZIP may use for the
// NDJSON payload, in order of preference.
var replayEntryExtensions = []string{".echoreplay", ".ndjson", ".jsonl", ".json", ".txt"}

// selectZipEntry chooses the NDJSON payload inside a replay ZIP: directory
// entries, macOS resource forks and dot-files are ignored; entries with a
// known replay extension win, largest first; otherwise the largest file wins.
func selectZipEntry(files []*zip.File) *zip.File {
	var candidates []*zip.File
	for _, f := range files {
		name := f.Name
		if strings.HasSuffix(name, "/") || f.FileInfo().IsDir() {
			continue
		}
		if strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/__MACOSX/") {
			continue
		}
		if strings.HasPrefix(filepath.Base(name), ".") {
			continue
		}
		candidates = append(candidates, f)
	}
	if len(candidates) == 0 {
		return nil
	}
	rank := func(f *zip.File) int {
		ext := strings.ToLower(filepath.Ext(f.Name))
		for i, want := range replayEntryExtensions {
			if ext == want {
				return i
			}
		}
		return len(replayEntryExtensions)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ri, rj := rank(candidates[i]), rank(candidates[j])
		if ri != rj {
			return ri < rj
		}
		return candidates[i].UncompressedSize64 > candidates[j].UncompressedSize64
	})
	return candidates[0]
}

// parseNDJSON parses a raw (non-ZIP) NDJSON replay file.
func (p *EchoReplayParser) parseNDJSON(path string, fn func(*ParsedTick) error) (*model.MatchContext, *DiagnosticReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening replay: %w", err)
	}
	defer f.Close()

	return p.parseReader(f, filepath.Base(path), fn)
}

// parseReader parses NDJSON from any io.Reader, streaming ticks to fn.
func (p *EchoReplayParser) parseReader(r io.Reader, filename string, fn func(*ParsedTick) error) (*model.MatchContext, *DiagnosticReport, error) {
	diag := NewDiagnosticReport()
	var matchCtx *model.MatchContext

	scanner := bufio.NewScanner(r)
	initial := 256 * 1024
	if initial > p.maxLineBytes {
		initial = p.maxLineBytes
	}
	scanner.Buffer(make([]byte, 0, initial), p.maxLineBytes)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()

		// Skip empty lines
		if len(line) == 0 {
			continue
		}

		// Parse format: "TIMESTAMP\tJSON"
		tabIdx := strings.IndexByte(string(line), '\t')
		if tabIdx < 0 {
			diag.FramesRejected++
			continue
		}

		sampleTime, err := ParseReplayLineTime(string(line[:tabIdx]))
		if err != nil {
			diag.FramesRejected++
			diag.recordBadTimestamp()
			continue
		}

		jsonBytes := line[tabIdx+1:]
		if len(jsonBytes) < 2 {
			diag.FramesRejected++
			continue
		}

		var session EchoVRSessionResponse
		if err := json.Unmarshal(jsonBytes, &session); err != nil {
			diag.FramesRejected++
			continue
		}

		// Skip frames with no teams/players (lobby states, errors)
		if len(session.Teams) == 0 {
			diag.RecordSnapshotNoTeams()
			continue
		}

		// Record diagnostics with key-presence tracking.
		diag.RecordSessionWithJSON(&session, jsonBytes)

		// Map session to internal frames at the line's real sample time.
		result := p.mapper.MapSessionAt(&session, sampleTime)
		diag.RecordMappingResult(result)
		if result.MatchCtx != nil {
			if matchCtx == nil {
				matchCtx = result.MatchCtx
				matchCtx.ReplayFile = filename
			} else {
				MergeMatchContext(matchCtx, result.MatchCtx)
			}
		}
		if result.SkippedDuplicate || len(result.Frames) == 0 {
			continue
		}

		tick := &ParsedTick{
			MatchID:    matchCtx.MatchID,
			FrameIndex: result.Frames[0].FrameIndex,
			SampleTime: sampleTime,
			Frames:     result.Frames,
			RawJSON:    string(jsonBytes),
		}
		if err := fn(tick); err != nil {
			diag.RecordMapperStats(p.mapper.Stats())
			return matchCtx, diag, err
		}
	}

	diag.RecordMapperStats(p.mapper.Stats())

	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return matchCtx, diag, fmt.Errorf("reading replay: line %d exceeds the %d-byte line limit (EchoReplayParser.SetMaxLineBytes to raise it): %w", lineNum+1, p.maxLineBytes, err)
		}
		return matchCtx, diag, fmt.Errorf("reading replay at line %d: %w", lineNum, err)
	}

	if matchCtx == nil {
		return nil, diag, fmt.Errorf("no valid frames found in replay (%d lines read, %d rejected)", lineNum, diag.FramesRejected)
	}

	return matchCtx, diag, nil
}

// isZipFile checks if a file starts with the ZIP magic bytes (PK\x03\x04).
func isZipFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	var magic [4]byte
	n, err := f.Read(magic[:])
	if err != nil || n < 4 {
		return false, nil // not enough bytes = not a zip
	}
	return magic[0] == 0x50 && magic[1] == 0x4b && magic[2] == 0x03 && magic[3] == 0x04, nil
}
