// Package flyagentlive contains the private-session, observation-only runtime
// boundary for the fly agent. It intentionally has no controller, process
// injection, matchmaking, or anti-cheat integration.
package flyagentlive

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/flyagent"
)

const (
	LiveActionSchema = "nevr.fly.private-live-action/v1"

	LiveSourceKind      = "echovr_http"
	LiveSourceAuthority = "client_reported"
	LiveSourceTimeBasis = "http_response_body_received"

	SinkDryRun SinkCapability = "dry-run"

	MinPrivateConfirmations = 2
	MaxPrivateConfirmations = 10
	MinPollInterval         = 10 * time.Millisecond
	MaxPrivateSessionLimit  = 30 * time.Minute
	MaxStaleTelemetry       = 5 * time.Second
)

var (
	ErrActuationDenied       = errors.New("live actuation is denied; this runner accepts a dry-run sink only")
	ErrPrivateEvidence       = errors.New("explicit private-session evidence is unavailable")
	ErrSessionChanged        = errors.New("live session identity changed")
	ErrTelemetryStale        = errors.New("live telemetry watchdog expired")
	ErrSelectedPlayerMissing = errors.New("selected player is unavailable in the private session")
)

type SinkCapability string

// ActionSink is the only output boundary of Runner. This milestone accepts
// SinkDryRun only. A future actuating implementation requires a separate policy
// change and cannot be enabled through configuration here.
type ActionSink interface {
	Capability() SinkCapability
	Apply(context.Context, LiveAction) error
	Neutralize(context.Context, string) error
	Close(context.Context) error
}

// SourceProvenance describes the fixed acquisition path of a SessionSource.
type SourceProvenance struct {
	Kind      string
	ID        string
	Authority string
	TimeBasis string
}

// SessionEvidence records exact top-level JSON-field presence independently of
// Go zero values. Every field must be present on every accepted sample.
type SessionEvidence struct {
	SessionIDPresent    bool
	MatchTypePresent    bool
	PrivateMatchPresent bool
	ClientNamePresent   bool
	GameStatusPresent   bool
}

// SessionSample is one complete /session response received at ReceivedAt.
// SnapshotSHA256 fingerprints the exact response body without retaining it.
type SessionSample struct {
	Session        *adapter.EchoVRSessionResponse
	ReceivedAt     time.Time
	SnapshotSHA256 string
	Evidence       SessionEvidence
}

// SessionSource returns one bounded live sample and must honor ctx. Runner also
// selects independently on its watchdog, session limit, and operator context.
type SessionSource interface {
	Provenance() SourceProvenance
	Poll(context.Context) (SessionSample, error)
}

// Config contains mandatory safety limits. Zero values are rejected rather
// than silently disabling a watchdog or extending a session.
type Config struct {
	ExpectedSessionID     string
	PlayerSelector        string
	AttackGoal            flyagent.AttackGoal
	VerifiedPolicyAdapter flyagent.VerifiedPolicyAdapter
	PollInterval          time.Duration
	StaleAfter            time.Duration
	SessionLimit          time.Duration
	PrivateConfirmations  int
	NeutralizeTimeout     time.Duration
}

// DefaultConfig supplies conservative timing values. Identity, player, attack
// goal, and an explicit session limit still have to be supplied by the caller.
func DefaultConfig() Config {
	return Config{
		PollInterval:         67 * time.Millisecond,
		StaleAfter:           750 * time.Millisecond,
		PrivateConfirmations: 3,
		NeutralizeTimeout:    time.Second,
	}
}

type StopReason string

const (
	StopOperator       StopReason = "operator_stop"
	StopSessionLimit   StopReason = "session_limit"
	StopTelemetryStale StopReason = "telemetry_stale"
	StopPostMatch      StopReason = "post_match"
	StopSafetyError    StopReason = "safety_error"
)

type Result struct {
	StopReason           StopReason
	StartedAt            time.Time
	StoppedAt            time.Time
	Polls                int
	FreshSamples         int
	DuplicateSamples     int
	Actions              int
	Neutralizations      int
	PrivateConfirmations int
}

