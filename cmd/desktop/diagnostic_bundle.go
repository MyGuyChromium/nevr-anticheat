package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
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

// diagnosticRedactor replaces what identifies people or the match in a
// diagnostic export. Keys are compared without case, "_" and "-", so the
// game's sessionid, the app's own session_id and a future sessionId all hit.
type diagnosticRedactor struct {
	players map[string]string // canonical player identity -> PLAYER-nnn
	secrets map[string]string // literal text -> replacement, for the final sweep
	next    int
}

func redactionKey(key string) string {
	return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
}

var (
	redactedNameKeys = map[string]bool{"name": true, "playername": true, "clientname": true, "displayname": true,
		"username": true, "personscored": true, "assistedby": true, "assistscored": true}
	redactedMatchKeys = map[string]bool{"matchid": true, "sessionid": true, "sessionguid": true, "captureid": true,
		"lobbyid": true, "sourceid": true}
	redactedOpaqueKeys = map[string]bool{"ip": true, "ipaddress": true, "clientip": true, "sessionip": true, "serverid": true,
		"accesstoken": true, "refreshtoken": true, "sessiontoken": true, "authtoken": true,
		"replayfile": true, "sourcefile": true, "sourcepath": true}
)

func playerIdentityKey(normalized string) bool {
	return normalized == "userid" || normalized == "playerid" || strings.HasSuffix(normalized, "playerid") ||
		normalized == "throwerid" || normalized == "possessorid" || normalized == "holderid" ||
		normalized == "previouspossessorid" || normalized == "scorerid" || normalized == "assistid"
}

func (r *diagnosticRedactor) remember(literal, replacement string) {
	if len(literal) < 4 {
		return // too short to sweep for without shredding unrelated text
	}
	if r.secrets == nil {
		r.secrets = make(map[string]string)
	}
	r.secrets[literal] = replacement
}

// pseudonym maps one player to one stable placeholder. The app's player id
// ("echovr:1001") and the game's userid (1001) are the same person and get the
// same placeholder, so the raw tick can still be matched to the derived frames.
func (r *diagnosticRedactor) pseudonym(value string) string {
	if value == "" {
		return ""
	}
	canonical := strings.TrimPrefix(strings.ToLower(value), "echovr:")
	if r.players == nil {
		r.players = make(map[string]string)
	}
	name := r.players[canonical]
	if name == "" {
		r.next++
		name = fmt.Sprintf("PLAYER-%03d", r.next)
		r.players[canonical] = name
	}
	r.remember(value, name)
	r.remember("echovr:"+canonical, name)
	return name
}

func scalarText(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	default:
		return "", false
	}
}

func (r *diagnosticRedactor) redact(value any, key string) any {
	normalized := redactionKey(key)
	if text, scalar := scalarText(value); scalar && text != "" {
		switch {
		case redactedNameKeys[normalized]:
			r.remember(text, "[redacted]")
			return "[redacted]"
		case redactedMatchKeys[normalized]:
			r.remember(text, "MATCH")
			return "MATCH"
		case redactedOpaqueKeys[normalized]:
			r.remember(text, "[redacted]")
			return "[redacted]"
		case playerIdentityKey(normalized):
			// The game's "playerid" is a slot index (0-15), not an identity.
			if number, isNumber := value.(json.Number); isNumber && key == "playerid" {
				if slot, err := number.Int64(); err == nil && slot >= 0 && slot < 64 {
					return value
				}
			}
			return r.pseudonym(text)
		}
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

// sweep replaces every remembered identity wherever else it appears: map keys
// (rosters keyed by player id), evidence text, file names, and numbers that
// are exactly a remembered id. It works on the decoded tree, so the result is
// always valid JSON. Longer literals go first so an id is not cut in half by
// a shorter one it contains.
func (r *diagnosticRedactor) sweep(value any) any {
	literals := make([]string, 0, len(r.secrets))
	for literal := range r.secrets {
		literals = append(literals, literal)
	}
	sort.Slice(literals, func(i, j int) bool {
		if len(literals[i]) != len(literals[j]) {
			return len(literals[i]) > len(literals[j])
		}
		return literals[i] < literals[j]
	})
	pairs := make([]string, 0, 2*len(literals))
	for _, literal := range literals {
		pairs = append(pairs, literal, r.secrets[literal])
	}
	replacer := strings.NewReplacer(pairs...)
	var walk func(any) any
	walk = func(node any) any {
		switch current := node.(type) {
		case string:
			return replacer.Replace(current)
		case json.Number:
			if replacement, ok := r.secrets[current.String()]; ok {
				return replacement
			}
			return current
		case map[string]any:
			out := make(map[string]any, len(current))
			for key, child := range current {
				out[replacer.Replace(key)] = walk(child)
			}
			return out
		case []any:
			out := make([]any, len(current))
			for i, child := range current {
				out[i] = walk(child)
			}
			return out
		default:
			return node
		}
	}
	return walk(value)
}

// redactedJSON renders value with identities replaced. known are identities
// the caller already has (the match id, the focus player) so they are swept
// even where no recognised key carries them.
func redactedJSON(value any, known map[string]string) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber() // a 64-bit user id must not be rewritten as 3.9e+15
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	redactor := &diagnosticRedactor{}
	for literal, replacement := range known {
		redactor.remember(literal, replacement)
	}
	return json.MarshalIndent(redactor.sweep(redactor.redact(generic, "")), "", "  ")
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
	known := map[string]string{}
	if inspection != nil {
		known[inspection.MatchID] = "MATCH"
		known[inspection.PlayerName] = "[redacted]"
	}
	inspectionJSON, err := redactedJSON(inspection, known)
	if err != nil {
		return nil, err
	}
	manifest := diagnosticManifest{
		Version: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339), AppVersion: appVersion, BuildRevision: buildRevision(),
		SchemaVersion: s.schemaVersion(), ConfigFingerprint: s.configFingerprint(),
		Contents:          []string{"manifest.json", "physics-inspector.redacted.json", "README.txt"},
		Privacy:           "Known identity fields (player names and ids, the match/session id, addresses, file names) are redacted or pseudonymized wherever their values appear. Raw source and evidence may contain unknown identifying fields or free text; this is not guaranteed anonymous. Inspect before sharing only with authorized private reviewers.",
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
