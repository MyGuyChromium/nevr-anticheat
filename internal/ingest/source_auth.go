package ingest

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"unicode"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
)

var ErrSourceGrantsRequired = errors.New("ingest: non-loopback listeners require operator-configured source grants; legacy shared-token or unauthenticated ingestion is loopback development only")

// SourceGrant pairs trusted operator policy with its environment-only secret.
// A grant authorizes source assertions; it does not authenticate game physics,
// a broadcaster's HTTP response, or a player's independent identity.
type SourceGrant struct {
	config.SourceGrantConfig
	Token string
}

func literalLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// ValidateServerAuth is also called before cmd/server opens its database.
func ValidateServerAuth(cfg ServerConfig) error {
	if len(cfg.SourceGrants) == 0 {
		if !literalLoopbackListen(cfg.ListenAddr) {
			return ErrSourceGrantsRequired
		}
		if cfg.AuthToken == "" && !cfg.AllowUnauthenticated {
			return ErrAuthTokenRequired
		}
		return nil
	}
	if cfg.AllowUnauthenticated {
		return errors.New("ingest: source grants cannot be combined with allow_unauthenticated")
	}
	policies := make([]config.SourceGrantConfig, len(cfg.SourceGrants))
	secrets := map[string]bool{}
	for i, g := range cfg.SourceGrants {
		policies[i] = g.SourceGrantConfig
		if g.Token == "" || len(g.Token) > 4096 || strings.TrimSpace(g.Token) != g.Token || strings.ContainsFunc(g.Token, unicode.IsControl) || secrets[g.Token] {
			return fmt.Errorf("ingest: source grant %d has a missing, invalid or reused credential", i)
		}
		secrets[g.Token] = true
	}
	return config.ValidateSourceGrants(policies)
}

func cloneSourceGrants(in []SourceGrant) []SourceGrant {
	out := append([]SourceGrant(nil), in...)
	for i := range out {
		out[i].ServerIDs = append([]string(nil), out[i].ServerIDs...)
		out[i].MatchIDs = append([]string(nil), out[i].MatchIDs...)
		out[i].PlayerIDs = append([]string(nil), out[i].PlayerIDs...)
	}
	return out
}

// authenticate returns only trusted policy, never an identity from the wire.
// nil means explicitly permitted loopback development mode.
func (s *Server) authenticate(header string) (*SourceGrant, bool) {
	if len(s.cfg.SourceGrants) > 0 {
		var matched *SourceGrant
		for i := range s.cfg.SourceGrants {
			g := &s.cfg.SourceGrants[i]
			if subtle.ConstantTimeCompare([]byte(header), []byte("Bearer "+g.Token)) == 1 {
				matched = g
			}
		}
		return matched, matched != nil
	}
	if s.cfg.AuthToken == "" {
		return nil, s.cfg.AllowUnauthenticated && literalLoopbackListen(s.cfg.ListenAddr)
	}
	return nil, literalLoopbackListen(s.cfg.ListenAddr) && subtle.ConstantTimeCompare([]byte(header), []byte("Bearer "+s.cfg.AuthToken)) == 1
}

func (g *SourceGrant) allowsMatch(matchID, serverID string) bool {
	return g == nil || containsString(g.ServerIDs, serverID) && (g.AllowAnyMatch || containsString(g.MatchIDs, matchID))
}

func (g *SourceGrant) allowsPlayer(playerID string) bool {
	return g == nil || g.AllowAnyPlayer || containsString(g.PlayerIDs, playerID)
}
