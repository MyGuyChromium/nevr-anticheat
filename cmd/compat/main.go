// Command compat reads raw Echo VR JSON payloads and prints a compatibility report.
//
// Usage:
//
//	compat <session.json> [session2.json ...]
//	compat --strict <session.json>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
)

func main() {
	strict := flag.Bool("strict", false, "Enable strict mode (fail on uncertain fields)")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: compat [--strict] <session.json> [session2.json ...]")
		fmt.Fprintln(os.Stderr, "  Reads raw Echo VR /session JSON payloads and prints mapping diagnostics.")
		os.Exit(1)
	}

	diag := adapter.NewDiagnosticReport()
	var strictMapper *adapter.StrictMapper
	normalMapper := adapter.NewMapper()
	if *strict {
		strictMapper = adapter.NewStrictMapper()
	}

	for _, path := range args {
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

		// Record diagnostics
		diag.RecordSession(&session)

		// Run mapping
		if *strict && strictMapper != nil {
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

	// Print diagnostic report
	fmt.Println(diag.FormatReport())
	fmt.Println(diag.CompatibilityReport())
}
