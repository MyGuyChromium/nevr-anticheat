package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/enforce"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func TestDisplayConfigFingerprintVersionedCompleteAndPrivate(t *testing.T) {
	base := config.DefaultConfig()
	before := displayConfigFingerprint(base)
	if !strings.HasPrefix(before, "nevr-runtime-config/v2:") || len(strings.TrimPrefix(before, "nevr-runtime-config/v2:")) != 64 {
		t.Fatalf("unversioned/incomplete hash: %q", before)
	}
	for name, change := range map[string]func(*config.Config){
		"pipeline": func(c *config.Config) { c.Pipeline.HighPingThresholdMs++ },
		"rules":    func(c *config.Config) { c.ProjectRules.Version = "independently-versioned-test-rule" },
		"physics":  func(c *config.Config) { c.Physics.DiscSpeedCap += .1 },
		"scoring":  func(c *config.Config) { c.Scoring.CorrelationBonusCap++ },
		"detector": func(c *config.Config) {
			d := c.Detectors["THROW_001"]
			d.Enabled = !d.Enabled
			c.Detectors["THROW_001"] = d
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneConfig(base)
			change(candidate)
			if displayConfigFingerprint(candidate) == before {
				t.Fatal("changed relevant settings kept the same identity")
			}
		})
	}
	candidate := cloneConfig(base)
	candidate.Server.Listen = "private-sensitive-host:8111"
	candidate.Warnings = []string{"private-sensitive-warning"}
	if displayConfigFingerprint(candidate) != before {
		t.Fatal("private server/logging context should not enter desktop analysis fingerprint")
	}
	if displayConfigFingerprint(base) != before {
		t.Fatal("fingerprinting mutated settings")
	}
}

func TestRuntimeProvenanceCannotMistakeDirtyUnknownOrMismatchedBuildForRelease(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, revision, modified, linked string
		wantVerified                     bool
	}{
		{name: "clean", revision: commit, modified: "false", linked: commit, wantVerified: true},
		{name: "dirty", revision: commit, modified: "true", linked: commit},
		{name: "missing-clean-state", revision: commit, linked: commit},
		{name: "mismatch", revision: commit, modified: "false", linked: strings.Repeat("b", 40)},
		{name: "development", revision: commit, modified: "false", linked: "development"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: tc.revision}, {Key: "vcs.modified", Value: tc.modified}}}
			revision, modified, identity := provenanceBuildIdentity(info, tc.linked)
			if revision != tc.revision || (identity == "verified_clean_revision") != tc.wantVerified {
				t.Fatalf("identity %q / %q", revision, identity)
			}
			if tc.modified == "" && modified != nil {
				t.Fatal("missing clean state invented a boolean")
			}
			if tc.modified != "" && (modified == nil || *modified != (tc.modified == "true")) {
				t.Fatal("modified state was lost")
			}
		})
	}
	if revision, modified, identity := provenanceBuildIdentity(nil, commit); revision != "" || modified != nil || identity != "unverified_build" {
		t.Fatal("missing build information was treated as verified")
	}
}

func TestRuntimeProvenanceHealthSupportDiagnosticAndStoredRunAgree(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	if resp, out := upload(t, ts, true, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != http.StatusOK || !out.Results[0].OK {
		t.Fatal("synthetic import failed")
	}
	var health healthResponse
	if resp := getJSON(t, base+"/api/health", &health); resp.StatusCode != http.StatusOK {
		t.Fatalf("health=%d", resp.StatusCode)
	}
	want := health.Provenance
	if want.AppVersion != appVersion || want.SchemaVersion != sqlite.SchemaVersion() || want.Scope != "current_runtime_not_original_analysis" || len(want.ExecutableSHA256) != 64 || want.ConfigFingerprint != s.configFingerprint() || !want.ReviewOnly || want.EnforcementPolicy != enforce.PolicyVersion {
		t.Fatalf("incomplete runtime provenance: %+v", want)
	}
	path, err := s.createSupportBundle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	support, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer support.Close()
	var supportRuntime struct {
		Provenance runtimeProvenance `json:"runtime_provenance"`
	}
	readProvenanceZipEntry(t, support.File, "runtime.json", &supportRuntime)
	if supportRuntime.Provenance.ConfigFingerprint != want.ConfigFingerprint || supportRuntime.Provenance.ExecutableSHA256 != want.ExecutableSHA256 {
		t.Fatal("support runtime differs from health")
	}
	diagnostic, err := s.buildDiagnosticBundle(&physicsInspection{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(diagnostic), int64(len(diagnostic)))
	if err != nil {
		t.Fatal(err)
	}
	var manifest diagnosticManifest
	readProvenanceZipEntry(t, zr.File, "manifest.json", &manifest)
	if manifest.RuntimeProvenance.ConfigFingerprint != want.ConfigFingerprint || manifest.RuntimeProvenance.ExecutableSHA256 != want.ExecutableSHA256 || !strings.Contains(manifest.Privacy, "not guaranteed anonymous") {
		t.Fatal("diagnostic provenance/privacy incomplete")
	}
	runs, err := s.engine.Store().ListAnalysisRuns(context.Background(), "SYN-FIXTURE-001", 1)
	if err != nil || len(runs) != 1 || runs[0].ConfigFingerprint != want.ConfigFingerprint {
		t.Fatalf("stored run provenance: %+v %v", runs, err)
	}
}

func readProvenanceZipEntry(t *testing.T, files []*zip.File, name string, out any) {
	t.Helper()
	for _, file := range files {
		if file.Name != name {
			continue
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if err := json.NewDecoder(r).Decode(out); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("missing %s", name)
}

func TestCaseReportDistinguishesCurrentHistoricalUnknownAndEscapes(t *testing.T) {
	var out bytes.Buffer
	current := runtimeProvenance{AppVersion: "new", ConfigFingerprint: "new-config", BuildCommit: "<script>current</script>"}
	runs := []sqlite.AnalysisRun{{AppVersion: "old", ConfigFingerprint: "legacy-config", BuildCommit: "<script>old</script>"}}
	writeCaseReportProvenance(&out, current, runs)
	for _, want := range []string{"current export process", "Stored findings may come from an older analysis", "new-config", "legacy-config", "Historical schema versions were not recorded", "not a claim that every displayed finding"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out.String(), "<script>") {
		t.Fatal("unescaped provenance")
	}
	out.Reset()
	writeCaseReportProvenance(&out, current, nil)
	if !strings.Contains(out.String(), "Original analysis provenance unavailable") {
		t.Fatal("missing historical data was not explicit")
	}
}

func TestCaseReportHTTPIncludesCurrentAndHistoricalIdentity(t *testing.T) {
	_, ts := newTestServer(t)
	if resp, out := upload(t, ts, true, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != 200 || !out.Results[0].OK {
		t.Fatal("import failed")
	}
	resp, err := http.Get(ts.URL + "/" + testToken + "/api/match/SYN-FIXTURE-001/report")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte("Current export configuration (not original analysis)")) || !bytes.Contains(body, []byte("Recorded analysis history")) {
		t.Fatalf("report status=%d body=%s", resp.StatusCode, body)
	}
}
