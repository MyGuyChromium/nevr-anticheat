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
		w.Write([]byte(pe.Render()))
	}
}

// Render returns the metrics in Prometheus text exposition format.
func (pe *PrometheusExporter) Render() string {
	m := pe.metrics
	var b strings.Builder

	gauge := func(name, help string, v float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, v)
	}
	counter := func(name, help string, v int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}
	labeled := func(name, help, label string, lc *LabeledCounter) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
		snapshot := lc.Snapshot()
		keys := make([]string, 0, len(snapshot))
		for k := range snapshot {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s{%s=%q} %d\n", name, label, k, snapshot[k])
		}
	}

	gauge("nevr_ac_uptime_seconds", "Time since server start.", time.Since(pe.startTime).Seconds())

	// Frames
	counter("nevr_ac_frames_received_total", "Frames present in decoded telemetry batches.", m.FramesReceived.Get())
	counter("nevr_ac_frames_processed_total", "Frames accepted into the detection pipeline.", m.FramesProcessed.Get())
	counter("nevr_ac_frames_invalid_total", "Frames rejected by validation.", m.FramesInvalid.Get())
	labeled("nevr_ac_frames_invalid_reason_total", "Frames rejected by validation, by reason.", "reason", &m.FramesInvalidReasons)
	counter("nevr_ac_frames_ratelimited_total", "Frames dropped by the per-player ingest rate limit.", m.FramesRateLimited.Get())
	counter("nevr_ac_frames_ignored_total", "Frames discarded by the store as duplicates.", m.FramesIgnored.Get())
	counter("nevr_ac_frames_rebased_total", "Frames whose index was re-based to keep the match monotonic.", m.FramesRebased.Get())

	// Protocol
	counter("nevr_ac_batches_received_total", "Telemetry batches received.", m.BatchesReceived.Get())
	counter("nevr_ac_batches_malformed_total", "Messages that failed to decode.", m.BatchesMalformed.Get())
	labeled("nevr_ac_batches_rejected_total", "Batches rejected before processing, by reason.", "reason", &m.BatchesRejected)
	labeled("nevr_ac_control_messages_total", "Control messages received, by type.", "type", &m.ControlMessages)
	counter("nevr_ac_auth_failures_total", "Connections rejected for bad credentials.", m.AuthFailures.Get())

	// Detection
	labeled("nevr_ac_detection_events_total", "Detection events by detector.", "detector", &m.DetectionEvents)
	labeled("nevr_ac_shadow_events_total", "Shadow-mode detection events by detector.", "detector", &m.ShadowEvents)
	counter("nevr_ac_events_deduplicated_total", "Raw detector emissions merged into incidents.", m.EventsDeduplicated.Get())
	counter("nevr_ac_events_ratelimited_total", "Events dropped by the per player/detector cap.", m.EventsRateLimited.Get())
	counter("nevr_ac_events_invalid_total", "Detector emissions dropped by event validation.", m.EventsInvalid.Get())
	counter("nevr_ac_store_errors_total", "Database write failures.", m.StoreErrors.Get())
	counter("nevr_ac_matches_created_total", "Live matches created.", m.MatchesCreated.Get())
	counter("nevr_ac_matches_ended_total", "Live matches finalized.", m.MatchesEnded.Get())
	counter("nevr_ac_score_snapshots_total", "Suspicion score snapshots persisted.", m.ScoreSnapshotsStored.Get())

	// Gauges
	gauge("nevr_ac_active_connections", "Open telemetry WebSocket connections.", float64(m.ActiveConnections.Get()))
	gauge("nevr_ac_active_matches", "Live matches currently tracked.", float64(m.ActiveMatches.Get()))

	return b.String()
}
