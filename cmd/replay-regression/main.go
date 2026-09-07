// replay-regression records and checks private replay behavior contracts using
// the same replay.Engine as nevr-ac and the desktop application.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/regression"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "capture" && args[0] != "check") {
		fmt.Fprintln(stderr, "Usage: replay-regression capture --manifest NEW.json --out NEW-DIR [--config FILE] REPLAY...\n       replay-regression check --manifest BASELINE.json --out NEW-DIR [--config FILE]\nPrivate outputs contain player IDs and raw evidence. Keep them outside version control. A passed baseline is not detector validation.")
		return 2
	}
	mode := args[0]
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "Versioned private JSON baseline (created by capture, read by check)")
	runDir := flags.String("out", "", "New directory for isolated analysis databases and report.json")
	configPath := flags.String("config", "", "Production TOML configuration; default is the app's built-in defaults")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *manifestPath == "" || *runDir == "" || (mode == "capture" && flags.NArg() == 0) || (mode == "check" && flags.NArg() != 0) {
		fmt.Fprintln(stderr, "--manifest and --out are required; capture also requires replay paths, check takes paths only from its pinned manifest")
		return 2
	}
	// #nosec G703 -- Local CLI operator explicitly selects this path; read-only preflight, followed by new-directory and O_EXCL file creation.
	if _, statErr := os.Stat(*runDir); !errors.Is(statErr, os.ErrNotExist) {
		fmt.Fprintln(stderr, "--out must be a new directory; existing files are never reused")
		return 2
	}
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var report regression.Report
	if mode == "capture" {
		// #nosec G703 -- Local CLI manifest path is intentional; this read-only check refuses existing files and WriteJSON also uses O_EXCL.
		if _, statErr := os.Stat(*manifestPath); !errors.Is(statErr, os.ErrNotExist) {
			fmt.Fprintln(stderr, "capture requires a new manifest path; existing baselines are never overwritten")
			return 2
		}
		var manifest regression.Manifest
		manifest, report, err = regression.Capture(ctx, cfg, flags.Args(), *runDir)
		if err == nil {
			err = regression.WriteJSON(*manifestPath, manifest)
		}
	} else {
		var manifest regression.Manifest
		manifest, err = regression.LoadManifest(*manifestPath)
		if err == nil {
			report, err = regression.Check(ctx, cfg, manifest, filepath.Dir(*manifestPath), *runDir)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		report.Passed = false
		// A preflight refusal creates no output directory. A failed run retains
		// its partial report and isolated DB, never a replacement baseline.
		// #nosec G703 -- Operator-selected run path was required absent above; this read-only check locates partial output and WriteJSON refuses replacement.
		if _, statErr := os.Stat(*runDir); statErr == nil {
			if writeErr := regression.WriteJSON(filepath.Join(*runDir, "report.json"), report); writeErr != nil {
				fmt.Fprintln(stderr, writeErr)
			}
		}
		return 2
	}
	if err := regression.WriteJSON(filepath.Join(*runDir, "report.json"), report); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	signals, windows := 0, 0
	for _, item := range report.Cases {
		windows += len(item.Windows)
		for _, match := range item.Actual {
			signals += len(match.Signals)
		}
	}
	fmt.Fprintf(stdout, "Regression %s: passed=%t, replays=%d, detector incidents=%d, labeled/behavior windows=%d\nReport: %s\n%s\n", mode, report.Passed, len(report.Cases), signals, windows, filepath.Join(*runDir, "report.json"), report.Notice)
	if !report.Passed {
		return 1
	}
	return 0
}
