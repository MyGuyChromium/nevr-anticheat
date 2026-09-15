package flyagentlive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/flyagent"
)

type sourceStep struct {
	sample SessionSample
	err    error
}

type scriptedSource struct {
	mu    sync.Mutex
	steps []sourceStep
	prov  SourceProvenance
}

func (s *scriptedSource) Provenance() SourceProvenance { return s.prov }

func (s *scriptedSource) Poll(ctx context.Context) (SessionSample, error) {
	s.mu.Lock()
	if len(s.steps) > 0 {
		step := s.steps[0]
		s.steps = s.steps[1:]
		s.mu.Unlock()
		return step.sample, step.err
	}
	s.mu.Unlock()
	<-ctx.Done()
	return SessionSample{}, ctx.Err()
}

type recordingSink struct {
	mu       sync.Mutex
	cap      SinkCapability
	actions  []LiveAction
	neutral  []string
	closed   bool
	onApply  func()
	applyErr error
}

func (s *recordingSink) Capability() SinkCapability { return s.cap }

func (s *recordingSink) Apply(ctx context.Context, action LiveAction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.actions = append(s.actions, action)
	callback := s.onApply
	err := s.applyErr
	s.mu.Unlock()
	if callback != nil {
		callback()
	}
	return err
}

func (s *recordingSink) Neutralize(_ context.Context, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.neutral = append(s.neutral, reason)
	return nil
}

func (s *recordingSink) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func testProvenance() SourceProvenance {
	return SourceProvenance{
		Kind: LiveSourceKind, ID: "http://127.0.0.1:6721/session",
		Authority: LiveSourceAuthority, TimeBasis: LiveSourceTimeBasis,
	}
}

func testConfig() Config {
	return Config{
		ExpectedSessionID: "PRIVATE-SESSION", PlayerSelector: "Fly",
		AttackGoal: flyagent.GoalPositiveZ, PollInterval: 10 * time.Millisecond,
		StaleAfter: 100 * time.Millisecond, SessionLimit: 500 * time.Millisecond,
		PrivateConfirmations: 2, NeutralizeTimeout: 100 * time.Millisecond,
	}
}

