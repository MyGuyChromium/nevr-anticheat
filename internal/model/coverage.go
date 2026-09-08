package model

// PlayerCoverage records what the pipeline could observe, not whether a player
// was honest. CandidateFrames counts dispatch after phase/warmup gates; detector
// internal guards may still skip them. InputFrames is only a necessary-input
// check for the named detector, never an independent ground-truth opportunity.
type PlayerCoverage struct {
	DataHealth     *DataHealth        `json:"data_health,omitempty"`
	Version        int                `json:"version"`
	Status         string             `json:"status"`
	ValidFrames    int                `json:"valid_frames"`
	RejectedFrames int                `json:"rejected_frames"`
	QualityGated   bool               `json:"quality_gated"`
	Limitations    []string           `json:"limitations"`
	Detectors      []DetectorCoverage `json:"detectors"`
}

type DetectorCoverage struct {
	Capability      *DetectorCapability    `json:"capability,omitempty"`
	DetectorID      string                 `json:"detector_id"`
	Enabled         bool                   `json:"enabled"`
	Status          string                 `json:"status"`
	CandidateFrames int                    `json:"candidate_frames"`
	InputFrames     int                    `json:"input_frames"`
	InputCheck      bool                   `json:"input_check"`
	Limitations     []string               `json:"limitations"`
	DecisionTrace   *DetectorDecisionTrace `json:"decision_trace,omitempty"`
	CatchReview     *CatchReviewLog        `json:"catch_review,omitempty"`
	MechanicsReview *MechanicsReviewLog    `json:"mechanics_review,omitempty"`
}

// DetectorDecisionTrace is a bounded summary of branches actually visited,
// not an explanation synthesized by running detector guards a second time.
// Multiple branches/hands may be counted on one frame. Raw emissions are not
// retained incidents; scoring and storage are reported separately downstream.
type DetectorDecisionTrace struct {
	Version          int                      `json:"version"`
	InternalBranches bool                     `json:"internal_branches"`
	Reasons          []DetectorDecisionReason `json:"reasons"`
	OverflowCount    int                      `json:"overflow_count"`
}

type DetectorDecisionReason struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	Count       int    `json:"count"`
	FirstFrame  int    `json:"first_frame"`
	LastFrame   int    `json:"last_frame"`
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
