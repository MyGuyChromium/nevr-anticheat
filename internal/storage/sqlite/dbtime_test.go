package sqlite

import (
	"testing"
	"time"
)

func TestDBTime_FormatIsUTCZ(t *testing.T) {
	loc := time.FixedZone("CDT", -5*3600)
	local := time.Date(2026, 9, 2, 11, 10, 40, 500, loc)
	got := fmtDBTime(local)
	if got != "2026-09-02T16:10:40Z" {
		t.Fatalf("fmtDBTime = %q, want 2026-09-02T16:10:40Z", got)
	}
	back, err := parseDBTime(got)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Equal(local.Truncate(time.Second)) {
		t.Fatalf("round trip = %v, want %v", back, local)
	}
}

func TestDBTime_ParsesLegacyAndOffsetLayouts(t *testing.T) {
	cases := map[string]time.Time{
		"2026-09-02 16:10:40":       time.Date(2026, 9, 2, 16, 10, 40, 0, time.UTC),
		"2026-09-02T11:10:40-05:00": time.Date(2026, 9, 2, 16, 10, 40, 0, time.UTC),
		"2026-09-02T16:10:40.123Z":  time.Date(2026, 9, 2, 16, 10, 40, 123e6, time.UTC),
	}
	for in, want := range cases {
		got, err := parseDBTime(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("%q = %v, want %v", in, got, want)
		}
		if got.Location() != time.UTC {
			t.Errorf("%q: location %v, want UTC", in, got.Location())
		}
	}
	if _, err := parseDBTime("garbage"); err == nil {
		t.Error("expected error for garbage")
	}
	if !parseDBTimeLenient("").IsZero() {
		t.Error("lenient parse of empty should be zero")
	}
}

// Lexicographic order of the canonical layout must equal chronological order,
// including across the boundary that broke the legacy layout (' ' < 'T').
func TestDBTime_LexicographicOrderMatchesChronological(t *testing.T) {
	base := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	prev := fmtDBTime(base)
	for i := 1; i < 48*60; i++ {
		cur := fmtDBTime(base.Add(time.Duration(i) * time.Minute))
		if !(prev < cur) {
			t.Fatalf("order broken: %q !< %q", prev, cur)
		}
		prev = cur
	}
}
