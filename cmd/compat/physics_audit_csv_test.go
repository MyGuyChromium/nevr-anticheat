package main

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The physics audit is opened in a spreadsheet, and its match id and game
// phase are copied from the recording. A recording that names them like a
// formula must not produce a live formula cell, while negative numbers stay
// numbers. Mutation: drop the CSVSafeCell loop in writePhysicsAudit and the
// match_id / game_phase assertions fail.
func TestWritePhysicsAudit_NeutralisesSpreadsheetFormulas(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay"))
	if err != nil {
		t.Fatal(err)
	}
	const session, status = `"sessionid":"SYN-FIXTURE-001"`, `"game_status":"round_start"`
	text := string(data)
	if !strings.Contains(text, session) || !strings.Contains(text, status) {
		t.Fatal("fixture no longer carries the fields this test rewrites")
	}
	text = strings.ReplaceAll(text, session, `"sessionid":"=HYPERLINK(\"http://example.invalid\",\"match\")"`)
	text = strings.ReplaceAll(text, status, `"game_status":"@sum(1+1)"`)
	replayPath := filepath.Join(t.TempDir(), "formula.echoreplay")
	if err := os.WriteFile(replayPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "physics.csv")
	rows, err := writePhysicsAudit(replayPath, outputPath)
	if err != nil || rows == 0 {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
	f, err := os.Open(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := make(map[string]int, len(records[0]))
	for i, name := range records[0] {
		col[name] = i
	}
	sawPhase, sawNegative := false, false
	for n, record := range records[1:] {
		for i, cell := range record {
			if cell == "" {
				continue
			}
			if strings.ContainsRune("=@\t\r", rune(cell[0])) {
				t.Fatalf("row %d column %s starts a formula: %q", n+1, records[0][i], cell)
			}
			if cell[0] == '+' || cell[0] == '-' {
				if _, err := strconv.ParseFloat(cell, 64); err != nil {
					t.Fatalf("row %d column %s starts a formula: %q", n+1, records[0][i], cell)
				}
				sawNegative = true
			}
		}
		if id := record[col["match_id"]]; !strings.HasPrefix(id, `'=HYPERLINK(`) {
			t.Fatalf("row %d match_id = %q, want the text kept behind a leading quote", n+1, id)
		}
		if record[col["game_phase"]] == "'@sum(1+1)" {
			sawPhase = true
		}
	}
	if !sawPhase {
		t.Error("no row carried the rewritten game phase; the phase cell was not exercised")
	}
	if !sawNegative {
		t.Error("no negative number in the export; the numbers-stay-numbers check was not exercised")
	}
}
