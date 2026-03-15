package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Poller periodically polls an Echo VR game server's /session endpoint
// and feeds frames into the anticheat pipeline.
type Poller struct {
	apiURL   string // e.g., "http://127.0.0.1:6721/session"
	interval time.Duration
	mapper   *Mapper
	client   *http.Client
	logger   *slog.Logger

	// Callback for processed frames
	OnFrames func(matchID string, frames []model.PlayerTelemetryFrame)
	OnError  func(err error)
}

// NewPoller creates an Echo VR API poller.
func NewPoller(apiURL string, pollInterval time.Duration, logger *slog.Logger) *Poller {
	return &Poller{
		apiURL:   apiURL,
		interval: pollInterval,
		mapper:   NewMapper(),
		client:   &http.Client{Timeout: 2 * time.Second},
		logger:   logger,
	}
}

// Start begins polling. Blocks until context is cancelled.
func (p *Poller) Start(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	p.logger.Info("echo_vr_poller_started", "url", p.apiURL, "interval", p.interval)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.poll(ctx); err != nil {
				if p.OnError != nil {
					p.OnError(err)
				}
				p.logger.Debug("poll_error", "error", err)
				// Don't log every error at Info level — game may not be running
			}
		}
	}
}

func (p *Poller) poll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", p.apiURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("polling: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return fmt.Errorf("reading body: %w", err)
	}

	var session EchoVRSessionResponse
	if err := json.Unmarshal(body, &session); err != nil {
		return fmt.Errorf("parsing session: %w", err)
	}

	// Skip if no teams/players
	if len(session.Teams) == 0 {
		return nil
	}

	result := p.mapper.MapSession(&session)

	// Log warnings (first occurrence only — mapper deduplicates)
	for _, w := range result.Warnings {
		p.logger.Debug("mapping_warning", "field", w.Field, "message", w.Message)
	}
	for _, e := range result.Errors {
		p.logger.Warn("mapping_error", "player", e.PlayerName, "field", e.Field, "message", e.Message)
	}

	if len(result.Frames) > 0 && p.OnFrames != nil {
		matchID := session.SessionID
		if matchID == "" {
			matchID = "unknown"
		}
		p.OnFrames(matchID, result.Frames)
	}

	return nil
}
