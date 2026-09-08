package model

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func catchRecordForTest(frame int) CatchReviewRecord {
	outcomes := []CatchReviewOutcome{CatchReviewObservation, CatchReviewExcluded, CatchReviewInsufficientData, CatchReviewUnconfirmed}
	return CatchReviewRecord{FrameIndex: frame, Timestamp: float64(frame) / 15, Confirmed: frame%4 == 0,
		Outcome: outcomes[frame%4], Reason: "catch_test", Metrics: map[string]float64{"sample_duration_s": .2}}
}

func TestCatchReviewLogBoundsOwnershipAndDuplicateIdentity(t *testing.T) {
	log := NewCatchReviewLog()
	record := catchRecordForTest(0)
	log.Add(record)
	record.Metrics["sample_duration_s"] = 999
	if log.Records[0].Metrics["sample_duration_s"] != .2 {
		t.Fatal("retained caller-owned metrics")
	}
	for frame := 1; frame < MaxCatchReviewRecords+20; frame++ {
		log.Add(catchRecordForTest(frame))
	}
	before := log.Clone()
	log.Add(catchRecordForTest(MaxCatchReviewRecords + 19))
	log.Add(catchRecordForTest(0))
	if !reflect.DeepEqual(log, before) {
		t.Fatal("duplicate/stale identity inflated counts after cap")
	}
	if len(log.Records) != MaxCatchReviewRecords || log.Total != MaxCatchReviewRecords+20 || log.Dropped != 20 || log.Invalid != 0 {
		t.Fatalf("unbounded or incorrect log: %+v", log)
	}
	if err := log.Validate(); err != nil {
		t.Fatal(err)
	}
	copy := log.Clone()
	copy.Records[0].Metrics["sample_duration_s"] = 42
	if log.Records[0].Metrics["sample_duration_s"] != .2 {
		t.Fatal("clone shared metrics")
	}
	data, err := json.Marshal(log)
	if err != nil {
		t.Fatal(err)
	}
	var loaded CatchReviewLog
	if err := json.Unmarshal(data, &loaded); err != nil || !reflect.DeepEqual(log, &loaded) {
		t.Fatalf("JSON changed diagnostics: %v", err)
	}
}

func TestCatchReviewRejectsUnboundedOrNonfinitePayloads(t *testing.T) {
	for name, mutate := range map[string]func(*CatchReviewRecord){
		"nan timestamp":                 func(r *CatchReviewRecord) { r.Timestamp = math.NaN() },
		"negative frame":                func(r *CatchReviewRecord) { r.FrameIndex = -1 },
		"future start":                  func(r *CatchReviewRecord) { r.StartFrame = 99 },
		"future free":                   func(r *CatchReviewRecord) { r.LastFreeFrame = 99 },
		"unknown outcome":               func(r *CatchReviewRecord) { r.Outcome = "clean" },
		"unconfirmed observation":       func(r *CatchReviewRecord) { r.Confirmed = false },
		"confirmed unconfirmed outcome": func(r *CatchReviewRecord) { r.Outcome = CatchReviewUnconfirmed },
		"private reason":                func(r *CatchReviewRecord) { r.Reason = "arbitrary player text" },
		"private metric":                func(r *CatchReviewRecord) { r.Metrics["private path"] = 1 },
		"infinite metric":               func(r *CatchReviewRecord) { r.Metrics["sample_duration_s"] = math.Inf(1) },
		"too many metrics": func(r *CatchReviewRecord) {
			for i := 0; i < MaxCatchReviewMetrics; i++ {
				r.Metrics[string(rune('a'+i))] = 1
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := catchRecordForTest(0)
			mutate(&r)
			log := NewCatchReviewLog()
			log.Add(r)
			if log.Invalid != 1 || log.Total != 0 || log.Dropped != 0 || len(log.Records) != 0 {
				t.Fatalf("invalid payload counted as a catch: %+v", log)
			}
			if _, err := json.Marshal(log); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCatchReviewMergeIsChunkInvariantAndBounded(t *testing.T) {
	const total = MaxCatchReviewRecords*3 + 19
	want := NewCatchReviewLog()
	for i := 0; i < total; i++ {
		want.Add(catchRecordForTest(i))
	}
	for _, size := range []int{1, 3, 128, 150, total} {
		got := NewCatchReviewLog()
		for start := 0; start < total; start += size {
			chunk := NewCatchReviewLog()
			for i := start; i < min(start+size, total); i++ {
				chunk.Add(catchRecordForTest(i))
			}
			got.Merge(chunk)
			got.Merge(chunk) // same accepted chunk must not inflate counts
		}
		if err := got.Validate(); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("chunk size %d changed bounded log: err=%v got=%+v", size, err, got)
		}
	}
}

func TestCatchReviewOldCoverageJSONDoesNotInventDiagnostics(t *testing.T) {
	var coverage PlayerCoverage
	if err := json.Unmarshal([]byte(`{"version":1,"status":"limited","detectors":[{"detector_id":"STATE_008","enabled":true}]}`), &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.Detectors[0].CatchReview != nil {
		t.Fatal("legacy JSON invented catch diagnostics")
	}
}

func TestCatchReviewEmptyMetricsHaveStableJSONOwnership(t *testing.T) {
	log := NewCatchReviewLog()
	r := catchRecordForTest(0)
	r.Metrics = map[string]float64{}
	log.Add(r)
	data, err := json.Marshal(log)
	if err != nil {
		t.Fatal(err)
	}
	var loaded CatchReviewLog
	if err := json.Unmarshal(data, &loaded); err != nil || !reflect.DeepEqual(log, &loaded) {
		t.Fatalf("empty optional metrics changed after persistence: %v", err)
	}
}
