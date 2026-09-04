// Command compat reads raw Echo VR JSON payloads or .echoreplay files
// and prints a compatibility/diagnostic report.
//
// Usage:
//
//	compat <session.json>              # single session JSON
//	compat --replay <file.echoreplay>  # full replay file (NDJSON, extracted from ZIP)
//	compat --strict <session.json>     # strict mode
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
)

func main() {
	strict := flag.Bool("strict", false, "Enable strict mode (fail on uncertain fields)")
	replayMode := flag.Bool("replay", false, "Parse as .echoreplay NDJSON file (not single session JSON)")
	maxFrames := flag.Int("max-frames", 500, "Max frames to process in replay mode")
	physicsAudit := flag.String("physics-audit", "", "Export frame-by-frame independent physics audit CSV (requires one replay path)")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: compat [flags] <file> [file2 ...]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fmt.Fprintln(os.Stderr, "  --replay       Parse as .echoreplay NDJSON (not single JSON)")
		fmt.Fprintln(os.Stderr, "  --strict       Fail on uncertain fields")
		fmt.Fprintln(os.Stderr, "  --max-frames N Max frames in replay mode (default 500)")
		fmt.Fprintln(os.Stderr, "  --physics-audit FILE Export raw and derived physics columns to CSV")
		os.Exit(1)
	}
	if *physicsAudit != "" {
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "--physics-audit requires exactly one replay input")
			os.Exit(2)
		}
		rows, err := writePhysicsAudit(args[0], *physicsAudit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Physics audit failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Physics audit wrote %d player-frames to %s\n", rows, *physicsAudit)
		return
	}

	if *replayMode {
		runReplayMode(args, *maxFrames)
	} else {
		runSessionMode(args, *strict)
	}
}

func runReplayMode(paths []string, maxFrames int) {
	for _, path := range paths {
		fmt.Printf("=== REPLAY: %s ===\n", path)

		parser := adapter.NewEchoReplayParser()
		matchCtx, frames, diag, err := parser.ParseFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			continue
		}

		// Limit output
		frameCount := len(frames)
		if frameCount > maxFrames {
			frameCount = maxFrames
		}

		fmt.Printf("Match ID:       %s\n", matchCtx.MatchID)
		fmt.Printf("Game Mode:      %s\n", matchCtx.GameMode)
		fmt.Printf("Map:            %s\n", matchCtx.Map)
		fmt.Printf("Players:        %d\n", len(matchCtx.PlayerIDs))
		fmt.Printf("Total frames:   %d\n", len(frames))
		fmt.Printf("Analyzed:       %d\n", frameCount)
		fmt.Println()

		// Print first few player IDs
		fmt.Println("Players:")
		for i, pid := range matchCtx.PlayerIDs {
			team := matchCtx.TeamAssignments[pid]
			fmt.Printf("  %s (%s)\n", pid, team)
			if i >= 9 {
				fmt.Printf("  ... and %d more\n", len(matchCtx.PlayerIDs)-10)
				break
			}
		}
		fmt.Println()

		// Sample frame data
		if len(frames) > 0 {
			f := frames[0]
			fmt.Println("First frame sample:")
			fmt.Printf("  PlayerID:     %s\n", f.PlayerID)
			fmt.Printf("  Position:     [%.2f, %.2f, %.2f]\n", f.Position[0], f.Position[1], f.Position[2])
			fmt.Printf("  LHand:        [%.2f, %.2f, %.2f]\n", f.LeftHandPosition[0], f.LeftHandPosition[1], f.LeftHandPosition[2])
			fmt.Printf("  RHand:        [%.2f, %.2f, %.2f]\n", f.RightHandPosition[0], f.RightHandPosition[1], f.RightHandPosition[2])
			fmt.Printf("  Stunned:      %v\n", f.IsStunned)
			fmt.Printf("  Possession:   %v\n", f.HasPossession)
			fmt.Printf("  Ping:         %.0f ms\n", f.EstimatedPingMs)
			fmt.Printf("  GamePhase:    %s\n", f.GamePhase)
			if f.Disc != nil {
				fmt.Printf("  Disc speed:   %.2f m/s\n", f.Disc.Speed)
				fmt.Printf("  Disc held:    %v\n", f.Disc.IsHeld)
			}
			rotStr := "identity"
			if f.Rotation != [4]float64{0, 0, 0, 1} {
				rotStr = fmt.Sprintf("[%.3f, %.3f, %.3f, %.3f]", f.Rotation[0], f.Rotation[1], f.Rotation[2], f.Rotation[3])
			}
			fmt.Printf("  Rotation:     %s\n", rotStr)
			lhrStr := "identity"
			if f.LeftHandRotation != [4]float64{0, 0, 0, 1} {
				lhrStr = fmt.Sprintf("[%.3f, %.3f, %.3f, %.3f]",
					f.LeftHandRotation[0], f.LeftHandRotation[1], f.LeftHandRotation[2], f.LeftHandRotation[3])
			}
			fmt.Printf("  LHand rot:    %s\n", lhrStr)
			fmt.Println()
		}

		// Print diagnostics
		fmt.Println(diag.FormatReport())
		fmt.Println(diag.CompatibilityReport())

		// Print position range summary
		if len(frames) > 100 {
			var minX, maxX, minY, maxY, minZ, maxZ float64
			minX, minY, minZ = 1e9, 1e9, 1e9
			maxX, maxY, maxZ = -1e9, -1e9, -1e9
			for _, f := range frames[:frameCount] {
				if f.Position[0] < minX {
					minX = f.Position[0]
				}
				if f.Position[0] > maxX {
					maxX = f.Position[0]
				}
				if f.Position[1] < minY {
					minY = f.Position[1]
				}
				if f.Position[1] > maxY {
					maxY = f.Position[1]
				}
				if f.Position[2] < minZ {
					minZ = f.Position[2]
				}
				if f.Position[2] > maxZ {
					maxZ = f.Position[2]
				}
			}
			fmt.Println("=== Position Ranges ===")
			fmt.Printf("  X: [%.1f, %.1f]\n", minX, maxX)
			fmt.Printf("  Y: [%.1f, %.1f]\n", minY, maxY)
			fmt.Printf("  Z: [%.1f, %.1f]\n", minZ, maxZ)
			fmt.Println()
		}

		// Check for issues
		fmt.Println("=== ISSUES ===")
		issues := 0
		if diag.ZeroHandRotations > 0 {
			fmt.Printf("  WARNING: %d frames with zero hand rotations (BIO_001/BIO_004 affected)\n", diag.ZeroHandRotations)
			issues++
		}
		if diag.PositionOutOfBounds > 0 {
			fmt.Printf("  WARNING: %d frames with position out of bounds\n", diag.PositionOutOfBounds)
			issues++
		}
		if diag.PossessionMultiplePlayers > 0 {
			fmt.Printf("  WARNING: %d frames with multiple players holding disc\n", diag.PossessionMultiplePlayers)
			issues++
		}
		if diag.HandFarFromBody > 0 {
			fmt.Printf("  WARNING: %d hand measurements > 2m from body\n", diag.HandFarFromBody)
			issues++
		}
		if issues == 0 {
			fmt.Println("  No issues detected.")
		}

		// Detector readiness
		fmt.Println()
		fmt.Println("=== DETECTOR READINESS ===")
		fd := diag.FieldPresence
		checkField := func(name, detectors string) {
			if f, ok := fd[name]; ok && f.Present > 0 {
				fmt.Printf("  %-25s READY  (%s)\n", name, detectors)
			} else {
				fmt.Printf("  %-25s MISSING (%s)\n", name, detectors)
			}
		}
		checkField("position", "ALL detectors")
		checkField("lhand.pos", "BIO_002, BIO_003, STATE_001, PAT_005, MOV_006")
		checkField("rhand.pos", "BIO_002, BIO_003, STATE_001, PAT_005, MOV_006")
		checkField("velocity", "MOV_006")
		checkField("lhand.forward", "BIO_001, BIO_004")
		checkField("rhand.forward", "BIO_001, BIO_004")
		checkField("disc.position", "THROW_001-008, STATE_001")
		checkField("disc.velocity", "THROW_001-008")
		checkField("possession", "THROW_001-008")
		checkField("stunned", "STATE_002, BIO_001, BIO_002")
		checkField("blocking", "STATE_003, STATE_005")
		checkField("ping", "THROW_001, MOV_001, MOV_002")

		_ = strings.TrimSpace // suppress unused import if needed
	}
}

