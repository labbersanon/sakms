package throttle

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWait_FirstCallDoesNotBlock(t *testing.T) {
	tr := New(200 * time.Millisecond)
	start := time.Now()
	if err := tr.Wait(context.Background(), "hostA"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("first call to a host should not wait, took %s", elapsed)
	}
}

func TestWait_SecondCallWaitsOutInterval(t *testing.T) {
	tr := New(150 * time.Millisecond)
	ctx := context.Background()
	_ = tr.Wait(ctx, "hostA")
	start := time.Now()
	_ = tr.Wait(ctx, "hostA")
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("second call should wait out most of the interval, only waited %s", elapsed)
	}
}

// Guards against sleeping while holding the lock, which would make host B's
// Wait() block on host A's sleep too, even though they're unrelated hosts.
func TestWait_DifferentHostsDoNotBlockEachOther(t *testing.T) {
	tr := New(300 * time.Millisecond)
	ctx := context.Background()
	_ = tr.Wait(ctx, "hostA") // primes hostA so its NEXT call must wait

	var wg sync.WaitGroup
	results := make(map[string]time.Duration)
	var mu sync.Mutex
	start := time.Now()

	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = tr.Wait(ctx, "hostA") // must wait ~300ms
		mu.Lock()
		results["hostA"] = time.Since(start)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		_ = tr.Wait(ctx, "hostB") // different host, must return almost immediately
		mu.Lock()
		results["hostB"] = time.Since(start)
		mu.Unlock()
	}()
	wg.Wait()

	if results["hostB"] > 100*time.Millisecond {
		t.Fatalf("hostB should not be blocked by hostA's wait, took %s", results["hostB"])
	}
	if results["hostA"] < 200*time.Millisecond {
		t.Fatalf("hostA should have waited out its interval, only took %s", results["hostA"])
	}
}

func TestWait_ContextCancellation(t *testing.T) {
	tr := New(time.Second)
	ctx := context.Background()
	_ = tr.Wait(ctx, "hostA") // primes a long wait for the next call

	cancelCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := tr.Wait(cancelCtx, "hostA")
	if err == nil {
		t.Fatal("expected an error when the context is cancelled mid-wait")
	}
}

func TestWait_StaggersSameHostCallsCorrectly(t *testing.T) {
	// Multiple concurrent callers for the SAME host should each get a
	// distinct, staggered slot rather than all waking at once.
	tr := New(50 * time.Millisecond)
	ctx := context.Background()
	const n = 5
	var wg sync.WaitGroup
	times := make([]time.Duration, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = tr.Wait(ctx, "hostA")
			times[idx] = time.Since(start)
		}(i)
	}
	wg.Wait()

	// The total spread across n calls at minInterval apart should be roughly
	// (n-1)*minInterval at minimum — confirms staggering, not everyone
	// piling up on the same wake time.
	var maxT time.Duration
	for _, tt := range times {
		if tt > maxT {
			maxT = tt
		}
	}
	if maxT < time.Duration(n-1)*40*time.Millisecond {
		t.Fatalf("expected calls to be staggered by ~minInterval each, max was only %s", maxT)
	}
}
