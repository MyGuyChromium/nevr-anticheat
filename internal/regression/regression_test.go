package regression

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func defaults(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestConfigFingerprintPinsProjectRules(t *testing.T) {
	cfg := defaults(t)
	baseline, err := ConfigFingerprint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise every current project-rule field so later additions cannot
	// silently sit outside the regression configuration pin.
	rules := reflect.ValueOf(&cfg.ProjectRules).Elem()
	for i := 0; i < rules.NumField(); i++ {
		field := rules.Field(i)
		old := reflect.New(field.Type()).Elem()
		old.Set(field)
		switch field.Kind() {
		case reflect.Float64:
			field.SetFloat(field.Float() + 1)
		case reflect.String:
			field.SetString(field.String() + "-changed")
		default:
			t.Fatalf("add mutation for project rule %s", rules.Type().Field(i).Name)
		}
		changed, err := ConfigFingerprint(cfg)
		if err != nil || changed == baseline {
			t.Fatalf("un-pinned project rule %s: %v", rules.Type().Field(i).Name, err)
		}
		field.Set(old)
	}
	cfg.General.DBPath = "different-output.db"
	cfg.General.LogLevel = "debug"
	if got, err := ConfigFingerprint(cfg); err != nil || got != baseline {
		t.Fatal("output location changed behavior fingerprint")
	}
}

func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Generated kinematics go through the real JSON reader, production pipeline,
// shadow routing and SQLite persistence, rather than mocking detector output.
func throwReplay(t *testing.T, dir string, speed float64) string {
	t.Helper()
	frames := testutil.NewFrameBuilder("echovr:1").WithTickRate(15).WithStartPos(model.Vec3{2, 1.7, 0}).ThrowSequence([]testutil.ThrowSpec{
		{HoldFrames: 35, FlightFrames: 12, GapFrames: 8, ReleaseSpeed: speed, HandSpeed: 8, DeviationDeg: 8},
	})
	// Reprocess explicit hand-attachment reports through the real raw mapper.
	// The older JSON replay format has only a possession boolean and cannot
	// truthfully supply the known attachment prerequisite for a throw.
	hand := func(pos model.Vec3, q model.Quat) adapter.EchoVRHand {
		return adapter.EchoVRHand{Position: pos, Forward: q.Rotate(model.Vec3{0, 0, 1}), Left: q.Rotate(model.Vec3{1, 0, 0}), Up: q.Rotate(model.Vec3{0, 1, 0})}
	}
	var doc strings.Builder
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, f := range frames {
		body := adapter.EchoVRBodyHead{Position: f.Position, Forward: f.Rotation.Rotate(model.Vec3{0, 0, 1}), Left: f.Rotation.Rotate(model.Vec3{1, 0, 0}), Up: f.Rotation.Rotate(model.Vec3{0, 1, 0})}
		player := adapter.EchoVRPlayer{UserID: 1, Name: "synthetic-player", Body: body, Head: body, LHand: hand(f.LeftHandPosition, f.LeftHandRotation), RHand: hand(f.RightHandPosition, f.RightHandRotation), HoldingLeft: "none", HoldingRight: "none", Possession: f.HasPossession, Ping: int(f.EstimatedPingMs)}
		if f.HasPossession {
			player.HoldingRight = "disc"
		}
		raw := adapter.EchoVRSessionResponse{SessionID: "synthetic-match", MatchType: "Echo_Arena", GameStatus: "playing", GameClock: 600 - f.Timestamp, ClientName: "synthetic-player", Teams: []adapter.EchoVRTeam{{TeamName: "BLUE TEAM", Players: []adapter.EchoVRPlayer{player}}}}
		if f.Disc != nil {
			bounce := 0
			raw.Disc = &adapter.EchoVRDisc{Position: f.Disc.Position, Velocity: f.Disc.Velocity, BounceCount: &bounce}
		}
		payload, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		doc.WriteString(start.Add(time.Duration(f.Timestamp * float64(time.Second))).Format("2006/01/02 15:04:05.000"))
		doc.WriteByte('\t')
		doc.Write(payload)
		doc.WriteByte('\n')
	}
	path := filepath.Join(dir, "generated.echoreplay")
	writeFixture(t, path, []byte(doc.String()))
	return path
}

func TestProductionPipelineCaptureCheckSyntheticThrows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		speed float64
		want  int
	}{{"under_cap", 18.7, 0}, {"over_cap", 24, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := throwReplay(t, dir, tc.speed)
			cfg := defaults(t)
			before, _ := FileSHA256(source)
			cfgBefore, _ := json.Marshal(cfg)
			manifest, report, err := Capture(context.Background(), cfg, []string{source}, filepath.Join(dir, "capture"))
			if err != nil || !report.Passed {
				t.Fatalf("capture: %+v %v", report, err)
			}
			got := 0
			for _, s := range manifest.Cases[0].Expected[0].Signals {
				if s.DetectorID == "THROW_001" {
					got++
					if !s.Shadow || s.AutoEnforce {
						t.Fatalf("unsafe production posture: %+v", s)
					}
				}
			}
			if got != tc.want {
				t.Fatalf("THROW_001 incidents=%d want=%d; %+v", got, tc.want, manifest.Cases[0].Expected)
			}
			m := manifest.Cases[0].Expected[0]
			manifest.Cases[0].Windows = []Window{{ID: "generated-release", MatchID: m.MatchID, PlayerID: "echovr:1", DetectorID: "THROW_001", Start: 30, End: 45,
				Expectation: map[bool]string{true: "signal", false: "quiet"}[tc.want > 0], Provenance: Provenance{Kind: "synthetic", Truth: "unknown"}}}
			report, err = Check(context.Background(), cfg, manifest, dir, filepath.Join(dir, "check"))
			if err != nil || !report.Passed {
				t.Fatalf("check: %+v %v", report, err)
			}
			if report.Cases[0].Windows[0].Outcome != "synthetic_behavior" || report.Cases[0].Windows[0].Samples == 0 {
				t.Fatalf("synthetic assertions are not ground truth: %+v", report.Cases[0].Windows)
			}
			after, _ := FileSHA256(source)
			cfgAfter, _ := json.Marshal(cfg)
			if before != after || string(cfgBefore) != string(cfgAfter) {
				t.Fatal("source or caller configuration mutated")
			}
			if _, err := os.Stat(filepath.Join(dir, "check", "001", "source.json")); !os.IsNotExist(err) {
				t.Fatal("private staging copy was not removed")
			}
			if _, err := os.Stat(filepath.Join(dir, "check", "001", "regression.db")); err != nil {
				t.Fatal("isolated database missing", err)
			}
		})
	}
}

