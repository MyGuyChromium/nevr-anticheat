package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	capture "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"github.com/echotools/tape/v4/pkg/codec"
	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
	"google.golang.org/protobuf/proto"
)

// All sources below are generated from the synthetic integration fixture. They
// deliberately share a session ID to exercise source conflicts, not game physics.
func sourceGuardVariant(t *testing.T, from, to int, captureID string, legacy bool) string {
	t.Helper()
	p := adapter.NewEchoReplayParser()
	if _, _, _, err := p.ParseFile(nativeFixture(t)); err != nil {
		t.Fatal(err)
	}
	raw := p.RawSessionByFrame()
	var text strings.Builder
	var writer *codec.Writer
	path := filepath.Join(t.TempDir(), "candidate.tape")
	if legacy {
		path = filepath.Join(filepath.Dir(path), "candidate.echoreplay")
	}
	for idx := from; idx <= to; idx++ {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw[idx]), &fields); err != nil {
			t.Fatal(err)
		}
		var record adapter.TapeRawRecord
		if err := json.Unmarshal(fields["_nevr_tape"], &record); err != nil {
			t.Fatal(err)
		}
		if legacy {
			delete(fields, "_nevr_tape")
			data, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			stamp, err := adapter.NativeTapeSampleTime(raw[idx])
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&text, "%s\t%s\n", stamp.Format(time.RFC3339Nano), data)
			continue
		}
		if writer == nil {
			var err error
			writer, err = codec.NewWriter(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = writer.Close() })
			header := new(capture.CaptureHeader)
			if err := proto.Unmarshal(record.Header, header); err != nil {
				t.Fatal(err)
			}
			if captureID != "" {
				header.CaptureId = captureID
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
		}
		frame := new(capture.Frame)
		if err := proto.Unmarshal(record.Frame, frame); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	if legacy {
		if err := os.WriteFile(path, []byte(text.String()), 0600); err != nil {
			t.Fatal(err)
		}
	} else if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func sourceGuardSnapshot(t *testing.T, store *sqlite.Store) map[string][][]string {
	t.Helper()
	out := make(map[string][][]string)
	for _, table := range []string{"match_ticks", "telemetry_frames", "match_contexts", "detection_events", "suspicion_scores", "review_cases", "match_summaries", "match_labels", "investigation_notes"} {
		rows, err := store.DB().Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			cells := make([]string, len(values))
			for i, value := range values {
				cells[i] = fmt.Sprintf("%T:%v", value, value)
			}
			out[table] = append(out[table], cells)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		_ = rows.Close()
	}
	return out
}

