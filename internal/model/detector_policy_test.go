package model

import "testing"

func TestIsPlayspaceDetectorHasExactScope(t *testing.T) {
	for _, id := range []string{"MOV_006", "PAT_005"} {
		if !IsPlayspaceDetector(id) {
			t.Errorf("dedicated playspace detector %q was not recognized", id)
		}
	}
	for _, id := range []string{"", "MOV_001", "PAT_003", "PAT_004", "THROW_003", "STATE_001", "STATE_008", "BIO_001", "mov_006", "MOV_006 "} {
		if IsPlayspaceDetector(id) {
			t.Errorf("pause widened to unrelated or unknown detector %q", id)
		}
	}
}
