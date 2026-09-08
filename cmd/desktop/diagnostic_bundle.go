package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type diagnosticManifest struct {
	Version           int               `json:"version"`
	CreatedAt         string            `json:"created_at"`
	AppVersion        string            `json:"app_version"`
	BuildRevision     string            `json:"build_revision,omitempty"`
	SchemaVersion     int               `json:"schema_version"`
	ConfigFingerprint string            `json:"config_fingerprint"`
	Contents          []string          `json:"contents"`
	Privacy           string            `json:"privacy"`
	RuntimeProvenance runtimeProvenance `json:"runtime_provenance"`
}

func buildRevision() string {
	return analysisBuildRevision()
}

func (s *server) configFingerprint() string {
	return displayConfigFingerprint(s.engine.Config())
}

type diagnosticRedactor struct {
	ids  map[string]string
	next int
}

func (r *diagnosticRedactor) pseudonym(value string) string {
	if value == "" {
		return ""
	}
	if r.ids == nil {
		r.ids = make(map[string]string)
	}
	if found := r.ids[value]; found != "" {
		return found
	}
	r.next++
	name := fmt.Sprintf("PLAYER-%03d", r.next)
	r.ids[value] = name
	return name
}

func (r *diagnosticRedactor) redact(value any, key string) any {
	lower := strings.ToLower(key)
	switch lower {
	case "name", "player_name", "client_name", "display_name", "username", "user_name", "person_scored", "assisted_by", "assist_scored":
		if value != nil && fmt.Sprint(value) != "" {
			return "[redacted]"
		}
	case "player_id", "playerid", "userid", "thrower_id", "possessor_id", "holder_id", "previous_possessor_id", "scorer_id", "assist_id":
		return r.pseudonym(fmt.Sprint(value))
	case "match_id", "sessionid":
		if value != nil && fmt.Sprint(value) != "" {
			return "MATCH"
		}
	case "ip", "ip_address", "client_ip", "access_token", "refresh_token", "session_token", "auth_token":
		return "[redacted]"
	}
	switch current := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(current))
		for childKey, child := range current {
			out[childKey] = r.redact(child, childKey)
		}
		return out
	case []any:
		out := make([]any, len(current))
		for i, child := range current {
			out[i] = r.redact(child, key)
		}
		return out
	default:
		return value
	}
}

func redactedJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	redacted := (&diagnosticRedactor{}).redact(generic, "")
	return json.MarshalIndent(redacted, "", "  ")
}

func addZipBytes(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func (s *server) buildDiagnosticBundle(inspection *physicsInspection) ([]byte, error) {
	inspectionJSON, err := redactedJSON(inspection)
	if err != nil {
		return nil, err
	}
	manifest := diagnosticManifest{
		Version: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339), AppVersion: appVersion, BuildRevision: buildRevision(),
		SchemaVersion: s.schemaVersion(), ConfigFingerprint: s.configFingerprint(),
		Contents:          []string{"manifest.json", "physics-inspector.redacted.json", "README.txt"},
		Privacy:           "Known identity fields are redacted or pseudonymized. Raw source and evidence may contain unknown identifying fields or free text; this is not guaranteed anonymous. Inspect before sharing only with authorized private reviewers.",
		RuntimeProvenance: s.currentRuntimeProvenance(),
	}
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	readme := []byte("NEVR-Anticheat diagnostic bundle\n\nThis bundle contains the incident's normalized source values, recomputed derived physics, detector rationale, telemetry health, and a key-redacted copy of the focus raw tick. Unknown source fields, map keys, and free text may still identify people. It is not guaranteed anonymous: inspect every entry and share only through the agreed private channel with authorized reviewers. No automatic upload occurs.\n\nRuntime provenance identifies the exporting process and its current inspector configuration, not the historical process that generated stored detector findings. Replays are client-side observations and may be interpolated. A build hash is not a publisher signature or detector-accuracy guarantee.\n")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"manifest.json", manifestJSON}, {"physics-inspector.redacted.json", inspectionJSON}, {"README.txt", readme}} {
		if err := addZipBytes(zw, entry.name, entry.data); err != nil {
			_ = zw.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
