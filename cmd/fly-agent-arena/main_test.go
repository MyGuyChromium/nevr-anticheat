package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/flyagent"
)

func TestRunTrainsAndEmitsStrictlyLoadableReport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--mode", "train", "--training-episodes", "1", "--evaluation-episodes", "1",
		"--passes", "1", "--max-steps", "4",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run code=%d stderr=%s", code, stderr.String())
	}
	report, err := flyagent.LoadAdapterTrainingReport(bytes.NewReader(stdout.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != flyagent.AdapterTrainingReportSchema || !report.FrozenConnectomeEdges {
		t.Fatalf("training report = %+v", report)
	}
	if !strings.Contains(stderr.String(), "plumbing metrics") {
		t.Fatalf("missing synthetic warning: %s", stderr.String())
	}
	reportPath := filepath.Join(t.TempDir(), "training-report.json")
	if err := os.WriteFile(reportPath, stdout.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	wantReportDigest := sha256.Sum256(stdout.Bytes())
	stdout.Reset()
	stderr.Reset()
	code = run([]string{
		"--mode", "compare-trained", "--adapter-report", reportPath,
		"--baseline", "weight", "--max-steps", "4",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("compare-trained code=%d stderr=%s", code, stderr.String())
	}
	var comparison flyagent.ComparisonReport
	if err := json.Unmarshal(stdout.Bytes(), &comparison); err != nil {
		t.Fatal(err)
	}
	if comparison.ExperimentMode != "verified_trained_adapter_control" || comparison.PolicyAdapterReportDigest != fmt.Sprintf("%x", wantReportDigest[:]) {
		t.Fatalf("trained comparison provenance = %+v", comparison)
	}
}

func TestRunComparesAndRefusesExistingOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "compare", "--evaluation-episodes", "2", "--max-steps", "4"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run code=%d stderr=%s", code, stderr.String())
	}
	var report flyagent.ComparisonReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != flyagent.ComparisonReportSchema || len(report.PairedDeltas) != 2 || !report.ParameterParity.ExactWeightMultiset {
		t.Fatalf("comparison report = %+v", report)
	}

	existing := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--mode", "compare", "--evaluation-episodes", "1", "--max-steps", "1", "--output", existing}, &stdout, &stderr); code != 1 {
		t.Fatalf("existing output code=%d stderr=%s", code, stderr.String())
	}
	contents, err := os.ReadFile(existing)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("existing output changed: %q err=%v", contents, err)
	}
}

func TestRunExposesRewiredTopologyControl(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "compare", "--baseline", "rewired", "--evaluation-episodes", "1", "--max-steps", "2"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run code=%d stderr=%s", code, stderr.String())
	}
	var report flyagent.ComparisonReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.BaselineMethod != "degree_preserving_endpoint_permutation_with_protected_motor_paths" ||
		!report.ParameterParity.ConnectivityChanged || report.ProtectedEdges == 0 {
		t.Fatalf("rewired report = %+v", report)
	}
}

func TestRunRejectsModeSpecificArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--mode", "unknown"},
		{"--mode", "compare-trained"},
		{"--mode", "train", "--adapter-report", "report.json"},
		{"--mode", "train", "--baseline-seed", "7"},
		{"--mode", "compare", "--passes", "3"},
		{"--mode", "compare-trained", "--adapter-report", "report.json", "--master-seed", "7"},
		{"--mode", "compare-trained", "--adapter-report", "report.json", "--evaluation-episodes", "2"},
		{"--mode", "compare", "--baseline", "unknown", "--evaluation-episodes", "1", "--max-steps", "1"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
}