// LiveAction is an auditable abstract intent. It is not a controller pose.
type LiveAction struct {
	Schema                    string                   `json:"schema"`
	Mode                      flyagent.ExecutionMode   `json:"mode"`
	SessionID                 string                   `json:"session_id"`
	PlayerID                  string                   `json:"player_id"`
	FrameIndex                int                      `json:"frame_index"`
	Sequence                  uint64                   `json:"sequence"`
	ReceivedAt                time.Time                `json:"received_at"`
	SnapshotSHA256            string                   `json:"snapshot_sha256"`
	PrivateMatch              bool                     `json:"private_match"`
	PrivateConfirmations      int                      `json:"private_confirmations"`
	AttackGoal                flyagent.AttackGoal      `json:"attack_goal"`
	RawDeltaTime              float64                  `json:"raw_delta_time"`
	AppliedDeltaTime          float64                  `json:"applied_delta_time"`
	SourceKind                string                   `json:"source_kind"`
	SourceID                  string                   `json:"source_id"`
	SourceAuthority           string                   `json:"source_authority"`
	SourceTimeBasis           string                   `json:"source_time_basis"`
	SourceEpoch               uint64                   `json:"source_epoch,omitempty"`
	SourceEpochKnown          bool                     `json:"source_epoch_known"`
	ObservationDigest         string                   `json:"observation_digest"`
	StateDigest               string                   `json:"state_digest"`
	TopologyDigest            string                   `json:"topology_digest"`
	ModelDigest               string                   `json:"model_digest"`
	PolicyAdapterSchema       string                   `json:"policy_adapter_schema"`
	PolicyAdapterDigest       string                   `json:"policy_adapter_digest"`
	PolicyAdapterReportDigest string                   `json:"policy_adapter_report_digest,omitempty"`
	StateResetReason          string                   `json:"state_reset_reason,omitempty"`
	Action                    flyagent.ActionIntent    `json:"action"`
	Dataset                   flyagent.DatasetMetadata `json:"dataset"`
}

type Runner struct {
	config                    Config
	source                    SessionSource
	sink                      ActionSink
	network                   *flyagent.Network
	encoder                   *flyagent.Encoder
	mapper                    *adapter.Mapper
	policyAdapter             flyagent.PolicyAdapter
	policyAdapterDigest       string
	policyAdapterReportDigest string
	started                   atomic.Bool
}

func NewRunner(config Config, source SessionSource, network *flyagent.Network, sink ActionSink) (*Runner, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if nilInterface(source) {
		return nil, errors.New("live session source is required")
	}
	provenance := source.Provenance()
	if provenance.Kind != LiveSourceKind || provenance.Authority != LiveSourceAuthority ||
		provenance.TimeBasis != LiveSourceTimeBasis || !validLoopbackSessionURL(provenance.ID) {
		return nil, errors.New("live source must be an exact loopback HTTP /session endpoint with client-reported receive-time provenance")
	}
	if network == nil || network.ModelDigest() == "" {
		return nil, errors.New("compiled fly network is required")
	}
	if nilInterface(sink) || sink.Capability() != SinkDryRun {
		return nil, ErrActuationDenied
	}
	// The global execution policy still denies private-live actuation. A dry-run
	// sink is observation/output only, so that expected denial is retained as the
	// boundary that prevents silently substituting a controller sink.
	if err := flyagent.AuthorizeExecution(flyagent.ModePrivateLive, true); err != nil &&
		!errors.Is(err, flyagent.ErrLiveActuationUnavailable) {
		return nil, fmt.Errorf("private-live policy: %w", err)
	}
	encoder, err := flyagent.NewEncoder(config.PlayerSelector, config.AttackGoal)
	if err != nil {
		return nil, err
	}
	mapper := adapter.NewMapper()
	mapper.SetObservationSource(provenance.Kind, provenance.TimeBasis, provenance.ID)
	mapper.SetDedupeIdentical(true)
	policyAdapter := flyagent.DefaultPolicyAdapter()
	policyAdapterReportDigest := ""
	if config.VerifiedPolicyAdapter.Verified() {
		policyAdapter, policyAdapterReportDigest, err = config.VerifiedPolicyAdapter.Bind(network)
		if err != nil {
			return nil, err
		}
	}
	policyAdapterDigest, err := flyagent.DigestPolicyAdapter(policyAdapter)
	if err != nil {
		return nil, fmt.Errorf("policy adapter: %w", err)
	}
	return &Runner{
		config: config, source: source, sink: sink, network: network, encoder: encoder, mapper: mapper,
		policyAdapter: policyAdapter, policyAdapterDigest: policyAdapterDigest,
		policyAdapterReportDigest: policyAdapterReportDigest,
	}, nil
}

