package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// EchoReplayParser reads .echoreplay files (ZIP-compressed NDJSON).
// Format: each line is "YYYY/MM/DD HH:MM:SS.mmm\t{json_session_payload}"
// The file may be raw NDJSON or ZIP-compressed containing a single NDJSON file.
type EchoReplayParser struct {
	mapper *Mapper
}

// NewEchoReplayParser creates a parser for .echoreplay files.
func NewEchoReplayParser() *EchoReplayParser {
	return &EchoReplayParser{
		mapper: NewMapper(),
	}
}

// ParseFile reads an .echoreplay file (raw NDJSON, not ZIP) and returns
// the match context and all telemetry frames.
// For ZIP files, extract first then call this on the inner file.
func (p *EchoReplayParser) ParseFile(path string) (*model.MatchContext, []model.PlayerTelemetryFrame, *DiagnosticReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opening replay: %w", err)
	}
	defer f.Close()

	diag := NewDiagnosticReport()
	var allFrames []model.PlayerTelemetryFrame
	var matchCtx *model.MatchContext

	scanner := bufio.NewScanner(f)
	// Replay lines can be very long (10KB+ per frame with 8 players)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Parse format: "TIMESTAMP\tJSON"
		tabIdx := strings.IndexByte(line, '\t')
		if tabIdx < 0 {
			continue // skip lines without tab separator
		}

		jsonStr := line[tabIdx+1:]

		var session EchoVRSessionResponse
		if err := json.Unmarshal([]byte(jsonStr), &session); err != nil {
			diag.FramesRejected++
			continue
		}

		// Record diagnostics
		diag.RecordSession(&session)

		// Map session to frames
		result := p.mapper.MapSession(&session)
		if matchCtx == nil && result.MatchCtx != nil {
			matchCtx = result.MatchCtx
			matchCtx.ReplayFile = filepath.Base(path)
		}

		allFrames = append(allFrames, result.Frames...)
	}

	if err := scanner.Err(); err != nil {
		return matchCtx, allFrames, diag, fmt.Errorf("reading replay: %w", err)
	}

	if matchCtx == nil {
		return nil, nil, diag, fmt.Errorf("no valid frames found in replay")
	}

	return matchCtx, allFrames, diag, nil
}
