package model

// PlayerCoverage records what the pipeline could observe, not whether a player
// was honest. CandidateFrames counts dispatch after phase/warmup gates; detector
// internal guards may still skip them. InputFrames is only a necessary-input
// check for the named detector, never an independent ground-truth opportunity.
type PlayerCoverage struct {
	Version        int                `json:"version"`
	Status         string             `json:"status"`
	ValidFrames    int                `json:"valid_frames"`
	RejectedFrames int                `json:"rejected_frames"`
	QualityGated   bool               `json:"quality_gated"`
	Limitations    []string           `json:"limitations"`
	Detectors      []DetectorCoverage `json:"detectors"`
}

type DetectorCoverage struct {
	DetectorID      string   `json:"detector_id"`
	Enabled         bool     `json:"enabled"`
	Status          string   `json:"status"`
	CandidateFrames int      `json:"candidate_frames"`
	InputFrames     int      `json:"input_frames"`
	InputCheck      bool     `json:"input_check"`
	Limitations     []string `json:"limitations"`
}

const ReviewStatusInsufficientData = "insufficient_data"

// AssessPlayerWithCoverage never hides findings in weak telemetry. Without a
// coverage record, old analyses must be re-run before silence is interpretable.
func AssessPlayerWithCoverage(playerID string, events []DetectionEvent, coverage *PlayerCoverage) ReviewAssessment {
	out := AssessPlayerEvents(playerID, events)
	if out.SignalCount == 0 && (coverage == nil || coverage.Version != 1 || coverage.Status != "limited") {
		out.Status = ReviewStatusInsufficientData
	}
	return out
}
