package main

import (
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
)

func TestResolveSourceCredentialsFailClosed(t *testing.T) {
	sv := config.DefaultConfig().Server
	sv.SourceGrants = []config.SourceGrantConfig{{Principal: "bridge", TokenEnv: "TEST_INGEST_TOKEN", ServerIDs: []string{"source-a"}, MatchIDs: []string{"M1"}, PlayerIDs: []string{"P1"}}}
	if _, err := resolveIngestConfig(sv, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "TEST_INGEST_TOKEN") {
		t.Fatalf("missing env must fail clearly: %v", err)
	}
	secret := "test-principal-secret-never-print"
	getenv := func(key string) string {
		if key == "TEST_INGEST_TOKEN" {
			return secret
		}
		return ""
	}
	got, err := resolveIngestConfig(sv, getenv)
	if err != nil || len(got.SourceGrants) != 1 || got.SourceGrants[0].Token != secret {
		t.Fatalf("valid credential resolution failed: %v", err)
	}
	second := sv.SourceGrants[0]
	second.Principal, second.TokenEnv, second.ServerIDs = "other", "OTHER_TOKEN", []string{"source-b"}
	sv.SourceGrants = append(sv.SourceGrants, second)
	_, err = resolveIngestConfig(sv, func(key string) string {
		if key == "TEST_INGEST_TOKEN" || key == "OTHER_TOKEN" {
			return secret
		}
		return ""
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("reused principal credentials must fail without printing secret")
	}
}
