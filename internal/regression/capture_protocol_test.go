package regression

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func captureFixture() *CaptureProvenance {
	return &CaptureProvenance{Protocol: CaptureProtocolVersion, SessionGroup: "synthetic-session-a", Split: "development", View: "local",
		SubjectPlayerID: "player", RecorderPlayerID: "player", GameBuild: CaptureIdentity{Status: "unknown"}, GameConfig: CaptureIdentity{Status: "unknown"}}
}

func captureAnchor() AlignmentAnchor {
	return AlignmentAnchor{ID: "marker-01", MatchID: "match", Method: "visual_event", RecordingTimeSeconds: 0, UncertaintySeconds: .04,
		Evidence: Artifact{Path: "private/marker.txt", SHA256: strings.Repeat("c", 64)}}
}

func comparisonManifest(t *testing.T) Manifest {
	t.Helper()
	m := simpleManifest(t)
	m.Cases[0].Capture = captureFixture()
	m.Cases[0].Expected[0].PlayerFrames["observer"] = 100
	return m
}

func TestCaptureProtocolBackwardCompatibleAndExplicitIdentity(t *testing.T) {
	m := simpleManifest(t)
	if err := m.Validate(); err != nil {
		t.Fatalf("legacy manifest changed: %v", err)
	}
	m = comparisonManifest(t)
	for _, view := range []string{"local", "remote", "server_relay"} {
		p := m.Cases[0].Capture
		p.View = view
		p.RecorderPlayerID = map[string]string{"local": "player", "remote": "observer", "server_relay": ""}[view]
		p.Anchors = []AlignmentAnchor{captureAnchor()}
		if err := m.Validate(); err != nil {
			t.Fatalf("view %s, unknown identities and measured zero-time marker must be valid: %v", view, err)
		}
		p.GameBuild = CaptureIdentity{Status: "recorded", Value: "synthetic-build/1.0+revision:abc"}
		p.GameConfig = CaptureIdentity{Status: "recorded", Value: strings.Repeat("d", 64)}
		if err := m.Validate(); err != nil {
			t.Fatalf("recorded identities rejected: %v", err)
		}
	}
	before, _ := json.Marshal(m)
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(m)
	if string(before) != string(after) {
		t.Fatal("protocol validation mutated operator provenance")
	}
	copy := m.Cases[0].Capture.clone()
	copy.Anchors[0].Evidence.Path = "other"
	if m.Cases[0].Capture.Anchors[0].Evidence.Path == "other" {
		t.Fatal("capture clone aliases source anchors")
	}
}

