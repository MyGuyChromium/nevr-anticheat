package model

import "testing"

func TestCoverageNeverHidesSignalsOrCertifiesPlayers(t *testing.T) {
	for _, coverage := range []*PlayerCoverage{nil, {Version: 1, Status: "insufficient_data"}, {Version: 2, Status: "limited"}, {Version: 1, Status: "limited"}} {
		events := []DetectionEvent{{PlayerID: "arbitrary", DetectorID: "BIO_001", IsShadow: true}}
		if got := AssessPlayerWithCoverage("arbitrary", events, coverage); got.Status != ReviewStatusReviewNeeded || got.ShadowSignals != 1 {
			t.Fatalf("hidden signal: %+v", got)
		}
		got := AssessPlayerWithCoverage("arbitrary", nil, coverage)
		want := ReviewStatusInsufficientData
		if coverage != nil && coverage.Version == 1 && coverage.Status == "limited" {
			want = ReviewStatusNoSignals
		}
		if got.Status != want {
			t.Fatalf("status=%s want=%s", got.Status, want)
		}
	}
}
