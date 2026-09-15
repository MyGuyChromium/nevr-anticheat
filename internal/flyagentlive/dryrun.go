package flyagentlive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

const DryRunEventSchema = "nevr.fly.private-live-dry-run-event/v1"

type DryRunEvent struct {
	Schema string      `json:"schema"`
	Kind   string      `json:"kind"`
	At     time.Time   `json:"at"`
	Reason string      `json:"reason,omitempty"`
	Action *LiveAction `json:"action,omitempty"`
}

// JSONDryRunSink writes auditable JSON Lines and performs no controller,
// keyboard, process, network, or game operation. It does not close its writer.
type JSONDryRunSink struct {
	mu      sync.Mutex
	encoder *json.Encoder
	closed  bool
}

func NewJSONDryRunSink(writer io.Writer) (*JSONDryRunSink, error) {
	if writer == nil {
		return nil, errors.New("dry-run output writer is required")
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return &JSONDryRunSink{encoder: encoder}, nil
}

func (*JSONDryRunSink) Capability() SinkCapability { return SinkDryRun }

func (s *JSONDryRunSink) Apply(ctx context.Context, action LiveAction) error {
	if ctx == nil {
		return errors.New("dry-run context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if action.Schema != LiveActionSchema || action.Mode != "private-live" || !action.PrivateMatch || action.Action.NeutralReason != "" {
		return errors.New("dry-run sink rejected an unsafe or malformed live action")
	}
	copy := action
	return s.write(DryRunEvent{Schema: DryRunEventSchema, Kind: "action", At: time.Now().UTC(), Action: &copy})
}

func (s *JSONDryRunSink) Neutralize(ctx context.Context, reason string) error {
	if ctx == nil {
		return errors.New("dry-run context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if reason == "" {
		reason = "unspecified"
	}
	return s.write(DryRunEvent{Schema: DryRunEventSchema, Kind: "neutralize", At: time.Now().UTC(), Reason: reason})
}

func (s *JSONDryRunSink) Close(ctx context.Context) error {
	if s == nil {
		return errors.New("dry-run sink is nil")
	}
	if ctx == nil {
		return errors.New("dry-run context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return nil
}

func (s *JSONDryRunSink) write(event DryRunEvent) error {
	if s == nil || s.encoder == nil {
		return errors.New("dry-run sink is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("dry-run sink is closed")
	}
	return s.encoder.Encode(event)
}
