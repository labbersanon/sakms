package main

import (
	"context"
	"sync"
)

// jobSem bounds concurrent hash jobs to MaxJobs. 0 means unlimited.
//
// Claude 2026-09-29: MaxJobs was stored and pushed but never applied.
// Reason: connect() spawned a goroutine per Job with no semaphore; ROADMAP
//
//	called this out as independent of the CPU cgroup cap.
//
// Troubleshooting: node MaxJobs=1 still ran many ffmpeg hashes at once.
// Review if: browse requests should share this cap (they do not).
type jobSem struct {
	mu      sync.Mutex
	running int
	waitCh  chan struct{}
}

func (s *jobSem) acquire(ctx context.Context, maxFn func() int) error {
	for {
		s.mu.Lock()
		max := maxFn()
		if max <= 0 || s.running < max {
			s.running++
			s.mu.Unlock()
			return nil
		}
		ch := s.waitCh
		if ch == nil {
			ch = make(chan struct{})
			s.waitCh = ch
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (s *jobSem) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running > 0 {
		s.running--
	}
	s.wakeLocked()
}

func (s *jobSem) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wakeLocked()
}

func (s *jobSem) wakeLocked() {
	if s.waitCh != nil {
		close(s.waitCh)
		s.waitCh = nil
	}
}
