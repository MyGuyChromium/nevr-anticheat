package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/flyagent"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func TestRunReplayProducesBoundedOfflineTrace(t *testing.T) {
	var stdout, stderr bytes.Buffer
	replayPath := filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay")
	replayBytes, err := os.ReadFile(replayPath)
	if err != nil {
		t.Fatal(err)
	}
	replaySum := sha256.Sum256(replayBytes)
	expectedInputDigest := hex.EncodeToString(replaySum[:])
	code := run([]string{
		"--replay", replayPath,
		"--player", "BlueOne", "--attack-goal", "positive-z", "--max-ticks", "6",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "synthetic reflex topology") || !strings.Contains(stderr.String(), "emitted 6 offline action records") {
		t.Fatalf("stderr=%s", stderr.String())
	}

	scanner := bufio.NewScanner(&stdout)
	count := 0
	nonNeutral := 0
	var prior uint64
	for scanner.Scan() {
		var record flyagent.ActionTraceRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("record %d: %v\n%s", count, err, scanner.Text())
		}
		if record.Schema != flyagent.ActionTraceSchema || record.Action.Schema != flyagent.ActionSchema || record.Mode != flyagent.ModeReplay ||
			record.ObservationSchema != flyagent.ObservationSchema || record.RateModelSchema != flyagent.RateModelSchema || record.DecoderPolicySchema != flyagent.DecoderPolicySchema ||
			record.PolicyAdapterSchema != flyagent.PolicyAdapterSchema {
			t.Fatalf("schemas = %q / %q", record.Schema, record.Action.Schema)
		}
		if !record.Dataset.Synthetic || record.PlayerID == "" || record.AttackGoal != flyagent.GoalPositiveZ || !record.SourceEpochKnown || record.Dynamics != flyagent.DefaultDynamics() ||
			record.SourceKind != "echoreplay" || record.SourceAuthority != "client_reported" || !strings.HasPrefix(record.SourceTimeBasis, "recorder_prefix") || record.AppliedDeltaTime <= 0 {
			t.Fatalf("record = %+v", record)
		}
		if record.InputDigest != expectedInputDigest {
			t.Fatalf("input digest = %q, want %q", record.InputDigest, expectedInputDigest)
		}
		for name, digest := range map[string]string{
			"observation": record.ObservationDigest, "state": record.StateDigest,
			"topology": record.TopologyDigest, "model": record.ModelDigest, "input": record.InputDigest,
			"policy adapter": record.PolicyAdapterDigest,
		} {
			decoded, err := hex.DecodeString(digest)
			if err != nil || len(decoded) != 32 {
				t.Fatalf("%s digest = %q err=%v", name, digest, err)
			}
		}
		if record.Action.NeutralReason == "" && record.Action.Confidence > 0 {
			nonNeutral++
		}
		if count == 0 && !strings.Contains(record.StateResetReason, "new_match") {
			t.Fatalf("first reset reason = %q", record.StateResetReason)
		}
		if count > 0 && record.Sequence != prior+1 {
			t.Fatalf("sequence jumped from %d to %d", prior, record.Sequence)
		}
		prior = record.Sequence
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("records=%d output=%s", count, stdout.String())
	}
	if nonNeutral == 0 {
		t.Fatalf("trace contains no non-neutral actions: %s", stdout.String())
	}
}