func seedSourceGuardReview(t *testing.T, e *Engine) {
	t.Helper()
	ctx := context.Background()
	mc, err := e.Store().GetMatchContext(ctx, testutil.NativeTapeMatchID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StoreMatchAnalysis(ctx, e.Store(), mc, matchResult(mc.MatchID, map[string]float64{testutil.NativeTapePlayerID: 70}), "synthetic", AnalysisOptions{Logger: quietLogger()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store().StoreMatchLabel(ctx, mc.MatchID, "suspected", "Synthetic persistence-test note, not a gameplay label", "tester", "test", "test"); err != nil {
		t.Fatal(err)
	}
}

func TestNativeSourceGuardRejectsSourceAndRangeCollisions(t *testing.T) {
	for _, method := range []string{"single", "batch"} {
		for _, mode := range []string{"different_capture", "shorter_prefix", "different_range", "legacy_into_native", "native_into_legacy"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				e := newTestEngine(t)
				initial := sourceGuardVariant(t, 0, 11, "", mode == "native_into_legacy")
				if res, err := e.AnalyzeFile(context.Background(), initial, true); err != nil || res.PersistError() != nil {
					t.Fatalf("initial import: %+v %v", res, err)
				}
				seedSourceGuardReview(t, e)
				before := sourceGuardSnapshot(t, e.Store())
				from, to, captureID := 0, 11, ""
				switch mode {
				case "different_capture":
					captureID = "8a0c0b6e-0000-4000-8000-000000000099"
				case "shorter_prefix":
					to = 5
				case "different_range":
					from = 5
				}
				candidate := sourceGuardVariant(t, from, to, captureID, mode == "legacy_into_native")
				if method == "single" {
					if res, err := e.AnalyzeFile(context.Background(), candidate, true); err == nil || res != nil || !strings.Contains(err.Error(), "source conflict") {
						t.Fatalf("conflicting input not rejected: %+v %v", res, err)
					}
				} else {
					ba := NewBatchAnalyzer(emptyPipeline(), e.Store(), func() FrameParser { return NewJSONFrameParser() }, 1, quietLogger())
					ba.SetForce(true)
					res, err := ba.AnalyzeDirectory(context.Background(), filepath.Dir(candidate))
					if err != nil || res.Processed != 0 || res.PersistFailed != 1 || res.Errors != 1 {
						t.Fatalf("conflicting batch reported success: %+v %v", res, err)
					}
				}
				if after := sourceGuardSnapshot(t, e.Store()); !reflect.DeepEqual(before, after) {
					t.Fatal("rejected capture changed stored evidence, context, results or notes")
				}
			})
		}
	}
}

func TestNativeSourceGuardOrphanPrefixRetryAndConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			e := newTestEngine(t)
			p := adapter.NewEchoReplayParser()
			if _, _, _, err := p.ParseFile(nativeFixture(t)); err != nil {
				t.Fatal(err)
			}
			raw := p.RawSessionByFrame()
			for idx := 4; idx < testutil.NativeTapeFrames; idx++ {
				delete(raw, idx)
			}
			if _, err := e.Store().StoreTelemetryFramesWithRaw(context.Background(), testutil.NativeTapeMatchID, nil, raw); err != nil {
				t.Fatal(err)
			}
			before := sourceGuardSnapshot(t, e.Store())
			captureID := ""
			if conflict {
				captureID = "8a0c0b6e-0000-4000-8000-000000000099"
			}
			res, err := e.AnalyzeFile(context.Background(), sourceGuardVariant(t, 0, 11, captureID, false), true)
			if conflict {
				if err == nil || !strings.Contains(err.Error(), "source conflict") || res != nil {
					t.Fatalf("orphan source conflict accepted: %+v %v", res, err)
				}
				if !reflect.DeepEqual(before, sourceGuardSnapshot(t, e.Store())) {
					t.Fatal("conflict modified interrupted source prefix")
				}
				return
			}
			if err != nil || res.PersistError() != nil || res.Telemetry.TicksIgnored != 4 || res.Telemetry.TicksInserted != 8 {
				t.Fatalf("matching interrupted capture did not recover: %+v %v", res, err)
			}
		})
	}
}

func TestNativeSourceGuardRawWriteFailurePreservesAllPublishedData(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			e := newTestEngine(t)
			path := nativeFixture(t)
			if existing {
				if _, err := e.AnalyzeFile(context.Background(), path, true); err != nil {
					t.Fatal(err)
				}
				seedSourceGuardReview(t, e)
			}
			before := sourceGuardSnapshot(t, e.Store())
			if _, err := e.Store().DB().Exec(`CREATE TRIGGER reject_native_raw BEFORE INSERT ON match_ticks BEGIN SELECT RAISE(ABORT, 'synthetic raw write failure'); END`); err != nil {
				t.Fatal(err)
			}
			res, err := e.AnalyzeFile(context.Background(), path, true)
			if err != nil || res == nil || res.RawTickErr == nil || res.PersistError() == nil || res.Replaced || !strings.Contains(res.RawTickErr.Error(), "synthetic raw write failure") {
				t.Fatalf("raw persistence failure not reported: %+v %v", res, err)
			}
			if after := sourceGuardSnapshot(t, e.Store()); !reflect.DeepEqual(before, after) {
				t.Fatal("failed original-evidence write changed published telemetry/context/results/notes")
			}
		})
	}
}

