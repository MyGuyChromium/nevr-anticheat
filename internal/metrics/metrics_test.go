package metrics

import (
	"strings"
	"testing"
)

func TestPrometheusRender_ExposesWiredCounters(t *testing.T) {
	m := NewMetrics()
	m.FramesProcessed.Add(35)
	m.FramesInvalid.Add(115)
	m.FramesInvalidReasons.Add("zero_position", 100)
	m.DetectionEvents.Inc("MOV_001")
	m.DetectionEvents.Inc("MOV_001")
	m.ShadowEvents.Inc("BIO_002")
	m.EventsDeduplicated.Add(7)
	m.ActiveConnections.Inc()
	m.ActiveMatches.Set(3)

	out := NewPrometheusExporter(m).Render()
	for _, want := range []string{
		"nevr_ac_frames_processed_total 35\n",
		"nevr_ac_frames_invalid_total 115\n",
		`nevr_ac_frames_invalid_reason_total{reason="zero_position"} 100`,
		`nevr_ac_detection_events_total{detector="MOV_001"} 2`,
		`nevr_ac_shadow_events_total{detector="BIO_002"} 1`,
		"nevr_ac_events_deduplicated_total 7\n",
		"nevr_ac_active_connections 1\n",
		"nevr_ac_active_matches 3\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
