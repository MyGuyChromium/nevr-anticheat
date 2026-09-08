package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestSourceGrantConfigLoadsExplicitPolicy(t *testing.T) {
	text := `[[server.source_grants]]
principal = "bridge-east"
token_env = "NEVR_INGEST_EAST_TOKEN"
server_ids = ["203.0.113.10:6721"]
match_ids = ["M1"]
player_ids = ["P1"]
`
	cfg, err := LoadConfigFromReader(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	want := []SourceGrantConfig{{Principal: "bridge-east", TokenEnv: "NEVR_INGEST_EAST_TOKEN", ServerIDs: []string{"203.0.113.10:6721"}, MatchIDs: []string{"M1"}, PlayerIDs: []string{"P1"}}}
	if !reflect.DeepEqual(cfg.Server.SourceGrants, want) {
		t.Fatalf("policy overlay: %+v", cfg.Server.SourceGrants)
	}
	for _, bad := range []string{
		strings.ReplaceAll(text, `match_ids = ["M1"]`, `match_ids = ["*"]`),
		strings.ReplaceAll(text, `player_ids = ["P1"]`, `player_ids = []`),
		text + "allow_any_match = true\n",
		text + "token = 'must-not-accept-secrets-in-toml'\n",
		text + strings.ReplaceAll(text, `principal = "bridge-east"`, `principal = "bridge-west"`),
	} {
		if _, err := LoadConfigFromReader(strings.NewReader(bad)); err == nil {
			t.Fatal("invalid or ambiguous source grant accepted")
		}
	}
	anyPolicy := strings.ReplaceAll(strings.ReplaceAll(text, `match_ids = ["M1"]`, "allow_any_match = true"), `player_ids = ["P1"]`, "allow_any_player = true")
	if _, err := LoadConfigFromReader(strings.NewReader(anyPolicy)); err != nil {
		t.Fatalf("explicit dynamic policy: %v", err)
	}
}
