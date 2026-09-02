// Command server runs the NEVR-Anticheat telemetry ingestion server.
// Accepts telemetry from game server profilers via WebSocket and stores it
// in the profiler database. Also runs inline detection for immediate feedback,
// but the canonical analysis path is async reprocessing from stored telemetry.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/catalog"
	"github.com/nevr-anticheat/nevr-anticheat/internal/ingest"
	"github.com/nevr-anticheat/nevr-anticheat/internal/logging"
	"github.com/nevr-anticheat/nevr-anticheat/internal/metrics"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func main() {
	os.Exit(run())
}

func run() int {
	defaults := ingest.DefaultServerConfig()

	configPath := flag.String("config", "", "Path to config file")
	listenAddr := flag.String("listen", ":8080", "Telemetry listen address")
	metricsAddr := flag.String("metrics", ":9090", "Metrics listen address")
	allowUnauth := flag.Bool("allow-unauthenticated", false,
		"Run without NEVR_AC_AUTH_TOKEN (INSECURE; also enabled by NEVR_AC_ALLOW_UNAUTH=1)")
	maxMatches := flag.Int("max-matches", ingest.DefaultMaxMatches, "Maximum concurrently tracked live matches")
	maxPlayers := flag.Int("max-players", defaults.MaxPlayersPerMatch, "Maximum players per live match")
	maxConns := flag.Int("max-connections", defaults.MaxConnectionsPerServer, "Maximum concurrent telemetry connections")
	maxFrameSize := flag.Int("max-message-bytes", defaults.MaxFrameSize, "Maximum bytes per WebSocket message")
	maxFrameRate := flag.Int("max-frame-rate", defaults.MaxFrameRatePerPlayer, "Maximum frames per second per player")
	idleTimeout := flag.Duration("idle-timeout", defaults.ReadTimeout, "Disconnect a peer silent for this long")
	staleAfter := flag.Duration("stale-match-after", 30*time.Minute, "Finalize a live match idle for this long")
	persistEvery := flag.Duration("persist-interval", ingest.DefaultPersistInterval, "How often live match context is re-persisted")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 1
	}

	logger := logging.NewLogger(cfg.General.LogLevel, cfg.General.LogFormat)

	store, err := sqlite.NewStore(cfg.General.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "store error: %v\n", err)
		return 1
	}
	defer store.Close()

	if fi, err := os.Stat(cfg.General.DBPath); err == nil && fi.Size() > 500*1024*1024 {
		logger.Warn("database file is large", "size_mb", fi.Size()/1024/1024, "path", cfg.General.DBPath)
	}

	// sqlite.NewStore applied every migration and verified the schema, or
	// failed above: there is no non-fatal migration path, so accepting
	// telemetry we cannot store is impossible by construction.

	// Metrics registry shared by the ingest server and match manager.
	m := metrics.NewMetrics()

	// Wire history provider so PAT_003 can query cross-match data
	historyProvider := sqlite.NewStoreHistoryProvider(store)

	// Detector factory — creates fresh detectors for each match
	detectorFactory := func() []detect.Detector {
		return catalog.Build(cfg, historyProvider)
	}

	// Match manager — handles telemetry ingestion and optional inline detection.
	// Inline detection is a convenience for immediate feedback. The canonical
	// analysis path is async reprocessing via the CLI (reprocess-match, etc.).
	matchMgr := ingest.NewMatchManager(cfg, store, detectorFactory, logger)
	matchMgr.SetMetrics(m)
	matchMgr.SetLimits(*maxMatches, *maxPlayers)
	matchMgr.SetPersistInterval(*persistEvery)

	// Telemetry server
	serverCfg := defaults
	serverCfg.ListenAddr = *listenAddr
	serverCfg.AuthToken = os.Getenv("NEVR_AC_AUTH_TOKEN")
	serverCfg.AllowUnauthenticated = *allowUnauth || os.Getenv("NEVR_AC_ALLOW_UNAUTH") == "1"
	serverCfg.MaxConnectionsPerServer = *maxConns
	serverCfg.MaxFrameSize = *maxFrameSize
	serverCfg.MaxFrameRatePerPlayer = *maxFrameRate
	serverCfg.MaxPlayersPerMatch = *maxPlayers
	serverCfg.ReadTimeout = *idleTimeout

	telemetryServer := ingest.NewServer(serverCfg, matchMgr, logger)
	telemetryServer.SetMetrics(m)

	// Bind before starting anything else so a bad address fails fast.
	if err := telemetryServer.Listen(); err != nil {
		if errors.Is(err, ingest.ErrAuthTokenRequired) {
			logger.Error("refusing to start: NEVR_AC_AUTH_TOKEN is not set; pass --allow-unauthenticated (or NEVR_AC_ALLOW_UNAUTH=1) to run an open ingest endpoint deliberately")
		} else {
			logger.Error("telemetry listener failed", "error", err)
		}
		return 1
	}

	// Metrics endpoint
	promExporter := metrics.NewPrometheusExporter(m)
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", promExporter.Handler())
	metricsServer := &http.Server{Addr: *metricsAddr, Handler: metricsMux}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Background maintenance
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				matchMgr.CleanupStaleMatches(*staleAfter)
				telemetryServer.CleanupStaleRateLimiters()
				// Prune DERIVED analysis outputs only. Detection events and scores are
				// recomputable from stored telemetry via reprocessing.
				// Telemetry frames (source data) are NEVER pruned automatically.
				if n, err := store.PruneOldEvents(ctx, 90*24*time.Hour); err != nil {
					logger.Error("prune events failed", "error", err)
				} else if n > 0 {
					logger.Info("pruned old events", "count", n)
				}
				if n, err := store.PruneOldScores(ctx, 30*24*time.Hour); err != nil {
					logger.Error("prune scores failed", "error", err)
				} else if n > 0 {
					logger.Info("pruned old scores", "count", n)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start metrics server
	go func() {
		logger.Info("metrics server starting", "addr", *metricsAddr)
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server error", "error", err)
		}
	}()

	// Start telemetry server; a failure here must take the process down.
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- telemetryServer.Start(ctx)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("NEVR telemetry ingestion server running",
		"telemetry", telemetryServer.Addr().String(),
		"metrics", *metricsAddr,
		"mode", cfg.General.Mode,
		"authenticated", serverCfg.AuthToken != "",
		"max_matches", *maxMatches,
	)

	exitCode := 0
	select {
	case sig := <-sigCh:
		logger.Info("shutting down...", "signal", sig.String())
	case err := <-serverErr:
		if err != nil {
			logger.Error("telemetry server error", "error", err)
			exitCode = 1
		} else {
			logger.Info("telemetry server stopped")
		}
	}
	cancel()

	// Graceful shutdown order: stop accepting and close WebSocket
	// connections, wait for in-flight batches, finalize live matches (persist
	// context, summary, scores), then the deferred store.Close runs.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := telemetryServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Warn("telemetry server shutdown", "error", err)
	}
	matchMgr.Close()
	_ = metricsServer.Shutdown(shutdownCtx)
	logger.Info("shutdown complete")
	return exitCode
}
