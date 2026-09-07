package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestGateDrainsAdmittedWorkAndRefusesNewRequests(t *testing.T) {
	var g requestGate
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := g.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	go func() { h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)); close(returned) }()
	<-entered
	done := g.stop()
	if done != g.stop() {
		t.Fatal("stop is not idempotent")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatalf("new work accepted: %d", w.Code)
	}
	select {
	case <-done:
		t.Fatal("active handler forgotten")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("drain did not finish")
	}
	<-returned
}

func TestShutdownCancelsDetachedAnalysisAndRequestContextBeforeStoreClose(t *testing.T) {
	workers := make(chan struct{})
	s := &server{quit: make(chan struct{}), runtime: &desktopRuntime{stopped: workers}}
	analysis, _ := s.beginAnalysis()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, returned := make(chan struct{}), make(chan struct{})
	h := s.requests.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(returned) }))
	ts := httptest.NewUnstartedServer(h)
	ts.Config.BaseContext = func(net.Listener) context.Context { return base }
	ts.Start()
	defer ts.Close()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		if resp, err := http.Get(ts.URL); err == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	go func() { <-s.quit; close(workers) }()
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	drained, err := shutdownDesktop(ctx, ts.Config, s, cancel)
	if err != nil || !drained {
		t.Fatalf("shutdown: drained=%t error=%v", drained, err)
	}
	if analysis.Err() == nil {
		t.Fatal("detached analysis was not cancelled")
	}
	select {
	case <-returned:
	default:
		t.Fatal("handler still running")
	}
	late, _ := s.beginAnalysis()
	if late.Err() == nil {
		t.Fatal("late analysis accepted during shutdown")
	}
	<-clientDone
}

func TestShutdownTimeoutDoesNotAuthorizeClosingBusyStore(t *testing.T) {
	workers := make(chan struct{})
	s := &server{quit: make(chan struct{}), runtime: &desktopRuntime{stopped: workers}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	drained, err := shutdownDesktop(ctx, &http.Server{}, s, func() {})
	if drained || err == nil {
		t.Fatal("busy worker was considered safe to close")
	}
	close(workers)
}
