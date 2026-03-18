package adapter

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// EchoReplayParser reads .echoreplay files.
// Format: NDJSON where each line is "YYYY/MM/DD HH:MM:SS.mmm\t{json_session_payload}"
// Files may be raw NDJSON or ZIP-compressed (containing a single NDJSON file).
type EchoReplayParser struct {
	mapper *Mapper

	// rawByFrame captures the original session JSON for each frame_index during parsing.
	// This preserves per-player stats, goal events, and other profiler fields that the
	// normalized PlayerTelemetryFrame does not carry.
	rawByFrame map[int]string
}

// NewEchoReplayParser creates a parser for .echoreplay files.
func NewEchoReplayParser() *EchoReplayParser {
	return &EchoReplayParser{
		mapper:     NewMapper(),
		rawByFrame: make(map[int]string),
	}
}

// RawSessionByFrame returns a map of frame_index → raw session JSON captured during parsing.
// Only populated after ParseFile has been called. Returns nil for non-.echoreplay sources.
func (p *EchoReplayParser) RawSessionByFrame() map[int]string {
	return p.rawByFrame
}

// ParseFile reads an .echoreplay file, auto-detecting ZIP vs raw NDJSON.
// Returns match context, telemetry frames, diagnostic report, and any error.
func (p *EchoReplayParser) ParseFile(path string) (*model.MatchContext, []model.PlayerTelemetryFrame, *DiagnosticReport, error) {
	// Detect ZIP by reading first 4 bytes (PK\x03\x04 magic)
	isZip, err := isZipFile(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("checking file format: %w", err)
	}

	if isZip {
		return p.parseZipReplay(path)
	}
	return p.parseNDJSON(path)
}

// parseZipReplay extracts the NDJSON file from a ZIP archive and parses it.
func (p *EchoReplayParser) parseZipReplay(path string) (*model.MatchContext, []model.PlayerTelemetryFrame, *DiagnosticReport, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opening zip: %w", err)
	}
	defer zr.Close()

	if len(zr.File) == 0 {
		return nil, nil, nil, fmt.Errorf("zip archive is empty")
	}

	// Use the first file in the archive (replays contain a single NDJSON file)
	entry := zr.File[0]

	// Safety: reject files larger than 500MB uncompressed
	if entry.UncompressedSize64 > 500*1024*1024 {
		return nil, nil, nil, fmt.Errorf("replay too large: %d bytes (max 500MB)", entry.UncompressedSize64)
	}

	rc, err := entry.Open()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opening zip entry %s: %w", entry.Name, err)
	}
	defer rc.Close()

	return p.parseReader(rc, filepath.Base(path))
}

// parseNDJSON parses a raw (non-ZIP) NDJSON replay file.
func (p *EchoReplayParser) parseNDJSON(path string) (*model.MatchContext, []model.PlayerTelemetryFrame, *DiagnosticReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opening replay: %w", err)
	}
	defer f.Close()

	return p.parseReader(f, filepath.Base(path))
}

// parseReader parses NDJSON from any io.Reader.
func (p *EchoReplayParser) parseReader(r io.Reader, filename string) (*model.MatchContext, []model.PlayerTelemetryFrame, *DiagnosticReport, error) {
	diag := NewDiagnosticReport()
	var allFrames []model.PlayerTelemetryFrame
	var matchCtx *model.MatchContext

	scanner := bufio.NewScanner(r)
	// Replay lines can be very long (10KB+ per frame with 8 players)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip empty lines
		if len(line) == 0 {
			continue
		}

		// Parse format: "TIMESTAMP\tJSON"
		tabIdx := strings.IndexByte(line, '\t')
		if tabIdx < 0 {
			diag.FramesRejected++
			continue
		}

		jsonStr := line[tabIdx+1:]
		if len(jsonStr) < 2 {
			diag.FramesRejected++
			continue
		}

		var session EchoVRSessionResponse
		if err := json.Unmarshal([]byte(jsonStr), &session); err != nil {
			diag.FramesRejected++
			continue
		}

		// Skip frames with no teams/players (lobby states, errors)
		if len(session.Teams) == 0 {
			continue
		}

		// Record diagnostics (samples first 3 payloads only)
		diag.RecordSession(&session)

		// Map session to internal frames
		result := p.mapper.MapSession(&session)
		if matchCtx == nil && result.MatchCtx != nil {
			matchCtx = result.MatchCtx
			matchCtx.ReplayFile = filename
		}

		// Capture raw session JSON keyed by frame_index for profiler-truth persistence.
		// All player-frames from this session snapshot share the same frame_index.
		for _, f := range result.Frames {
			if _, exists := p.rawByFrame[f.FrameIndex]; !exists {
				p.rawByFrame[f.FrameIndex] = jsonStr
			}
		}

		allFrames = append(allFrames, result.Frames...)
	}

	if err := scanner.Err(); err != nil {
		return matchCtx, allFrames, diag, fmt.Errorf("reading replay at line %d: %w", lineNum, err)
	}

	if matchCtx == nil {
		return nil, nil, diag, fmt.Errorf("no valid frames found in replay (%d lines read, %d rejected)", lineNum, diag.FramesRejected)
	}

	// Populate match context player list from all observed players
	if len(matchCtx.PlayerIDs) == 0 {
		seen := make(map[string]bool)
		for _, f := range allFrames {
			if !seen[f.PlayerID] {
				matchCtx.PlayerIDs = append(matchCtx.PlayerIDs, f.PlayerID)
				seen[f.PlayerID] = true
			}
		}
	}

	return matchCtx, allFrames, diag, nil
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
