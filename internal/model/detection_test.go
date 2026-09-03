package model

import (
	"encoding/json"
	"math"
	"testing"
)

func TestCausalKey_KeyExcludesFrameRange(t *testing.T) {
	a := CausalKey{PlayerID: "P", AnomalyType: "wrist", FrameStart: 10, FrameEnd: 20}
	b := CausalKey{PlayerID: "P", AnomalyType: "wrist", FrameStart: 11, FrameEnd: 21}
	if a.Key() != b.Key() || a.Key() != "P:wrist" {
		t.Errorf("keys differ or wrong format: %q %q", a.Key(), b.Key())
	}
	if !a.Overlaps(b) {
		t.Error("expected overlap")
	}
	c := CausalKey{PlayerID: "P", AnomalyType: "other", FrameStart: 10, FrameEnd: 20}
	if a.Key() == c.Key() || a.Overlaps(c) {
		t.Error("different anomaly types must not share a key or overlap")
	}
}

func TestDetectionEvent_EvidenceRoundTrip(t *testing.T) {
	ev := DetectionEvent{
		EventID: "e1", DetectorID: "BIO_002", DetectorVersion: "1", MatchID: "M", PlayerID: "P",
		FrameIndex: 5, FrameRangeStart: 3, FrameRangeEnd: 7, Timestamp: 0.3, Severity: 0.5, Confidence: 0.6,
		Evidence:      HandSpeedEvidence{Speed: 12.5, Hand: "left"},
		ObservedValue: "12.5", ExpectedRange: "0-8",
		CausalKey:         CausalKey{PlayerID: "P", AnomalyType: "hand_speed", FrameStart: 3, FrameEnd: 7},
		EnforcementWeight: 0.8, MergedCount: 3,
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	var evid map[string]any
	if err := json.Unmarshal(probe["evidence"], &evid); err != nil {
		t.Fatal(err)
	}
	if evid["type"] != "hand_speed" || evid["speed"] != 12.5 {
		t.Errorf("evidence envelope missing discriminator or fields: %v", evid)
	}

	var back DetectionEvent
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Decoded evidence has the VALUE type detectors emit, so a type switch
	// written for live events works on decoded ones too.
	hs, ok := back.Evidence.(HandSpeedEvidence)
	if !ok || hs.Speed != 12.5 || hs.Hand != "left" {
		t.Errorf("typed evidence not restored as a value: %#v", back.Evidence)
	}
	if len(EvidenceTypes()) != 15 || EvidenceTypes()[0] != "disc_acceleration" {
		t.Errorf("EvidenceTypes() = %v", EvidenceTypes())
	}
	if ev, err := DecodeEvidenceAs("state", []byte(`{"state_type":"x"}`)); err != nil || ev == nil {
		t.Errorf("DecodeEvidenceAs without an embedded type: %v %v", ev, err)
	}
	if back.MergedCount != 3 || back.CausalKey != ev.CausalKey || back.Severity != 0.5 {
		t.Errorf("fields lost in round trip: %+v", back)
	}

	// Unknown / legacy evidence (no type) decodes to nil without error.
	var legacy DetectionEvent
	if err := json.Unmarshal([]byte(`{"event_id":"x","evidence":{"foo":1}}`), &legacy); err != nil || legacy.Evidence != nil {
		t.Errorf("legacy evidence: err=%v evidence=%v", err, legacy.Evidence)
	}
	var nilEv DetectionEvent
	if err := json.Unmarshal([]byte(`{"event_id":"x","evidence":null}`), &nilEv); err != nil {
		t.Errorf("null evidence: %v", err)
	}
}

func TestDetectionEvent_Validate(t *testing.T) {
	good := DetectionEvent{
		EventID: "e", DetectorID: "D", DetectorVersion: "1", MatchID: "M", PlayerID: "P",
		FrameIndex: 1, FrameRangeStart: 0, FrameRangeEnd: 1, Severity: 0.5, Confidence: 0.5,
		ObservedValue: "x", EnforcementWeight: 0.5,
		CausalKey: CausalKey{PlayerID: "P", AnomalyType: "a"},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	cases := map[string]func(*DetectionEvent){
		"nan severity":      func(e *DetectionEvent) { e.Severity = math.NaN() },
		"severity > 1":      func(e *DetectionEvent) { e.Severity = 1.5 },
		"inf confidence":    func(e *DetectionEvent) { e.Confidence = math.Inf(1) },
		"weight > 1":        func(e *DetectionEvent) { e.EnforcementWeight = 2 },
		"missing match":     func(e *DetectionEvent) { e.MatchID = "" },
		"negative frame":    func(e *DetectionEvent) { e.FrameIndex = -1 },
		"inverted range":    func(e *DetectionEvent) { e.FrameRangeEnd = -5 },
		"missing version":   func(e *DetectionEvent) { e.DetectorVersion = "" },
		"missing causal":    func(e *DetectionEvent) { e.CausalKey.AnomalyType = "" },
		"nan timestamp":     func(e *DetectionEvent) { e.Timestamp = math.NaN() },
		"missing observed":  func(e *DetectionEvent) { e.ObservedValue = "" },
		"missing player id": func(e *DetectionEvent) { e.PlayerID = "" },
	}
	for name, mutate := range cases {
		e := good
		mutate(&e)
		if err := e.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}
