package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobSem_UnlimitedNeverBlocks(t *testing.T) {
	var s jobSem
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		if err := s.acquire(ctx, func() int { return 0 }); err != nil {
			t.Fatalf("acquire: %v", err)
		}
	}
	if s.running != 8 {
		t.Fatalf("running = %d, want 8", s.running)
	}
}

func TestJobSem_MaxOneSerializes(t *testing.T) {
	var s jobSem
	ctx := context.Background()
	if err := s.acquire(ctx, func() int { return 1 }); err != nil {
		t.Fatal(err)
	}

	var started atomic.Bool
	done := make(chan struct{})
	go func() {
		if err := s.acquire(ctx, func() int { return 1 }); err != nil {
			t.Errorf("second acquire: %v", err)
		}
		started.Store(true)
		s.release()
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	if started.Load() {
		t.Fatal("second job started while MaxJobs=1 slot was held")
	}
	s.release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second job did not proceed after release")
	}
}

func TestJobSem_NotifyUnblocksWhenCapRaised(t *testing.T) {
	var s jobSem
	ctx := context.Background()
	max := 1
	if err := s.acquire(ctx, func() int { return max }); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.acquire(ctx, func() int { return max }); err != nil {
			t.Errorf("acquire after raise: %v", err)
		}
		s.release()
	}()

	time.Sleep(20 * time.Millisecond)
	max = 2
	s.notify()
	wg.Wait()
	s.release()
}

func TestJobSem_AcquireRespectsCancel(t *testing.T) {
	var s jobSem
	if err := s.acquire(context.Background(), func() int { return 1 }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.acquire(ctx, func() int { return 1 }) }()
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("want canceled acquire")
		}
	case <-time.After(time.Second):
		t.Fatal("acquire did not return after cancel")
	}
	s.release()
}
