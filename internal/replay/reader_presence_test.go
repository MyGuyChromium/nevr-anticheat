package replay

import (
	"encoding/json"
	"testing"
)

func TestLegacyCanonicalZeroAndNullNeverUseConflictingAliases(t *testing.T) {
	for _, value := range []string{"[0,0,0]", "null"} {
		var p RawPlayerFrame
		if err := json.Unmarshal([]byte(`{"left_hand":`+value+`,"left_hand_position":[1,2,3],"ping_ms":0,"estimated_ping_ms":90}`), &p); err != nil {
			t.Fatal(err)
		}
		if p.LeftHand != [3]float64{} || p.PingMs != 0 {
			t.Fatalf("canonical lost: %+v", p)
		}
	}
	for _, value := range []string{`""`, `null`} {
		var d RawDiscFrame
		if err := json.Unmarshal([]byte(`{"holder_id":`+value+`,"possessor_id":"other"}`), &d); err != nil {
			t.Fatal(err)
		}
		if d.HolderID != "" {
			t.Fatal("canonical empty holder replaced")
		}
	}
}

func TestLegacyScoreRequiresBothExplicitNonNullSidesAndReusedDecodeClears(t *testing.T) {
	var p RawPlayerFrame
	for _, tc := range []struct {
		doc   string
		known bool
	}{
		{`{"blue_score":0,"orange_score":0}`, true},
		{`{"blue_score":null,"orange_score":0}`, false},
		{`{"blue_score":2}`, false},
		{`{}`, false},
		{`null`, false},
	} {
		if err := json.Unmarshal([]byte(tc.doc), &p); err != nil {
			t.Fatal(err)
		}
		if p.HasScore != tc.known {
			t.Fatalf("%s HasScore=%v", tc.doc, p.HasScore)
		}
	}
}