func TestCaptureProtocolRejectsMalformedClaimsAndAnchors(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ReplayCase)
	}{
		{"protocol", func(c *ReplayCase) { c.Capture.Protocol = "v2" }},
		{"case identifier", func(c *ReplayCase) { c.ID = "case\nname" }},
		{"session identifier", func(c *ReplayCase) { c.Capture.SessionGroup = "../group" }},
		{"missing session", func(c *ReplayCase) { c.Capture.SessionGroup = "" }},
		{"large session", func(c *ReplayCase) { c.Capture.SessionGroup = strings.Repeat("a", 129) }},
		{"split", func(c *ReplayCase) { c.Capture.Split = "training" }},
		{"authority", func(c *ReplayCase) { c.Capture.View = "authoritative_server" }},
		{"absent subject", func(c *ReplayCase) { c.Capture.SubjectPlayerID = "absent" }},
		{"local recorder mismatch", func(c *ReplayCase) { c.Capture.RecorderPlayerID = "observer" }},
		{"remote recorder same", func(c *ReplayCase) { c.Capture.View = "remote" }},
		{"remote recorder absent", func(c *ReplayCase) { c.Capture.View = "remote"; c.Capture.RecorderPlayerID = "absent" }},
		{"relay player recorder", func(c *ReplayCase) { c.Capture.View = "server_relay" }},
		{"missing build identity", func(c *ReplayCase) { c.Capture.GameBuild = CaptureIdentity{} }},
		{"unknown build value", func(c *ReplayCase) { c.Capture.GameBuild.Value = "unverified-build" }},
		{"recorded unknown", func(c *ReplayCase) { c.Capture.GameBuild = CaptureIdentity{Status: "recorded", Value: "UNKNOWN"} }},
		{"verified build assertion", func(c *ReplayCase) { c.Capture.GameBuild = CaptureIdentity{Status: "verified", Value: "build"} }},
		{"bad build ID", func(c *ReplayCase) { c.Capture.GameBuild = CaptureIdentity{Status: "recorded", Value: "build\nsecret"} }},
		{"bad configuration digest", func(c *ReplayCase) { c.Capture.GameConfig = CaptureIdentity{Status: "recorded", Value: "defaults"} }},
		{"missing configuration identity", func(c *ReplayCase) { c.Capture.GameConfig = CaptureIdentity{} }},
		{"unmeasured ping method", func(c *ReplayCase) { c.Capture.Anchors[0].Method = "half_ping" }},
		{"unknown anchor match", func(c *ReplayCase) { c.Capture.Anchors[0].MatchID = "other" }},
		{"duplicate marker", func(c *ReplayCase) { c.Capture.Anchors = append(c.Capture.Anchors, c.Capture.Anchors[0]) }},
		{"case variant marker", func(c *ReplayCase) {
			copy := c.Capture.Anchors[0]
			copy.ID = strings.ToUpper(copy.ID)
			c.Capture.Anchors = append(c.Capture.Anchors, copy)
		}},
		{"many markers", func(c *ReplayCase) { c.Capture.Anchors = make([]AlignmentAnchor, maxCaptureAnchors+1) }},
		{"blank marker", func(c *ReplayCase) { c.Capture.Anchors[0].ID = "" }},
		{"negative time", func(c *ReplayCase) { c.Capture.Anchors[0].RecordingTimeSeconds = -1 }},
		{"nonfinite time", func(c *ReplayCase) { c.Capture.Anchors[0].RecordingTimeSeconds = math.NaN() }},
		{"oversized time", func(c *ReplayCase) { c.Capture.Anchors[0].RecordingTimeSeconds = maxCaptureSeconds + 1 }},
		{"zero uncertainty", func(c *ReplayCase) { c.Capture.Anchors[0].UncertaintySeconds = 0 }},
		{"negative uncertainty", func(c *ReplayCase) { c.Capture.Anchors[0].UncertaintySeconds = -1 }},
		{"infinite uncertainty", func(c *ReplayCase) { c.Capture.Anchors[0].UncertaintySeconds = math.Inf(1) }},
		{"oversized uncertainty", func(c *ReplayCase) { c.Capture.Anchors[0].UncertaintySeconds = 3601 }},
		{"missing evidence", func(c *ReplayCase) { c.Capture.Anchors[0].Evidence.Path = "" }},
		{"ambiguous evidence path", func(c *ReplayCase) { c.Capture.Anchors[0].Evidence.Path = "a\nfile" }},
		{"oversized evidence path", func(c *ReplayCase) { c.Capture.Anchors[0].Evidence.Path = strings.Repeat("a", 4097) }},
		{"unpinned evidence", func(c *ReplayCase) { c.Capture.Anchors[0].Evidence.SHA256 = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := comparisonManifest(t)
			m.Cases[0].Capture.Anchors = []AlignmentAnchor{captureAnchor()}
			tc.edit(&m.Cases[0])
			if m.Validate() == nil {
				t.Fatal("malformed capture provenance accepted")
			}
		})
	}
	m := comparisonManifest(t)
	m.Cases = make([]ReplayCase, maxCaptureCases+1)
	m.Cases[0].Capture = captureFixture()
	if m.validateCaptureProtocol() == nil {
		t.Fatal("oversized controlled cohort accepted")
	}
}

func TestCaptureProtocolPreventsCorrelatedSplitLeakage(t *testing.T) {
	for _, kind := range []string{"same bytes", "same session group", "same observed match", "unassigned case", "independent session", "same split multi-view"} {
		t.Run(kind, func(t *testing.T) {
			m := comparisonManifest(t)
			other := comparisonManifest(t).Cases[0]
			other.ID, other.SHA256 = "replay-002", strings.Repeat("b", 64)
			other.Expected[0].MatchID = "different-match"
			other.Capture.SessionGroup, other.Capture.Split = "different-session", "holdout"
			switch kind {
			case "same bytes":
				other.SHA256 = strings.ToUpper(m.Cases[0].SHA256)
			case "same session group":
				other.Capture.SessionGroup = strings.ToUpper(m.Cases[0].Capture.SessionGroup)
			case "same observed match":
				other.Expected[0].MatchID = strings.ToUpper(m.Cases[0].Expected[0].MatchID)
			case "unassigned case":
				other.Capture = nil
			case "same split multi-view":
				other.Capture.Split = "development"
				other.Capture.SessionGroup = m.Cases[0].Capture.SessionGroup
				other.Expected[0].MatchID = m.Cases[0].Expected[0].MatchID
				other.Capture.View, other.Capture.RecorderPlayerID = "remote", "observer"
			}
			m.Cases = append(m.Cases, other)
			err := m.Validate()
			wantValid := kind == "independent session" || kind == "same split multi-view"
			if (err == nil) != wantValid {
				t.Fatalf("split result: %v, expected valid=%t", err, wantValid)
			}
		})
	}
}

