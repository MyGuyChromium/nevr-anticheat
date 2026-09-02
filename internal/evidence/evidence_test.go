package evidence

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func mkEvent(id, det, player string, frame int, sev float64) model.DetectionEvent {
	return model.DetectionEvent{
		EventID: id, DetectorID: det, DetectorVersion: "1.2", PlayerID: player, MatchID: "m1",
		FrameIndex: frame, FrameRangeStart: frame - 2, FrameRangeEnd: frame + 2,
		Timestamp: float64(frame) / 15.0, Severity: sev, Confidence: 0.9, EnforcementWeight: 0.8,
		ObservedValue: "25.0 m/s", ExpectedRange: "<= 20.0 m/s",
		CausalKey: model.CausalKey{PlayerID: player, AnomalyType: "release_speed"},
	}
}

func TestBuilderPopulatesLocatorFields(t *testing.T) {
	b := NewBuilder()
	b.SetDetectorNames(map[string]string{"THROW_001": "Release Velocity Cap"})
	fixed := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	b.SetClock(func() time.Time { return fixed })
	mc := &model.MatchContext{MatchID: "m1", StartTime: fixed.Add(-time.Hour)}
	score := model.SuspicionScore{PlayerID: "p", TotalScore: 72}
	events := []model.DetectionEvent{
		mkEvent("e2", "THROW_001", "p", 300, 0.9),
		mkEvent("e1", "THROW_001", "p", 100, 0.5),
		mkEvent("e3", "MOV_001", "p", 200, 0.7),
		mkEvent("x", "THROW_001", "other", 50, 0.9),
	}
	shadow := mkEvent("s", "BIO_001", "p", 400, 0.9)
	shadow.IsShadow = true
	events = append(events, shadow)

	rc := b.Build("p", mc, score, events)
	if len(rc.DetectorsTriggered) != 3 {
		t.Fatalf("expected 3 triggered (player p, non-shadow), got %d", len(rc.DetectorsTriggered))
	}
	if rc.DetectorsTriggered[0].EventID != "e1" || rc.DetectorsTriggered[2].EventID != "e2" {
		t.Errorf("triggered not sorted by frame: %+v", rc.DetectorsTriggered)
	}
	td := rc.DetectorsTriggered[0]
	if td.DetectorName != "Release Velocity Cap" || td.FrameIndex != 100 || td.FrameRangeStart != 98 ||
		td.FrameRangeEnd != 102 || td.ExpectedRange != "<= 20.0 m/s" || td.DetectorVersion != "1.2" ||
		td.MetricName != "release_speed" || td.Severity != "medium" || td.Timestamp == 0 {
		t.Errorf("locator fields missing: %+v", td)
	}
	if rc.DetectorsTriggered[1].DetectorName != "MOV_001" {
		t.Errorf("unknown detector should fall back to ID, got %q", rc.DetectorsTriggered[1].DetectorName)
	}
	if !strings.Contains(rc.Explanation, "3 detection(s) from 2 distinct detector(s)") {
		t.Errorf("explanation wrong: %s", rc.Explanation)
	}
	if rc.Level != string(model.LevelHighRisk) || rc.Severity != "high" || rc.RecommendedAction != "review_only" {
		t.Errorf("classification wrong: level=%s sev=%s action=%s", rc.Level, rc.Severity, rc.RecommendedAction)
	}
	if !strings.HasPrefix(rc.CaseID, "RC-20260304-") || len(rc.CaseID) != len("RC-20260304-")+36 {
		t.Errorf("case id should carry a full UUID: %s", rc.CaseID)
	}
	if rc.ThresholdVersion == "" || rc.Status != model.CaseStatusPending {
		t.Errorf("threshold version/status missing: %+v", rc)
	}
	if rc.TimestampEnd.IsZero() || !rc.TimestampEnd.After(rc.TimestampStart) {
		t.Errorf("timestamp end should be derived from last event: %v %v", rc.TimestampStart, rc.TimestampEnd)
	}
	// Report must render the locator data.
	report := FormatReport(rc)
	for _, want := range []string{"Release Velocity Cap", "frame 100", "Expected: <= 20.0 m/s", "Event: e1", "THROW_001    x2", "t=0:06.667"} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}

func TestBuilderNilMatchContextAndLevels(t *testing.T) {
	b := NewBuilderWithLevels(model.DefaultLevelTable().WithReviewThreshold(15))
	rc := b.Build("p", nil, model.SuspicionScore{TotalScore: 15}, []model.DetectionEvent{mkEvent("e1", "THROW_001", "p", 10, 0.9)})
	if rc.MatchID != "m1" {
		t.Errorf("match id should come from events when ctx is nil: %q", rc.MatchID)
	}
	if rc.Level != string(model.LevelHighRisk) {
		t.Errorf("builder should honour its level table: %s", rc.Level)
	}
	if ThresholdVersion(b.Levels(), map[string]string{"A": "1"}) == ThresholdVersion(model.DefaultLevelTable(), map[string]string{"A": "1"}) {
		t.Error("threshold version must change with the table")
	}
	if ThresholdVersion(model.DefaultLevelTable(), map[string]string{"A": "1", "B": "2"}) !=
		ThresholdVersion(model.DefaultLevelTable(), map[string]string{"B": "2", "A": "1"}) {
		t.Error("threshold version must be order independent")
	}
}

