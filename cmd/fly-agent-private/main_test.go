package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRunRequiresExplicitPrivateSessionParameters(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), nil, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--expected-session-id") || stdout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunPrivateLoopbackDryRunStopsOnOperatorCancel(t *testing.T) {
	var polls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/session" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, privateSessionJSON(polls.Add(1)))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	stdout := &cancelOnActionWriter{cancel: cancel}
	var stderr bytes.Buffer
	code := run(ctx, []string{
		"--session-url=" + server.URL + "/session",
		"--expected-session-id=PRIVATE-SESSION",
		"--player=Fly",
		"--attack-goal=positive-z",
		"--session-limit=1s",
		"--poll-interval=10ms",
		"--stale-after=200ms",
		"--request-timeout=100ms",
		"--private-confirmations=2",
	}, stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q output=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"kind":"action"`) ||
		!strings.Contains(stdout.String(), `"private_match":true`) ||
		!strings.Contains(stderr.String(), "DRY RUN ONLY") ||
		!strings.Contains(stderr.String(), "stopped=operator_stop") {
		t.Fatalf("stderr=%q output=%q", stderr.String(), stdout.String())
	}
}

func TestRunRejectsNonLoopbackSessionURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--session-url=http://192.0.2.1:6721/session",
		"--expected-session-id=PRIVATE",
		"--player=Fly",
		"--attack-goal=positive-z",
		"--session-limit=1m",
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "loopback-ip") || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

type cancelOnActionWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelOnActionWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written, err := w.buffer.Write(payload)
	if bytes.Contains(payload, []byte(`"kind":"action"`)) {
		w.cancel()
	}
	return written, err
}

func (w *cancelOnActionWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func privateSessionJSON(sequence int64) string {
	return fmt.Sprintf(`{
"sessionid":"PRIVATE-SESSION","match_type":"Echo_Arena","map_name":"mpl_arena_a",
"game_status":"playing","game_clock":%d,"private_match":true,"client_name":"Fly",
"disc":{"position":[0,2,0],"velocity":[0,0,0]},
"teams":[
 {"team":"BLUE TEAM","players":[{"name":"Fly","userid":1,"playerid":0,
  "body":{"position":[1,1,-10],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},
  "head":{"position":[1,1,-10],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},
  "velocity":[0,0,0],"lhand":{"pos":[1,1,-10],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},
  "rhand":{"pos":[1,1,-10],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},
  "holding_left":"none","holding_right":"none"}]},
 {"team":"ORANGE TEAM","players":[{"name":"Opponent","userid":2,"playerid":1,
  "body":{"position":[-1,1,10],"forward":[0,0,-1],"left":[1,0,0],"up":[0,1,0]},
  "head":{"position":[-1,1,10],"forward":[0,0,-1],"left":[1,0,0],"up":[0,1,0]},
  "velocity":[0,0,0],"lhand":{"pos":[-1,1,10],"forward":[0,0,-1],"left":[1,0,0],"up":[0,1,0]},
  "rhand":{"pos":[-1,1,10],"forward":[0,0,-1],"left":[1,0,0],"up":[0,1,0]},
  "holding_left":"none","holding_right":"none"}]}
]}`, 300-sequence)
}