func TestNativeSourceGuardMissingEvidenceAndIdenticalRetry(t *testing.T) {
	e := newTestEngine(t)
	path := nativeFixture(t)
	if _, err := e.AnalyzeFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	seedSourceGuardReview(t, e)
	if res, err := e.AnalyzeFile(context.Background(), path, true); err != nil || res.PersistError() != nil || !res.Replaced {
		t.Fatalf("identical capture reanalysis failed: %+v %v", res, err)
	}
	label, found, err := e.Store().GetMatchLabel(context.Background(), testutil.NativeTapeMatchID)
	if err != nil || !found || label.Comment != "Synthetic persistence-test note, not a gameplay label" {
		t.Fatalf("identical retry lost reviewer label: %+v %v", label, err)
	}
	if _, err := e.Store().DB().Exec("DELETE FROM match_ticks"); err != nil {
		t.Fatal(err)
	}
	before := sourceGuardSnapshot(t, e.Store())
	if res, err := e.AnalyzeFile(context.Background(), path, true); err == nil || res != nil || !strings.Contains(err.Error(), "no original protobuf") {
		t.Fatalf("missing native evidence accepted as verifiable duplicate: %+v %v", res, err)
	}
	if !reflect.DeepEqual(before, sourceGuardSnapshot(t, e.Store())) {
		t.Fatal("unverifiable replacement changed stored data")
	}
}