func testNetwork(t *testing.T) *flyagent.Network {
	t.Helper()
	topology, err := flyagent.DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	network, err := flyagent.NewNetwork(topology, flyagent.DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	return network
}

func testVerifiedPolicyAdapter(t *testing.T, topology *flyagent.Topology) flyagent.VerifiedPolicyAdapter {
	t.Helper()
	arenaConfig := flyagent.DefaultArenaConfig()
	arenaConfig.MaxSteps = 1
	trainingConfig := flyagent.DefaultAdapterTrainingConfig()
	trainingConfig.TrainingEpisodes, trainingConfig.EvaluationEpisodes, trainingConfig.Passes = 1, 1, 1
	report, err := flyagent.TrainPolicyAdapter(topology, flyagent.DefaultDynamics(), arenaConfig, trainingConfig)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	_, capability, err := flyagent.LoadVerifiedPolicyAdapter(bytes.NewReader(document), topology, flyagent.DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	return capability
}

func testSample(t *testing.T, index int) SessionSample {
	t.Helper()
	raw := fmt.Sprintf(`{
"sessionid":"PRIVATE-SESSION","match_type":"Echo_Arena","map_name":"mpl_arena_a",
"game_status":"playing","game_clock":%d,"private_match":true,"client_name":"Fly",
"disc":{"position":[0,2,0],"velocity":[0,0,0]},
"teams":[
 {"team":"BLUE TEAM","players":[{"name":"Fly","userid":1,"playerid":0,
  "body":{"position":[1,1,-10],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},
  "head":{"position":[1,1,-10],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},
  "velocity":[0,0,0],"lhand":{"pos":[1,1,-10],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},
  "rhand":{"pos":[1,1,-10],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},
  "holding_left":"none","holding_right":"none"}]},
 {"team":"ORANGE TEAM","players":[{"name":"Opponent","userid":2,"playerid":1,
  "body":{"position":[-1,1,10],"forward":[0,0,-1],"left":[1,0,0],"up":[0,1,0]},
  "head":{"position":[-1,1,10],"forward":[0,0,-1],"left":[1,0,0],"up":[0,1,0]},
  "velocity":[0,0,0],"lhand":{"pos":[-1,1,10],"forward":[0,0,-1],"left":[1,0,0],"up":[0,1,0]},
  "rhand":{"pos":[-1,1,10],"forward":[0,0,-1],"left":[1,0,0],"up":[0,1,0]},
  "holding_left":"none","holding_right":"none"}]}
]}`, 300-index)
	var session adapter.EchoVRSessionResponse
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(raw))
	return SessionSample{
		Session: &session, ReceivedAt: time.Now().Add(time.Duration(index) * time.Millisecond),
		SnapshotSHA256: fmt.Sprintf("%x", digest),
		Evidence: SessionEvidence{SessionIDPresent: true, MatchTypePresent: true,
			PrivateMatchPresent: true, ClientNamePresent: true, GameStatusPresent: true},
	}
}

func TestRunnerRequiresConfirmationsAndStopsOnOperatorCancel(t *testing.T) {
	source := &scriptedSource{prov: testProvenance(), steps: []sourceStep{
		{sample: testSample(t, 0)}, {sample: testSample(t, 1)},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	sink := &recordingSink{cap: SinkDryRun, onApply: cancel}
	config := testConfig()
	topology, err := flyagent.DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	config.VerifiedPolicyAdapter = testVerifiedPolicyAdapter(t, topology)
	runner, err := NewRunner(config, source, testNetwork(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != StopOperator || result.Actions != 1 || result.FreshSamples != 2 || result.PrivateConfirmations != 2 {
		t.Fatalf("result = %+v", result)
	}
	if len(sink.actions) != 1 || sink.actions[0].AppliedDeltaTime != flyagent.NominalReplayStepSeconds ||
		!sink.actions[0].PrivateMatch || sink.actions[0].SnapshotSHA256 == "" ||
		!sink.actions[0].SourceEpochKnown || sink.actions[0].StateResetReason != "reacquired_observation" ||
		sink.actions[0].PolicyAdapterSchema != flyagent.PolicyAdapterSchema || len(sink.actions[0].PolicyAdapterDigest) != 64 ||
		sink.actions[0].PolicyAdapterReportDigest != config.VerifiedPolicyAdapter.ReportDigest() {
		t.Fatalf("actions = %+v", sink.actions)
	}
	if len(sink.neutral) < 3 || sink.neutral[0] != "startup_default_deny" || sink.neutral[1] != "private_evidence_pending" || !sink.closed {
		t.Fatalf("neutral=%v closed=%v", sink.neutral, sink.closed)
	}
}

func TestRunnerRejectsMismatchedVerifiedPolicyAdapter(t *testing.T) {
	config := testConfig()
	topology, err := flyagent.DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	shuffled, err := flyagent.ShuffledWeightBaseline(topology, 42)
	if err != nil {
		t.Fatal(err)
	}
	config.VerifiedPolicyAdapter = testVerifiedPolicyAdapter(t, shuffled)
	_, err = NewRunner(config, &scriptedSource{prov: testProvenance()}, testNetwork(t), &recordingSink{cap: SinkDryRun})
	if err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("mismatched capability error = %v", err)
	}
}

func TestRunnerPollErrorResetsPrivateConfirmationWindow(t *testing.T) {
	source := &scriptedSource{prov: testProvenance(), steps: []sourceStep{
		{sample: testSample(t, 0)}, {err: errors.New("temporary read error")},
		{sample: testSample(t, 1)}, {sample: testSample(t, 2)},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	sink := &recordingSink{cap: SinkDryRun, onApply: cancel}
	runner, err := NewRunner(testConfig(), source, testNetwork(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Actions != 1 || result.FreshSamples != 3 || result.PrivateConfirmations != 2 {
		t.Fatalf("result = %+v", result)
	}
	if len(sink.actions) != 1 || sink.actions[0].AppliedDeltaTime != flyagent.NominalReplayStepSeconds ||
		sink.actions[0].StateResetReason != "reacquired_observation" {
		t.Fatalf("actions = %+v", sink.actions)
	}
	foundPollError := false
	for _, reason := range sink.neutral {
		foundPollError = foundPollError || reason == "telemetry_poll_error"
	}
	if !foundPollError {
		t.Fatalf("neutral reasons = %v", sink.neutral)
	}
}

func TestRunnerRejectsPrivateEvidenceLossAndSessionChange(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*SessionSample)
		want   error
	}{
		{name: "missing field", change: func(s *SessionSample) { s.Evidence.PrivateMatchPresent = false }, want: ErrPrivateEvidence},
		{name: "public", change: func(s *SessionSample) { s.Session.PrivateMatch = false }, want: ErrPrivateEvidence},
		{name: "missing local client", change: func(s *SessionSample) { s.Evidence.ClientNamePresent = false }, want: ErrPrivateEvidence},
		{name: "missing phase", change: func(s *SessionSample) { s.Evidence.GameStatusPresent = false }, want: ErrPrivateEvidence},
		{name: "session switch", change: func(s *SessionSample) { s.Session.SessionID = "OTHER" }, want: ErrSessionChanged},
		{name: "combat mode", change: func(s *SessionSample) { s.Session.MatchType = "Echo_Combat" }, want: ErrPrivateEvidence},
	} {
		t.Run(test.name, func(t *testing.T) {
			sample := testSample(t, 0)
			test.change(&sample)
			source := &scriptedSource{prov: testProvenance(), steps: []sourceStep{{sample: sample}}}
			sink := &recordingSink{cap: SinkDryRun}
			runner, err := NewRunner(testConfig(), source, testNetwork(t), sink)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(context.Background())
			if !errors.Is(err, test.want) || result.StopReason != StopSafetyError || len(sink.actions) != 0 || !sink.closed {
				t.Fatalf("result=%+v err=%v actions=%d closed=%v", result, err, len(sink.actions), sink.closed)
			}
		})
	}
}

func TestRunnerWatchdogAndSessionLimitNeutralize(t *testing.T) {
	for _, test := range []struct {
		name       string
		stale      time.Duration
		limit      time.Duration
		wantReason StopReason
		wantErr    error
	}{
		{name: "stale", stale: 25 * time.Millisecond, limit: 200 * time.Millisecond, wantReason: StopTelemetryStale, wantErr: ErrTelemetryStale},
		{name: "limit", stale: 200 * time.Millisecond, limit: 25 * time.Millisecond, wantReason: StopSessionLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig()
			config.StaleAfter, config.SessionLimit = test.stale, test.limit
			source := &scriptedSource{prov: testProvenance()}
			sink := &recordingSink{cap: SinkDryRun}
			runner, err := NewRunner(config, source, testNetwork(t), sink)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(context.Background())
			if !errors.Is(err, test.wantErr) || result.StopReason != test.wantReason || len(sink.actions) != 0 || len(sink.neutral) < 2 || !sink.closed {
				t.Fatalf("result=%+v err=%v neutral=%v closed=%v", result, err, sink.neutral, sink.closed)
			}
		})
	}
}

func TestRunnerRejectsNonDryRunSinkAndUnsafeSource(t *testing.T) {
	config := testConfig()
	unsafeSink := &recordingSink{cap: SinkCapability("controller")}
	source := &scriptedSource{prov: testProvenance()}
	if _, err := NewRunner(config, source, testNetwork(t), unsafeSink); !errors.Is(err, ErrActuationDenied) {
		t.Fatalf("actuating sink error = %v", err)
	}
	source.prov.ID = "http://192.0.2.10:6721/session"
	if _, err := NewRunner(config, source, testNetwork(t), &recordingSink{cap: SinkDryRun}); err == nil {
		t.Fatal("remote session source was accepted")
	}
}

func TestRunnerRejectsRemotePlayerSelection(t *testing.T) {
	source := &scriptedSource{prov: testProvenance(), steps: []sourceStep{{sample: testSample(t, 0)}}}
	config := testConfig()
	config.PlayerSelector = "Opponent"
	sink := &recordingSink{cap: SinkDryRun}
	runner, err := NewRunner(config, source, testNetwork(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background())
	if !errors.Is(err, ErrSelectedPlayerMissing) || result.StopReason != StopSafetyError || len(sink.actions) != 0 {
		t.Fatalf("result=%+v err=%v actions=%v", result, err, sink.actions)
	}
}
