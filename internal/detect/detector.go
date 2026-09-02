// Package detect defines the Detector interface and base types for all cheat detectors.
package detect

import (
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Detector is the core detection interface. Each detector identifies
// a specific class of anomaly in player behavior.
// Detectors are stateful per-match: created at match start, receive frames
// throughout the match, and are reset at match end.
type Detector interface {
	ID() string
	Version() string
	Name() string
	Category() string // "throw", "bio", "movement", "state", "pattern"
	RequiredInputs() []string
	WarmupFrames() int
	DefaultEnforcementWeight() float64
	AutoEnforce() bool

	// Evaluate processes the current frame and returns zero or more detection events.
	// It receives the full match context, all player states, and the current frame index.
	Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent

	// Reset clears all internal state between matches.
	Reset()

	// Configure applies detector-specific configuration from params map.
	Configure(params map[string]any) error
}

// BaseDetector provides common field storage and method implementations.
type BaseDetector struct {
	DetectorID       string
	DetectorVersion  string
	DetectorName     string
	DetectorCategory string
	Inputs           []string
	Warmup           int
	Weight           float64
	IsAutoEnforce    bool
}

func (b *BaseDetector) ID() string                        { return b.DetectorID }
func (b *BaseDetector) Version() string                   { return b.DetectorVersion }
func (b *BaseDetector) Name() string                      { return b.DetectorName }
func (b *BaseDetector) Category() string                  { return b.DetectorCategory }
func (b *BaseDetector) RequiredInputs() []string          { return b.Inputs }
func (b *BaseDetector) WarmupFrames() int                 { return b.Warmup }
func (b *BaseDetector) DefaultEnforcementWeight() float64 { return b.Weight }
func (b *BaseDetector) AutoEnforce() bool                 { return b.IsAutoEnforce }

// SetWeight overrides the enforcement weight stamped on every event this
// detector emits. Registration code applies the configured
// enforcement_weight through this setter; the constructor literal is only
// the fallback when no config entry exists. Values are clamped to [0, 1].
func (b *BaseDetector) SetWeight(w float64) {
	b.Weight = model.Clamp01(w)
}

// SetAutoEnforce overrides the detector's auto-enforce eligibility flag from config.
func (b *BaseDetector) SetAutoEnforce(auto bool) {
	b.IsAutoEnforce = auto
}

// MakeEvent constructs a DetectionEvent with common fields populated.
// Severity and confidence are clamped to [0, 1] (NaN becomes 0) and the
// causal frame range is normalised so FrameRangeStart <= FrameRangeEnd.
//
// EnforcementWeight is the detector's (config-settable) weight and
// AutoEnforce is the detector's auto-enforce flag; detectors that gate
// auto-enforcement on per-event evidence (THROW_001) override the field
// after the call. A nil matchCtx is tolerated so unit tests can build events
// without a match, but the resulting event has no MatchID and therefore
// fails DetectionEvent.Validate: the pipeline drops it. Production callers
// always pass the match context.
func (b *BaseDetector) MakeEvent(
	matchCtx *model.MatchContext,
	playerID string,
	frameIdx int,
	timestamp float64,
	severity, confidence float64,
	evidence model.Evidence,
	observed, expected string,
	causalKey model.CausalKey,
) model.DetectionEvent {
	if causalKey.FrameStart < 0 {
		causalKey.FrameStart = 0
	}
	if causalKey.FrameEnd < causalKey.FrameStart {
		causalKey.FrameEnd = causalKey.FrameStart
	}
	if causalKey.PlayerID == "" {
		causalKey.PlayerID = playerID
	}
	matchID := ""
	if matchCtx != nil {
		matchID = matchCtx.MatchID
	}
	return model.DetectionEvent{
		EventID:           model.NewEventID(),
		DetectorID:        b.DetectorID,
		DetectorVersion:   b.DetectorVersion,
		MatchID:           matchID,
		PlayerID:          playerID,
		FrameIndex:        frameIdx,
		FrameRangeStart:   causalKey.FrameStart,
		FrameRangeEnd:     causalKey.FrameEnd,
		Timestamp:         timestamp,
		Severity:          model.Clamp01(severity),
		Confidence:        model.Clamp01(confidence),
		Evidence:          evidence,
		ObservedValue:     observed,
		ExpectedRange:     expected,
		CausalKey:         causalKey,
		EnforcementWeight: b.Weight,
		AutoEnforce:       b.IsAutoEnforce,
	}
}

// SortedPlayers returns every player state ordered by PlayerID. Detectors
// must iterate this slice instead of ranging over the map so that event
// order, rate-limit victims and any "first player" sample are reproducible
// across reprocessing runs.
func SortedPlayers(players map[string]*model.PlayerState) []*model.PlayerState {
	out := make([]*model.PlayerState, 0, len(players))
	for _, ps := range players {
		if ps == nil {
			continue
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PlayerID < out[j].PlayerID })
	return out
}

// ActivePlayers returns, in PlayerID order, the players whose state was
// updated by the feature extractor at frameIdx. A PlayerState that still
// carries kinematics from an earlier frame (the player left, or their frame
// was rejected by the validator) is stale and must not be re-scored: a
// single-frame speed spike would otherwise be re-counted on every later
// frame until match end. States that have never been updated (FrameCount
// == 0, e.g. hand-built states in unit tests) are included, because the
// extractor always sets FrameCount together with LastFrameIdx.
func ActivePlayers(players map[string]*model.PlayerState, frameIdx int) []*model.PlayerState {
	out := make([]*model.PlayerState, 0, len(players))
	for _, ps := range players {
		if ps == nil {
			continue
		}
		if IsStale(ps, frameIdx) {
			continue
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PlayerID < out[j].PlayerID })
	return out
}

// IsStale reports whether ps was NOT updated at frameIdx (see ActivePlayers).
func IsStale(ps *model.PlayerState, frameIdx int) bool {
	return ps.FrameCount > 0 && ps.LastFrameIdx != frameIdx
}

// GetFloat reads a float64 from a params map with a default fallback.
func GetFloat(params map[string]any, key string, def float64) float64 {
	if v, ok := params[key]; ok {
		switch val := v.(type) {
		case float64:
			return val
		case float32:
			return float64(val)
		case int:
			return float64(val)
		case int64:
			return float64(val)
		}
	}
	return def
}

// GetInt reads an int from a params map with a default fallback.
func GetInt(params map[string]any, key string, def int) int {
	if v, ok := params[key]; ok {
		switch val := v.(type) {
		case int:
			return val
		case int64:
			return int(val)
		case float64:
			return int(val)
		case float32:
			return int(val)
		}
	}
	return def
}

// GetBool reads a bool from a params map with a default fallback.
func GetBool(params map[string]any, key string, def bool) bool {
	if v, ok := params[key]; ok {
		if val, ok2 := v.(bool); ok2 {
			return val
		}
	}
	return def
}

// GetFloatAlias reads the first of keys present in params (canonical key
// first, then legacy/TOML aliases) so that constructors and Configure()
// honour exactly the same spellings.
func GetFloatAlias(params map[string]any, def float64, keys ...string) float64 {
	for _, k := range keys {
		if _, ok := params[k]; ok {
			return GetFloat(params, k, def)
		}
	}
	return def
}

// GetIntAlias is GetFloatAlias for integers.
func GetIntAlias(params map[string]any, def int, keys ...string) int {
	for _, k := range keys {
		if _, ok := params[k]; ok {
			return GetInt(params, k, def)
		}
	}
	return def
}

// GetStringList reads a list of strings from params. Both []string and
// []any (as produced by TOML/JSON decoding) are accepted; other values yield nil.
func GetStringList(params map[string]any, key string) []string {
	v, ok := params[key]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case []string:
		return append([]string(nil), val...)
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
