// Command fly-agent-benchmark measures local compile, memory, and deterministic
// rate-step costs for a bounded connectome topology.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/flyagent"
)

const benchmarkReportSchema = "nevr.fly.benchmark/v1"

type benchmarkReport struct {
	Schema                      string                   `json:"schema"`
	MeasuredAt                  time.Time                `json:"measured_at"`
	GoVersion                   string                   `json:"go_version"`
	GOOS                        string                   `json:"goos"`
	GOARCH                      string                   `json:"goarch"`
	GOMAXPROCS                  int                      `json:"gomaxprocs"`
	Dataset                     flyagent.DatasetMetadata `json:"dataset"`
	NodeCount                   int                      `json:"node_count"`
	EdgeCount                   int                      `json:"edge_count"`
	InputPopulationCount        int                      `json:"input_population_count"`
	OutputPopulationCount       int                      `json:"output_population_count"`
	TopologyDocumentBytes       int64                    `json:"topology_document_bytes,omitempty"`
	TopologyDigest              string                   `json:"topology_digest"`
	ModelDigest                 string                   `json:"model_digest"`
	Dynamics                    flyagent.Dynamics        `json:"dynamics"`
	DeltaTimeSeconds            float64                  `json:"delta_time_seconds"`
	WarmupSteps                 int                      `json:"warmup_steps"`
	MeasuredSteps               int                      `json:"measured_steps"`
	TimingBatchSteps            int                      `json:"timing_batch_steps"`
	TimingSampleCount           int                      `json:"timing_sample_count"`
	PlannedNodeEdgeUpdates      uint64                   `json:"planned_node_edge_updates"`
	CompileNanoseconds          int64                    `json:"compile_nanoseconds"`
	HeapAllocBeforeBytes        uint64                   `json:"heap_alloc_before_bytes"`
	HeapAllocAfterCompileBytes  uint64                   `json:"heap_alloc_after_compile_bytes"`
	HeapAllocCompileDeltaBytes  int64                    `json:"heap_alloc_compile_delta_bytes"`
	CoreStorageLowerBoundBytes  uint64                   `json:"core_storage_lower_bound_bytes"`
	MeanStepNanoseconds         int64                    `json:"mean_step_nanoseconds"`
	P50BatchMeanNanoseconds     int64                    `json:"p50_batch_mean_step_nanoseconds"`
	P95BatchMeanNanoseconds     int64                    `json:"p95_batch_mean_step_nanoseconds"`
	P99BatchMeanNanoseconds     int64                    `json:"p99_batch_mean_step_nanoseconds"`
	MaximumBatchMeanNanoseconds int64                    `json:"maximum_batch_mean_step_nanoseconds"`
	FinalStateDigest            string                   `json:"final_state_digest"`
	Limitations                 []string                 `json:"limitations"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fly-agent-benchmark", flag.ContinueOnError)
	flags.SetOutput(stderr)
	topologyPath := flags.String("topology", "", "bounded topology JSON; empty uses the synthetic plumbing circuit")
	outputPath := flags.String("output", "-", "JSON report path, or - for stdout (existing files are never replaced)")
	warmup := flags.Int("warmup", 10, "unmeasured warmup steps")
	steps := flags.Int("steps", 100, "measured network steps")
	deltaTime := flags.Float64("dt", flyagent.NominalReplayStepSeconds, "seconds per network step")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *warmup < 0 || *warmup > 10_000 || *steps < 1 || *steps > 10_000 {
		fmt.Fprintln(stderr, "usage: fly-agent-benchmark [--topology FILE] [--warmup 10] [--steps 100] [--dt SECONDS] [--output FILE]")
		return 2
	}
	dynamics := flyagent.DefaultDynamics()
	if math.IsNaN(*deltaTime) || math.IsInf(*deltaTime, 0) || *deltaTime <= 0 || *deltaTime > dynamics.MaxStep {
		fmt.Fprintf(stderr, "fly-agent-benchmark: --dt must be finite and in (0, %.3f]\n", dynamics.MaxStep)
		return 2
	}
	if err := preflightReportOutput(*outputPath); err != nil {
		fmt.Fprintf(stderr, "fly-agent-benchmark: output: %v\n", err)
		return 1
	}
	topology, documentBytes, err := readTopology(*topologyPath)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-benchmark: %v\n", err)
		return 1
	}
	if err := flyagent.ValidateAgentContract(topology); err != nil {
		fmt.Fprintf(stderr, "fly-agent-benchmark: incompatible topology: %v\n", err)
		return 1
	}
	work, err := boundedWork(topology, *warmup+*steps)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-benchmark: %v\n", err)
		return 1
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	compileStart := time.Now()
	network, err := flyagent.NewNetwork(topology, dynamics)
	compileDuration := time.Since(compileStart)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-benchmark: compile topology: %v\n", err)
		return 1
	}
	runtime.ReadMemStats(&after)
	currents := deterministicCurrents(topology)
	for range *warmup {
		if err := network.Step(*deltaTime, currents); err != nil {
			fmt.Fprintf(stderr, "fly-agent-benchmark: warmup: %v\n", err)
			return 1
		}
	}
	const timingBatchSteps = 16
	durations := make([]int64, 0, (*steps+timingBatchSteps-1)/timingBatchSteps)
	remaining := *steps
	var total int64
	for remaining > 0 {
		batchSteps := min(remaining, timingBatchSteps)
		started := time.Now()
		for range batchSteps {
			if err := network.Step(*deltaTime, currents); err != nil {
				fmt.Fprintf(stderr, "fly-agent-benchmark: measured step: %v\n", err)
				return 1
			}
		}
		elapsed := time.Since(started).Nanoseconds()
		total += elapsed
		durations = append(durations, elapsed/int64(batchSteps))
		remaining -= batchSteps
	}
	sorted := append([]int64(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	heapDelta := int64(after.HeapAlloc) - int64(before.HeapAlloc)            // #nosec G115 -- runtime counters are practically bounded by address space and subtraction is intentionally signed.
	nodes, edges := uint64(len(topology.Nodes)), uint64(len(topology.Edges)) // #nosec G115 -- strict topology limits are far below uint64.
	report := benchmarkReport{
		Schema: benchmarkReportSchema, MeasuredAt: time.Now().UTC(), GoVersion: runtime.Version(),
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GOMAXPROCS: runtime.GOMAXPROCS(0),
		Dataset: topology.Dataset, NodeCount: len(topology.Nodes), EdgeCount: len(topology.Edges),
		InputPopulationCount: len(topology.Inputs), OutputPopulationCount: len(topology.Outputs),
		TopologyDocumentBytes: documentBytes, TopologyDigest: network.TopologyDigest(), ModelDigest: network.ModelDigest(),
		Dynamics: dynamics, DeltaTimeSeconds: *deltaTime, WarmupSteps: *warmup, MeasuredSteps: *steps,
		TimingBatchSteps: timingBatchSteps, TimingSampleCount: len(durations), PlannedNodeEdgeUpdates: work,
		CompileNanoseconds: compileDuration.Nanoseconds(), HeapAllocBeforeBytes: before.HeapAlloc,
		HeapAllocAfterCompileBytes: after.HeapAlloc, HeapAllocCompileDeltaBytes: heapDelta,
		CoreStorageLowerBoundBytes: nodes*4*8 + edges*3*8,
		MeanStepNanoseconds:        total / int64(*steps), P50BatchMeanNanoseconds: percentile(sorted, 0.50),
		P95BatchMeanNanoseconds: percentile(sorted, 0.95), P99BatchMeanNanoseconds: percentile(sorted, 0.99),
		MaximumBatchMeanNanoseconds: sorted[len(sorted)-1], FinalStateDigest: network.StateDigest(),
		Limitations: []string{
			"Local wall-clock measurements include Go runtime, scheduler, map sorting, and host load; they are not real-match latency.",
			"Percentiles and maximum are nearest-rank values over per-step means from small timing batches; they are not per-step latency percentiles, and the final batch may contain fewer steps.",
			"Heap deltas are process snapshots and core storage is a lower bound that excludes maps, slices, allocator overhead, source JSON, and transient allocations.",
			"A rate-step benchmark is not biological validation or evidence of policy skill.",
		},
	}
	output, finish, err := reportOutput(*outputPath, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-benchmark: output: %v\n", err)
		return 1
	}
	finished := false
	defer func() {
		if !finished {
			_ = finish(false)
		}
	}()
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(stderr, "fly-agent-benchmark: encode report: %v\n", err)
		return 1
	}
	if err := finish(true); err != nil {
		fmt.Fprintf(stderr, "fly-agent-benchmark: finalize report: %v\n", err)
		return 1
	}
	finished = true
	fmt.Fprintf(stderr, "fly-agent-benchmark: %d nodes, %d edges, mean step %.3f ms, p95 %.3f ms\n",
		len(topology.Nodes), len(topology.Edges), float64(report.MeanStepNanoseconds)/1e6, float64(report.P95BatchMeanNanoseconds)/1e6)
	return 0
}

func preflightReportOutput(path string) error {
	if path == "-" {
		return nil
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("output path is empty")
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("output already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("inspect output directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("output parent is not a directory: %s", parent)
	}
	return nil
}

func readTopology(path string) (*flyagent.Topology, int64, error) {
	if strings.TrimSpace(path) == "" {
		topology, err := flyagent.DemoTopology()
		return topology, 0, err
	}
	file, err := os.Open(path) // #nosec G304 -- explicit operator-provided research topology.
	if err != nil {
		return nil, 0, fmt.Errorf("open topology: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat topology: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errors.New("topology path is not a regular file")
	}
	topology, err := flyagent.LoadTopology(file)
	if err != nil {
		return nil, 0, fmt.Errorf("load topology: %w", err)
	}
	return topology, info.Size(), nil
}

func deterministicCurrents(topology *flyagent.Topology) map[string]float64 {
	names := make([]string, 0, len(topology.Inputs))
	for name := range topology.Inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	currents := make(map[string]float64, len(names))
	for i, name := range names {
		currents[name] = float64((i%7)+1) / 8
	}
	return currents
}

func boundedWork(topology *flyagent.Topology, steps int) (uint64, error) {
	if topology == nil || steps < 1 {
		return 0, errors.New("benchmark work requires a topology and positive step count")
	}
	units := uint64(len(topology.Nodes)) + uint64(len(topology.Edges)) // #nosec G115 -- strict topology limits are far below uint64.
	const maximum = uint64(1_000_000_000)
	if units == 0 || uint64(steps) > maximum/units { // #nosec G115 -- steps is checked in 1..20000 by the caller.
		return 0, fmt.Errorf("benchmark exceeds %d node/edge updates; reduce --steps or --warmup", maximum)
	}
	return units * uint64(steps), nil // #nosec G115 -- steps is positive and bounded above.
}

func percentile(sorted []int64, fraction float64) int64 {
	index := int(math.Ceil(float64(len(sorted))*fraction)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func reportOutput(path string, stdout io.Writer) (io.Writer, func(bool) error, error) {
	if path == "-" {
		return stdout, func(bool) error { return nil }, nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit exclusive output.
	if err != nil {
		return nil, func(bool) error { return nil }, err
	}
	return file, func(success bool) error {
		closeErr := file.Close()
		if !success || closeErr != nil {
			return errors.Join(closeErr, os.Remove(path))
		}
		return nil
	}, nil
}
