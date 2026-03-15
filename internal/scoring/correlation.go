package scoring

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// ComputeCorrelationBonus computes bonus points for correlated detections.
func ComputeCorrelationBonus(events []model.DetectionEvent, cap float64) float64 {
	detectorIDs := make(map[string]bool)
	for _, e := range events {
		detectorIDs[e.DetectorID] = true
	}

	bonus := 0.0

	// THROW_003 + THROW_006 = +15
	if detectorIDs["THROW_003"] && detectorIDs["THROW_006"] {
		bonus += 15.0
	}

	// BIO_003 + BIO_004 + PAT_001 = +25
	if detectorIDs["BIO_003"] && detectorIDs["BIO_004"] && detectorIDs["PAT_001"] {
		bonus += 25.0
	}

	// 3+ throw detectors = +10
	throwCount := 0
	for id := range detectorIDs {
		if len(id) > 5 && id[:5] == "THROW" {
			throwCount++
		}
	}
	if throwCount >= 3 {
		bonus += 10.0
	}

	if bonus > cap {
		bonus = cap
	}
	return bonus
}
