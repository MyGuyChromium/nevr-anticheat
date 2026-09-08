package config

import (
	"fmt"
	"strings"
	"unicode"
)

const MaxSourceGrants = 64
const MaxSourceGrantIDs = 1024

// SourceGrantConfig is trusted operator policy. No field is read from a
// telemetry message; a credential authorizes these exact submitter claims.
type SourceGrantConfig struct {
	Principal      string   `toml:"principal"`
	TokenEnv       string   `toml:"token_env"`
	ServerIDs      []string `toml:"server_ids"`
	MatchIDs       []string `toml:"match_ids"`
	PlayerIDs      []string `toml:"player_ids"`
	AllowAnyMatch  bool     `toml:"allow_any_match"`
	AllowAnyPlayer bool     `toml:"allow_any_player"`
}

func ValidateSourceGrants(grants []SourceGrantConfig) error {
	if len(grants) > MaxSourceGrants {
		return fmt.Errorf("server.source_grants exceeds %d principals", MaxSourceGrants)
	}
	principals, envs, servers := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, g := range grants {
		prefix := fmt.Sprintf("server.source_grants[%d]", i)
		if !validGrantID(g.Principal) || principals[g.Principal] {
			return fmt.Errorf("%s.principal must be a unique bounded identifier", prefix)
		}
		principals[g.Principal] = true
		if !validTokenEnv(g.TokenEnv) || envs[strings.ToUpper(g.TokenEnv)] {
			return fmt.Errorf("%s.token_env must be a unique environment variable name", prefix)
		}
		envs[strings.ToUpper(g.TokenEnv)] = true
		for _, scope := range []struct {
			name string
			ids  []string
			any  bool
		}{{"server_ids", g.ServerIDs, false}, {"match_ids", g.MatchIDs, g.AllowAnyMatch}, {"player_ids", g.PlayerIDs, g.AllowAnyPlayer}} {
			if len(scope.ids) > MaxSourceGrantIDs || (len(scope.ids) == 0) != scope.any {
				return fmt.Errorf("%s.%s requires 1..%d exact IDs, or an explicit allow_any flag with an empty list", prefix, scope.name, MaxSourceGrantIDs)
			}
			seen := map[string]bool{}
			for _, id := range scope.ids {
				if !validGrantID(id) || seen[id] {
					return fmt.Errorf("%s.%s has an invalid or duplicate ID (wildcards are not supported)", prefix, scope.name)
				}
				seen[id] = true
				if scope.name == "server_ids" {
					if servers[id] {
						return fmt.Errorf("%s.server_ids overlaps another principal; each server ID must have one owner", prefix)
					}
					servers[id] = true
				}
			}
		}
	}
	return nil
}

func validGrantID(s string) bool {
	return s != "" && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.Contains(s, "*") &&
		!strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == unicode.ReplacementChar })
}

func validTokenEnv(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
