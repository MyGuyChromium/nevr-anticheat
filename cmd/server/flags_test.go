package main

import (
	"flag"
	"reflect"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/ingest"
)

func parseServerFlags(t *testing.T, args ...string) (*flag.FlagSet, *serverFlags) {
	t.Helper()
	fs := flag.NewFlagSet("nevr-server", flag.ContinueOnError)
	f := registerServerFlags(fs, config.DefaultConfig().Server)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return fs, f
}

// fileServerSection is a [server] block that differs from every default.
func fileServerSection() config.ServerConfig {
	return config.ServerConfig{
		Listen:                ":7000",
		Metrics:               ":7001",
		AllowUnauthenticated:  true,
		MaxMatches:            8,
		MaxPlayersPerMatch:    10,
		MaxConnections:        7,
		MaxMessageBytes:       4096,
		MaxFrameRatePerPlayer: 12,
		IdleTimeout:           90 * time.Second,
		StaleMatchAfter:       9 * time.Minute,
		PersistInterval:       45 * time.Second,
	}
}

// TestResolveServerConfig_Precedence: file beats compiled default, explicit
// flag beats file, and an absent flag never overwrites a file value.
func TestResolveServerConfig_Precedence(t *testing.T) {
	file := fileServerSection()

	// No flags: the file section is used verbatim.
	fs, f := parseServerFlags(t)
	if got := resolveServerConfig(file, fs, f); !reflect.DeepEqual(got, file) {
		t.Fatalf("no flags: got %+v, want the file section %+v", got, file)
	}

	// Explicit flags override only the keys they name.
	fs, f = parseServerFlags(t, "--max-matches", "3", "--idle-timeout", "20s", "--allow-unauthenticated=false", "--listen", ":9000")
	got := resolveServerConfig(file, fs, f)
	want := file
	want.MaxMatches = 3
	want.IdleTimeout = 20 * time.Second
	want.AllowUnauthenticated = false
	want.Listen = ":9000"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flags over file:\n got %+v\nwant %+v", got, want)
	}

	// A flag passed with its default value still wins over the file.
	def := config.DefaultConfig().Server
	fs, f = parseServerFlags(t, "--max-matches", "64", "--metrics", def.Metrics)
	got = resolveServerConfig(file, fs, f)
	if got.MaxMatches != 64 || got.Metrics != def.Metrics {
		t.Fatalf("explicit default-valued flag must win: %+v", got)
	}
	if got.MaxConnections != file.MaxConnections || got.PersistInterval != file.PersistInterval {
		t.Fatalf("unrelated file keys clobbered: %+v", got)
	}
}

// TestResolveServerConfig_DefaultsAreTheCompiledDefaults: with an untouched
// config and no flags the result is DefaultConfig().Server, which the tests
// package pins to ingest.DefaultServerConfig().
func TestResolveServerConfig_DefaultsAreTheCompiledDefaults(t *testing.T) {
	fs, f := parseServerFlags(t)
	def := config.DefaultConfig().Server
	if got := resolveServerConfig(def, fs, f); !reflect.DeepEqual(got, def) {
		t.Fatalf("got %+v, want %+v", got, def)
	}
	for _, name := range []string{"listen", "metrics", "allow-unauthenticated", "max-matches", "max-players",
		"max-connections", "max-message-bytes", "max-frame-rate", "idle-timeout", "stale-match-after", "persist-interval"} {
		if fs.Lookup(name) == nil {
			t.Errorf("flag --%s is not registered", name)
		}
	}
}

func TestIngestServerConfig(t *testing.T) {
	sv := fileServerSection()
	sv.AllowUnauthenticated = false
	got := ingestServerConfig(sv, "secret", false)
	def := ingest.DefaultServerConfig()
	if got.ListenAddr != ":7000" || got.AuthToken != "secret" || got.AllowUnauthenticated ||
		got.MaxConnectionsPerServer != 7 || got.MaxFrameSize != 4096 || got.MaxFrameRatePerPlayer != 12 ||
		got.MaxPlayersPerMatch != 10 || got.ReadTimeout != 90*time.Second {
		t.Fatalf("mapping wrong: %+v", got)
	}
	if got.MaxFramesPerBatch != def.MaxFramesPerBatch || got.MaxIDLength != def.MaxIDLength ||
		got.WriteTimeout != def.WriteTimeout || got.AckInterval != def.AckInterval {
		t.Fatalf("non-config limits must keep the ingest defaults: %+v", got)
	}
	// NEVR_AC_ALLOW_UNAUTH=1 and [server] allow_unauthenticated each open the endpoint.
	if !ingestServerConfig(sv, "", true).AllowUnauthenticated {
		t.Fatal("env opt-in ignored")
	}
	sv.AllowUnauthenticated = true
	if !ingestServerConfig(sv, "", false).AllowUnauthenticated {
		t.Fatal("config opt-in ignored")
	}
}
