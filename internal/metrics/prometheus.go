package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// PrometheusExporter exposes metrics in Prometheus text format.
type PrometheusExporter struct {
	metrics   *Metrics
	startTime time.Time
}

// NewPrometheusExporter creates a Prometheus exporter.
func NewPrometheusExporter(m *Metrics) *PrometheusExporter {
	return &PrometheusExporter{
		metrics:   m,
		startTime: time.Now(),
	}
}

// Handler returns an HTTP handler for the /metrics endpoint.
func (pe *PrometheusExporter) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		var b strings.Builder

		// Uptime
		b.WriteString("# HELP nevr_ac_uptime_seconds Time since server start.\n")
		b.WriteString("# TYPE nevr_ac_uptime_seconds gauge\n")
		b.WriteString(fmt.Sprintf("nevr_ac_uptime_seconds %f\n", time.Since(pe.startTime).Seconds()))

		// Frames
		b.WriteString("# HELP nevr_ac_frames_processed_total Total frames processed.\n")
		b.WriteString("# TYPE nevr_ac_frames_processed_total counter\n")
		b.WriteString(fmt.Sprintf("nevr_ac_frames_processed_total %d\n", pe.metrics.FramesProcessed.Get()))

		b.WriteString("# HELP nevr_ac_frames_invalid_total Total invalid frames rejected.\n")
		b.WriteString("# TYPE nevr_ac_frames_invalid_total counter\n")
		b.WriteString(fmt.Sprintf("nevr_ac_frames_invalid_total %d\n", pe.metrics.FramesInvalid.Get()))

		// Detection events by detector
		b.WriteString("# HELP nevr_ac_detection_events_total Detection events by detector.\n")
		b.WriteString("# TYPE nevr_ac_detection_events_total counter\n")
		snapshot := pe.metrics.DetectionEvents.Snapshot()
		keys := make([]string, 0, len(snapshot))
		for k := range snapshot {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("nevr_ac_detection_events_total{detector=%q} %d\n", k, snapshot[k]))
		}

		// Dedup/ratelimit
		b.WriteString("# HELP nevr_ac_events_deduplicated_total Events merged by dedup.\n")
		b.WriteString("# TYPE nevr_ac_events_deduplicated_total counter\n")
		b.WriteString(fmt.Sprintf("nevr_ac_events_deduplicated_total %d\n", pe.metrics.EventsDeduplicated.Get()))

		b.WriteString("# HELP nevr_ac_events_ratelimited_total Events dropped by rate limiter.\n")
		b.WriteString("# TYPE nevr_ac_events_ratelimited_total counter\n")
		b.WriteString(fmt.Sprintf("nevr_ac_events_ratelimited_total %d\n", pe.metrics.EventsRateLimited.Get()))

		w.Write([]byte(b.String()))
	}
}
