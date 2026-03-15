// Package detect defines the Detector interface and base types for all cheat detectors.
package detect

import (
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

// MakeEvent constructs a DetectionEvent with common fields populated.
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
	return model.DetectionEvent{
		EventID:           model.NewEventID(),
		DetectorID:        b.DetectorID,
		DetectorVersion:   b.DetectorVersion,
		MatchID:           matchCtx.MatchID,
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
		AutoEnforce:       false,
	}
}

// GetFloat reads a float64 from a params map with a default fallback.
func GetFloat(params map[string]any, key string, def float64) float64 {
	if v, ok := params[key]; ok {
		switch val := v.(type) {
		case float64:
			return val
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
