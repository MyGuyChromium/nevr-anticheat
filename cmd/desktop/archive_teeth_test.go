package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// These tests exist because the raw-tick archive is the ONLY copy of a match's
// source evidence once "prune" has run. Counting ticks is not enough: an
// archive of N empty objects has the right count.

const teethMatchID = "SYN-FIXTURE-001"

func teethUploadFixture(t *testing.T) (*server, string) {
	t.Helper()
	s, ts := newTestServer(t)
	if resp, out := upload(t, ts, false, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != http.StatusOK || len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("upload status=%d out=%+v", resp.StatusCode, out)
	}
	return s, ts.URL + "/" + testToken
}

func teethRawTicks(t *testing.T, s *server) map[int]string {
	t.Helper()
	ticks, err := s.engine.Store().GetAllMatchRawTicks(context.Background(), teethMatchID)
	if err != nil {
		t.Fatal(err)
	}
	return ticks
}

// teethJSONValue decodes a tick keeping every number as its source literal, so
// two ticks compare equal only if every key, string and number literal agrees.
func teethJSONValue(t *testing.T, raw string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		t.Fatalf("raw tick is not JSON: %v\n%.200s", err, raw)
	}
	return value
}

func TestTeethArchivePruneRestoreReturnsTheOriginalTicks(t *testing.T) {
	s, base := teethUploadFixture(t)
	original := teethRawTicks(t, s)
	if len(original) == 0 {
		t.Fatal("fixture stored no raw ticks")
	}
	distinct := make(map[string]bool)
	for _, raw := range original {
		distinct[raw] = true
	}
	if len(distinct) < 2 {
		t.Fatal("fixture ticks are all identical; this test could not detect swapped content")
	}

	var archive rawArchiveResult
	resp := postJSONTest(t, base+"/api/match/"+teethMatchID+"/archive", map[string]any{"prune_raw": true, "confirmation": teethMatchID}, &archive)
	if resp.StatusCode != http.StatusOK || archive.TicksPruned != int64(len(original)) {
		t.Fatalf("prune status=%d result=%+v", resp.StatusCode, archive)
	}
	if left := teethRawTicks(t, s); len(left) != 0 {
		t.Fatalf("%d raw ticks survived pruning", len(left))
	}

	// The archive on disk must already hold the original content: after the
	// prune it is the only copy.
	_, archived, err := readAndVerifyRawArchive(archive.Path, teethMatchID)
	if err != nil {
		t.Fatal(err)
	}
	for idx, want := range original {
		if got, ok := archived[idx]; !ok || got != want {
			t.Fatalf("archived tick %d differs from the database source\n got: %.200s\nwant: %.200s", idx, got, want)
		}
	}

	resp = postJSONTest(t, base+"/api/match/"+teethMatchID+"/restore-raw", map[string]any{}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore status=%d", resp.StatusCode)
	}
	restored := teethRawTicks(t, s)
	if len(restored) != len(original) {
		t.Fatalf("restored %d ticks, want %d", len(restored), len(original))
	}
	for idx, want := range original {
		got, ok := restored[idx]
		if !ok {
			t.Fatalf("frame %d was not restored", idx)
		}
		if got != want {
			t.Fatalf("restored tick %d differs from the original source evidence\n got: %.200s\nwant: %.200s", idx, got, want)
		}
	}
}

