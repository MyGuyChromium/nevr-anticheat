package model

// IsPlayspaceDetector identifies the two dedicated playspacing checks paused
// for the current review release. Keep the shared legal-motion features and
// unrelated movement, wrist, grab-geometry and pre-catch checks available.
// Historical observations remain evidence, but must not earn new aggregate
// findings while these checks are paused.
func IsPlayspaceDetector(id string) bool {
	return id == "MOV_006" || id == "PAT_005"
}
