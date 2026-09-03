package evidence

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// ctxStore is a fakeStore that also serves the stored match context.
type ctxStore struct {
	*fakeStore
	mc *model.MatchContext
}

func (c *ctxStore) GetMatchContext(_ context.Context, _ string) (*model.MatchContext, error) {
	return c.mc, nil
}

func TestServerIDFromContext(t *testing.T) {
	if MatchServerID(nil) != "" || MatchServerID(&model.MatchContext{MatchID: "m1"}) != "" {
		t.Fatal("a context without a server id must yield \"\"")
	}
	if got := serverIDFromJSON([]byte(`{"match_id":"m1","server_id":" srv-eu-1 "}`)); got != "srv-eu-1" {
		t.Fatalf("server_id not read from the context JSON: %q", got)
	}
	if got := serverIDFromJSON([]byte(`not json`)); got != "" {
		t.Fatalf("bad JSON should yield \"\", got %q", got)
	}
}

func TestReportShowsProvenance(t *testing.T) {
	rc := NewBuilder().Build("p", &model.MatchContext{MatchID: "m1", Source: "live"},
		model.SuspicionScore{PlayerID: "p", TotalScore: 72},
		[]model.DetectionEvent{mkEvent("e1", "THROW_001", "p", 100, 0.9)})

	plain := FormatReport(rc)
	if strings.Contains(plain, "Server:") || strings.Contains(plain, "Source:") {
		t.Fatalf("report without a context should not print provenance:\n%s", plain)
	}
	withCtx := FormatReportWithContext(rc, &model.MatchContext{MatchID: "m1", Source: "live"})
	if !strings.Contains(withCtx, "Source:         live") || strings.Contains(withCtx, "Server:") {
		t.Fatalf("report with a context lacking a server id: \n%s", withCtx)
	}
	// Once ingest records MatchContext.ServerID, the report names the server.
	full := formatReport(rc, &model.MatchContext{MatchID: "m1", Source: "live"}, "srv-eu-1")
	for _, want := range []string{"Server:         srv-eu-1", "Source:         live", "Match ID:       m1"} {
		if !strings.Contains(full, want) {
			t.Errorf("report missing %q:\n%s", want, full)
		}
	}
	if strings.Index(full, "Match ID:") > strings.Index(full, "Server:") {
		t.Errorf("server line should follow the match id:\n%s", full)
	}
}

func TestExportBundleCarriesMatchContext(t *testing.T) {
	base := &fakeStore{
		cases:  map[string]model.ReviewCase{"c1": {CaseID: "c1", PlayerID: "p", MatchID: "m1"}},
		events: []model.DetectionEvent{mkEvent("e1", "THROW_001", "p", 100, 0.9)},
		frames: frames(200, "p"),
	}
	// A store without GetMatchContext still exports, without provenance.
	b0, err := NewExporter(base).ExportForCase(context.Background(), "c1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b0.MatchContext != nil {
		t.Fatalf("plain store should not produce a match context: %+v", b0.MatchContext)
	}
	if _, ok := b0.Metadata["server_id"]; ok {
		t.Fatal("server_id must be absent when unknown")
	}

	st := &ctxStore{fakeStore: base, mc: &model.MatchContext{MatchID: "m1", Source: "live", Map: "mpl_arena_a"}}
	b1, err := NewExporter(st).ExportForCase(context.Background(), "c1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b1.MatchContext == nil || b1.MatchContext.Source != "live" {
		t.Fatalf("bundle should carry the stored match context: %+v", b1.MatchContext)
	}
	buf, err := MarshalBundle(b1)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["match_context"]; !ok {
		t.Fatalf("match_context missing from bundle JSON: %s", buf)
	}

	// A store that has no context for the match degrades to no provenance.
	st.mc = nil
	b2, err := NewExporter(st).ExportForCase(context.Background(), "c1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b2.MatchContext != nil {
		t.Fatal("nil context must not be attached")
	}
}

func TestExportFrameWindowSpansAllClips(t *testing.T) {
	long := mkEvent("a", "PAT_001", "p", 100, 0.9)
	long.FrameRangeStart, long.FrameRangeEnd = 90, 5000
	short := mkEvent("b", "THROW_001", "p", 200, 0.9)
	short.FrameRangeStart, short.FrameRangeEnd = 198, 202
	st := &fakeStore{
		cases:  map[string]model.ReviewCase{"c1": {CaseID: "c1", PlayerID: "p", MatchID: "m1"}},
		events: []model.DetectionEvent{short, long},
		frames: frames(5100, "p"),
	}
	bundle, err := NewExporter(st).ExportForCase(context.Background(), "c1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Clips) != 2 || bundle.Clips[0].EventID != "a" || bundle.Clips[1].EventID != "b" {
		t.Fatalf("clips should be in event (frame) order: %+v", bundle.Clips)
	}
	// Clip order is 45-5045 then 153-247: the window is the union span.
	if got := bundle.Metadata["frame_window"]; got != "45-5045" {
		t.Fatalf("frame_window should span all clips, got %q", got)
	}
	if first, last := bundle.Frames[0].FrameIndex, bundle.Frames[len(bundle.Frames)-1].FrameIndex; first != 45 || last != 5045 {
		t.Fatalf("union frames %d-%d disagree with the window", first, last)
	}
}
