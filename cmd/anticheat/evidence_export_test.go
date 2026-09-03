package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/evidence"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestExportEvidenceCaseHTMLAndShadowMatchJSON(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	captureStdout(t, func() {
		if err := analyzeReplay(ctx, a, fixtureReplay, false); err != nil {
			t.Fatal(err)
		}
	})
	seedDerivedOutputs(t, a, "SYN-FIXTURE-001")
	dir := t.TempDir()

	htmlPath := filepath.Join(dir, "case.html")
	bundle, abs, err := exportEvidence(ctx, a, evidenceExportOptions{
		caseID: "RC-SYN-FIXTURE-001-echovr:1001", outputPath: htmlPath,
		format: "auto", before: 2, after: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if abs != htmlPath || bundle.CaseID == "" || len(bundle.DetectionEvents) != 1 || len(bundle.Frames) == 0 {
		t.Fatalf("case export = path %q case %q events %d frames %d", abs, bundle.CaseID, len(bundle.DetectionEvents), len(bundle.Frames))
	}
	html, err := os.ReadFile(htmlPath)
	if err != nil || !strings.Contains(string(html), "NEVR evidence review") || !strings.Contains(string(html), "MOV_001") {
		t.Fatalf("HTML output: %v, %q", err, html)
	}
	if _, _, err := exportEvidence(ctx, a, evidenceExportOptions{
		caseID: bundle.CaseID, outputPath: htmlPath, format: "html", before: 2, after: 3,
	}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error = %v", err)
	}
	if _, _, err := exportEvidence(ctx, a, evidenceExportOptions{
		caseID: bundle.CaseID, outputPath: htmlPath, format: "html", before: 2, after: 3, force: true,
	}); err != nil {
		t.Fatalf("forced replacement: %v", err)
	}

	shadow := model.DetectionEvent{
		EventID: "shadow-1", DetectorID: "BIO_002", DetectorVersion: "2.2.0",
		MatchID: "SYN-FIXTURE-001", PlayerID: "echovr:1001", FrameIndex: 20,
		FrameRangeStart: 19, FrameRangeEnd: 21, Timestamp: 1.33,
		Severity: 0.5, Confidence: 0.6, EnforcementWeight: 0, IsShadow: true,
		ObservedValue: "relative hand speed: 55 m/s", ExpectedRange: "<= 50 m/s",
		CausalKey: model.CausalKey{PlayerID: "echovr:1001", FrameStart: 19, FrameEnd: 21, AnomalyType: "hand_speed"},
	}
	if _, err := a.store.StoreDetectionEvents(ctx, []model.DetectionEvent{shadow}, "initial"); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(dir, "shadow.json")
	shadowBundle, _, err := exportEvidence(ctx, a, evidenceExportOptions{
		matchID: "SYN-FIXTURE-001", playerID: "echovr:1001", includeShadow: true,
		outputPath: jsonPath, format: "json", before: 1, after: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(shadowBundle.DetectionEvents) != 2 || shadowBundle.Metadata["includes_shadow"] != "true" {
		t.Fatalf("shadow export events=%d metadata=%v", len(shadowBundle.DetectionEvents), shadowBundle.Metadata)
	}
	var decoded evidence.ReplayBundle
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.MatchID != "SYN-FIXTURE-001" {
		t.Fatalf("JSON output: match=%q err=%v", decoded.MatchID, err)
	}
}

func TestExportEvidenceValidatesSelectionWindowAndFormat(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for name, opts := range map[string]evidenceExportOptions{
		"negative window": {caseID: "c", outputPath: "x.html", before: -1},
		"huge window":     {caseID: "c", outputPath: "x.html", after: 901},
		"mixed selection": {caseID: "c", matchID: "m", playerID: "p", outputPath: "x.html"},
		"shadow case":     {caseID: "c", outputPath: "x.html", includeShadow: true},
		"blank output":    {caseID: "c", outputPath: " "},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := exportEvidence(ctx, a, opts); err == nil {
				t.Error("invalid export accepted")
			}
		})
	}
}