func TestProductionPipelineMultipleSessionsAndRelativeManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.echoreplay")
	_, _, err := testutil.SplitReplaySessions(filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay"), path, "second-synthetic-session", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaults(t)
	manifest, report, err := Capture(context.Background(), cfg, []string{path}, filepath.Join(dir, "capture"))
	if err != nil || !report.Passed || len(manifest.Cases[0].Expected) != 2 {
		t.Fatalf("capture sessions: %+v %v", report, err)
	}
	manifest.Cases[0].Path = filepath.Base(path)
	manifestPath := filepath.Join(dir, "v1.json")
	if err := WriteJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err = Check(context.Background(), cfg, loaded, dir, filepath.Join(dir, "check"))
	if err != nil || !report.Passed {
		t.Fatalf("check sessions: %+v %v", report, err)
	}
}

func simpleManifest(t *testing.T) Manifest {
	t.Helper()
	hash, err := ConfigFingerprint(defaults(t))
	if err != nil {
		t.Fatal(err)
	}
	return Manifest{Schema: Schema, ConfigSHA256: hash, Cases: []ReplayCase{{ID: "replay-001", Path: "private.echoreplay", SHA256: strings.Repeat("a", 64), Expected: []Snapshot{{MatchID: "match", Frames: 100, MaxFrame: 99, PlayerFrames: map[string]int{"player": 100}, Signals: []Signal{}}}}}}
}