func TestRunLoadsOnlyTopologyBoundAdapterReport(t *testing.T) {
	topology, err := flyagent.DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	arenaConfig := flyagent.DefaultArenaConfig()
	arenaConfig.MaxSteps = 2
	trainingConfig := flyagent.DefaultAdapterTrainingConfig()
	trainingConfig.TrainingEpisodes, trainingConfig.EvaluationEpisodes, trainingConfig.Passes = 1, 1, 1
	report, err := flyagent.TrainPolicyAdapter(topology, flyagent.DefaultDynamics(), arenaConfig, trainingConfig)
	if err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(t.TempDir(), "adapter-report.json")
	document, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, document, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	replayPath := filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay")
	code := run([]string{
		"--replay", replayPath, "--player", "BlueOne", "--attack-goal", "positive-z",
		"--max-ticks", "1", "--adapter-report", reportPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run code=%d stderr=%s", code, stderr.String())
	}
	records := decodeTrace(t, stdout.Bytes())
	wantDigest, err := flyagent.DigestPolicyAdapter(report.TrainedAdapter)
	if err != nil {
		t.Fatal(err)
	}
	reportSum := sha256.Sum256(document)
	wantReportDigest := hex.EncodeToString(reportSum[:])
	if len(records) != 1 || records[0].PolicyAdapterDigest != wantDigest || records[0].PolicyAdapterReportDigest != wantReportDigest {
		t.Fatalf("adapter-bound records = %+v", records)
	}

	shuffled, err := flyagent.ShuffledWeightBaseline(topology, 42)
	if err != nil {
		t.Fatal(err)
	}
	wrongReport, err := flyagent.TrainPolicyAdapter(shuffled, flyagent.DefaultDynamics(), arenaConfig, trainingConfig)
	if err != nil {
		t.Fatal(err)
	}
	wrongDocument, _ := json.Marshal(wrongReport)
	wrongPath := filepath.Join(t.TempDir(), "wrong-adapter-report.json")
	if err := os.WriteFile(wrongPath, wrongDocument, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = run([]string{
		"--replay", replayPath, "--player", "BlueOne", "--attack-goal", "positive-z",
		"--max-ticks", "1", "--adapter-report", wrongPath,
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "different topology or dynamics") {
		t.Fatalf("mismatched report code=%d stderr=%s", code, stderr.String())
	}
}

func TestPrepareReplaySnapshotBindsExactBytesAndEnforcesLimit(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "input.tape")
	original := []byte("exact replay bytes")
	if err := os.WriteFile(sourcePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshotPath, digest, cleanup, err := prepareReplaySnapshotWithLimit(sourcePath, int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(snapshotPath) != ".tape" {
		_ = cleanup()
		t.Fatalf("snapshot extension = %q", filepath.Ext(snapshotPath))
	}
	if err := os.WriteFile(sourcePath, []byte("different source"), 0o600); err != nil {
		_ = cleanup()
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		_ = cleanup()
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(original)
	if !bytes.Equal(snapshot, original) || digest != hex.EncodeToString(wantDigest[:]) {
		_ = cleanup()
		t.Fatalf("snapshot=%q digest=%q", snapshot, digest)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Fatalf("snapshot was not removed: %v", err)
	}
	if _, _, _, err := prepareReplaySnapshotWithLimit(sourcePath, 2); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversize snapshot error = %v", err)
	}
}

func TestRunNativeTapeProducesActionableTrace(t *testing.T) {
	tapePath := filepath.Join(t.TempDir(), "native.tape")
	if err := testutil.WriteNativeTapeFixture(tapePath); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--replay", tapePath, "--player", testutil.NativeTapePlayerID,
		"--attack-goal", "positive-z", "--max-ticks", "6",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run code=%d stderr=%s", code, stderr.String())
	}
	records := decodeTrace(t, stdout.Bytes())
	if len(records) != 6 {
		t.Fatalf("records=%d output=%s", len(records), stdout.String())
	}
	nonNeutral := 0
	for i, record := range records {
		if record.MatchID != testutil.NativeTapeMatchID || record.PlayerID != testutil.NativeTapePlayerID ||
			record.SourceKind != "tape" || record.SourceID != testutil.NativeTapeCaptureID ||
			record.SourceAuthority != "client_reported" || !strings.HasPrefix(record.SourceTimeBasis, "capture_offset_ms") ||
			!record.SourceEpochKnown || record.Action.NeutralReason != "" {
			t.Fatalf("record %d = %+v", i, record)
		}
		if record.Action.Confidence > 0 {
			nonNeutral++
		}
	}
	if nonNeutral == 0 || !strings.Contains(records[0].StateResetReason, "new_match") {
		t.Fatalf("native trace never acted or reset: %+v", records)
	}
}

func TestRunResetsAfterSelectedPlayerAbsence(t *testing.T) {
	source := filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 8 {
		t.Fatalf("fixture lines=%d", len(lines))
	}
	parts := strings.SplitN(strings.TrimSuffix(lines[6], "\r"), "\t", 2)
	if len(parts) != 2 {
		t.Fatalf("fixture line has no timestamp separator: %q", lines[6])
	}
	var session adapter.EchoVRSessionResponse
	if err := json.Unmarshal([]byte(parts[1]), &session); err != nil {
		t.Fatal(err)
	}
	removed := false
	for teamIndex := range session.Teams {
		players := session.Teams[teamIndex].Players[:0]
		for _, player := range session.Teams[teamIndex].Players {
			if player.Name == "BlueOne" {
				removed = true
				continue
			}
			players = append(players, player)
		}
		session.Teams[teamIndex].Players = players
	}
	if !removed {
		t.Fatal("BlueOne was not present in fixture line")
	}
	modified, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	lines[6] = parts[0] + "\t" + string(modified)
	replayPath := filepath.Join(t.TempDir(), "absence.echoreplay")
	if err := os.WriteFile(replayPath, []byte(strings.Join(lines[:8], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--replay", replayPath, "--player", "BlueOne", "--attack-goal", "positive-z", "--max-ticks", "7",
	}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stderr.String(), "1 ticks without selected player") {
		t.Fatalf("run code=%d stderr=%s", code, stderr.String())
	}
	records := decodeTrace(t, stdout.Bytes())
	if len(records) != 7 || records[5].Action.Confidence == 0 {
		t.Fatalf("pre-gap trace = %+v", records)
	}
	returned := records[6]
	wantFresh := flyagent.ActionIntent{Schema: flyagent.ActionSchema}
	if returned.FrameIndex != 7 || !strings.Contains(returned.StateResetReason, "player_absence") ||
		!reflect.DeepEqual(returned.Action, wantFresh) || returned.Sequence != records[5].Sequence+1 ||
		returned.AppliedDeltaTime != flyagent.NominalReplayStepSeconds {
		t.Fatalf("return record = %+v; prior=%+v", returned, records[5])
	}
}

func decodeTrace(t *testing.T, data []byte) []flyagent.ActionTraceRecord {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var records []flyagent.ActionTraceRecord
	for scanner.Scan() {
		var record flyagent.ActionTraceRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode record %d: %v\n%s", len(records), err, scanner.Text())
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestRunHelpAndUnknownFlagExitCodes(t *testing.T) {
	for _, help := range []string{"--help", "-h"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{help}, &stdout, &stderr); code != 0 || !strings.Contains(stderr.String(), "-replay") {
			t.Fatalf("%s code=%d stderr=%s", help, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--does-not-exist"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown flag code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunRejectsIncompatibleTopologyBeforeOutput(t *testing.T) {
	topology, err := flyagent.DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	delete(topology.Outputs, "brake")
	document, err := json.Marshal(topology)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	topologyPath := filepath.Join(directory, "bad-topology.json")
	if err := os.WriteFile(topologyPath, document, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "must-not-exist.jsonl")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--replay", filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay"),
		"--player", "BlueOne", "--attack-goal", "positive-z", "--topology", topologyPath, "--output", output,
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "missing required motor populations: brake") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("incompatible topology created output: %v", err)
	}
}

func TestRunRejectsMissingArgumentsAndExistingOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("missing args code=%d stderr=%s", code, stderr.String())
	}

	existing := filepath.Join(t.TempDir(), "trace.jsonl")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code := run([]string{
		"--replay", filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay"),
		"--player", "BlueOne", "--attack-goal", "positive-z", "--output", existing,
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "file exists") {
		t.Fatalf("existing output code=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing output changed: %q err=%v", data, err)
	}
}

func TestRunRemovesOwnedOutputAfterFailure(t *testing.T) {
	output := filepath.Join(t.TempDir(), "retryable.jsonl")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--replay", filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay"),
		"--player", "AbsentPlayer", "--attack-goal", "positive-z", "--output", output,
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "no actions emitted") || !strings.Contains(stderr.String(), "available:") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("failed trace remains at %s: %v", output, err)
	}
}
