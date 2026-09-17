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

func constantPingReport(snapshots int, pings func(snapshot, player int) int, players int) *DiagnosticReport {
	diag := NewDiagnosticReport()
	for i := 0; i < snapshots; i++ {
		var blue, orange []EchoVRPlayer
		for p := 0; p < players; p++ {
			player := testPlayer("P", int64(p+1), [3]float64{1, 1.6, float64(p)})
			player.Ping = pings(i, p)
			if p%2 == 0 {
				blue = append(blue, player)
			} else {
				orange = append(orange, player)
			}
		}
		diag.RecordSession(twoTeamSession("ping-health", blue, orange))
	}
	return diag
}

func pingWarning(diag *DiagnosticReport) *TelemetryWarning {
	for _, warning := range diag.HealthWarnings() {
		if warning.Code == "ping_constant" {
			w := warning
			return &w
		}
	}
	return nil
}

// A server that measures no round-trip time advertises one fixed ping for
// every entrant. The recording must say so; nothing else may change.
func TestTelemetryHealthWarnsWhenEveryPingIsOneConstant(t *testing.T) {
	fixed := func(int, int) int { return 50 }
	w := pingWarning(constantPingReport(constantPingMinSnapshots, fixed, 4))
	if w == nil {
		t.Fatal("identical time-invariant ping for every player was not reported")
	}
	if w.Level != "warning" || w.Field != "ping" || w.Count != 4*constantPingMinSnapshots ||
		!strings.Contains(w.Message, "50 ms") || !strings.Contains(w.Message, "not meaningful") || !strings.Contains(w.Message, "not a finding about any player") {
		t.Fatalf("unexpected warning: %+v", w)
	}
	if w := pingWarning(constantPingReport(constantPingMinSnapshots, func(int, int) int { return 0 }, 2)); w == nil || !strings.Contains(w.Message, "0 ms") {
		t.Fatalf("an all-zero ping is just as meaningless and must be reported: %+v", w)
	}
}

func TestTelemetryHealthConstantPingStaysQuietWithoutEvidence(t *testing.T) {
	fixed := func(int, int) int { return 50 }
	for name, diag := range map[string]*DiagnosticReport{
		"short clip":                  constantPingReport(constantPingMinSnapshots-1, fixed, 4),
		"single player":               constantPingReport(constantPingMinSnapshots*3, fixed, 1),
		"players differ":              constantPingReport(constantPingMinSnapshots, func(_, p int) int { return 40 + p }, 4),
		"one sample moved once":       constantPingReport(constantPingMinSnapshots, func(i, p int) int { return 50 + map[bool]int{true: 1}[i == 400 && p == 2] }, 4),
		"one zero among real values":  constantPingReport(constantPingMinSnapshots, func(i, p int) int { return map[bool]int{false: 50}[i == 10 && p == 0] }, 4),
		"ordinary jittering real RTT": constantPingReport(constantPingMinSnapshots, func(i, p int) int { return 45 + (i/30+p*7)%11 }, 4),
	} {
		if w := pingWarning(diag); w != nil {
			t.Errorf("%s: constant-ping warning must not fire: %+v", name, w)
		}
	}
}
