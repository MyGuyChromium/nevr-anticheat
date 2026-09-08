package model

import (
	"math"
	"testing"
)

func TestObservationProvenanceAndClone(t *testing.T) {
	o := &ObservationContext{Source: "replay", Authority: "client_reported", TimeBasis: "recorder_prefix", SessionID: "m", SourcePlayerID: "p", FrameIndex: 3, Timestamp: 0.2, Freshness: "value_change"}
	if !o.BoundLocalThrow("p", 3, 0.2) || o.BoundLocalThrow("q", 3, 0.2) {
		t.Fatal("binding check")
	}
	r := &ReleaseObservation{Source: o, HandCandidates: []string{"left"}, PlayerMovement: []MovementObservation{{ReportedVelocity: &Vec3{1, 0, 0}}}}
	c := r.Clone()
	c.Source.Source = "other"
	c.HandCandidates[0] = "right"
	c.PlayerMovement[0].ReportedVelocity[0] = 9
	if r.Source.Source != "replay" || r.HandCandidates[0] != "left" || r.PlayerMovement[0].ReportedVelocity[0] != 1 {
		t.Fatal("clone aliases evidence")
	}
	c.Source = o.Clone()
	c.Source.FrameIndex = 4
	c.Source.Timestamp = 0.3
	if !o.SameSource(c.Source) {
		t.Fatal("sample identity confused with source identity")
	}
	c.Source.Timestamp = math.NaN()
	if o.SameSource(c.Source) {
		t.Fatal("invalid source time accepted")
	}
}

func TestAttachmentUnknownAndThrowPublication(t *testing.T) {
	var unknown *DiscAttachment
	if unknown.Known() || unknown.Free() || unknown.HeldBy("p") {
		t.Fatal("nil became known")
	}
	for _, a := range []*DiscAttachment{{State: "held", HolderID: "p"}, {State: "held", HolderID: "p", HandCandidates: []string{"left", "left"}}, {State: "free", HolderID: "p"}} {
		if a.Known() {
			t.Fatalf("malformed attachment accepted: %+v", a)
		}
	}
	e := &ThrowEvent{FrameIndex: 3}
	if !e.ObservedAt(3) {
		t.Fatal("legacy event broken")
	}
	i := 4
	e.ObservedFrameIndex = &i
	if e.ObservedAt(3) || !e.ObservedAt(4) {
		t.Fatal("publication identity ignored")
	}
}