func TestNativeSourceGuardMissingMetadataAndCancellation(t *testing.T) {
	e := newTestEngine(t)
	path := nativeFixture(t)
	res, err := e.AnalyzeFile(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	mc := cloneMatchContext(res.MatchCtx)
	mc.NativeCapture = nil
	if err := e.Store().StoreMatchContext(context.Background(), mc, testutil.NativeTapeFrames); err != nil {
		t.Fatal(err)
	}
	before := sourceGuardSnapshot(t, e.Store())
	if _, err := e.AnalyzeFile(context.Background(), path, true); err == nil || !strings.Contains(err.Error(), "metadata is missing") {
		t.Fatalf("missing metadata did not fail closed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ba := NewBatchAnalyzer(emptyPipeline(), e.Store(), func() FrameParser { return NewJSONFrameParser() }, 1, quietLogger())
	if _, _, err := ba.analyzeFile(ctx, emptyPipeline(), path, make(map[string]string), &sync.Mutex{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("native batch parse ignored cancellation: %v", err)
	}
	if !reflect.DeepEqual(before, sourceGuardSnapshot(t, e.Store())) {
		t.Fatal("rejected/cancelled import changed stored data")
	}
}

func TestNativeSourceGuardRemapRefreshesDecoderMetadata(t *testing.T) {
	e := newTestEngine(t)
	res, err := e.AnalyzeFile(context.Background(), nativeFixture(t), true)
	if err != nil {
		t.Fatal(err)
	}
	mc := cloneMatchContext(res.MatchCtx)
	mc.NativeCapture.SchemaRevision = "previous-decoder"
	mc.NativeCapture.Limitations = []string{"previous limitations"}
	remapped, err := RemapStoredTelemetry(context.Background(), e.Store(), mc, model.DefaultPhysics())
	if err != nil || remapped.Context.NativeCapture.SchemaRevision != adapter.TapeSchemaRevision || remapped.Context.NativeCapture.ContainerIntegrity != "verified_footer_and_checksum" || reflect.DeepEqual(remapped.Context.NativeCapture.Limitations, mc.NativeCapture.Limitations) {
		t.Fatalf("remap kept stale decoder metadata: %+v %v", remapped, err)
	}
	if mc.NativeCapture.SchemaRevision != "previous-decoder" || mc.NativeCapture.Limitations[0] != "previous limitations" {
		t.Fatal("remap mutated caller metadata")
	}
}

func TestNativeSourceGuardMissingRawTailCannotHideShorterCapture(t *testing.T) {
	e := newTestEngine(t)
	if _, err := e.AnalyzeFile(context.Background(), nativeFixture(t), true); err != nil {
		t.Fatal(err)
	}
	// Simulate damaged retention in this disposable database, not a legitimate
	// shorter source: the published context still describes the full capture.
	if _, err := e.Store().DB().Exec("DELETE FROM match_ticks WHERE frame_index >= 6"); err != nil {
		t.Fatal(err)
	}
	before := sourceGuardSnapshot(t, e.Store())
	if res, err := e.AnalyzeFile(context.Background(), sourceGuardVariant(t, 0, 5, "", false), true); err == nil || res != nil || !strings.Contains(err.Error(), "shorter recorded span") {
		t.Fatalf("partial raw storage concealed a shortened replacement: %+v %v", res, err)
	}
	if !reflect.DeepEqual(before, sourceGuardSnapshot(t, e.Store())) {
		t.Fatal("shortened input changed published data after raw tail loss")
	}
}

func TestNativeForceBatchRefreshesCacheAndRollsBackWriteFailures(t *testing.T) {
	for _, failure := range []string{"none", "raw", "cache"} {
		t.Run(failure, func(t *testing.T) {
			e := newTestEngine(t)
			ctx := context.Background()
			path := nativeFixture(t)
			if _, err := e.AnalyzeFile(ctx, path, true); err != nil {
				t.Fatal(err)
			}
			seedSourceGuardReview(t, e)
			frames, err := e.Store().GetMatchFrames(ctx, testutil.NativeTapeMatchID)
			if err != nil || len(frames) != testutil.NativeTapeFrames {
				t.Fatalf("load fixture cache: frames=%d %v", len(frames), err)
			}
			original := frames[0].Position
			frames[0].Position[0] = 99 // Deliberately stale cache, not source evidence.
			if _, err := e.Store().ReplaceMatchTelemetryFrames(ctx, testutil.NativeTapeMatchID, frames); err != nil {
				t.Fatal(err)
			}
			before := sourceGuardSnapshot(t, e.Store())
			var trigger string
			switch failure {
			case "raw":
				trigger = `CREATE TRIGGER fail_native_batch BEFORE INSERT ON match_ticks BEGIN SELECT RAISE(ABORT, 'synthetic raw failure'); END`
			case "cache":
				trigger = `CREATE TRIGGER fail_native_batch BEFORE INSERT ON telemetry_frames WHEN NEW.frame_index = 5 BEGIN SELECT RAISE(ABORT, 'synthetic cache failure'); END`
			}
			if trigger != "" {
				if _, err := e.Store().DB().Exec(trigger); err != nil {
					t.Fatal(err)
				}
			}
			ba := NewBatchAnalyzer(emptyPipeline(), e.Store(), func() FrameParser { return NewJSONFrameParser() }, 1, quietLogger())
			ba.SetForce(true)
			result, err := ba.AnalyzeDirectory(ctx, filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			if failure != "none" {
				if result.Processed != 0 || result.PersistFailed != 1 || !reflect.DeepEqual(before, sourceGuardSnapshot(t, e.Store())) {
					t.Fatalf("failed native batch changed old cache/source/results: %+v", result)
				}
				return
			}
			after, err := e.Store().GetMatchFrames(ctx, testutil.NativeTapeMatchID)
			if err != nil || len(after) != testutil.NativeTapeFrames || after[0].Position != original {
				var got model.Vec3
				if len(after) > 0 {
					got = after[0].Position
				}
				t.Fatalf("forced native batch retained stale normalized pose: frames=%d first=%v want=%v err=%v", len(after), got, original, err)
			}
			if result.Processed != 1 || result.Errors != 0 || result.FramesInserted != testutil.NativeTapeFrames || result.FramesIgnored != 0 {
				t.Fatalf("native batch replacement counters: %+v", result)
			}
			if snapshot := sourceGuardSnapshot(t, e.Store()); !reflect.DeepEqual(before["match_ticks"], snapshot["match_ticks"]) || !reflect.DeepEqual(before["match_labels"], snapshot["match_labels"]) {
				t.Fatal("successful cache refresh changed original evidence or notes")
			}
		})
	}
}
