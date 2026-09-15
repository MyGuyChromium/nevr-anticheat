package flyagent

import (
	"errors"
	"fmt"
)

type ExecutionMode string

const (
	ModeReplay      ExecutionMode = "replay"
	ModePrivateLive ExecutionMode = "private-live"
	ModePublicLive  ExecutionMode = "public-live"
)

var ErrLiveActuationUnavailable = errors.New("live actuation is not implemented in the offline fly-agent prototype")

// AuthorizeExecution is a fail-closed boundary shared by future sources and
// action sinks. This milestone authorizes replay inference only.
func AuthorizeExecution(mode ExecutionMode, matchIsPrivate bool) error {
	switch mode {
	case ModeReplay:
		return nil
	case ModePrivateLive:
		if !matchIsPrivate {
			return errors.New("private-live mode requires an explicitly private session")
		}
		return ErrLiveActuationUnavailable
	case ModePublicLive:
		return fmt.Errorf("public matchmaking: %w", ErrLiveActuationUnavailable)
	default:
		return fmt.Errorf("unknown execution mode %q", mode)
	}
}
