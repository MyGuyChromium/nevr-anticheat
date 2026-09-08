package config

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// DetectorPauseReason describes the compiled temporary pause. Saved profiles
// and older TOML files cannot resume these checks; a future reviewed code
// change can lift the pause without losing their parameters or raw telemetry.
func DetectorPauseReason(id string) string {
	if model.IsPlayspaceDetector(id) {
		return "Playspacing checks are paused while wrist angle, mags and autopocket are the review focus."
	}
	return ""
}
