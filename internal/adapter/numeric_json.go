package adapter

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Some replay exporters serialize integer-valued fields as JSON decimals
// (for example blue_points: 5.0). Accept only mathematically integral values,
// never a rounded float64. Identity fields retain their existing strict integer
// wire representation; this compatibility applies only to integer measurements.
// This type is a decoding detail, not a change to the telemetry schema.
type integralInt int

func (n *integralInt) UnmarshalJSON(data []byte) error {
	if strings.Trim(string(data), " \t\r\n") == "null" {
		return nil // Match encoding/json's existing integer null semantics.
	}
	v, err := parseIntegralJSON(data, strconv.IntSize)
	if err == nil {
		*n = integralInt(v)
	}
	return err
}

func parseIntegralJSON(data []byte, bits int) (int64, error) {
	raw := strings.Trim(string(data), " \t\r\n")
	invalid := func() (int64, error) {
		preview := raw
		if len(preview) > 64 {
			preview = preview[:64] + "..."
		}
		return 0, fmt.Errorf("expected an exact %d-bit JSON integer, got %q", bits, preview)
	}
	if raw == "" || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) || !json.Valid([]byte(raw)) {
		return invalid()
	}
	negative := raw[0] == '-'
	number := raw
	if negative {
		number = number[1:]
	}
	mantissa, exponentText := number, "0"
	if i := strings.IndexAny(number, "eE"); i >= 0 {
		mantissa, exponentText = number[:i], number[i+1:]
	}
	fractionDigits := 0
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		fractionDigits = len(mantissa) - i - 1
		mantissa = mantissa[:i] + mantissa[i+1:]
	}
	digits := strings.TrimLeft(mantissa, "0")
	if digits == "" {
		return 0, nil
	}
	exponent, err := strconv.ParseInt(exponentText, 10, 64)
	// Bound exponent arithmetic and allocation by the input length, not by an
	// attacker-controlled exponent. A nonzero value outside these bounds cannot
	// be both integral and representable in a signed 64-bit integer.
	if err != nil || exponent > int64(len(raw))+19 || exponent < -int64(len(raw)) {
		return invalid()
	}
	scale := exponent - int64(fractionDigits)
	if scale < 0 {
		remove := -scale
		if remove >= int64(len(digits)) {
			return invalid()
		}
		cut := len(digits) - int(remove)
		if strings.Trim(digits[cut:], "0") != "" {
			return invalid()
		}
		digits = digits[:cut]
	} else if scale > 0 {
		if int64(len(digits))+scale > 19 {
			return invalid()
		}
		digits += strings.Repeat("0", int(scale))
	}
	if negative {
		digits = "-" + digits
	}
	value, err := strconv.ParseInt(digits, 10, bits)
	if err != nil {
		return invalid()
	}
	return value, nil
}

type sessionIntegralJSON EchoVRSessionResponse

func (s *EchoVRSessionResponse) UnmarshalJSON(data []byte) error {
	decoded := sessionIntegralJSON(*s)
	fields := struct {
		*sessionIntegralJSON
		BluePoints   integralInt `json:"blue_points"`
		OrangePoints integralInt `json:"orange_points"`
	}{sessionIntegralJSON: &decoded, BluePoints: integralInt(s.BluePoints), OrangePoints: integralInt(s.OrangePoints)}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	decoded.BluePoints, decoded.OrangePoints = int(fields.BluePoints), int(fields.OrangePoints)
	*s = EchoVRSessionResponse(decoded)
	return nil
}

type playerStatsIntegralJSON EchoVRPlayerStats

func (s *EchoVRPlayerStats) UnmarshalJSON(data []byte) error {
	decoded := playerStatsIntegralJSON(*s)
	fields := struct {
		*playerStatsIntegralJSON
		Points        integralInt `json:"points"`
		Goals         integralInt `json:"goals"`
		Assists       integralInt `json:"assists"`
		Saves         integralInt `json:"saves"`
		Steals        integralInt `json:"steals"`
		Stuns         integralInt `json:"stuns"`
		Passes        integralInt `json:"passes"`
		Catches       integralInt `json:"catches"`
		Blocks        integralInt `json:"blocks"`
		Interceptions integralInt `json:"interceptions"`
		ShotsOnGoal   integralInt `json:"shots_taken"`
	}{playerStatsIntegralJSON: &decoded,
		Points: integralInt(s.Points), Goals: integralInt(s.Goals), Assists: integralInt(s.Assists),
		Saves: integralInt(s.Saves), Steals: integralInt(s.Steals), Stuns: integralInt(s.Stuns),
		Passes: integralInt(s.Passes), Catches: integralInt(s.Catches), Blocks: integralInt(s.Blocks),
		Interceptions: integralInt(s.Interceptions), ShotsOnGoal: integralInt(s.ShotsOnGoal)}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	decoded.Points, decoded.Goals, decoded.Assists = int(fields.Points), int(fields.Goals), int(fields.Assists)
	decoded.Saves, decoded.Steals, decoded.Stuns = int(fields.Saves), int(fields.Steals), int(fields.Stuns)
	decoded.Passes, decoded.Catches, decoded.Blocks = int(fields.Passes), int(fields.Catches), int(fields.Blocks)
	decoded.Interceptions, decoded.ShotsOnGoal = int(fields.Interceptions), int(fields.ShotsOnGoal)
	*s = EchoVRPlayerStats(decoded)
	return nil
}

type teamStatsIntegralJSON EchoVRTeamStats

func (s *EchoVRTeamStats) UnmarshalJSON(data []byte) error {
	decoded := teamStatsIntegralJSON(*s)
	fields := struct {
		*teamStatsIntegralJSON
		Points integralInt `json:"points"`
	}{teamStatsIntegralJSON: &decoded, Points: integralInt(s.Points)}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	decoded.Points = int(fields.Points)
	*s = EchoVRTeamStats(decoded)
	return nil
}

type lastScoreIntegralJSON EchoVRLastScore

func (s *EchoVRLastScore) UnmarshalJSON(data []byte) error {
	decoded := lastScoreIntegralJSON(*s)
	fields := struct {
		*lastScoreIntegralJSON
		PointAmount integralInt `json:"point_amount"`
	}{lastScoreIntegralJSON: &decoded, PointAmount: integralInt(s.PointAmount)}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	decoded.PointAmount = int(fields.PointAmount)
	*s = EchoVRLastScore(decoded)
	return nil
}