func validateConfig(config Config) error {
	if config.ExpectedSessionID == "" || strings.TrimSpace(config.ExpectedSessionID) != config.ExpectedSessionID || len(config.ExpectedSessionID) > 256 {
		return errors.New("an exact expected private session ID (1..256 bytes, no surrounding whitespace) is required")
	}
	if strings.TrimSpace(config.PlayerSelector) == "" {
		return errors.New("player selector is required")
	}
	if config.PollInterval < MinPollInterval || config.PollInterval > time.Second {
		return fmt.Errorf("poll interval must be within [%s, 1s]", MinPollInterval)
	}
	if config.StaleAfter <= config.PollInterval || config.StaleAfter > MaxStaleTelemetry {
		return fmt.Errorf("stale watchdog must be greater than the poll interval and at most %s", MaxStaleTelemetry)
	}
	if config.SessionLimit <= 0 || config.SessionLimit > MaxPrivateSessionLimit {
		return fmt.Errorf("explicit session limit must be within (0, %s]", MaxPrivateSessionLimit)
	}
	if config.PrivateConfirmations < MinPrivateConfirmations || config.PrivateConfirmations > MaxPrivateConfirmations {
		return fmt.Errorf("private confirmations must be within %d..%d", MinPrivateConfirmations, MaxPrivateConfirmations)
	}
	if config.NeutralizeTimeout <= 0 || config.NeutralizeTimeout > 5*time.Second {
		return errors.New("neutralize timeout must be within (0, 5s]")
	}
	return nil
}

type pollResult struct {
	sample SessionSample
	err    error
}

