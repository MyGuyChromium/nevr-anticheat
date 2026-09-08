package model

import (
	"strings"
	"testing"
)

func TestReleaseIdentityScopesPrivateSourceAndNotDiagnosticKind(t *testing.T) {
	r := &ReleaseObservation{PlayerID: "player", FirstFreeFrame: 10, EndTime: 1,
		Source: &ObservationContext{Source: "echo_http_session", SourceID: "http://user:secret@private-endpoint/session", SourcePlayerID: "local-client",
			SessionID: "session", Authority: "client_reported", TimeBasis: "recorder_prefix", FrameIndex: 10, Timestamp: 1}}
	identity := r.EventID()
	if identity == "" || identity != r.Clone().EventID() || strings.Contains(identity, "secret") || strings.Contains(identity, "private-endpoint") {
		t.Fatal("unstable or privacy-leaking identity")
	}
	for _, mutate := range []func(*ReleaseObservation){
		func(r *ReleaseObservation) { r.Source.SourceID = "recorder-B" },
		func(r *ReleaseObservation) { r.Source.SourcePlayerID = "remote-client" },
		func(r *ReleaseObservation) { r.Source.SessionID = "another-session" },
		func(r *ReleaseObservation) { r.Source.TimeBasis = "receive_time" },
		func(r *ReleaseObservation) { r.Source.Authority = "claimed_engine" },
		func(r *ReleaseObservation) { r.PlayerID = "another-player" },
		func(r *ReleaseObservation) { r.FirstFreeFrame++ },
	} {
		copy := r.Clone()
		mutate(copy)
		if copy.EventID() == identity {
			t.Fatal("different release/source scope reused the same identity")
		}
	}
	if (*ReleaseObservation)(nil).EventID() != "" {
		t.Fatal("missing event acquired an identity")
	}
}