// The archive wraps each tick in an NDJSON envelope, which re-encodes the tick:
// insignificant whitespace is dropped and <, >, & and U+2028 become \u escapes.
// That is NOT byte-preserving (see the PR notes), so this pins the contract the
// format can actually keep: every key, string value and number LITERAL survives,
// including literals a float64 round trip would destroy.
func TestTeethArchiveRoundTripKeepsAwkwardTickValuesAndNumberLiterals(t *testing.T) {
	s, base := teethUploadFixture(t)
	const frame = 900000
	awkward := "{ \"name\" : \"<b>& x\",\n \"big\": 12345678901234567890.123456789e+40, \"tiny\":0.1000000000000000055, \"nested\": [1.50, {\"k\": null}] }"
	if _, _, err := s.engine.Store().RestoreMatchRawTicks(context.Background(), teethMatchID, map[int]string{frame: awkward}); err != nil {
		t.Fatal(err)
	}
	if resp := postJSONTest(t, base+"/api/match/"+teethMatchID+"/archive", map[string]any{"prune_raw": true, "confirmation": teethMatchID}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("prune status=%d", resp.StatusCode)
	}
	if resp := postJSONTest(t, base+"/api/match/"+teethMatchID+"/restore-raw", map[string]any{}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("restore status=%d", resp.StatusCode)
	}
	got, ok := teethRawTicks(t, s)[frame]
	if !ok {
		t.Fatal("awkward tick was not restored")
	}
	if !reflect.DeepEqual(teethJSONValue(t, got), teethJSONValue(t, awkward)) {
		t.Fatalf("restored tick changed value\n got: %s\nwant: %s", got, awkward)
	}
	for _, literal := range []string{"12345678901234567890.123456789e+40", "0.1000000000000000055", "1.50"} {
		if !strings.Contains(got, literal) {
			t.Fatalf("number literal %s was rewritten: %s", literal, got)
		}
	}
}

