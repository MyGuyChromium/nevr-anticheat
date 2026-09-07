package main

import (
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestPrintReviewAssessmentsIncludesShadowForAnyPlayer(t *testing.T) {
	events := []model.DetectionEvent{}
	for i := 0; i < 5; i++ {
		events = append(events, model.DetectionEvent{PlayerID: "synthetic-player", DetectorID: "THROW_001", IsShadow: true, MergedCount: 8})
	}
	out := captureStdout(t, func() { printReviewAssessments(events) })
	for _, want := range []string{"review needed", "not cheating verdicts", "Player synthetic-player: 5 signals (5 observation-only, 0 scored)", "THROW_001 - Impossible Release Velocity: 5"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output: %s", want, out)
		}
	}
}

func TestPrintReviewAssessmentsNoSignalsIsNotAnAcquittal(t *testing.T) {
	out := captureStdout(t, func() { printReviewAssessments(nil) })
	if !strings.Contains(out, "no detector signals") || !strings.Contains(out, "not verified fair play") || strings.Contains(out, "review needed") {
		t.Fatalf("unexpected no-signal output: %s", out)
	}
}

func TestPrintReviewAssessmentInsufficientCoverageKeepsSignals(t *testing.T) {
	coverage := map[string]*model.PlayerCoverage{"P": {Version: 1, Status: "insufficient_data"}}
	out := captureStdout(t, func() {
		printReviewAssessments([]model.DetectionEvent{{PlayerID: "P", DetectorID: "BIO_001", IsShadow: true}}, coverage)
	})
	if !strings.Contains(out, "insufficient data") || !strings.Contains(out, "BIO_001") {
		t.Fatalf("missing limitation or finding: %s", out)
	}
}
