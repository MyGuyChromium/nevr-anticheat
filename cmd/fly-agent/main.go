// Command fly-agent runs the development-only, offline connectome agent over
// an Echo replay and writes abstract action intents as JSON Lines.
//
// It has no live network source, matchmaking client or controller-input sink.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/flyagent"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fly-agent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	replayPath := flags.String("replay", "", "Echo .echoreplay or native .tape input")
	player := flags.String("player", "", "exact player ID or display name to embody")
	attackGoal := flags.String("attack-goal", "", "goal end: negative-z or positive-z (required; telemetry does not identify attack direction)")
	topologyPath := flags.String("topology", "", "bounded topology JSON; empty uses the synthetic plumbing circuit")
	outputPath := flags.String("output", "-", "action trace JSONL path, or - for stdout (existing files are never replaced)")
	maxTicks := flags.Int("max-ticks", 0, "maximum emitted action records; 0 processes the full replay")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*replayPath) == "" || strings.TrimSpace(*player) == "" || strings.TrimSpace(*attackGoal) == "" {
		fmt.Fprintln(stderr, "usage: fly-agent --replay FILE --player ID_OR_NAME --attack-goal negative-z|positive-z [--topology FILE] [--output FILE.jsonl]")
		return 2
	}
	if *maxTicks < 0 {
		fmt.Fprintln(stderr, "fly-agent: --max-ticks cannot be negative")
		return 2
	}
	if err := flyagent.AuthorizeExecution(flyagent.ModeReplay, false); err != nil {
		fmt.Fprintf(stderr, "fly-agent: execution policy: %v\n", err)
		return 1
	}

	encoder, err := flyagent.NewEncoder(*player, flyagent.AttackGoal(*attackGoal))
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent: %v\n", err)
		return 2
	}
	topology, err := readTopology(*topologyPath)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent: %v\n", err)
		return 1
	}
	if err := flyagent.ValidateAgentContract(topology); err != nil {
		fmt.Fprintf(stderr, "fly-agent: incompatible topology: %v\n", err)
		return 1
	}
	network, err := flyagent.NewNetwork(topology, flyagent.DefaultDynamics())
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent: compile topology: %v\n", err)
		return 1
	}
	if topology.Dataset.Synthetic {
		fmt.Fprintln(stderr, "fly-agent: using the synthetic reflex topology; this verifies plumbing only and is not MaleCNS simulation")
	}
	inputDigest, err := digestInputFile(*replayPath)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent: hash replay input: %v\n", err)
		return 1
	}

	output, finishOutput, err := actionOutput(*outputPath, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent: output: %v\n", err)
		return 1
	}
	finishedOutput := false
	defer func() {
		if !finishedOutput {
			if cleanupErr := finishOutput(false); cleanupErr != nil {
				fmt.Fprintf(stderr, "fly-agent: WARNING: incomplete output cleanup failed: %v\n", cleanupErr)
			}
		}
	}()
	buffered := bufio.NewWriter(output)
	jsonOut := json.NewEncoder(buffered)
	jsonOut.SetEscapeHTML(false)

	parser := adapter.NewEchoReplayParser()
	seen, emitted, absent := 0, 0, 0
	var firstAbsent, pendingResetReason string
	var priorSourceEpoch uint64
	var priorSourceEpochKnown bool
	stop := errors.New("requested action limit reached")
	_, diagnostics, parseErr := parser.ParseFileStream(*replayPath, func(tick *adapter.ParsedTick) error {
		seen++
		if tick.NewMatch {
			network.Reset()
			encoder.Reset()
			priorSourceEpochKnown = false
			pendingResetReason = "new_match"
		}
		observation, err := encoder.Encode(tick)
		if errors.Is(err, flyagent.ErrPlayerAbsent) {
			absent++
			network.Reset()
			pendingResetReason = appendResetReason(pendingResetReason, "player_absence")
			if firstAbsent == "" {
				firstAbsent = err.Error()
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("encode frame %d: %w", tick.FrameIndex, err)
		}
		resetReason := pendingResetReason
		pendingResetReason = ""
		if observation.SourceEpochKnown {
			if priorSourceEpochKnown && observation.SourceEpoch != priorSourceEpoch {
				network.Reset()
				resetReason = appendResetReason(resetReason, "source_epoch_change")
			}
			priorSourceEpoch = observation.SourceEpoch
			priorSourceEpochKnown = true
		}
		if observation.Sequence > 0 && observation.DeltaTime == 0 {
			network.Reset()
			resetReason = appendResetReason(resetReason, "timebase_reset")
		}
		var action flyagent.ActionIntent
		appliedDeltaTime := 0.0
		if blockReason := flyagent.ControlBlockReason(observation); blockReason != "" {
			network.Reset()
			resetReason = appendResetReason(resetReason, blockReason)
			action = flyagent.NeutralIntent(blockReason)
		} else {
			stepDeltaTime := observation.DeltaTime
			if observation.DeltaTime > network.Dynamics().MaxStep {
				network.Reset()
				resetReason = appendResetReason(resetReason, "telemetry_gap")
			}
			if resetReason != "" {
				// A reacquired observation is one sample, not evidence that its
				// endpoint currents persisted throughout the missing interval.
				stepDeltaTime = flyagent.NominalReplayStepSeconds
			}
			appliedDeltaTime, _, err = network.AppliedDeltaTime(stepDeltaTime)
			if err != nil {
				return fmt.Errorf("plan simulation at frame %d: %w", tick.FrameIndex, err)
			}
			if err := network.Step(stepDeltaTime, observation.Currents); err != nil {
				return fmt.Errorf("simulate frame %d: %w", tick.FrameIndex, err)
			}
			action = flyagent.Decode(observation, network)
		}
		observationDigest, err := flyagent.DigestObservation(observation)
		if err != nil {
			return fmt.Errorf("digest observation at frame %d: %w", tick.FrameIndex, err)
		}
		record := flyagent.ActionTraceRecord{
			Schema: flyagent.ActionTraceSchema, Mode: flyagent.ModeReplay,
			ObservationSchema: flyagent.ObservationSchema, RateModelSchema: flyagent.RateModelSchema,
			DecoderPolicySchema: flyagent.DecoderPolicySchema, Sequence: observation.Sequence,
			MatchID: observation.MatchID, FrameIndex: observation.FrameIndex,
			Timestamp: observation.Timestamp, DeltaTime: observation.DeltaTime,
			AppliedDeltaTime: appliedDeltaTime, SourceKind: observation.SourceKind,
			SourceID: observation.SourceID, SourceAuthority: observation.SourceAuthority,
			SourceTimeBasis: observation.SourceTimeBasis,
			SourceEpoch:     observation.SourceEpoch, SourceEpochKnown: observation.SourceEpochKnown,
			InputDigest: inputDigest, PlayerID: observation.PlayerID, AttackGoal: observation.AttackGoal,
			TargetKind: observation.TargetKind, Target: observation.Target,
			Action: action, ObservationDigest: observationDigest, StateDigest: network.StateDigest(),
			TopologyDigest: network.TopologyDigest(), ModelDigest: network.ModelDigest(),
			Dynamics: network.Dynamics(), StateResetReason: resetReason, Dataset: network.Dataset(),
		}
		if err := jsonOut.Encode(record); err != nil {
			return fmt.Errorf("write action trace: %w", err)
		}
		emitted++
		if *maxTicks > 0 && emitted >= *maxTicks {
			return stop
		}
		return nil
	})
	if errors.Is(parseErr, stop) {
		parseErr = nil
	}
	if parseErr == nil {
		verifiedDigest, digestErr := digestInputFile(*replayPath)
		if digestErr != nil {
			parseErr = fmt.Errorf("rehash replay input: %w", digestErr)
		} else if verifiedDigest != inputDigest {
			parseErr = errors.New("replay input changed while it was being processed")
		}
	}
	if err := buffered.Flush(); err != nil && parseErr == nil {
		parseErr = fmt.Errorf("flush action trace: %w", err)
	}
	if parseErr != nil {
		fmt.Fprintf(stderr, "fly-agent: replay failed after %d records: %v\n", emitted, parseErr)
		return 1
	}
	if emitted == 0 {
		if diagnostics != nil && diagnostics.FramesRejected > 0 {
			fmt.Fprintf(stderr, "fly-agent: no actions emitted; replay rejected %d source frames\n", diagnostics.FramesRejected)
		} else if firstAbsent != "" {
			fmt.Fprintf(stderr, "fly-agent: no actions emitted: %s\n", firstAbsent)
		} else {
			fmt.Fprintf(stderr, "fly-agent: no actions emitted for player %q\n", *player)
		}
		return 1
	}
	err = finishOutput(true)
	finishedOutput = true
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent: finalize output: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "fly-agent: emitted %d offline action records from %d ticks (%d ticks without selected player); dataset=%s@%s synthetic=%t\n",
		emitted, seen, absent, topology.Dataset.Name, topology.Dataset.Version, topology.Dataset.Synthetic)
	return 0
}

func appendResetReason(current, next string) string {
	if next == "" {
		return current
	}
	for _, existing := range strings.Split(current, "+") {
		if existing == next {
			return current
		}
	}
	if current == "" {
		return next
	}
	return current + "+" + next
}

func readTopology(path string) (*flyagent.Topology, error) {
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

func actionOutput(path string, stdout io.Writer) (io.Writer, func(bool) error, error) {
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
			// This exact file was created with O_EXCL by this invocation. Remove
			// an incomplete trace so a corrected retry is not blocked.
			removeErr := os.Remove(path)
			return errors.Join(closeErr, removeErr)
		}
		return nil
	}, nil
}

func digestInputFile(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- explicit operator-provided replay input.
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
