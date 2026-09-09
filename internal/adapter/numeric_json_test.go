package adapter

import (
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseIntegralJSONExactValues(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int64
	}{
		{"5", 5}, {"5.0", 5}, {"5.000000000000000000", 5}, {"5e0", 5},
		{"50e-1", 5}, {"0.5e1", 5}, {"123.4500e2", 12345}, {"1E+3", 1000},
		{"-1.0", -1}, {"-0.000", 0}, {"0e999999999999999999999999", 0},
		{"  9007199254740993.0  ", 9007199254740993},
		{"9223372036854775807.0", math.MaxInt64},
		{"-9223372036854775808.0", math.MinInt64},
		{"92233720368547758070e-1", math.MaxInt64},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseIntegralJSON([]byte(tc.input), 64)
			if err != nil || got != tc.want {
				t.Fatalf("got %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestParseIntegralJSONRejectsLossAndInvalidNumbers(t *testing.T) {
	for _, input := range []string{
		"", "null", "true", "[]", "{}", `"5"`, `"5.0"`, "NaN", "Infinity", "-Infinity",
		"01", "+1", "1.", ".0", "1e", "1e+", "--1", "1 2", "\u00a05", "5\u00a0",
		"0.1", "-0.1", "5.000000000000000001", "9007199254740993.1", "1e-1", "10.01",
		"9223372036854775808", "9223372036854775808.0", "-9223372036854775809.0",
		"1e9999999999999999999999999", "1e-9999999999999999999999999",
		"1e9223372036854775807", "1e-9223372036854775808", "1e1000000", "1e-1000000",
	} {
		t.Run(input, func(t *testing.T) {
			if got, err := parseIntegralJSON([]byte(input), 64); err == nil {
				t.Fatalf("accepted %q as %d", input, got)
			}
		})
	}
	for _, input := range []string{"2147483648.0", "-2147483649.0"} {
		if _, err := parseIntegralJSON([]byte(input), 32); err == nil {
			t.Fatalf("accepted 32-bit overflow %s", input)
		}
	}
	for _, input := range []string{"2147483647.0", "-2147483648.0"} {
		if _, err := parseIntegralJSON([]byte(input), 32); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionIntegralJSONPreservesIntegerFieldValues(t *testing.T) {
	// Synthetic data covers every integral schema field. Possession time and
	// continuous telemetry remain floating point, while a large ID stays exact.
	const input = `{"sessionid":"integral-test","blue_points":5.0,"orange_points":2e0,"possession":null,
		"last_score":{"point_amount":2.0,"disc_speed":14.9},
		"disc":{"position":[0,0,0],"velocity":[0,0,0],"bounce_count":3.0},
		"teams":[{"team":"BLUE TEAM","stats":{"points":5.0},"players":[{
		"name":"Synthetic player","userid":9007199254740993,"playerid":1,"level":50e0,"ping":42.0,
		"velocity":[0,0,0],"holding_left":"disc","holding_right":"none",
		"stats":{"points":1.0,"goals":2.0,"assists":3.0,"saves":4.0,"steals":5.0,
		"stuns":6.0,"passes":7.0,"catches":8.0,"blocks":9.0,"interceptions":10.0,
		"shots_taken":11.0,"possession_time":12.25}}]}]}`
	var session EchoVRSessionResponse
	if err := json.Unmarshal([]byte(input), &session); err != nil {
		t.Fatal(err)
	}
	player := session.Teams[0].Players[0]
	if session.BluePoints != 5 || session.OrangePoints != 2 || session.LastScore.PointAmount != 2 || session.LastScore.DiscSpeed != 14.9 || session.Teams[0].Stats.Points != 5 {
		t.Fatalf("score fields lost: %+v", session)
	}
	if player.UserID != 9007199254740993 || player.PlayerID != 1 || player.Level != 50 || player.Ping != 42 {
		t.Fatalf("integer player fields lost or rounded: %+v", player)
	}
	wantStats := EchoVRPlayerStats{Points: 1, Goals: 2, Assists: 3, Saves: 4, Steals: 5, Stuns: 6, Passes: 7, Catches: 8, Blocks: 9, Interceptions: 10, ShotsOnGoal: 11, Possession: 12.25}
	if player.Stats != wantStats {
		t.Fatalf("stats=%+v; want %+v", player.Stats, wantStats)
	}
	if !player.hasVelocity() || !player.HasHoldingFields() || !player.HoldsDisc() || !session.Disc.hasPosition() || !session.Disc.hasVelocity() || session.Disc.BounceCount == nil || *session.Disc.BounceCount != 3 {
		t.Fatal("integer compatibility changed observed zero vectors or attachment fields")
	}
	serialized, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip EchoVRSessionResponse
	if err := json.Unmarshal(serialized, &roundTrip); err != nil || !reflect.DeepEqual(session, roundTrip) {
		t.Fatalf("integer decode/marshal round trip changed telemetry: %v", err)
	}
}

func TestSessionIntegralJSONRejectsInvalidFields(t *testing.T) {
	for _, template := range []string{
		`{"blue_points":%s}`, `{"orange_points":%s}`,
		`{"last_score":{"point_amount":%s}}`, `{"disc":{"bounce_count":%s}}`,
		`{"teams":[{"stats":{"points":%s}}]}`,
		`{"teams":[{"players":[{"userid":%s}]}]}`, `{"teams":[{"players":[{"playerid":%s}]}]}`,
		`{"teams":[{"players":[{"level":%s}]}]}`, `{"teams":[{"players":[{"ping":%s}]}]}`,
	} {
		for _, value := range []string{"1.1", `"1"`, "9223372036854775808.0"} {
			data := strings.Replace(template, "%s", value, 1)
			var session EchoVRSessionResponse
			if err := json.Unmarshal([]byte(data), &session); err == nil {
				t.Errorf("accepted invalid integer field: %s", data)
			}
		}
	}
	for _, key := range []string{"points", "goals", "assists", "saves", "steals", "stuns", "passes", "catches", "blocks", "interceptions", "shots_taken"} {
		for _, value := range []string{"1.1", `"1"`, "9223372036854775808.0"} {
			var stats EchoVRPlayerStats
			if err := json.Unmarshal([]byte(`{"`+key+`":`+value+`}`), &stats); err == nil {
				t.Errorf("accepted invalid stats field %s=%s", key, value)
			}
		}
	}
}

func TestIntegralJSONDoesNotInventMissingObservations(t *testing.T) {
	for _, input := range []string{
		`{"disc":{},"teams":[{"players":[{}]}]}`,
		`{"disc":{"position":null,"velocity":null,"bounce_count":null},"teams":[{"players":[{"velocity":null}]}]}`,
		`{"disc":{"position":[0,0],"velocity":[0,null,0]},"teams":[{"players":[{"velocity":[0,0]}]}]}`,
	} {
		var session EchoVRSessionResponse
		if err := json.Unmarshal([]byte(input), &session); err != nil {
			t.Fatal(err)
		}
		player := session.Teams[0].Players[0]
		if session.Disc.hasPosition() || session.Disc.hasVelocity() || session.Disc.BounceCount != nil || player.hasVelocity() || player.HasHoldingFields() {
			t.Fatalf("unavailable observations invented for %s", input)
		}
	}
	var session EchoVRSessionResponse
	if err := json.Unmarshal([]byte(`{"blue_points":null,"orange_points":null}`), &session); err != nil || session.BluePoints != 0 || session.OrangePoints != 0 {
		t.Fatal("existing nullable zero-score decoding changed")
	}
}

func TestIntegralJSONPreservesExistingPartialAndNullDecode(t *testing.T) {
	for _, input := range []string{"null", "{}", `{"blue_points":null,"orange_points":null,"points":null,"point_amount":null}`} {
		for _, test := range []struct {
			value any
			want  any
		}{
			{&EchoVRSessionResponse{SessionID: "existing", BluePoints: 5, OrangePoints: 7}, &EchoVRSessionResponse{SessionID: "existing", BluePoints: 5, OrangePoints: 7}},
			{&EchoVRPlayerStats{Points: 5, Goals: 2}, &EchoVRPlayerStats{Points: 5, Goals: 2}},
			{&EchoVRTeamStats{Points: 5}, &EchoVRTeamStats{Points: 5}},
			{&EchoVRLastScore{PointAmount: 3, DiscSpeed: 14.9}, &EchoVRLastScore{PointAmount: 3, DiscSpeed: 14.9}},
		} {
			if err := json.Unmarshal([]byte(input), test.value); err != nil || !reflect.DeepEqual(test.value, test.want) {
				t.Fatalf("partial/null decode changed %T for %s: got %+v want %+v err=%v", test.value, input, test.value, test.want, err)
			}
		}
	}
	for _, key := range []string{"userid", "playerid"} {
		for _, value := range []string{"5.0", "5e0", `"5"`} {
			var player EchoVRPlayer
			if err := json.Unmarshal([]byte(`{"`+key+`":`+value+`}`), &player); err == nil {
				t.Fatalf("strict identity representation widened for %s=%s", key, value)
			}
		}
	}
}

func TestReplayParserIntegralScoresKeepOriginalRawJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "integral-scores.echoreplay")
	line := sessionLine(t, "2026/09/08 10:00:00.000", customTeamSession())
	line = strings.Replace(line, `"blue_points":0`, `"blue_points":5.0`, 1)
	line = strings.Replace(line, `"orange_points":0`, `"orange_points":2.0`, 1)
	writeLines(t, path, []string{line})
	seen := 0
	_, diag, err := NewEchoReplayParser().ParseFileStream(path, func(tick *ParsedTick) error {
		seen++
		if tick.Session.BluePoints != 5 || tick.Session.OrangePoints != 2 || !strings.Contains(string(tick.RawJSON), `"blue_points":5.0`) {
			t.Fatalf("integral scores or original lexical evidence changed: %+v", tick.Session)
		}
		return nil
	})
	if err != nil || seen != 1 || diag.PlayerCount != 2 || diag.FramesRejected != 0 {
		t.Fatalf("parse decimal-integral replay: seen=%d diagnostics=%+v err=%v", seen, diag, err)
	}
}