// Run polls until the operator cancels, the explicit session limit expires,
// post_match is observed, or a safety boundary fails. Every exit attempts a
// final neutral command with a fresh bounded context before closing the sink.
func (r *Runner) Run(ctx context.Context) (result Result, runErr error) {
	if r == nil {
		return result, errors.New("live runner is nil")
	}
	if ctx == nil {
		return result, errors.New("operator context is required")
	}
	if !r.started.CompareAndSwap(false, true) {
		return result, errors.New("live runner is single-use")
	}
	result.StartedAt = time.Now().UTC()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finalReason := "runner_exit"
	defer func() {
		cancel()
		result.StoppedAt = time.Now().UTC()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), r.config.NeutralizeTimeout)
		defer cleanupCancel()
		if result.StopReason != "" {
			finalReason = string(result.StopReason)
		}
		if err := r.sink.Neutralize(cleanupCtx, finalReason); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("final neutralize: %w", err))
		} else {
			result.Neutralizations++
		}
		if err := r.sink.Close(cleanupCtx); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close action sink: %w", err))
		}
	}()
	if ctx.Err() != nil {
		result.StopReason = StopOperator
		return result, nil
	}

	if err := r.neutralize(runCtx, &result, "startup_default_deny"); err != nil {
		result.StopReason = StopSafetyError
		return result, err
	}

	pollTimer := time.NewTimer(0)
	defer pollTimer.Stop()
	staleTimer := time.NewTimer(r.config.StaleAfter)
	defer staleTimer.Stop()
	limitTimer := time.NewTimer(r.config.SessionLimit)
	defer limitTimer.Stop()
	pollCh := make(chan pollResult, 1)
	polling := false

	lastDigest := ""
	privateConfirmations := 0
	needsNominalStep := true
	var priorSourceEpoch uint64
	priorSourceEpochKnown := false

	for {
		select {
		case <-runCtx.Done():
			result.StopReason = StopOperator
			return result, nil
		case <-limitTimer.C:
			result.StopReason = StopSessionLimit
			return result, nil
		case <-staleTimer.C:
			result.StopReason = StopTelemetryStale
			return result, ErrTelemetryStale
		case <-pollTimer.C:
			if polling {
				continue
			}
			polling = true
			result.Polls++
			go func() {
				sample, err := r.source.Poll(runCtx)
				pollCh <- pollResult{sample: sample, err: err}
			}()
		case outcome := <-pollCh:
			polling = false
			if outcome.err != nil {
				if runCtx.Err() != nil {
					result.StopReason = StopOperator
					return result, nil
				}
				r.network.Reset()
				needsNominalStep = true
				privateConfirmations = 0
				result.PrivateConfirmations = 0
				if err := r.neutralize(runCtx, &result, "telemetry_poll_error"); err != nil {
					result.StopReason = StopSafetyError
					return result, err
				}
				resetTimer(pollTimer, r.config.PollInterval)
				continue
			}
			sample := outcome.sample
			if err := validatePrivateSample(sample, r.config.ExpectedSessionID); err != nil {
				result.StopReason = StopSafetyError
				return result, err
			}
			if sample.SnapshotSHA256 == lastDigest {
				result.DuplicateSamples++
				resetTimer(pollTimer, r.config.PollInterval)
				continue
			}
			lastDigest = sample.SnapshotSHA256

			mapped := r.mapper.MapSessionAt(sample.Session, sample.ReceivedAt)
			if mapped.MatchCtx == nil || len(mapped.Errors) > 0 {
				result.StopReason = StopSafetyError
				return result, fmt.Errorf("map private /session sample: %v", mapped.Errors)
			}
			if mapped.SkippedDuplicate {
				result.DuplicateSamples++
				resetTimer(pollTimer, r.config.PollInterval)
				continue
			}
			if len(mapped.Frames) == 0 {
				result.StopReason = StopSafetyError
				return result, ErrSelectedPlayerMissing
			}
			tick := &adapter.ParsedTick{
				MatchID: sample.Session.SessionID, MatchCtx: mapped.MatchCtx,
				NewMatch: result.FreshSamples == 0, FrameIndex: mapped.Frames[0].FrameIndex,
				SampleTime: sample.ReceivedAt, Frames: mapped.Frames, Session: sample.Session,
			}
			observation, err := r.encoder.EncodePrivateLive(tick)
			if errors.Is(err, flyagent.ErrPlayerAbsent) {
				result.StopReason = StopSafetyError
				return result, ErrSelectedPlayerMissing
			}
			if err != nil {
				result.StopReason = StopSafetyError
				return result, fmt.Errorf("encode private /session sample: %w", err)
			}
			if !observation.SourceEpochKnown || !observation.IsPrivate {
				result.StopReason = StopSafetyError
				return result, fmt.Errorf("%w: selected player does not match the uniquely resolved local client", ErrSelectedPlayerMissing)
			}

			result.FreshSamples++
			privateConfirmations++
			result.PrivateConfirmations = privateConfirmations
			resetTimer(staleTimer, r.config.StaleAfter)

			resetReason := ""
			if priorSourceEpochKnown && observation.SourceEpoch != priorSourceEpoch {
				resetReason = appendReason(resetReason, "source_epoch_change")
			}
			priorSourceEpoch, priorSourceEpochKnown = observation.SourceEpoch, observation.SourceEpochKnown
			if observation.Sequence > 0 && observation.DeltaTime == 0 {
				resetReason = appendReason(resetReason, "timebase_reset")
			}
			if observation.DeltaTime > r.network.Dynamics().MaxStep {
				resetReason = appendReason(resetReason, "telemetry_gap")
			}
			if needsNominalStep {
				resetReason = appendReason(resetReason, "reacquired_observation")
			}

			if privateConfirmations < r.config.PrivateConfirmations {
				r.network.Reset()
				needsNominalStep = true
				if err := r.neutralize(runCtx, &result, "private_evidence_pending"); err != nil {
					result.StopReason = StopSafetyError
					return result, err
				}
				resetTimer(pollTimer, r.config.PollInterval)
				continue
			}
			if reason := flyagent.ControlBlockReason(observation); reason != "" {
				r.network.Reset()
				needsNominalStep = true
				if err := r.neutralize(runCtx, &result, reason); err != nil {
					result.StopReason = StopSafetyError
					return result, err
				}
				if strings.EqualFold(strings.TrimSpace(sample.Session.GameStatus), "post_match") {
					result.StopReason = StopPostMatch
					return result, nil
				}
				resetTimer(pollTimer, r.config.PollInterval)
				continue
			}

			stepDelta := observation.DeltaTime
			if needsNominalStep || resetReason != "" {
				r.network.Reset()
				stepDelta = flyagent.NominalReplayStepSeconds
			}
			appliedDelta, _, err := r.network.AppliedDeltaTime(stepDelta)
			if err != nil {
				result.StopReason = StopSafetyError
				return result, err
			}
			adaptedCurrents, err := r.policyAdapter.AdaptCurrents(observation.Currents)
			if err != nil {
				result.StopReason = StopSafetyError
				return result, fmt.Errorf("adapt live sensory currents: %w", err)
			}
			if err := r.network.Step(stepDelta, adaptedCurrents); err != nil {
				result.StopReason = StopSafetyError
				return result, err
			}
			action, err := flyagent.DecodeWithAdapter(observation, r.network, r.policyAdapter)
			if err != nil {
				result.StopReason = StopSafetyError
				return result, fmt.Errorf("decode live action: %w", err)
			}
			if action.NeutralReason != "" {
				r.network.Reset()
				needsNominalStep = true
				if err := r.neutralize(runCtx, &result, action.NeutralReason); err != nil {
					result.StopReason = StopSafetyError
					return result, err
				}
				resetTimer(pollTimer, r.config.PollInterval)
				continue
			}
			observationDigest, err := flyagent.DigestObservation(observation)
			if err != nil {
				result.StopReason = StopSafetyError
				return result, err
			}
			command := LiveAction{
				Schema: LiveActionSchema, Mode: flyagent.ModePrivateLive,
				SessionID: observation.MatchID, PlayerID: observation.PlayerID,
				FrameIndex: observation.FrameIndex, Sequence: observation.Sequence,
				ReceivedAt: sample.ReceivedAt.UTC(), SnapshotSHA256: sample.SnapshotSHA256,
				PrivateMatch: true, PrivateConfirmations: privateConfirmations,
				AttackGoal: observation.AttackGoal, RawDeltaTime: observation.DeltaTime,
				AppliedDeltaTime: appliedDelta, SourceKind: observation.SourceKind,
				SourceID: observation.SourceID, SourceAuthority: observation.SourceAuthority,
				SourceTimeBasis: observation.SourceTimeBasis, SourceEpoch: observation.SourceEpoch,
				SourceEpochKnown:  observation.SourceEpochKnown,
				ObservationDigest: observationDigest, StateDigest: r.network.StateDigest(),
				TopologyDigest: r.network.TopologyDigest(), ModelDigest: r.network.ModelDigest(),
				PolicyAdapterSchema: flyagent.PolicyAdapterSchema, PolicyAdapterDigest: r.policyAdapterDigest,
				PolicyAdapterReportDigest: r.policyAdapterReportDigest,
				StateResetReason:          resetReason, Action: action, Dataset: r.network.Dataset(),
			}
			if err := r.sink.Apply(runCtx, command); err != nil {
				result.StopReason = StopSafetyError
				return result, fmt.Errorf("apply dry-run action: %w", err)
			}
			result.Actions++
			needsNominalStep = false
			resetTimer(pollTimer, r.config.PollInterval)
		}
	}
}