func runSessionMode(paths []string, strict bool) {
	diag := adapter.NewDiagnosticReport()
	var strictMapper *adapter.StrictMapper
	normalMapper := adapter.NewMapper()
	if strict {
		strictMapper = adapter.NewStrictMapper()
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", path, err)
			continue
		}

		var session adapter.EchoVRSessionResponse
		if err := json.Unmarshal(data, &session); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing %s: %v\n", path, err)
			continue
		}

		// Record with the raw bytes so key presence is tracked and the
		// compatibility report can tell MISSING from present-but-false.
		diag.RecordSessionWithJSON(&session, data)

		if strict && strictMapper != nil {
			result := strictMapper.MapSessionStrict(&session)
			fmt.Printf("=== %s (STRICT MODE) ===\n", path)
			fmt.Printf("Frames mapped:  %d\n", len(result.Frames))
			fmt.Printf("Errors:         %d\n", len(result.Errors))
			for _, e := range result.Errors {
				fmt.Printf("  ERROR: player=%s field=%s: %s\n", e.PlayerName, e.Field, e.Message)
			}
			for _, se := range strictMapper.Errors() {
				fmt.Printf("  STRICT: player=%s field=%s issue=%s raw=%s\n",
					se.PlayerName, se.Field, se.Issue, se.RawValue)
			}
			fmt.Println()
		} else {
			result := normalMapper.MapSession(&session)
			fmt.Printf("=== %s ===\n", path)
			fmt.Printf("Frames mapped:  %d\n", len(result.Frames))
			fmt.Printf("Warnings:       %d\n", len(result.Warnings))
			fmt.Printf("Errors:         %d\n", len(result.Errors))
			for _, w := range result.Warnings {
				fmt.Printf("  WARN: %s: %s\n", w.Field, w.Message)
			}
			for _, e := range result.Errors {
				fmt.Printf("  ERROR: player=%s field=%s: %s\n", e.PlayerName, e.Field, e.Message)
			}
			fmt.Println()
		}
	}

	fmt.Println(diag.FormatReport())
	fmt.Println(diag.CompatibilityReport())
}
