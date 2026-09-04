package adapter

import (
	"strings"
	"testing"
)

func TestTelemetryHealthWarningsAndUnknownFields(t *testing.T) {
	raw := `{
		"sessionid":"M1","game_status":"playing","game_clock":10,"blue_points":0,"orange_points":0,
		"disc":{"position":[0,0,1],"velocity":[1,0,0],"future_disc_field":7},
		"teams":[{"team":"BLUE TEAM","players":[{
			"name":"P","userid":1,"playerid":0,"body":{"position":[1,1,1],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},
			"head":{"position":[1,1,1]},"velocity":[1,0,0],
			"lhand":{"pos":[1,1,2]},"rhand":{"pos":[1,1,0]},
			"stunned":false,"invulnerable":false,"possession":false,"blocking":false,
			"holding_left":"none","holding_right":"none","ping":20,"stats":{},"future_player_field":true
		}]}],"future_top_field":{"x":1}
	}`
	diag := NewDiagnosticReport()
	if _, err := diag.RecordSessionJSON([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if diag.FieldPresence["velocity"].Present != 1 {
		t.Fatalf("velocity presence = %+v", diag.FieldPresence["velocity"])
	}
	warnings := diag.HealthWarnings()
	var unknown bool
	for _, warning := range warnings {
		if warning.Code == "required_field_unavailable" {
			t.Errorf("complete required telemetry was marked unavailable: %+v", warning)
		}
		if warning.Code == "unknown_fields" {
			unknown = strings.Contains(warning.Message, "top.future_top_field") &&
				strings.Contains(warning.Message, "disc.future_disc_field") &&
				strings.Contains(warning.Message, "player.future_player_field")
		}
	}
	if !unknown {
		t.Fatalf("unknown field warning missing or incomplete: %+v", warnings)
	}
}

func TestTelemetryHealthWarningsRequiredFields(t *testing.T) {
	diag := NewDiagnosticReport()
	if _, err := diag.RecordSessionJSON([]byte(`{"sessionid":"M","teams":[]}`)); err != nil {
		t.Fatal(err)
	}
	var required int
	for _, warning := range diag.HealthWarnings() {
		if warning.Code == "required_field_unavailable" {
			required++
		}
	}
	if required == 0 {
		t.Fatal("expected required-field warnings")
	}
}
