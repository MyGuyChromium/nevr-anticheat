package model

import "encoding/json"

type RuleDefinition struct {
	Version              string          `json:"version"`
	Meaning              string          `json:"meaning"`
	Units                string          `json:"units"`
	Provenance           string          `json:"provenance"`
	Applicability        string          `json:"applicability"`
	Exceptions           string          `json:"exceptions"`
	Tests                []string        `json:"tests"`
	ConfiguredParameters json.RawMessage `json:"configured_parameters,omitempty"`
}

// DetectorCapability is a versioned evidence contract, not a claim of accuracy.
// RequiredInputs are necessary for descriptive review. EnforcementRequires are
// additional, independently verified prerequisites; receiving data is not trust.
type DetectorCapability struct {
	Rule                *RuleDefinition `json:"rule,omitempty"`
	Version             string          `json:"version"`
	DetectorID          string          `json:"detector_id"`
	RequiredInputs      []string        `json:"required_inputs"`
	SourceTrust         string          `json:"source_trust"`
	Timing              string          `json:"timing"`
	Validity            string          `json:"validity"`
	RuleID              string          `json:"rule_id"`
	ApplicableBuild     string          `json:"applicable_build"`
	EnforcementRequires []string        `json:"enforcement_requires"`
	EnforcementEligible bool            `json:"enforcement_eligible"`
	MissingBehavior     string          `json:"missing_behavior"`
	Limitations         string          `json:"limitations"`
}

// DataHealth describes observation usability independently of suspicion. Counts
// are samples, NOT independent incidents, valid opportunities, or probabilities.
// A healthy feed is still not authenticated/authoritative gameplay validation.
type DataHealth struct {
	Revision                 uint64   `json:"revision"`
	Version                  int      `json:"version"`
	State                    string   `json:"state"`
	Reasons                  []string `json:"reasons"`
	AffectedDetectors        []string `json:"affected_detectors"`
	RecoverySamplesRemaining int      `json:"recovery_samples_remaining"`
	HealthySamples           int      `json:"healthy_samples"`
	DegradedSamples          int      `json:"degraded_samples"`
	BlindSamples             int      `json:"blind_samples"`
	LastFrame                int      `json:"last_frame"`
}

const (
	HealthHealthy  = "healthy"
	HealthDegraded = "degraded"
	HealthBlind    = "blind"
)

func (h *DataHealth) Clone() *DataHealth {
	if h == nil {
		return nil
	}
	out := *h
	out.Reasons = append([]string(nil), h.Reasons...)
	out.AffectedDetectors = append([]string(nil), h.AffectedDetectors...)
	return &out
}
