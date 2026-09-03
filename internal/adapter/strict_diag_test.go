package adapter

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// F99: strict bounds follow the confirmed geometry (X narrow, Z long).
func TestStrictMapper_BoundsFromPhysics(t *testing.T) {
	sm := NewStrictMapper()

	// Deep in the arena along Z (goal region): must pass.
	deep := testPlayer("Deep", 1, [3]float64{2, 1.6, 60})
	r := sm.MapSessionStrict(twoTeamSession("s", []EchoVRPlayer{deep}, nil))
	if len(r.Frames) != 1 || len(sm.Errors()) != 0 {
		t.Errorf("z=60 should be inside bounds: frames=%d errors=%v", len(r.Frames), sm.Errors())
	}

	// 30 m sideways is outside the 15 m-wide arena even with tolerance.
	wide := testPlayer("Wide", 2, [3]float64{30, 1.6, 0})
	r = sm.MapSessionStrict(twoTeamSession("s", []EchoVRPlayer{wide}, nil))
	if len(r.Frames) != 0 {
		t.Errorf("x=30 should fail strict bounds")
	}
	found := false
	for _, e := range sm.Errors() {
		if e.Field == "position" && strings.Contains(e.Issue, "out of arena bounds") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected position bound error, got %v", sm.Errors())
	}

	// Bounds derive from the physics constants supplied.
	sm2 := NewStrictMapper()
	phys := model.DefaultPhysics()
	phys.ArenaWidth = 100
	sm2.SetPhysics(phys)
	r = sm2.MapSessionStrict(twoTeamSession("s", []EchoVRPlayer{wide}, nil))
	if len(r.Frames) != 1 {
		t.Errorf("with ArenaWidth=100, x=30 should pass: %v", sm2.Errors())
	}
	if r.MatchCtx.Physics.ArenaWidth != 100 {
		t.Error("strict physics not propagated to match context")
	}
}

// reportLine returns the report line that starts with the given detector id.
func reportLine(report, id string) string {
	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(line, id+" ") {
			return line
		}
	}
	return ""
}

// F94: key presence is tracked separately from value activity.
func TestDiagnostics_PresenceVsInactive(t *testing.T) {
	data, err := os.ReadFile(fixtureDir + "echovr_session_normal.json")
	if err != nil {
		t.Fatal(err)
	}
	diag := NewDiagnosticReport()
	session, err := diag.RecordSessionJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID == "" {
		t.Error("decoded session not returned")
	}
	if !diag.PresenceTracked {
		t.Error("PresenceTracked should be set")
	}

	// stunned/blocking/invulnerable are present but false in the fixture.
	for _, key := range []string{"stunned", "blocking", "invulnerable"} {
		fd := diag.FieldPresence[key]
		if fd == nil || fd.Inactive != 2 || fd.Missing != 0 || fd.Present != 0 {
			t.Errorf("%s = %+v, want inactive=2 missing=0", key, fd)
		}
	}
	// possession and ping keys are absent from the fixture entirely.
	for _, key := range []string{"possession", "ping"} {
		fd := diag.FieldPresence[key]
		if fd == nil || fd.Missing != 2 || fd.Inactive != 0 {
			t.Errorf("%s = %+v, want missing=2", key, fd)
		}
	}
	// position present and non-zero.
	if fd := diag.FieldPresence["position"]; fd == nil || fd.Present != 2 {
		t.Errorf("position = %+v", fd)
	}

	report := diag.CompatibilityReport()
	if line := reportLine(report, "STATE_002"); !strings.Contains(line, "OK (inactive in sample: stunned)") {
		t.Errorf("STATE_002 should be OK-inactive, got %q in report:\n%s", line, report)
	}
	if line := reportLine(report, "THROW_001-008"); !strings.Contains(line, "MISSING: possession") {
		t.Errorf("THROW should be MISSING possession (key absent), got %q", line)
	}
	if line := reportLine(report, "MOV_001"); strings.TrimSpace(strings.TrimPrefix(line, "MOV_001")) != "OK" {
		t.Errorf("MOV_001 should be OK, got %q", line)
	}

	// Struct-only path cannot tell absent from false: UNVERIFIED, never MISSING
	// for a field that is merely false.
	var s EchoVRSessionResponse
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	d2 := NewDiagnosticReport()
	d2.RecordSession(&s)
	r2 := d2.CompatibilityReport()
	line := reportLine(r2, "STATE_002")
	if !strings.Contains(line, "UNVERIFIED") {
		t.Errorf("struct path should report UNVERIFIED for stunned, got %q", line)
	}
	if strings.Contains(line, "MISSING") {
		t.Errorf("false must not be reported as MISSING: %q", line)
	}
	if !strings.Contains(d2.FormatReport(), "key presence not tracked") {
		t.Error("FormatReport should say presence is not tracked")
	}
}

func TestDiagnostics_SpectatorsExcluded(t *testing.T) {
	data, err := os.ReadFile(fixtureDir + "echovr_session_three_teams.json")
	if err != nil {
		t.Fatal(err)
	}
	diag := NewDiagnosticReport()
	if _, err := diag.RecordSessionJSON(data); err != nil {
		t.Fatal(err)
	}
	if diag.PlayerCount != 2 || diag.SpectatorEntriesDropped != 1 || diag.PlayerEntriesSeen != 2 {
		t.Errorf("players=%d spectators=%d entries=%d", diag.PlayerCount, diag.SpectatorEntriesDropped, diag.PlayerEntriesSeen)
	}
	// The spectator sits at y=12, z=60: excluded, so no out-of-bounds count.
	if diag.PositionOutOfBounds != 0 {
		t.Errorf("spectator position counted as out of bounds")
	}
	if fd := diag.FieldPresence["possession"]; fd == nil || fd.Present != 1 || fd.Inactive != 1 {
		t.Errorf("possession = %+v", fd)
	}
	if fd := diag.FieldPresence["ping"]; fd == nil || fd.Present != 2 {
		t.Errorf("ping = %+v", fd)
	}
}

func TestDiagnostics_RecordMappingResult(t *testing.T) {
	diag := NewDiagnosticReport()
	diag.RecordMappingResult(&MappingResult{Frames: make([]model.PlayerTelemetryFrame, 3), Errors: []MappingError{{Field: "body.position"}}})
	diag.RecordMappingResult(&MappingResult{SkippedDuplicate: true})
	diag.RecordMappingResult(nil)
	if diag.FramesMapped != 3 || diag.PlayerFramesRejected != 1 || diag.SnapshotsDuplicate != 1 || diag.RejectionsByField["body.position"] != 1 {
		t.Errorf("diag = %+v", diag)
	}
}