type fakeStore struct {
	cases  map[string]model.ReviewCase
	events []model.DetectionEvent
	frames []model.PlayerTelemetryFrame
	score  *model.SuspicionScore
}

func (f *fakeStore) GetReviewCase(_ context.Context, id string) (model.ReviewCase, error) {
	rc, ok := f.cases[id]
	if !ok {
		return rc, errors.New("not found")
	}
	return rc, nil
}
func (f *fakeStore) GetMatchEvents(_ context.Context, matchID string) ([]model.DetectionEvent, error) {
	var out []model.DetectionEvent
	for _, ev := range f.events {
		if ev.MatchID == matchID {
			out = append(out, ev)
		}
	}
	return out, nil
}
func (f *fakeStore) GetMatchFrames(_ context.Context, _ string) ([]model.PlayerTelemetryFrame, error) {
	return f.frames, nil
}
func (f *fakeStore) GetPlayerScore(_ context.Context, _ string) (model.SuspicionScore, error) {
	if f.score == nil {
		return model.SuspicionScore{}, errors.New("no score")
	}
	return *f.score, nil
}

func frames(n int, players ...string) []model.PlayerTelemetryFrame {
	var out []model.PlayerTelemetryFrame
	for i := 0; i < n; i++ {
		for _, p := range players {
			out = append(out, model.PlayerTelemetryFrame{PlayerID: p, FrameIndex: i, Timestamp: float64(i) / 15})
		}
	}
	return out
}

func TestExportPerEventClips(t *testing.T) {
	st := &fakeStore{
		cases: map[string]model.ReviewCase{"c1": {CaseID: "c1", PlayerID: "p", MatchID: "m1", Severity: "high"}},
		events: []model.DetectionEvent{
			mkEvent("e1", "THROW_001", "p", 100, 0.9),
			mkEvent("e2", "MOV_001", "p", 9000, 0.9),
			mkEvent("other", "THROW_001", "q", 100, 0.9),
		},
		frames: frames(9100, "p", "q"),
	}
	shadow := mkEvent("s", "BIO_001", "p", 500, 0.9)
	shadow.IsShadow = true
	st.events = append(st.events, shadow)

	ex := NewExporter(st)
	bundle, err := ex.ExportForCase(context.Background(), "c1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.DetectionEvents) != 2 || len(bundle.Clips) != 2 {
		t.Fatalf("expected 2 events / 2 clips, got %d / %d", len(bundle.DetectionEvents), len(bundle.Clips))
	}
	c := bundle.Clips[0]
	if c.WindowStart != 98-45 || c.WindowEnd != 102+45 {
		t.Errorf("clip window wrong: %d-%d", c.WindowStart, c.WindowEnd)
	}
	// 95 frames * 2 players
	if len(c.Frames) != 95*2 {
		t.Errorf("clip should include all players in the window: %d frames", len(c.Frames))
	}
	if c.Frames[0].PlayerID != "p" || c.Frames[1].PlayerID != "q" || c.Frames[0].FrameIndex != 53 {
		t.Errorf("clip frames not sorted: %+v", c.Frames[:2])
	}
	// Union is not one giant 53..9047 window.
	if len(bundle.Frames) != 2*95*2 {
		t.Errorf("union should be the two clips only, got %d frames", len(bundle.Frames))
	}
	if bundle.SuspicionScore != nil || bundle.Metadata["suspicion_score"] != "unavailable" {
		t.Errorf("missing score should be reported, not zero-filled: %+v", bundle.SuspicionScore)
	}
	if bundle.Metadata["clip_count"] != "2" || bundle.Metadata["frame_window"] != "53-9047" {
		t.Errorf("metadata: %v", bundle.Metadata)
	}
	if _, err := MarshalBundle(bundle); err != nil {
		t.Fatal(err)
	}
	// Caller-supplied frames take precedence over the store.
	b2, err := ex.ExportForCase(context.Background(), "c1", frames(200, "p"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b2.Clips[1].Frames) != 0 || len(b2.Clips[0].Frames) != 95 {
		t.Errorf("caller frames not used: %d %d", len(b2.Clips[0].Frames), len(b2.Clips[1].Frames))
	}
}

func TestExportErrors(t *testing.T) {
	st := &fakeStore{
		cases:  map[string]model.ReviewCase{"c1": {CaseID: "c1", PlayerID: "p", MatchID: "m1"}},
		events: []model.DetectionEvent{mkEvent("other", "THROW_001", "q", 100, 0.9)},
		frames: frames(10, "p"),
	}
	ex := NewExporter(st)
	if _, err := ex.ExportForCase(context.Background(), "c1", nil); !errors.Is(err, ErrNoEvents) {
		t.Fatalf("expected ErrNoEvents, got %v", err)
	}
	st.events = append(st.events, mkEvent("e1", "THROW_001", "p", 5000, 0.9))
	if _, err := ex.ExportForCase(context.Background(), "c1", nil); !errors.Is(err, ErrNoFrames) {
		t.Fatalf("expected ErrNoFrames, got %v", err)
	}
	if _, err := ex.ExportForCase(context.Background(), "missing", nil); err == nil {
		t.Fatal("missing case should error")
	}
}
