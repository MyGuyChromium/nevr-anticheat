package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunProducesBoundedBenchmarkReport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--warmup", "1", "--steps", "5"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var report benchmarkReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != benchmarkReportSchema || report.NodeCount == 0 || report.EdgeCount == 0 ||
		report.MeasuredSteps != 5 || report.DeltaTimeSeconds <= 0 || report.P95BatchMeanNanoseconds < 0 || len(report.FinalStateDigest) != 64 ||
		len(report.Limitations) == 0 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(stderr.String(), "mean step") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRejectsUnboundedOrInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--steps", "0"}, {"--warmup", "10001"}, {"--dt", "1"}, {"--dt", "NaN"}, {"--dt", "+Inf"}, {"positional"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
}

func TestPercentileUsesNearestRank(t *testing.T) {
	values := []int64{1, 2, 3, 4, 5}
	if got := percentile(values, 0.5); got != 3 {
		t.Fatalf("p50=%d", got)
	}
	if got := percentile(values, 0.95); got != 5 {
		t.Fatalf("p95=%d", got)
	}
}
