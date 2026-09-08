package main

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
)

func TestPlayspacePauseRejectsPromotionWithoutWrites(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()
	before, err := s.engine.Store().ListConfigProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"MOV_006", "PAT_005"} {
		var response map[string]any
		resp := postJSONTest(t, ts.URL+"/"+testToken+"/api/lab/promotions/"+id, map[string]any{}, &response)
		if resp.StatusCode != http.StatusUnprocessableEntity || response["promotion_block"] != config.DetectorPauseReason(id) {
			t.Fatalf("promotion did not explain pause: %d %v", resp.StatusCode, response)
		}
		metric := representativeGateMetric()
		metric.DetectorID = id
		applyPromotionGate(&metric)
		if metric.Eligible || metric.PromotionBlock != config.DetectorPauseReason(id) {
			t.Fatalf("paused promotion offered: %+v", metric)
		}
	}
	after, err := s.engine.Store().ListConfigProfiles(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rejected promotion changed profiles", err)
	}
	promotions, err := s.engine.Store().ListDetectorPromotions(ctx)
	if err != nil || len(promotions) != 0 {
		t.Fatal("rejected promotion created approval", err)
	}
}

func TestPlayspaceSandboxCannotResumePausedDetector(t *testing.T) {
	for _, id := range []string{"MOV_006", "PAT_005"} {
		enabled := true
		cfg := config.DefaultConfig()
		_, _, err := setSandboxParam(cfg, thresholdPreviewRequest{Detector: id, Enabled: &enabled})
		if err == nil || !strings.Contains(err.Error(), "paused") || cfg.IsDetectorEnabled(id) {
			t.Fatalf("sandbox resumed %s: %v", id, err)
		}
	}
}

func TestPlayspacePauseDiagnosticsUseEffectiveConfig(t *testing.T) {
	s, ts := newTestServer(t)
	for _, id := range []string{"MOV_006", "PAT_005"} {
		dc := s.engine.Config().Detectors[id]
		dc.Enabled, dc.Mode = true, "review"
		s.engine.Config().Detectors[id] = dc
	}
	var setup struct {
		Ready  bool     `json:"ready"`
		Unsafe []string `json:"unsafe_detectors"`
	}
	resp := getJSON(t, ts.URL+"/"+testToken+"/api/setup", &setup)
	if resp.StatusCode != http.StatusOK || !setup.Ready || len(setup.Unsafe) != 0 {
		t.Fatalf("paused overrides misrepresented as unsafe: %d %+v", resp.StatusCode, setup)
	}
	var specs struct {
		Detectors []struct {
			ID          string `json:"id"`
			Enabled     bool   `json:"enabled"`
			PauseReason string `json:"pause_reason"`
		} `json:"detectors"`
	}
	resp = getJSON(t, ts.URL+"/"+testToken+"/api/lab/thresholds", &specs)
	if resp.StatusCode != http.StatusOK {
		t.Fatal("cannot read effective detector specs", resp.StatusCode)
	}
	paused := 0
	for _, d := range specs.Detectors {
		if reason := config.DetectorPauseReason(d.ID); reason != "" {
			paused++
			if d.Enabled || d.PauseReason != reason {
				t.Fatalf("threshold specs misrepresent pause: %+v", d)
			}
		}
	}
	if paused != 2 {
		t.Fatalf("expected both paused detectors in specs, got %d", paused)
	}
}
