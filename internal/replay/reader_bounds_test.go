package replay

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// allocatedDuring reports the bytes allocated (not retained: allocated) while
// fn runs. It is process-wide, so budgets leave headroom for the runtime.
func allocatedDuring(fn func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

const legacyDoc = `{"header":{"MatchID":"bounds","Map":"arena","PlayerIDs":["a","b"],"Teams":{"a":"blue","b":"orange"}},
"frames":[
 {"Index":0,"Timestamp":1.5,"GamePhase":"playing","Players":[{"player_id":"a","position":[1,2,3],"blue_score":0,"orange_score":0},{"player_id":"b","left_hand_position":[4,5,6]}],"Disc":{"position":[1,1,1],"possessor_id":"a"}},
 {"Index":1,"Timestamp":1.6,"Players":[{"player_id":"a","ping_ms":0,"estimated_ping_ms":70}]}
]}`

// TestLegacyJSONLoadMatchesOpen keeps the file path covered now that the fuzz
// target feeds load from memory: both routes give the same header and frames
// and both enforce the byte limit.
func TestLegacyJSONLoadMatchesOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, []byte(legacyDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, fromMemory := NewJSONFrameParser(), NewJSONFrameParser()
	if err := fromFile.Open(path); err != nil {
		t.Fatal(err)
	}
	if err := fromMemory.load(strings.NewReader(legacyDoc)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromFile.header, fromMemory.header) || !reflect.DeepEqual(fromFile.frames, fromMemory.frames) {
		t.Fatalf("Open and load disagree:\n%+v\n%+v", fromFile.frames, fromMemory.frames)
	}
	if fromFile.header.MatchID != "bounds" || len(fromFile.frames) != 2 || len(fromFile.frames[0].Players) != 2 {
		t.Fatalf("decoded %+v / %d frames", fromFile.header, len(fromFile.frames))
	}
	first := fromFile.frames[0]
	if !first.Players[0].HasScore || first.Players[1].LeftHand != [3]float64{4, 5, 6} || first.Disc == nil || first.Disc.HolderID != "a" {
		t.Fatalf("aliases or score presence lost: %+v disc=%+v", first.Players, first.Disc)
	}
	if got := fromFile.frames[1].Players[0].PingMs; got != 0 {
		t.Fatalf("explicit canonical ping replaced by alias: %v", got)
	}

	for name, open := range map[string]func(*JSONFrameParser) error{
		"Open": func(p *JSONFrameParser) error { return p.Open(path) },
		"load": func(p *JSONFrameParser) error { return p.load(strings.NewReader(legacyDoc)) },
	} {
		p := NewJSONFrameParser()
		p.SetMaxBytes(8)
		if err := open(p); !errors.Is(err, ErrLegacyReplayTooLarge) {
			t.Errorf("%s at 8 bytes: %v, want ErrLegacyReplayTooLarge", name, err)
		}
		if p.header != nil || p.frames != nil {
			t.Errorf("%s kept data after failing", name)
		}
	}

	mc, frames, err := NewReplayReader(path, NewJSONFrameParser()).ReadMatch()
	if err != nil || mc.MatchID != "bounds" || len(frames) != 3 {
		t.Fatalf("ReadMatch: %v %+v %d frames", err, mc, len(frames))
	}
	if mc.ReplayFile != "legacy.json" {
		t.Fatalf("ReplayFile = %q; the uploader's local path must not be persisted", mc.ReplayFile)
	}
}

// TestLegacyJSONRejectsPlayerFlood: every "{}," used to become ~600 bytes of
// frames (a 16 MiB upload reached 9 GB of heap). A 300 KiB flood must be
// refused with the typed, readable error and within a fixed allocation budget.
func TestLegacyJSONRejectsPlayerFlood(t *testing.T) {
	doc := `{"frames":[{"Players":[` + strings.Repeat("{},", 100_000) + `{}]}]}`
	if len(doc) < 290<<10 || len(doc) > 310<<10 {
		t.Fatalf("input is %d bytes, want about 300 KiB", len(doc))
	}
	path := filepath.Join(t.TempDir(), "flood.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	var framesOut int
	allocated := allocatedDuring(func() {
		_, frames, readErr := NewReplayReader(path, NewJSONFrameParser()).ReadMatch()
		err, framesOut = readErr, len(frames)
	})
	if !errors.Is(err, ErrLegacyReplayTooManyPlayers) || framesOut != 0 {
		t.Fatalf("flood: err=%v frames=%d, want ErrLegacyReplayTooManyPlayers", err, framesOut)
	}
	for _, want := range []string{"too many players", "more than 64 players", "nothing was imported"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	// Before the limit this input produced 100,001 frames and allocated about
	// 400 MiB (measured: 425,368,120 bytes). The budget covers the
	// decoder's buffer for the 300 KiB frame value plus 64 decoded players.
	const budget = 8 << 20
	if allocated > budget {
		t.Fatalf("rejecting a %d-byte flood allocated %d bytes, budget %d", len(doc), allocated, budget)
	}
	t.Logf("rejected %d-byte flood with %d bytes allocated", len(doc), allocated)
}

func TestLegacyJSONRejectsFrameFloods(t *testing.T) {
	sixtyFour := `{"Players":[` + strings.TrimSuffix(strings.Repeat("{},", MaxLegacyReplayPlayersPerFrame), ",") + `]}`
	for name, doc := range map[string]string{
		"empty frames":      `{"frames":[` + strings.Repeat("{},", 100_000) + `{}]}`,
		"per-player frames": `{"frames":[` + strings.Repeat(sixtyFour+",", 400) + sixtyFour + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := NewJSONFrameParser()
			p.maxFrames = 1000
			var err error
			allocated := allocatedDuring(func() { err = p.load(strings.NewReader(doc)) })
			if !errors.Is(err, ErrLegacyReplayTooManyFrames) || p.frames != nil {
				t.Fatalf("err=%v kept=%d frames, want ErrLegacyReplayTooManyFrames", err, len(p.frames))
			}
			if !strings.Contains(err.Error(), "more than 1000") || !strings.Contains(err.Error(), "nothing was imported") {
				t.Errorf("error is not readable: %q", err)
			}
			if allocated > 8<<20 {
				t.Fatalf("allocated %d bytes rejecting %d bytes of input", allocated, len(doc))
			}
		})
	}

	// Exactly at the limits is accepted.
	p := NewJSONFrameParser()
	p.maxFrames = MaxLegacyReplayPlayersPerFrame
	if err := p.load(strings.NewReader(`{"frames":[` + sixtyFour + `]}`)); err != nil || len(p.frames[0].Players) != MaxLegacyReplayPlayersPerFrame {
		t.Fatalf("a frame at the player limit was refused: %v", err)
	}
	if NewJSONFrameParser().frameLimit() != MaxLegacyReplayFrames || (&JSONFrameParser{}).frameLimit() != MaxLegacyReplayFrames {
		t.Fatal("default frame limit is not MaxLegacyReplayFrames")
	}
}

func TestLegacyJSONRejectsHeaderRosterFlood(t *testing.T) {
	ids := `{"header":{"PlayerIDs":[` + strings.Repeat(`"",`, 50_000) + `""]},"frames":[]}`
	if err := NewJSONFrameParser().load(strings.NewReader(ids)); !errors.Is(err, ErrLegacyReplayTooManyPlayers) {
		t.Fatalf("header roster flood: %v", err)
	}
	var teams bytes.Buffer
	teams.WriteString(`{"header":{"Teams":{`)
	for i := 0; i <= MaxLegacyReplayPlayersPerFrame; i++ {
		if i > 0 {
			teams.WriteByte(',')
		}
		teams.WriteString(`"p` + strings.Repeat("x", i) + `":"blue"`)
	}
	teams.WriteString(`}},"frames":[]}`)
	if err := NewJSONFrameParser().load(&teams); !errors.Is(err, ErrLegacyReplayTooManyPlayers) {
		t.Fatalf("header team flood: %v", err)
	}
}

// The streaming decoder must keep what json.Unmarshal of the whole document
// did: case-insensitive keys in any order, unknown keys ignored, null
// tolerated, and anything that is not one complete JSON object refused.
func TestLegacyJSONDocumentSemantics(t *testing.T) {
	accepted := map[string]int{
		`{"frames":[{"Index":3,"Players":[{}]}],"extra":{"deep":[1,{"a":[]}]},"header":{"MatchID":"late"}}`: 1,
		`{"HEADER":{"matchid":"caps"},"Frames":[{"players":[{},{}]},null]}`:                                 2,
		`{"header":null,"frames":null}`: 0,
		`{}`:                            0,
		`null`:                          0,
		" \n{\"frames\":[]}\n ":         0,
	}
	for doc, wantFrames := range accepted {
		p := NewJSONFrameParser()
		if err := p.load(strings.NewReader(doc)); err != nil {
			t.Errorf("%s: %v", doc, err)
			continue
		}
		if h, err := p.Header(); err != nil || h == nil || len(p.frames) != wantFrames {
			t.Errorf("%s: header=%v err=%v frames=%d want %d", doc, h, err, len(p.frames), wantFrames)
		}
	}
	p := NewJSONFrameParser()
	if err := p.load(strings.NewReader(`{"frames":[{"Players":[{}]}],"header":{"MatchID":"late"}}`)); err != nil || p.header.MatchID != "late" {
		t.Fatalf("header after frames: %v %+v", err, p.header)
	}
	for _, doc := range []string{
		``, `[]`, `7`, `"x"`, `{"frames":{}}`, `{"frames":[{"Players":{}}]}`, `{"frames":[]} trailing`, `{"frames":[]}{}`,
		`{"frames":[`, `{"frames":[{"Players":[{"position":"north"}]}]}`, `{"header":5}`, `{"frames":[{"Index":"zero"}]}`,
	} {
		p := NewJSONFrameParser()
		if err := p.load(strings.NewReader(doc)); err == nil {
			t.Errorf("%q accepted", doc)
		}
		if p.header != nil || p.frames != nil {
			t.Errorf("%q: data kept after a failed load", doc)
		}
	}
}

// The single-pass player decode must agree with the three-pass rule it
// replaced: an alias applies only when the canonical key is absent.
func TestLegacyPlayerSinglePassDecode(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want RawPlayerFrame
	}{
		{`{"player_id":"p","left_hand":[1,2,3],"left_hand_position":[9,9,9]}`, RawPlayerFrame{PlayerID: "p", LeftHand: [3]float64{1, 2, 3}}},
		{`{"left_hand_position":[9,8,7],"right_hand_position":[1,1,1],"left_hand_rotation":[0,0,0,1],"right_hand_rotation":[1,0,0,0],"estimated_ping_ms":55}`,
			RawPlayerFrame{LeftHand: [3]float64{9, 8, 7}, RightHand: [3]float64{1, 1, 1}, LeftHandRot: [4]float64{0, 0, 0, 1}, RightHandRot: [4]float64{1, 0, 0, 0}, PingMs: 55}},
		{`{"left_hand":null,"left_hand_position":[9,9,9],"left_hand_rotation":null}`, RawPlayerFrame{}},
		{`{"left_hand":[1,2,3],"left_hand":null}`, RawPlayerFrame{LeftHand: [3]float64{1, 2, 3}}},
		{`{"blue_score":2,"orange_score":0,"goals":1,"stuns":4,"is_stunned":true}`, RawPlayerFrame{BlueScore: 2, HasScore: true, Goals: 1, Stuns: 4, IsStunned: true}},
		{`{"blue_score":2,"orange_score":1,"orange_score":null}`, RawPlayerFrame{BlueScore: 2, OrangeScore: 1}},
	} {
		var got RawPlayerFrame
		if err := json.Unmarshal([]byte(tc.doc), &got); err != nil {
			t.Fatalf("%s: %v", tc.doc, err)
		}
		if got != tc.want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.doc, got, tc.want)
		}
	}
	for _, doc := range []string{`{"left_hand":"x"}`, `{"left_hand_position":"x"}`, `{"ping_ms":[]}`, `[]`} {
		var got RawPlayerFrame
		if err := json.Unmarshal([]byte(doc), &got); err == nil {
			t.Errorf("%s accepted", doc)
		}
	}
	var disc RawDiscFrame
	if err := json.Unmarshal([]byte(`{"velocity":[1,2,3],"possessor_id":"holder"}`), &disc); err != nil || disc.HolderID != "holder" || disc.Velocity != [3]float64{1, 2, 3} {
		t.Fatalf("disc alias: %v %+v", err, disc)
	}
}