func (r *Runner) neutralize(ctx context.Context, result *Result, reason string) error {
	if err := r.sink.Neutralize(ctx, reason); err != nil {
		return fmt.Errorf("neutralize action sink (%s): %w", reason, err)
	}
	result.Neutralizations++
	return nil
}

func validatePrivateSample(sample SessionSample, expectedSessionID string) error {
	if sample.Session == nil || !sample.Evidence.SessionIDPresent || !sample.Evidence.MatchTypePresent ||
		!sample.Evidence.PrivateMatchPresent || !sample.Session.PrivateMatch {
		return ErrPrivateEvidence
	}
	if !sample.Evidence.ClientNamePresent || strings.TrimSpace(sample.Session.ClientName) == "" {
		return fmt.Errorf("%w: exact local client identity is unavailable", ErrPrivateEvidence)
	}
	if !sample.Evidence.GameStatusPresent || strings.TrimSpace(sample.Session.GameStatus) == "" {
		return fmt.Errorf("%w: exact game phase is unavailable", ErrPrivateEvidence)
	}
	if sample.Session.SessionID != expectedSessionID {
		return fmt.Errorf("%w: got %q, expected %q", ErrSessionChanged, sample.Session.SessionID, expectedSessionID)
	}
	mode := strings.ToLower(strings.TrimSpace(sample.Session.MatchType))
	if mode != "echo_arena" && !strings.HasPrefix(mode, "echo_arena_") {
		return fmt.Errorf("%w: unsupported match type %q", ErrPrivateEvidence, sample.Session.MatchType)
	}
	if sample.ReceivedAt.IsZero() {
		return errors.New("live sample has no receive timestamp")
	}
	digest, err := hex.DecodeString(sample.SnapshotSHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(sample.SnapshotSHA256) != sample.SnapshotSHA256 {
		return errors.New("live sample has an invalid canonical SHA-256")
	}
	return nil
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func appendReason(current, next string) string {
	if current == "" {
		return next
	}
	if next == "" || strings.Contains("+"+current+"+", "+"+next+"+") {
		return current
	}
	return current + "+" + next
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
