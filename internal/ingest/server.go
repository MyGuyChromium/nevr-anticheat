// Package ingest handles real-time telemetry ingestion from game servers.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// ServerConfig configures the ingestion server.
type ServerConfig struct {
	ListenAddr              string        // e.g., ":8080"
	MaxConnectionsPerServer int           // max concurrent game server connections
	MaxFrameSize            int           // max bytes per frame message (default 64KB)
	MaxFrameRatePerPlayer   int           // max frames/sec per player (default 30)
	MaxPlayersPerMatch      int           // max players per match (default 16)
	ReadTimeout             time.Duration // websocket read timeout
	WriteTimeout            time.Duration
	AuthToken               string // shared secret for server auth
}

func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		ListenAddr:              ":8080",
		MaxConnectionsPerServer: 100,
		MaxFrameSize:            65536,
		MaxFrameRatePerPlayer:   30,
		MaxPlayersPerMatch:      16,
		ReadTimeout:             30 * time.Second,
		WriteTimeout:            10 * time.Second,
	}
}

// FrameHandler processes validated telemetry frames.
type FrameHandler func(matchID string, frames []model.PlayerTelemetryFrame)

// Server accepts telemetry streams from game servers.
type Server struct {
	cfg        ServerConfig
	handler    FrameHandler
	logger     *slog.Logger
	httpServer *http.Server

	// Rate limiting per player
	playerRates   map[string]*rateLimiter // key: matchID:playerID
	playerRatesMu sync.RWMutex

	// Connection tracking
	activeConns atomic.Int64

	// Metrics
	FramesReceived  atomic.Int64
	FramesRejected  atomic.Int64
	FramesProcessed atomic.Int64
	BytesReceived   atomic.Int64
	ActiveMatchCount atomic.Int64
}

type rateLimiter struct {
	lastFrame time.Time
	count     int
	window    time.Time // start of current 1-second window
}

// NewServer creates a telemetry ingestion server.
func NewServer(cfg ServerConfig, handler FrameHandler, logger *slog.Logger) *Server {
	return &Server{
		cfg:         cfg,
		handler:     handler,
		logger:      logger,
		playerRates: make(map[string]*rateLimiter),
	}
}

// Start begins accepting connections. Blocks until context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.Handle("/telemetry", websocket.Handler(s.handleConnection))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","connections":%d,"frames_received":%d,"frames_rejected":%d}`,
			s.activeConns.Load(), s.FramesReceived.Load(), s.FramesRejected.Load())
	})

	s.httpServer = &http.Server{
		Addr:         s.cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  s.cfg.ReadTimeout,
		WriteTimeout: s.cfg.WriteTimeout,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.httpServer.Shutdown(shutdownCtx)
	}()

	s.logger.Info("telemetry server starting", "addr", s.cfg.ListenAddr)
	if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// handleConnection manages a single game server WebSocket connection.
func (s *Server) handleConnection(ws *websocket.Conn) {
	if int(s.activeConns.Load()) >= s.cfg.MaxConnectionsPerServer {
		s.logger.Warn("connection rejected: max connections reached")
		ws.Close()
		return
	}
	s.activeConns.Add(1)
	defer s.activeConns.Add(-1)
	defer ws.Close()

	// Auth check
	if s.cfg.AuthToken != "" {
		token := ws.Request().Header.Get("Authorization")
		if token != "Bearer "+s.cfg.AuthToken {
			s.logger.Warn("connection rejected: invalid auth")
			return
		}
	}

	remoteAddr := ws.Request().RemoteAddr
	s.logger.Info("game server connected", "remote", remoteAddr)
	defer s.logger.Info("game server disconnected", "remote", remoteAddr)

	// Set max frame size to prevent memory abuse
	ws.MaxPayloadBytes = s.cfg.MaxFrameSize

	for {
		var raw json.RawMessage
		if err := websocket.JSON.Receive(ws, &raw); err != nil {
			return // connection closed or error
		}
		s.BytesReceived.Add(int64(len(raw)))

		// Parse batch message
		var batch FrameBatch
		if err := json.Unmarshal(raw, &batch); err != nil {
			s.FramesRejected.Add(1)
			s.logger.Debug("malformed frame batch", "error", err)
			continue
		}

		// Validate batch
		if batch.MatchID == "" {
			s.FramesRejected.Add(int64(len(batch.Frames)))
			continue
		}
		if len(batch.Frames) > 100 {
			s.FramesRejected.Add(int64(len(batch.Frames)))
			s.logger.Warn("oversized batch rejected", "match", batch.MatchID, "frames", len(batch.Frames))
			continue
		}

		// Rate limit per player
		var accepted []model.PlayerTelemetryFrame
		for _, frame := range batch.Frames {
			if s.checkRateLimit(batch.MatchID, frame.PlayerID) {
				if err := validateIngestFrame(&frame); err != nil {
					s.FramesRejected.Add(1)
					continue
				}
				accepted = append(accepted, frame)
				s.FramesReceived.Add(1)
			} else {
				s.FramesRejected.Add(1)
			}
		}

		if len(accepted) > 0 {
			s.handler(batch.MatchID, accepted)
			s.FramesProcessed.Add(int64(len(accepted)))
		}
	}
}

// checkRateLimit enforces per-player frame rate limits.
func (s *Server) checkRateLimit(matchID, playerID string) bool {
	key := matchID + ":" + playerID
	now := time.Now()

	s.playerRatesMu.Lock()
	defer s.playerRatesMu.Unlock()

	rl, ok := s.playerRates[key]
	if !ok {
		rl = &rateLimiter{window: now}
		s.playerRates[key] = rl
	}

	// Reset window every second
	if now.Sub(rl.window) > time.Second {
		rl.count = 0
		rl.window = now
	}

	if rl.count >= s.cfg.MaxFrameRatePerPlayer {
		return false
	}
	rl.count++
	rl.lastFrame = now
	return true
}

// CleanupStaleRateLimiters removes entries older than 5 minutes.
func (s *Server) CleanupStaleRateLimiters() {
	s.playerRatesMu.Lock()
	defer s.playerRatesMu.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	for key, rl := range s.playerRates {
		if rl.lastFrame.Before(cutoff) {
			delete(s.playerRates, key)
		}
	}
}

// FrameBatch is a batch of frames from a game server for a single match.
type FrameBatch struct {
	MatchID   string                       `json:"match_id"`
	ServerID  string                       `json:"server_id"`
	Timestamp time.Time                    `json:"timestamp"`
	Frames    []model.PlayerTelemetryFrame `json:"frames"`
}

// validateIngestFrame performs fast validation on an incoming frame.
func validateIngestFrame(f *model.PlayerTelemetryFrame) error {
	if f.PlayerID == "" {
		return fmt.Errorf("missing player_id")
	}
	if f.Position.IsZero() {
		return fmt.Errorf("zero position")
	}
	if f.Position.HasNaN() || f.Position.HasInf() {
		return fmt.Errorf("invalid position")
	}
	if f.Timestamp < 0 {
		return fmt.Errorf("negative timestamp")
	}
	return nil
}
