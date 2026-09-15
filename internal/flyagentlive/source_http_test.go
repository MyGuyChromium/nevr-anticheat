package flyagentlive

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoopbackSessionSourceCapturesExactPrivateEvidence(t *testing.T) {
	body := `{"sessionid":"PRIVATE","match_type":"Echo_Arena","private_match":true,"client_name":"Fly","game_status":"playing"}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/session" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		_, _ = writer.Write([]byte(body))
	}))
	defer server.Close()
	source, err := NewLoopbackSessionSource(server.URL+"/session", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer source.CloseIdleConnections()
	sample, err := source.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sample.Session.SessionID != "PRIVATE" || !sample.Session.PrivateMatch ||
		!sample.Evidence.SessionIDPresent || !sample.Evidence.MatchTypePresent ||
		!sample.Evidence.PrivateMatchPresent || !sample.Evidence.ClientNamePresent ||
		!sample.Evidence.GameStatusPresent ||
		len(sample.SnapshotSHA256) != 64 || sample.ReceivedAt.IsZero() {
		t.Fatalf("sample = %+v", sample)
	}
}

func TestLoopbackSessionSourceRejectsRemoteAndRedirects(t *testing.T) {
	for _, endpoint := range []string{
		"http://192.0.2.1:6721/session",
		"https://127.0.0.1:6721/session",
		"http://localhost:6721/session",
		"http://127.0.0.1:6721/other",
		"http://127.0.0.1:6721/session?next=http://example.com",
		"http://user:secret@127.0.0.1:6721/session",
	} {
		if _, err := NewLoopbackSessionSource(endpoint, time.Second); err == nil {
			t.Errorf("unsafe endpoint accepted: %s", endpoint)
		}
	}

	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"sessionid":"PRIVATE","match_type":"Echo_Arena","private_match":true}`))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/session", http.StatusFound)
	}))
	defer redirect.Close()
	source, err := NewLoopbackSessionSource(redirect.URL+"/session", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer source.CloseIdleConnections()
	if _, err := source.Poll(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestLoopbackSessionSourceRejectsAmbiguousOrOversizedEvidence(t *testing.T) {
	for _, body := range []string{
		`{"sessionid":"PRIVATE","match_type":"Echo_Arena","private_match":true,"private_match":false}`,
		`{"sessionid":"PRIVATE","match_type":"Echo_Arena","private_match":true,"Private_Match":true}`,
		`{"sessionid":"PRIVATE","match_type":"Echo_Arena","private_match":true,"Client_Name":"Fly"}`,
		`{"sessionid":"PRIVATE","match_type":"Echo_Arena","private_match":true,"Game_Status":"playing"}`,
		`{"sessionid":"PRIVATE","match_type":"Echo_Arena","private_match":"true"}`,
		`{"sessionid":"PRIVATE","match_type":"Echo_Arena","private_match":true,"client_name":7}`,
		`{"sessionid":"PRIVATE","match_type":"Echo_Arena","private_match":true,"game_status":7}`,
		`[]`,
	} {
		if _, _, err := decodeSessionBody([]byte(body)); err == nil {
			t.Errorf("ambiguous body accepted: %s", body)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(writer, `{"padding":"`+strings.Repeat("x", MaxSessionResponseBytes)+`"}`)
	}))
	defer server.Close()
	source, err := NewLoopbackSessionSource(server.URL+"/session", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer source.CloseIdleConnections()
	if _, err := source.Poll(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response error = %v", err)
	}
}
