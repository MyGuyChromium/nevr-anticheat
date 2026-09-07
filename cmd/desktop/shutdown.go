package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

// requestGate stops admission before waiting so no new handler can race the
// store close, even when net/http is forced to close a stalled connection.
type requestGate struct {
	mu       sync.Mutex
	work     sync.WaitGroup
	stopping bool
	done     chan struct{}
}

func (g *requestGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		if g.stopping {
			g.mu.Unlock()
			writeError(w, http.StatusServiceUnavailable, "the app is shutting down; reopen it before starting more work")
			return
		}
		g.work.Add(1)
		g.mu.Unlock()
		defer g.work.Done()
		next.ServeHTTP(w, r)
	})
}

func (g *requestGate) stop() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done == nil {
		g.stopping = true
		g.done = make(chan struct{})
		go func() { g.work.Wait(); close(g.done) }()
	}
	return g.done
}

// shutdownDesktop reports whether every HTTP handler and background worker has
// stopped. If it times out, the caller must not explicitly close a store still
// in use. Process exit will release handles, and durable pending copies remain
// available to the existing recovery workflow on restart.
func shutdownDesktop(ctx context.Context, hs *http.Server, s *server, cancelRequests context.CancelFunc) (bool, error) {
	s.quitOnce.Do(func() { close(s.quit) })
	handlers := s.requests.stop()
	cancelRequests()
	s.cancelAnalysis()
	err := hs.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, hs.Close())
	}
	workers := s.runtime.stopped
	for handlers != nil || workers != nil {
		select {
		case <-handlers:
			handlers = nil
		case <-workers:
			workers = nil
		case <-ctx.Done():
			return false, errors.Join(err, ctx.Err())
		}
	}
	return true, err
}
