package model

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"
)

func mechanicsRecordForTest(frame int) MechanicsAssessment {
	v := Vec3{1, 2, 3}
	return MechanicsAssessment{Kind: MechanicsThrowPhysics, Result: MechanicsInconclusive, Reason: "release_rule_unverified", RuleVersion: DefaultProjectRules().Version, FrameIndex: frame, Timestamp: float64(frame), IntervalStart: float64(frame), IntervalEnd: float64(frame), EventID: fmt.Sprint(frame), Metrics: map[string]float64{"sampled_speed": 19}, RawSamples: []MechanicsRawSample{{FrameIndex: frame, Timestamp: float64(frame), DiscPosition: &v}}}
}

func TestMechanicsLogRejectsRekeyedEventsWithBoundedIdentityTail(t *testing.T) {
	log := NewMechanicsReviewLog()
	for i := 0; i < 300; i++ {
		log.Add(mechanicsRecordForTest(i))
	}
	for _, previous := range []int{0, 298, 299} {
		repeated := mechanicsRecordForTest(400)
		repeated.EventID = fmt.Sprint(previous)
		log.Add(repeated)
	}
	if log.Total != 300 || len(log.RecentEvents) != MaxMechanicsRecords {
		t.Fatal("repeated event inflated counts or identity tail is unbounded")
	}
	data, _ := json.Marshal(log)
	var restored MechanicsReviewLog
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	repeated := mechanicsRecordForTest(401)
	repeated.EventID = "298"
	restored.Add(repeated)
	if restored.Total != 300 || restored.Validate() != nil {
		t.Fatal("persistent tail did not preserve event dedup")
	}
	clone := log.Clone()
	clone.RecentEvents[0].EventID = "modified"
	if reflect.DeepEqual(clone.RecentEvents, log.RecentEvents) {
		t.Fatal("identity tail clone aliases original")
	}
}

func TestMechanicsLogInvalidOnlyMergeIdempotentAndOverflowSafe(t *testing.T) {
	log, invalid := NewMechanicsReviewLog(), NewMechanicsReviewLog()
	invalid.Invalid = 7
	log.Merge(invalid)
	log.Merge(invalid)
	if log.Invalid != 7 || log.Total != 0 {
		t.Fatal("identity-free invalid chunk replay inflated failures")
	}
	invalid.Invalid = math.MaxInt
	log.Merge(invalid)
	bad := mechanicsRecordForTest(1)
	bad.Result = "invalid"
	log.Add(bad)
	if log.Invalid != math.MaxInt || log.Validate() != nil {
		t.Fatal("invalid counter wrapped")
	}
	corrupt := NewMechanicsReviewLog()
	for i := 0; i < MaxMechanicsRecords; i++ {
		corrupt.Add(mechanicsRecordForTest(i))
	}
	corrupt.Dropped, corrupt.Total, corrupt.LastFrameIndex = 1, 129, 128
	corrupt.Consistent, corrupt.Inconclusive, corrupt.Anomaly, corrupt.ValidatedViolation = math.MaxInt, 131, math.MaxInt, 0
	if corrupt.Validate() == nil {
		t.Fatal("overflowed outcome sum accepted as valid counts")
	}
}

func TestMechanicsLogRekeyedDuplicateCannotReplayInvalidCounter(t *testing.T) {
	log, next := NewMechanicsReviewLog(), NewMechanicsReviewLog()
	first := mechanicsRecordForTest(1)
	log.Add(first)
	rekeyed := mechanicsRecordForTest(2)
	rekeyed.EventID = first.EventID
	next.Add(rekeyed)
	next.Invalid = 1
	log.Merge(next)
	log.Merge(next)
	if log.Total != 1 || log.LastFrameIndex != 1 || log.Invalid != 1 || log.Validate() != nil {
		t.Fatalf("replayed identity inflated invalid count: %+v", log)
	}
	newChunk := NewMechanicsReviewLog()
	newChunk.Add(mechanicsRecordForTest(3))
	newChunk.Invalid = 2
	log.Merge(newChunk)
	log.Merge(newChunk)
	if log.Total != 2 || log.Invalid != 3 || log.Validate() != nil {
		t.Fatal("new event chunk lost its invalid count or counted it twice")
	}
}

func TestMechanicsLogMergeOverlappingRetainedPrefixAndUnknownTail(t *testing.T) {
	left, next := NewMechanicsReviewLog(), NewMechanicsReviewLog()
	for i := 0; i < 75; i++ {
		left.Add(mechanicsRecordForTest(i))
	}
	for i := 50; i < 250; i++ {
		next.Add(mechanicsRecordForTest(i))
	}
	left.Merge(next)
	if left.Total != 250 || left.Inconclusive != 250 || left.Validate() != nil {
		t.Fatalf("known prefix overlap not merged exactly: %+v", left)
	}
	before := left.Clone()
	unknown := NewMechanicsReviewLog()
	for i := 100; i < 350; i++ {
		unknown.Add(mechanicsRecordForTest(i))
	}
	left.Merge(unknown)
	if !reflect.DeepEqual(left, before) {
		t.Fatal("uncountable overlap inside omitted tail mutated counters")
	}
	legacy := before.Clone()
	legacy.RecentEvents = nil
	if legacy.Validate() != nil {
		t.Fatal("older JSON without additive identity tail stopped working")
	}
}

func TestMechanicsLogBoundDedupCloneRoundtrip(t *testing.T) {
	log := NewMechanicsReviewLog()
	for i := 0; i < 300; i++ {
		r := mechanicsRecordForTest(i)
		log.Add(r)
		log.Add(r)
		r.Metrics["sampled_speed"] = 0
		(*r.RawSamples[0].DiscPosition)[0] = 99
	}
	if log.Total != 300 || log.Dropped != 172 || log.Inconclusive != 300 || len(log.Records) != 128 {
		t.Fatalf("%+v", log)
	}
	if log.Records[0].Metrics["sampled_speed"] != 19 || (*log.Records[0].RawSamples[0].DiscPosition)[0] != 1 {
		t.Fatal("caller owns retained evidence")
	}
	bad := mechanicsRecordForTest(301)
	bad.Metrics["bad"] = math.NaN()
	log.Add(bad)
	if log.Invalid != 1 || log.Total != 300 {
		t.Fatal("bad evidence counted as action")
	}
	clone := log.Clone()
	clone.Records[0].Metrics["sampled_speed"] = 1
	if log.Records[0].Metrics["sampled_speed"] != 19 {
		t.Fatal("shallow clone")
	}
	b, err := json.Marshal(log)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MechanicsReviewLog
	if err = json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if err = decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	decoded.Total++
	if decoded.Validate() == nil {
		t.Fatal("corrupt summary accepted")
	}
}

func TestMechanicsLogMergeDisjointAndOverlappingTails(t *testing.T) {
	a, b := NewMechanicsReviewLog(), NewMechanicsReviewLog()
	for i := 0; i < 150; i++ {
		a.Add(mechanicsRecordForTest(i))
		b.Add(mechanicsRecordForTest(i + 150))
	}
	a.Merge(b)
	a.Merge(b)
	if a.Total != 300 || a.Dropped != 172 || a.Validate() != nil {
		t.Fatalf("bad merge: %+v", a)
	}
	c := NewMechanicsReviewLog()
	for i := 170; i < 400; i++ {
		c.Add(mechanicsRecordForTest(i))
	}
	a.Merge(c)
	if a.Total != 300 {
		t.Fatal("unknowable partial tail overlap inflated evidence")
	}
}
