package flyagent

import "testing"

func TestEncodePrivateLiveRequiresPrivateBoundLoopbackSource(t *testing.T) {
	encoder, tick := testTick(false, GoalPositiveZ)
	tick.MatchCtx.IsPrivate = true
	tick.Session.PrivateMatch = true
	tick.Session.SessionID = tick.MatchID
	tick.Frames[0].Observation.Source = liveObservationSource
	tick.Frames[0].Observation.SourceID = "http://127.0.0.1:6721/session"
	tick.Frames[0].Observation.Authority = liveObservationAuthority
	tick.Frames[0].Observation.TimeBasis = liveObservationTimeBasis
	tick.Frames[0].Observation.SourcePlayerID = tick.Frames[0].PlayerID

	observation, err := encoder.EncodePrivateLive(tick)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.IsPrivate || !observation.SourceEpochKnown ||
		observation.SourceKind != liveObservationSource ||
		observation.SourceID != "http://127.0.0.1:6721/session" ||
		ControlBlockReason(observation) != "" {
		t.Fatalf("private live observation was not admitted: %+v", observation)
	}
}

func TestEncodePrivateLiveFailsClosedOnEvidenceOrSourceMismatch(t *testing.T) {
	for _, test := range []struct {
		name        string
		privateCtx  bool
		privateRaw  bool
		source      string
		authority   string
		timeBasis   string
		localPlayer bool
	}{
		{name: "normalized public", privateRaw: true, source: "http://127.0.0.1:6721/session", authority: liveObservationAuthority, timeBasis: liveObservationTimeBasis, localPlayer: true},
		{name: "raw public", privateCtx: true, source: "http://127.0.0.1:6721/session", authority: liveObservationAuthority, timeBasis: liveObservationTimeBasis, localPlayer: true},
		{name: "remote endpoint", privateCtx: true, privateRaw: true, source: "http://192.0.2.1:6721/session", authority: liveObservationAuthority, timeBasis: liveObservationTimeBasis, localPlayer: true},
		{name: "query endpoint", privateCtx: true, privateRaw: true, source: "http://127.0.0.1:6721/session?unsafe=1", authority: liveObservationAuthority, timeBasis: liveObservationTimeBasis, localPlayer: true},
		{name: "wrong authority", privateCtx: true, privateRaw: true, source: "http://127.0.0.1:6721/session", authority: "engine", timeBasis: liveObservationTimeBasis, localPlayer: true},
		{name: "prefixed time basis", privateCtx: true, privateRaw: true, source: "http://127.0.0.1:6721/session", authority: liveObservationAuthority, timeBasis: liveObservationTimeBasis + "_guessed", localPlayer: true},
		{name: "remote player", privateCtx: true, privateRaw: true, source: "http://127.0.0.1:6721/session", authority: liveObservationAuthority, timeBasis: liveObservationTimeBasis},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoder, tick := testTick(false, GoalPositiveZ)
			tick.MatchCtx.IsPrivate = test.privateCtx
			tick.Session.PrivateMatch = test.privateRaw
			tick.Session.SessionID = tick.MatchID
			tick.Frames[0].Observation.Source = liveObservationSource
			tick.Frames[0].Observation.SourceID = test.source
			tick.Frames[0].Observation.Authority = test.authority
			tick.Frames[0].Observation.TimeBasis = test.timeBasis
			if test.localPlayer {
				tick.Frames[0].Observation.SourcePlayerID = tick.Frames[0].PlayerID
			}
			observation, err := encoder.EncodePrivateLive(tick)
			if err != nil {
				t.Fatal(err)
			}
			if observation.SourceEpochKnown || observation.SourceKind != "" ||
				ControlBlockReason(observation) != "source_provenance_unavailable" {
				t.Fatalf("unsafe live source was admitted: %+v", observation)
			}
		})
	}
}
