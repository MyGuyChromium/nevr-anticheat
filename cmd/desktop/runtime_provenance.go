package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"runtime/debug"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/enforce"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// runtimeProvenance describes the process creating this response/export, not
// the historical process that produced a stored finding. It contains no local
// paths, environment values, credentials, player identity, or raw telemetry.
type runtimeProvenance struct {
	Version           string `json:"version"`
	Scope             string `json:"scope"`
	AppVersion        string `json:"app_version"`
	BuildCommit       string `json:"build_commit"`
	SourceRevision    string `json:"source_revision,omitempty"`
	SourceModified    *bool  `json:"source_modified"`
	BuildIdentity     string `json:"build_identity"`
	ExecutableSHA256  string `json:"executable_sha256,omitempty"`
	IdentityWarning   string `json:"identity_warning,omitempty"`
	SchemaVersion     int    `json:"schema_version"`
	ConfigFingerprint string `json:"config_fingerprint"`
	ReviewOnly        bool   `json:"review_only"`
	EnforcementPolicy string `json:"enforcement_policy"`
}

// displayConfigFingerprint deliberately versions the expanded diagnostic hash.
// Historical short hashes remain historical; calibrationFingerprint and its
// promotion/review bindings are separate and are not changed here.
func displayConfigFingerprint(cfg *config.Config) string {
	data, err := json.Marshal(struct {
		Version      string `json:"version"`
		Detectors    any    `json:"detectors"`
		Physics      any    `json:"physics"`
		Scoring      any    `json:"scoring"`
		Pipeline     any    `json:"pipeline"`
		ProjectRules any    `json:"project_rules"`
	}{"nevr-runtime-config/v2", cfg.EffectiveTable(), cfg.Physics.Constants(), cfg.Scoring, cfg.Pipeline, cfg.ProjectRules})
	if err != nil {
		return "unavailable"
	}
	sum := sha256.Sum256(data)
	return "nevr-runtime-config/v2:" + hex.EncodeToString(sum[:])
}

func provenanceBuildIdentity(info *debug.BuildInfo, linkedCommit string) (revision string, modified *bool, identity string) {
	identity = "unverified_build"
	if info != nil {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				if setting.Value == "true" || setting.Value == "false" {
					value := setting.Value == "true"
					modified = &value
				}
			}
		}
	}
	if trustedCalibrationBuild(info, linkedCommit) {
		identity = "verified_clean_revision"
	}
	return
}

func (s *server) currentRuntimeProvenance() runtimeProvenance {
	info, _ := debug.ReadBuildInfo()
	revision, modified, identity := provenanceBuildIdentity(info, buildCommit)
	hash, err := runningExecutableSHA256()
	warning := ""
	if err != nil {
		// Hashing errors may contain local paths; use a fixed export-safe message.
		warning = "Executable SHA-256 unavailable; exact artifact identity was not established."
	}
	return runtimeProvenance{
		Version: "nevr-runtime-provenance/v1", Scope: "current_runtime_not_original_analysis",
		AppVersion: appVersion, BuildCommit: buildCommit, SourceRevision: revision,
		SourceModified: modified, BuildIdentity: identity, ExecutableSHA256: hash, IdentityWarning: warning,
		SchemaVersion: sqlite.SchemaVersion(), ConfigFingerprint: s.configFingerprint(),
		ReviewOnly: enforce.ReviewOnly, EnforcementPolicy: enforce.PolicyVersion,
	}
}

func writeCaseReportProvenance(w io.Writer, current runtimeProvenance, runs []sqlite.AnalysisRun) {
	fmt.Fprint(w, "<h2>Report provenance</h2><p>This section identifies the current export process. Stored findings may come from an older analysis. Hashes identify artifacts and settings, not a trusted publisher or detection accuracy.</p><dl>")
	for _, field := range [][2]string{
		{"Export app version", current.AppVersion}, {"Linked build commit", current.BuildCommit},
		{"Source revision", current.SourceRevision}, {"Build identity", current.BuildIdentity},
		{"Executable SHA-256", current.ExecutableSHA256}, {"Current schema", fmt.Sprint(current.SchemaVersion)},
		{"Current configuration", current.ConfigFingerprint}, {"Enforcement policy", current.EnforcementPolicy},
	} {
		value := field[1]
		if value == "" {
			value = "not recorded / unavailable"
		}
		fmt.Fprintf(w, "<dt>%s</dt><dd><code>%s</code></dd>", html.EscapeString(field[0]), html.EscapeString(value))
	}
	fmt.Fprint(w, "</dl><h3>Recorded analysis history</h3><p>These are historical run records, not a claim that every displayed finding is independently bound to a run. Legacy short config hashes are retained as recorded; the v2 fingerprint covers additional settings. Historical schema versions were not recorded in analysis runs.</p>")
	if len(runs) == 0 {
		fmt.Fprint(w, "<p>Original analysis provenance unavailable. Re-analyze the approved source with the candidate if current-build attribution is required.</p>")
		return
	}
	fmt.Fprint(w, "<table><tr><th>Analyzed</th><th>App / build</th><th>Executable SHA-256</th><th>Recorded configuration</th></tr>")
	for _, run := range runs {
		fmt.Fprintf(w, "<tr><td>%s</td><td>%s<br><code>%s</code></td><td><code>%s</code></td><td><code>%s</code></td></tr>",
			html.EscapeString(fmtTime(run.CreatedAt)), html.EscapeString(run.AppVersion), html.EscapeString(run.BuildCommit),
			html.EscapeString(run.ExecutableSHA256), html.EscapeString(run.ConfigFingerprint))
	}
	fmt.Fprint(w, "</table>")
}
