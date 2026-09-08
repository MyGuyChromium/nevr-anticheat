package main

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/ingest"
)

func resolveIngestConfig(sv config.ServerConfig, getenv func(string) string) (ingest.ServerConfig, error) {
	out := ingestServerConfig(sv, getenv("NEVR_AC_AUTH_TOKEN"), getenv("NEVR_AC_ALLOW_UNAUTH") == "1")
	for _, g := range sv.SourceGrants {
		token := getenv(g.TokenEnv)
		if token == "" {
			return out, fmt.Errorf("source principal %q requires environment variable %s (secret value is not stored in TOML)", g.Principal, g.TokenEnv)
		}
		out.SourceGrants = append(out.SourceGrants, ingest.SourceGrant{SourceGrantConfig: g, Token: token})
	}
	if err := ingest.ValidateServerAuth(out); err != nil {
		return out, err
	}
	return out, nil
}