// teethRewriteArchive rewrites an archive in place after the caller has edited
// its entries (entry name -> bytes).
func teethRewriteArchive(t *testing.T, path string, edit func(entries map[string][]byte)) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := make(map[string][]byte)
	var order []string
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		if _, err := data.ReadFrom(r); err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
		entries[file.Name] = data.Bytes()
		order = append(order, file.Name)
	}
	_ = zr.Close()
	if len(entries["raw-ticks.ndjson"]) == 0 || len(entries["manifest.json"]) == 0 {
		t.Fatalf("archive layout changed (entries %v); update this test", order)
	}
	edit(entries)
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, name := range order {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entries[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func teethEditManifest(t *testing.T, entries map[string][]byte, edit func(*rawArchiveManifest)) {
	t.Helper()
	var manifest rawArchiveManifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	edit(&manifest)
	out, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	entries["manifest.json"] = out
}

func teethDropLastLine(data []byte) []byte {
	lines := strings.SplitAfter(strings.TrimRight(string(data), "\n"), "\n")
	return []byte(strings.Join(lines[:len(lines)-1], ""))
}

func TestTeethRestoreRawRejectsTamperedArchives(t *testing.T) {
	cases := map[string]struct {
		want string // the rejection must name this cause, not an unrelated one
		edit func(t *testing.T, entries map[string][]byte)
	}{
		// The edited line is still valid JSON, so only the checksum can notice.
		"changed tick content": {want: "checksum", edit: func(t *testing.T, entries map[string][]byte) {
			data := entries["raw-ticks.ndjson"]
			i := bytes.Index(data, []byte(`"raw":{`))
			if i < 0 {
				t.Fatal("archive tick layout changed; update this test")
			}
			edited := append([]byte(nil), data[:i+7]...)
			edited = append(edited, `"tampered":1,`...)
			entries["raw-ticks.ndjson"] = append(edited, data[i+7:]...)
		}},
		// The digest is recomputed, so only the manifest tick count can notice.
		"dropped tick with a recomputed checksum": {want: "tick count", edit: func(t *testing.T, entries map[string][]byte) {
			kept := teethDropLastLine(entries["raw-ticks.ndjson"])
			entries["raw-ticks.ndjson"] = kept
			sum := sha256.Sum256(kept)
			teethEditManifest(t, entries, func(m *rawArchiveManifest) { m.TicksSHA256 = hex.EncodeToString(sum[:]) })
		}},
		// Count and content both still agree with each other; only the digest differs.
		"replaced tick with a consistent count": {want: "checksum", edit: func(t *testing.T, entries map[string][]byte) {
			kept := teethDropLastLine(entries["raw-ticks.ndjson"])
			entries["raw-ticks.ndjson"] = append(kept, []byte(`{"frame_index":999999,"raw":{}}`+"\n")...)
		}},
		"manifest for another match": {want: "does not match", edit: func(t *testing.T, entries map[string][]byte) {
			teethEditManifest(t, entries, func(m *rawArchiveManifest) { m.MatchID = "SOME-OTHER-MATCH" })
		}},
		"unknown archive version": {want: "does not match", edit: func(t *testing.T, entries map[string][]byte) {
			teethEditManifest(t, entries, func(m *rawArchiveManifest) { m.Version = rawArchiveVersion + 1 })
		}},
		"duplicated tick": {want: "duplicate", edit: func(t *testing.T, entries map[string][]byte) {
			data := entries["raw-ticks.ndjson"]
			first := data[:bytes.IndexByte(data, '\n')+1]
			entries["raw-ticks.ndjson"] = append(append([]byte(nil), data...), first...)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s, base := teethUploadFixture(t)
			var archive rawArchiveResult
			resp := postJSONTest(t, base+"/api/match/"+teethMatchID+"/archive", map[string]any{"prune_raw": true, "confirmation": teethMatchID}, &archive)
			if resp.StatusCode != http.StatusOK || archive.TicksPruned == 0 {
				t.Fatalf("prune status=%d result=%+v", resp.StatusCode, archive)
			}
			teethRewriteArchive(t, archive.Path, func(entries map[string][]byte) { tc.edit(t, entries) })

			var failure struct {
				Error string `json:"error"`
			}
			resp = postJSONTest(t, base+"/api/match/"+teethMatchID+"/restore-raw", map[string]any{}, &failure)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("tampered archive restore status=%d body=%+v, want 422", resp.StatusCode, failure)
			}
			if !strings.Contains(failure.Error, tc.want) {
				t.Fatalf("rejected for an unrelated reason: %q does not mention %q", failure.Error, tc.want)
			}
			if restored := teethRawTicks(t, s); len(restored) != 0 {
				t.Fatalf("a rejected archive still restored %d ticks", len(restored))
			}
		})
	}
}

// A truncated download or a half-written file must never be mistaken for an
// archive, and must not be reported as "no archive".
func TestTeethRestoreRawRejectsTruncatedArchive(t *testing.T) {
	s, base := teethUploadFixture(t)
	var archive rawArchiveResult
	if resp := postJSONTest(t, base+"/api/match/"+teethMatchID+"/archive", map[string]any{"prune_raw": true, "confirmation": teethMatchID}, &archive); resp.StatusCode != http.StatusOK {
		t.Fatalf("prune status=%d", resp.StatusCode)
	}
	data, err := os.ReadFile(archive.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive.Path, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if resp := postJSONTest(t, base+"/api/match/"+teethMatchID+"/restore-raw", map[string]any{}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("truncated archive restore status=%d, want 422", resp.StatusCode)
	}
	if restored := teethRawTicks(t, s); len(restored) != 0 {
		t.Fatalf("a truncated archive restored %d ticks", len(restored))
	}
}

// The post-write verification is what stands between "archive written" and
// "source rows deleted". A stored tick the archive reader refuses (a negative
// frame index) makes the freshly written archive unrestorable, so the prune
// must be refused and every source row must survive.
func TestTeethPruneIsRefusedWhenTheWrittenArchiveDoesNotVerify(t *testing.T) {
	s, base := teethUploadFixture(t)
	if _, err := s.engine.Store().DB().Exec(`INSERT INTO match_ticks (match_id, frame_index, raw_json, ingested_at) VALUES (?, -1, '{"teeth":true}', '2026-01-01T00:00:00Z')`, teethMatchID); err != nil {
		t.Skipf("the schema refuses a negative frame index, so this state cannot occur: %v", err)
	}
	before := teethRawTicks(t, s)
	resp := postJSONTest(t, base+"/api/match/"+teethMatchID+"/archive", map[string]any{"prune_raw": true, "confirmation": teethMatchID}, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unverifiable archive status=%d, want 422", resp.StatusCode)
	}
	after := teethRawTicks(t, s)
	if len(after) != len(before) {
		t.Fatalf("source ticks were pruned behind an archive that cannot be restored: %d -> %d", len(before), len(after))
	}
	leftovers, _ := filepath.Glob(filepath.Join(s.archiveDir(), "*"))
	if len(leftovers) != 0 {
		t.Fatalf("an unverifiable archive was left on disk: %v", leftovers)
	}
}
