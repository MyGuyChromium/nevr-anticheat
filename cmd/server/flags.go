package main

import (
	"flag"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/ingest"
)

// serverFlags binds the command-line flags that mirror the [server] section
// of the config. Precedence, lowest to highest: compiled defaults
// (config.DefaultConfig().Server, also what --help shows), the [server]
// section of the loaded file, then any flag the user explicitly set.
type serverFlags struct {
	listen          *string
	metrics         *string
	allowUnauth     *bool
	maxMatches      *int
	maxPlayers      *int
	maxConns        *int
	maxMessageBytes *int
	maxFrameRate    *int
	idleTimeout     *time.Duration
	staleAfter      *time.Duration
	persistEvery    *time.Duration
}

// registerServerFlags defines the [server] flags on fs with def as the
// defaults shown by --help.
func registerServerFlags(fs *flag.FlagSet, def config.ServerConfig) *serverFlags {
	return &serverFlags{
		listen:  fs.String("listen", def.Listen, "Telemetry listen address ([server] listen)"),
		metrics: fs.String("metrics", def.Metrics, "Metrics listen address ([server] metrics)"),
		allowUnauth: fs.Bool("allow-unauthenticated", def.AllowUnauthenticated,
			"Run without NEVR_AC_AUTH_TOKEN (INSECURE; also enabled by NEVR_AC_ALLOW_UNAUTH=1 or [server] allow_unauthenticated)"),
		maxMatches:      fs.Int("max-matches", def.MaxMatches, "Maximum concurrently tracked live matches ([server] max_matches)"),
		maxPlayers:      fs.Int("max-players", def.MaxPlayersPerMatch, "Maximum players per live match ([server] max_players_per_match)"),
		maxConns:        fs.Int("max-connections", def.MaxConnections, "Maximum concurrent telemetry connections ([server] max_connections)"),
		maxMessageBytes: fs.Int("max-message-bytes", def.MaxMessageBytes, "Maximum bytes per WebSocket message ([server] max_message_bytes)"),
		maxFrameRate:    fs.Int("max-frame-rate", def.MaxFrameRatePerPlayer, "Maximum frames per second per player ([server] max_frame_rate_per_player)"),
		idleTimeout:     fs.Duration("idle-timeout", def.IdleTimeout, "Disconnect a peer silent for this long ([server] idle_timeout)"),
		staleAfter:      fs.Duration("stale-match-after", def.StaleMatchAfter, "Finalize a live match idle for this long ([server] stale_match_after)"),
		persistEvery:    fs.Duration("persist-interval", def.PersistInterval, "How often live match context is re-persisted ([server] persist_interval)"),
	}
}

// resolveServerConfig returns base (the [server] section of the loaded
// config) with every flag the user explicitly set on fs overriding it. Only
// flags that appear on the command line count (flag.FlagSet.Visit), so a
// flag passed with its default value still wins over the file, and a flag
// left out never clobbers a file value with the compiled default.
func resolveServerConfig(base config.ServerConfig, fs *flag.FlagSet, f *serverFlags) config.ServerConfig {
	out := base
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "listen":
			out.Listen = *f.listen
		case "metrics":
			out.Metrics = *f.metrics
		case "allow-unauthenticated":
			out.AllowUnauthenticated = *f.allowUnauth
		case "max-matches":
			out.MaxMatches = *f.maxMatches
		case "max-players":
			out.MaxPlayersPerMatch = *f.maxPlayers
		case "max-connections":
			out.MaxConnections = *f.maxConns
		case "max-message-bytes":
			out.MaxMessageBytes = *f.maxMessageBytes
		case "max-frame-rate":
			out.MaxFrameRatePerPlayer = *f.maxFrameRate
		case "idle-timeout":
			out.IdleTimeout = *f.idleTimeout
		case "stale-match-after":
			out.StaleMatchAfter = *f.staleAfter
		case "persist-interval":
			out.PersistInterval = *f.persistEvery
		}
	})
	return out
}

// ingestServerConfig turns the resolved [server] settings into the ingest
// server's configuration. authToken is NEVR_AC_AUTH_TOKEN; allowUnauthEnv
// reports NEVR_AC_ALLOW_UNAUTH=1, which opts into an open endpoint just like
// the flag or the config key. Limits the config does not expose
// (MaxFramesPerBatch, MaxIDLength, write/ack timing) keep the ingest defaults.
func ingestServerConfig(sv config.ServerConfig, authToken string, allowUnauthEnv bool) ingest.ServerConfig {
	out := ingest.DefaultServerConfig()
	out.ListenAddr = sv.Listen
	out.AuthToken = authToken
	out.AllowUnauthenticated = sv.AllowUnauthenticated || allowUnauthEnv
	out.MaxConnectionsPerServer = sv.MaxConnections
	out.MaxFrameSize = sv.MaxMessageBytes
	out.MaxFrameRatePerPlayer = sv.MaxFrameRatePerPlayer
	out.MaxPlayersPerMatch = sv.MaxPlayersPerMatch
	out.ReadTimeout = sv.IdleTimeout
	return out
}
