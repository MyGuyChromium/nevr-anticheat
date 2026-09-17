package replay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
)

// boundsPlayer is one mappable player entry of a synthetic snapshot.
func boundsPlayer(userID int, name string, x, z float64) string {
	return fmt.Sprintf(`{"userid":%d,"name":%q,"body":{"position":[%g,1.5,%g],"forward":[0,0,1],"left":[1,0,0],"up":[0,1,0]},"velocity":[0.3,0,0]}`,
		userID, name, x, z)
}

func boundsLine(tick int, session string, blue, orange []string) string {
	return fmt.Sprintf("2026/03/01 12:00:%02d.%03d\t", tick/30, (tick%30)*33) +
		`{"sessionid":"` + session + `","game_status":"playing","disc":{"position":[0,2,0],"velocity":[0,0,0]},"teams":[` +
		`{"team":"BLUE TEAM","players":[` + strings.Join(blue, ",") + `]},` +
		`{"team":"ORANGE TEAM","players":[` + strings.Join(orange, ",") + `]},` +
		`{"team":"SPECTATORS","players":[]}]}`
}

// TestAnalyzeFileDuplicateUserIDCanBeReanalyzed: a snapshot naming one user id
// twice used to yield two frames per tick for that id. The first import
// silently dropped half of them (INSERT OR IGNORE) and every forced
// re-analysis failed on the UNIQUE constraint, so the match could never be
// re-analysed. The repeats are now dropped in the mapper and counted.
func TestAnalyzeFileDuplicateUserIDCanBeReanalyzed(t *testing.T) {
	const ticks = 40
	var lines []string
	for i := 0; i < ticks; i++ {
		x := 1 + float64(i)*0.01
		lines = append(lines, boundsLine(i, "SYN-DUP-USERID",
			[]string{boundsPlayer(777, "Twin", x, 2)},
			[]string{boundsPlayer(777, "Twin", x+4, 2), boundsPlayer(778, "Other", -x, -2)}))
	}
	path := filepath.Join(t.TempDir(), "dup.echoreplay")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.AnalyzeFile(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Frames != 2*ticks || res.Telemetry.Inserted != 2*ticks || res.Telemetry.Ignored != 0 {
		t.Fatalf("first import: frames=%d telemetry=%+v; every analysed frame must be stored, none ignored", res.Frames, res.Telemetry)
	}
	if res.Diagnostics == nil || res.Diagnostics.DuplicatePlayerEntries != ticks {
		t.Fatalf("diagnostics do not count the %d dropped repeats: %+v", ticks, res.Diagnostics)
	}
	if res.Summary.FramesByPlayer["echovr:777"] != ticks || res.MatchCtx.TeamAssignments["echovr:777"] != "blue" {
		t.Fatalf("the first entry did not win: frames=%v teams=%v", res.Summary.FramesByPlayer, res.MatchCtx.TeamAssignments)
	}

	forced, err := e.AnalyzeFile(ctx, path, true)
	if err != nil {
		t.Fatalf("forced re-analysis: %v", err)
	}
	if !forced.Replaced || forced.Telemetry.Inserted != 2*ticks || forced.PersistError() != nil {
		t.Fatalf("forced re-analysis: replaced=%v telemetry=%+v persist=%v", forced.Replaced, forced.Telemetry, forced.PersistError())
	}

	// A database-only reprocess maps the stored raw ticks again: same frames.
	stored, err := e.Store().GetMatchContext(ctx, "SYN-DUP-USERID")
	if err != nil {
		t.Fatal(err)
	}
	remap, err := RemapStoredTelemetry(ctx, e.Store(), stored, e.Physics())
	if err != nil || len(remap.Frames) != 2*ticks {
		t.Fatalf("remap: err=%v frames=%d", err, len(remap.Frames))
	}
}

// TestAnalyzeFileRefusesCrowdedSnapshot: the upload path answers a crafted
// roster with a sentence, stores nothing and stays within a fixed budget
// (the measured attack took 3.7 GB of heap and wrote a 1.9 GB database).
func TestAnalyzeFileRefusesCrowdedSnapshot(t *testing.T) {
	var crowd []string
	for i := 0; i < 10_000; i++ {
		crowd = append(crowd, fmt.Sprintf(`{"userid":%d,"body":{"position":[1,1.5,%d]}}`, 100000+i, i%50+1))
	}
	honest := boundsLine(0, "SYN-CROWD", []string{boundsPlayer(1, "A", 1, 2)}, []string{boundsPlayer(2, "B", -1, -2)})
	path := filepath.Join(t.TempDir(), "crowd.echoreplay")
	if err := os.WriteFile(path, []byte(honest+"\n"+boundsLine(1, "SYN-CROWD", crowd, nil)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newTestEngine(t)
	var err error
	allocated := allocatedDuring(func() { _, err = e.AnalyzeFile(context.Background(), path, false) })
	if !errors.Is(err, adapter.ErrTooManySnapshotPlayers) {
		t.Fatalf("err = %v, want ErrTooManySnapshotPlayers", err)
	}
	for _, want := range []string{"reading echoreplay", "line 2", "too many players in one snapshot", "was not imported"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if exists, hasErr := e.Store().HasMatch(context.Background(), "SYN-CROWD"); hasErr != nil || exists {
		t.Fatalf("a refused recording left a match behind: %v %v", exists, hasErr)
	}
	if allocated > 16<<20 {
		t.Fatalf("refusing the upload allocated %d bytes, budget %d", allocated, 16<<20)
	}
}

// TestAnalyzeFileRefusesLegacyJSONFlood: .json uploads reach the legacy reader.
func TestAnalyzeFileRefusesLegacyJSONFlood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flood.json")
	doc := `{"header":{"MatchID":"SYN-JSON-FLOOD"},"frames":[{"Players":[` + strings.Repeat("{},", 100_000) + `{}]}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newTestEngine(t)
	var err error
	allocated := allocatedDuring(func() { _, err = e.AnalyzeFile(context.Background(), path, false) })
	if !errors.Is(err, ErrLegacyReplayTooManyPlayers) || !strings.Contains(err.Error(), "more than 64 players") {
		t.Fatalf("err = %v, want ErrLegacyReplayTooManyPlayers", err)
	}
	if exists, hasErr := e.Store().HasMatch(context.Background(), "SYN-JSON-FLOOD"); hasErr != nil || exists {
		t.Fatalf("a refused document left a match behind: %v %v", exists, hasErr)
	}
	if allocated > 8<<20 {
		t.Fatalf("refusing the upload allocated %d bytes, budget %d", allocated, 8<<20)
	}
}