func TestManifestRejectsUnverifiedTruthAndMalformedWindows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"schema", func(m *Manifest) { m.Schema = "v999" }},
		{"duplicate_case", func(m *Manifest) { m.Cases = append(m.Cases, m.Cases[0]) }},
		{"unknown_player", func(m *Manifest) { m.Cases[0].Windows[0].PlayerID = "other" }},
		{"outside_match", func(m *Manifest) { m.Cases[0].Windows[0].End = 100 }},
		{"unknown_detector", func(m *Manifest) { m.Cases[0].Windows[0].DetectorID = "THROW_typo" }},
		{"uncertain_truth", func(m *Manifest) { m.Cases[0].Windows[0].Provenance = Provenance{Kind: "uncertain", Truth: "negative"} }},
		{"behavior_truth", func(m *Manifest) {
			m.Cases[0].Windows[0].Provenance = Provenance{Kind: "behavior_only", Truth: "positive"}
		}},
		{"confirmed_without_evidence", func(m *Manifest) { m.Cases[0].Windows[0].Provenance = Provenance{Kind: "confirmed", Truth: "negative"} }},
		{"same_reviewer_twice", func(m *Manifest) {
			m.Cases[0].Windows[0].Provenance = Provenance{Kind: "confirmed", Truth: "positive", Reviewers: []string{"reviewer", " reviewer "}, Artifacts: []Artifact{{Path: "evidence", SHA256: strings.Repeat("b", 64)}}, Note: "reviewed release"}
		}},
		{"same_reviewer_case", func(m *Manifest) {
			m.Cases[0].Windows[0].Provenance = Provenance{Kind: "confirmed", Truth: "positive", Reviewers: []string{"Reviewer", "REVIEWER"}, Artifacts: []Artifact{{Path: "evidence", SHA256: strings.Repeat("b", 64)}}, Note: "reviewed release"}
		}},
		{"placeholder_reviewer", func(m *Manifest) {
			m.Cases[0].Windows[0].Provenance = Provenance{Kind: "confirmed", Truth: "positive", Reviewers: []string{"local-owner", "reviewer"}, Artifacts: []Artifact{{Path: "evidence", SHA256: strings.Repeat("b", 64)}}, Note: "reviewed release"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := simpleManifest(t)
			m.Cases[0].Windows = []Window{{ID: "window", MatchID: "match", PlayerID: "player", DetectorID: "THROW_001", Start: 10, End: 20, Expectation: "observe", Provenance: Provenance{Kind: "uncertain", Truth: "unknown"}}}
			tc.mutate(&m)
			if err := m.Validate(); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
}

func TestWindowOutcomesRespectEvidenceStrength(t *testing.T) {
	for _, tc := range []struct {
		kind, truth, expect string
		count               int
		outcome             string
		pass                bool
	}{
		{"confirmed", "positive", "observe", 0, "unresolved_opportunity_coverage", true},
		{"confirmed", "positive", "observe", 1, "unresolved_opportunity_coverage", true},
		{"confirmed", "negative", "observe", 1, "unresolved_opportunity_coverage", true},
		{"confirmed", "negative", "observe", 0, "unresolved_opportunity_coverage", true},
		{"confirmed", "positive", "signal", 0, "unresolved_opportunity_coverage", false},
		{"confirmed", "negative", "quiet", 1, "unresolved_opportunity_coverage", false},
		{"user_reported", "negative", "observe", 1, "user_reported_contradiction", true},
		{"user_reported", "positive", "observe", 0, "user_reported_contradiction", true},
		{"uncertain", "unknown", "observe", 4, "observation_only", true},
		{"behavior_only", "unknown", "signal", 0, "missing_expected_signal", false},
		{"behavior_only", "unknown", "quiet", 1, "unexpected_signal", false},
		{"synthetic", "positive", "signal", 1, "synthetic_behavior", true},
	} {
		t.Run(tc.kind+"_"+tc.outcome, func(t *testing.T) {
			got := evaluateWindow(Window{ID: "w", Expectation: tc.expect, Provenance: Provenance{Kind: tc.kind, Truth: tc.truth}}, tc.count)
			if got.Outcome != tc.outcome || got.Passed != tc.pass {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestProductionWindowsCannotTreatStoredFramesAsOpportunities(t *testing.T) {
	for _, tc := range []struct {
		name, detector string
		enabled        bool
	}{
		{"disabled_detector", "THROW_001", false},
		// Raw /session exposes no boosting observation. Enabling this
		// detector cannot manufacture its required boost-timing input.
		{"unavailable_boost_input", "MOV_004", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := throwReplay(t, dir, 18)
			cfg := defaults(t)
			dc := cfg.Detectors[tc.detector]
			dc.Enabled = tc.enabled
			cfg.Detectors[tc.detector] = dc
			manifest, _, err := Capture(context.Background(), cfg, []string{source}, filepath.Join(dir, "capture"))
			if err != nil {
				t.Fatal(err)
			}
			snapshot := manifest.Cases[0].Expected[0]
			evidencePath := filepath.Join(dir, "synthetic-review.txt")
			writeFixture(t, evidencePath, []byte("Fictional report-contract fixture; not a real accuracy label."))
			evidenceHash, err := FileSHA256(evidencePath)
			if err != nil {
				t.Fatal(err)
			}
			for _, expectation := range []string{"quiet", "signal"} {
				truth := "negative"
				if expectation == "signal" {
					truth = "positive"
				}
				manifest.Cases[0].Windows = append(manifest.Cases[0].Windows, Window{ID: expectation, MatchID: snapshot.MatchID, PlayerID: "echovr:1", DetectorID: tc.detector,
					Start: 0, End: snapshot.MaxFrame, Expectation: expectation, Provenance: Provenance{Kind: "confirmed", Truth: truth, Reviewers: []string{"fixture-reviewer-a", "fixture-reviewer-b"},
						Artifacts: []Artifact{{Path: evidencePath, SHA256: evidenceHash}}, Note: "Synthetic report semantics test; input availability is not established by this label."}})
			}
			report, err := Check(context.Background(), cfg, manifest, dir, filepath.Join(dir, "check"))
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Cases[0].Windows) != 2 {
				t.Fatal("missing window outputs")
			}
			for _, window := range report.Cases[0].Windows {
				if window.Samples < 20 || window.Signals != 0 || window.Outcome != "unresolved_opportunity_coverage" || window.OpportunityCoverage != "unresolved_opportunity_coverage" {
					t.Fatalf("raw frame count became accuracy evidence: %+v", window)
				}
				if window.Expectation == "quiet" && (!window.Passed || window.Assertion != "expected_quiet") {
					t.Fatalf("quiet assertion lost: %+v", window)
				}
				if window.Expectation == "signal" && (window.Passed || window.Assertion != "missing_expected_signal") {
					t.Fatalf("signal assertion lost: %+v", window)
				}
			}
		})
	}
}

func TestComparisonIncludesPlayerMatchMultiplicityShadowAndEnforcement(t *testing.T) {
	base := Snapshot{MatchID: "match", Frames: 100, MaxFrame: 99, PlayerFrames: map[string]int{"player": 100}, Signals: []Signal{{MatchID: "match", PlayerID: "player", DetectorID: "THROW_001", Frame: 25, Start: 20, End: 30, Shadow: true}}}
	for _, tc := range []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"different_player", func(s *Snapshot) { s.Signals[0].PlayerID = "another" }},
		{"different_match", func(s *Snapshot) { s.MatchID = "another"; s.Signals[0].MatchID = "another" }},
		{"duplicate", func(s *Snapshot) { s.Signals = append(s.Signals, s.Signals[0]) }},
		{"missing", func(s *Snapshot) { s.Signals = nil }},
		{"promoted", func(s *Snapshot) { s.Signals[0].Shadow = false }},
		{"enforcement", func(s *Snapshot) { s.Signals[0].AutoEnforce = true }},
		{"frame_count", func(s *Snapshot) { s.Frames-- }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := base
			actual.Signals = append([]Signal{}, base.Signals...)
			tc.mutate(&actual)
			result := CaseResult{Actual: []Snapshot{actual}, StructureMatch: true}
			compareCase(&result, ReplayCase{Expected: []Snapshot{base}})
			if result.Passed {
				t.Fatal("missed behavior drift")
			}
		})
	}
}

func TestWindowOverlapIsInclusiveScopedAndMissingSamplesNotTruth(t *testing.T) {
	m := simpleManifest(t)
	baseline := m.Cases[0].Expected[0]
	baseline.Signals = []Signal{{MatchID: "match", PlayerID: "player", DetectorID: "THROW_001", Frame: 20, Start: 10, End: 20, Shadow: true}, {MatchID: "match", PlayerID: "other", DetectorID: "THROW_001", Frame: 20, Start: 10, End: 20, Shadow: true}}
	w := Window{ID: "w", MatchID: "match", PlayerID: "player", DetectorID: "THROW_001", Start: 20, End: 30, Expectation: "signal", Provenance: Provenance{Kind: "behavior_only", Truth: "unknown"}}
	for _, samples := range []int{0, 11} {
		result := CaseResult{Actual: []Snapshot{baseline}, StructureMatch: true, WindowSamples: map[string]int{"w": samples}}
		compareCase(&result, ReplayCase{Expected: []Snapshot{baseline}, Windows: []Window{w}})
		if result.Windows[0].Signals != 1 {
			t.Fatal("window identity or inclusive range failed")
		}
		if samples == 0 {
			if result.Passed || result.Windows[0].Outcome != "unobservable_no_player_samples" {
				t.Fatal("absence mistaken for evidence")
			}
		} else if !result.Passed {
			t.Fatalf("observable window: %+v", result)
		}
	}
}

func TestPointEventWithoutExplicitRangeUsesEmittedFrame(t *testing.T) {
	m := simpleManifest(t)
	match := m.Cases[0].Expected[0]
	match.Signals = []Signal{{MatchID: "match", PlayerID: "player", DetectorID: "THROW_001", Frame: 42, Shadow: true}}
	w := Window{ID: "point", MatchID: "match", PlayerID: "player", DetectorID: "THROW_001", Start: 40, End: 45, Expectation: "signal", Provenance: Provenance{Kind: "behavior_only", Truth: "unknown"}}
	result := CaseResult{Actual: []Snapshot{match}, StructureMatch: true, WindowSamples: map[string]int{"point": 6}}
	compareCase(&result, ReplayCase{Expected: []Snapshot{match}, Windows: []Window{w}})
	if !result.Passed || result.Windows[0].Signals != 1 {
		t.Fatalf("point event lost: %+v", result)
	}
}

func TestPreflightRejectsConfigReplayAndEvidenceChangesWithoutOutputs(t *testing.T) {
	dir := t.TempDir()
	source := throwReplay(t, dir, 18)
	cfg := defaults(t)
	m, _, err := Capture(context.Background(), cfg, []string{source}, filepath.Join(dir, "capture"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"config", "source", "evidence", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			copyJSON, _ := json.Marshal(m)
			var copy Manifest
			_ = json.Unmarshal(copyJSON, &copy)
			candidate := defaults(t)
			ctx := context.Background()
			switch kind {
			case "config":
				candidate.Physics.DiscSpeedCap += 1
			case "source":
				copy.Cases[0].SHA256 = strings.Repeat("f", 64)
			case "evidence":
				match := copy.Cases[0].Expected[0]
				copy.Cases[0].Windows = []Window{{ID: "w", MatchID: match.MatchID, PlayerID: "echovr:1", DetectorID: "THROW_001", Start: 30, End: 45, Expectation: "observe", Provenance: Provenance{Kind: "confirmed", Truth: "negative", Reviewers: []string{"review-a", "review-b"}, Note: "Independent synthetic evidence test", Artifacts: []Artifact{{Path: source, SHA256: strings.Repeat("f", 64)}}}}}
			case "cancelled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			out := filepath.Join(dir, kind)
			report, err := Check(ctx, candidate, copy, dir, out)
			if err == nil || report.Passed {
				t.Fatalf("unsafe preflight result: %+v %v", report, err)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("preflight refusal created output")
			}
		})
	}
}

func TestStrictManifestAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	m := simpleManifest(t)
	path := filepath.Join(dir, "manifest.json")
	if err := WriteJSON(path, m); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := WriteJSON(path, Manifest{}); err == nil {
		t.Fatal("overwrote baseline")
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("baseline changed")
	}
	for _, doc := range []string{string(before) + " {}", strings.Replace(string(before), `"schema"`, `"misspelled_schema"`, 1)} {
		writeFixture(t, filepath.Join(dir, "invalid.json"), []byte(doc))
		if _, err := LoadManifest(filepath.Join(dir, "invalid.json")); err == nil {
			t.Fatal("accepted trailing data or typo")
		}
	}
}

func TestStagingRejectsUnpinnedBytesBeforeDatabaseCreation(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.echoreplay")
	writeFixture(t, source, []byte("a source changed after preflight"))
	out := filepath.Join(dir, "run")
	result, err := runCase(context.Background(), defaults(t), ReplayCase{ID: "changed", SHA256: strings.Repeat("f", 64)}, source, out)
	if err == nil || result.Passed || !strings.Contains(err.Error(), "source changed while staging") {
		t.Fatalf("unpinned staging accepted: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(out, "regression.db")); !os.IsNotExist(err) {
		t.Fatal("database created for unpinned bytes")
	}
	if _, err := os.Stat(filepath.Join(out, "source.echoreplay")); !os.IsNotExist(err) {
		t.Fatal("failed staging copy left behind")
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != "a source changed after preflight" {
		t.Fatal("original source changed")
	}
}
