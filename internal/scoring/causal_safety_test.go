package scoring

import "testing"

func TestMetaObservationsAndRepeatedIdentityCannotAddPoints(t *testing.T) {
	s := NewSuspicionScorer(testConfig())
	first := ev("MOV_001", "p", 0, 1, 1, 1)
	first.EventID = "immutable-event"
	s.IngestEvent(first)
	before := s.GetScore("p")
	first.FrameIndex = 100000 // retry metadata cannot turn the same identity into a new incident
	if _, accepted := s.IngestEventWithResult(first); accepted {
		t.Fatal("relabelled event identity was scored twice")
	}
	for _, id := range []string{"PAT_003", "PAT_004"} {
		if _, accepted := s.IngestEventWithResult(ev(id, "p", 200000, 1, 1, 1)); accepted {
			t.Fatalf("derived %s observation added independent points", id)
		}
	}
	s.ApplyCorrelationBonus()
	after := s.GetScore("p")
	if after.TotalScore != before.TotalScore || after.EventCount != before.EventCount || after.BaseScore != before.BaseScore {
		t.Fatalf("correlated/retried evidence inflated score: %+v -> %+v", before, after)
	}
	s.Reset()
	if _, accepted := s.IngestEventWithResult(first); !accepted {
		t.Fatal("new independent scoring run retained old dedup state")
	}
}
