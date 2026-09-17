package sqlite

import (
	"errors"
	"testing"
)

func TestMatchViewCacheRoundTripAndReplacement(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if _, _, err := s.GetMatchViewCache(ctx, "M1", "investigation/v1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing row: %v", err)
	}
	for _, bad := range [][3]string{{"", "k", `{}`}, {"M1", "", `{}`}, {"M1", "k", `{oops`}, {"M1", "k", ``}} {
		if err := s.StoreMatchViewCache(ctx, bad[0], bad[1], "key", []byte(bad[2])); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	if err := s.StoreMatchViewCache(ctx, "M1", "investigation/v1", "run-1", []byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMatchViewCache(ctx, "M1", "other/v1", "run-1", []byte(`{"other":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMatchViewCache(ctx, "M1", "investigation/v1", "run-2", []byte(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	key, doc, err := s.GetMatchViewCache(ctx, "M1", "investigation/v1")
	if err != nil || key != "run-2" || string(doc) != `{"n":2}` {
		t.Fatalf("key=%q doc=%s err=%v", key, doc, err)
	}
	if key, doc, err := s.GetMatchViewCache(ctx, "M1", "other/v1"); err != nil || key != "run-1" || string(doc) != `{"other":true}` {
		t.Fatalf("kinds are not independent: key=%q doc=%s err=%v", key, doc, err)
	}
	if _, _, err := s.GetMatchViewCache(ctx, "M2", "investigation/v1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other match: %v", err)
	}
}
