// Command fly-agent-private runs the fly agent against a local Echo /session
// endpoint and writes abstract action intents as JSON Lines. It is private-only
// and dry-run-only: it has no controller, keyboard, process, matchmaking, or
// anti-cheat integration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/flyagent"
	"github.com/nevr-anticheat/nevr-anticheat/internal/flyagentlive"
)

const privateUsage = "usage: fly-agent-private --expected-session-id ID --player LOCAL_PLAYER_ID_OR_NAME --attack-goal negative-z|positive-z --session-limit DURATION [--session-url http://127.0.0.1:6721/session] [--output FILE.jsonl]"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fly-agent-private", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sessionURL := flags.String("session-url", "http://127.0.0.1:6721/session", "exact loopback-IP Echo /session URL")
	expectedSessionID := flags.String("expected-session-id", "", "exact private session ID expected on every sample")
	player := flags.String("player", "", "exact local player ID or display name to embody")
	attackGoal := flags.String("attack-goal", "", "goal end: negative-z or positive-z (telemetry does not identify attack direction)")
	sessionLimit := flags.Duration("session-limit", 0, "mandatory maximum run duration, at most 30m")
	pollInterval := flags.Duration("poll-interval", 67*time.Millisecond, "delay between completed /session polls")
	staleAfter := flags.Duration("stale-after", 750*time.Millisecond, "stop if distinct usable telemetry is absent for this long")
	requestTimeout := flags.Duration("request-timeout", 500*time.Millisecond, "per-request timeout, at most 5s")
	confirmations := flags.Int("private-confirmations", 3, "distinct private samples required before emitting action intents (2..10)")
	topologyPath := flags.String("topology", "", "bounded topology JSON; empty uses the synthetic plumbing circuit")
	adapterReportPath := flags.String("adapter-report", "", "validated arena training report whose topology/model match this run")
	outputPath := flags.String("output", "-", "dry-run JSONL trace path, or - for stdout (existing files are never replaced)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*expectedSessionID) == "" ||
		strings.TrimSpace(*player) == "" || strings.TrimSpace(*attackGoal) == "" || *sessionLimit == 0 {
		fmt.Fprintln(stderr, privateUsage)
		return 2
	}
	if ctx == nil {
		fmt.Fprintln(stderr, "fly-agent-private: operator context is required")
		return 2
	}

	topology, err := readPrivateTopology(*topologyPath)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-private: %v\n", err)
		return 2
	}
	if err := flyagent.ValidateAgentContract(topology); err != nil {
		fmt.Fprintf(stderr, "fly-agent-private: incompatible topology: %v\n", err)
		return 2
	}
	network, err := flyagent.NewNetwork(topology, flyagent.DefaultDynamics())
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-private: compile topology: %v\n", err)
		return 2
	}
	var verifiedPolicyAdapter flyagent.VerifiedPolicyAdapter
	if strings.TrimSpace(*adapterReportPath) != "" {
		_, capability, err := readPrivateAdapterReport(*adapterReportPath, topology, network.Dynamics())
		if err != nil {
			fmt.Fprintf(stderr, "fly-agent-private: adapter report: %v\n", err)
			return 2
		}
		verifiedPolicyAdapter = capability
	}
	source, err := flyagentlive.NewLoopbackSessionSource(*sessionURL, *requestTimeout)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-private: source: %v\n", err)
		return 2
	}
	defer source.CloseIdleConnections()

	output, finishOutput, err := privateTraceOutput(*outputPath, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-private: output: %v\n", err)
		return 2
	}
	finishedOutput := false
	defer func() {
		if !finishedOutput {
			if cleanupErr := finishOutput(false); cleanupErr != nil {
				fmt.Fprintf(stderr, "fly-agent-private: WARNING: incomplete output cleanup failed: %v\n", cleanupErr)
			}
		}
	}()
	trackedOutput := &errorTrackingWriter{writer: output}
	sink, err := flyagentlive.NewJSONDryRunSink(trackedOutput)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-private: dry-run sink: %v\n", err)
		return 2
	}

	config := flyagentlive.DefaultConfig()
	config.ExpectedSessionID = *expectedSessionID
	config.PlayerSelector = *player
	config.AttackGoal = flyagent.AttackGoal(*attackGoal)
	config.VerifiedPolicyAdapter = verifiedPolicyAdapter
	config.SessionLimit = *sessionLimit
	config.PollInterval = *pollInterval
	config.StaleAfter = *staleAfter
	config.PrivateConfirmations = *confirmations
	runner, err := flyagentlive.NewRunner(config, source, network, sink)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-private: configuration denied: %v\n", err)
		return 2
	}

	fmt.Fprintln(stderr, "fly-agent-private: DRY RUN ONLY; reading local private-session telemetry and writing abstract intents; no game input is sent")
	if topology.Dataset.Synthetic {
		fmt.Fprintln(stderr, "fly-agent-private: using the synthetic reflex topology; this verifies plumbing only and is not a MaleCNS simulation")
	}
	result, runErr := runner.Run(ctx)
	outputOK := trackedOutput.Err() == nil
	if finishErr := finishOutput(outputOK); finishErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("finalize output: %w", finishErr))
	}
	finishedOutput = true
	fmt.Fprintf(stderr, "fly-agent-private: stopped=%s polls=%d fresh=%d duplicates=%d actions=%d neutralizations=%d private_confirmations=%d\n",
		result.StopReason, result.Polls, result.FreshSamples, result.DuplicateSamples,
		result.Actions, result.Neutralizations, result.PrivateConfirmations)
	if runErr != nil {
		fmt.Fprintf(stderr, "fly-agent-private: safety stop: %v\n", runErr)
		return 1
	}
	return 0
}

func readPrivateTopology(path string) (*flyagent.Topology, error) {
	if strings.TrimSpace(path) == "" {
		return flyagent.DemoTopology()
	}
	file, err := os.Open(path) // #nosec G304 -- explicit operator-provided research topology.
	if err != nil {
		return nil, fmt.Errorf("open topology: %w", err)
	}
	defer file.Close()
	topology, err := flyagent.LoadTopology(file)
	if err != nil {
		return nil, fmt.Errorf("load topology: %w", err)
	}
	return topology, nil
}

func readPrivateAdapterReport(path string, topology *flyagent.Topology, dynamics flyagent.Dynamics) (*flyagent.AdapterTrainingReport, flyagent.VerifiedPolicyAdapter, error) {
	file, err := os.Open(path) // #nosec G304 -- explicit operator-provided training artifact.
	if err != nil {
		return nil, flyagent.VerifiedPolicyAdapter{}, err
	}
	defer file.Close()
	return flyagent.LoadVerifiedPolicyAdapter(file, topology, dynamics)
}

func privateTraceOutput(path string, stdout io.Writer) (io.Writer, func(bool) error, error) {
	if path == "-" {
		return stdout, func(bool) error { return nil }, nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit output, exclusive create prevents replacement.
	if err != nil {
		return nil, func(bool) error { return nil }, err
	}
	return file, func(success bool) error {
		closeErr := file.Close()
		if !success || closeErr != nil {
			// This invocation created this exact file with O_EXCL.
			return errors.Join(closeErr, os.Remove(path))
		}
		return nil
	}, nil
}

type errorTrackingWriter struct {
	mu     sync.Mutex
	writer io.Writer
	err    error
}

func (w *errorTrackingWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	written, err := w.writer.Write(payload)
	if err == nil && written != len(payload) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return written, err
}

func (w *errorTrackingWriter) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