func TestDocumentedCaptureProvenanceRunsThroughProductionRegression(t *testing.T) {
	// The only JSON example is a complete metadata value, not invented replay
	// bytes. Bind it to freshly generated synthetic telemetry and execute the
	// same capture/check engine used by the CLI to keep the example tool-valid.
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "controlled_comparison_recordings.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, jsonTail, ok := strings.Cut(string(doc), "```json\n")
	if !ok { // Git's Windows checkout can use CRLF.
		_, jsonTail, ok = strings.Cut(string(doc), "```json\r\n")
	}
	if !ok {
		t.Fatal("documented JSON metadata missing")
	}
	example, _, ok := strings.Cut(jsonTail, "```")
	var metadata CaptureProvenance
	decoder := json.NewDecoder(strings.NewReader(example))
	decoder.DisallowUnknownFields()
	if !ok || decoder.Decode(&metadata) != nil {
		t.Fatal("documented JSON metadata is invalid")
	}
	dir := t.TempDir()
	source := throwReplay(t, dir, 18.7)
	cfg := defaults(t)
	m, _, err := Capture(context.Background(), cfg, []string{source}, filepath.Join(dir, "captured"))
	if err != nil {
		t.Fatal(err)
	}
	m.Cases[0].Capture = &metadata
	manifestPath := filepath.Join(dir, "comparison.json")
	if err := WriteJSON(manifestPath, m); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Check(context.Background(), cfg, loaded, dir, filepath.Join(dir, "checked"))
	if err != nil || !report.Passed || len(report.Cases) != 1 || !reflect.DeepEqual(report.Cases[0].Capture, &metadata) {
		t.Fatalf("documented metadata failed production check/report: %v %+v", err, report)
	}
	// A declared measured marker is hash-pinned independently of the replay.
	evidence := filepath.Join(dir, "synthetic-marker.txt")
	writeFixture(t, evidence, []byte("generated marker note; not a real synchronized recording"))
	hash, err := FileSHA256(evidence)
	if err != nil {
		t.Fatal(err)
	}
	m.Cases[0].Capture.Anchors = []AlignmentAnchor{{ID: "marker-0", MatchID: m.Cases[0].Expected[0].MatchID,
		Method: "instrumented_marker", RecordingTimeSeconds: 0, UncertaintySeconds: .02, Evidence: Artifact{Path: "synthetic-marker.txt", SHA256: hash}}}
	report, err = Check(context.Background(), cfg, m, dir, filepath.Join(dir, "checked-marker"))
	if err != nil || !report.Passed || len(report.Cases[0].Capture.Anchors) != 1 {
		t.Fatalf("pinned measured marker rejected: %v", err)
	}
	m.Cases[0].Capture.Anchors[0].ID = "changed-source-metadata"
	if report.Cases[0].Capture.Anchors[0].ID != "marker-0" {
		t.Fatal("report aliases mutable source metadata")
	}
	writeFixture(t, evidence, []byte("altered marker"))
	out := filepath.Join(dir, "must-not-exist")
	if _, err := Check(context.Background(), cfg, m, dir, out); err == nil {
		t.Fatal("changed alignment evidence was accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("failed provenance preflight created a run directory")
	}
}

func TestCaptureSplitFailureOccursBeforeSourceReadsOrOutput(t *testing.T) {
	m := comparisonManifest(t)
	other := comparisonManifest(t).Cases[0]
	other.ID, other.Capture.Split = "replay-002", "holdout"
	m.Cases = append(m.Cases, other)
	out := filepath.Join(t.TempDir(), "refused")
	_, err := Check(context.Background(), defaults(t), m, t.TempDir(), out)
	if err == nil || !strings.Contains(err.Error(), "crosses development/holdout") {
		t.Fatalf("expected split refusal before missing source read, got %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("split refusal created run output")
	}
}
