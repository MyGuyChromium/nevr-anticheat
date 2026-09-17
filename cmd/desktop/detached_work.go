package main

import (
	"context"
	"sync"
)

// detachedWork runs expensive, idempotent, store-backed rebuilds (telemetry
// health of an older match, the investigation's telemetry pass) at most once
// at a time per key and independent of the request that asked first.
//
// The page gives up on a GET after 20 s. A rebuild that was cancelled with its
// request would be started from scratch by every retry and never finish for a
// full-length match; here the first request starts it, every request waits for
// the same run, and a retry after a timeout finds either the run still in
// progress or its persisted result. The work still stops at shutdown, and the
// request gate waits for it so the store is never closed underneath it.
type detachedWork struct {
	mu       sync.Mutex
	inflight map[string]*detachedCall
}

type detachedCall struct {
	done  chan struct{}
	value any
	err   error
}

func (s *server) runDetached(ctx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	d := &s.detached
	d.mu.Lock()
	if d.inflight == nil {
		d.inflight = make(map[string]*detachedCall)
	}
	call, running := d.inflight[key]
	if !running {
		call = &detachedCall{done: make(chan struct{})}
		d.inflight[key] = call
		workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		s.requests.work.Add(1)
		go func() {
			defer s.requests.work.Done()
			defer cancel()
			go func() {
				select {
				case <-s.quit:
					cancel()
				case <-workCtx.Done():
				}
			}()
			call.value, call.err = fn(workCtx)
			d.mu.Lock()
			delete(d.inflight, key)
			d.mu.Unlock()
			close(call.done)
		}()
	}
	d.mu.Unlock()
	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
