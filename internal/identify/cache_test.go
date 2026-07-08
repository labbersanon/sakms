package identify

import (
	"sync"
	"testing"
)

// Mutating a result returned from one cache hit must never be visible to a
// subsequent cache hit for the same key.
func TestResultCache_MutatingReturnedValueDoesNotAffectCache(t *testing.T) {
	c := newResultCache()
	calls := 0
	compute := func() (*MatchResult, error) {
		calls++
		return &MatchResult{Title: "T", Studio: "S", Source: "stashdb_text"}, nil
	}

	r1, err := c.getOrCompute("key1", compute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r1.Source = "web+" + r1.Source // simulate a caller mutating the returned result
	r1.Date = "1999"

	r2, err := c.getOrCompute("key1", compute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r2.Source != "stashdb_text" {
		t.Fatalf("cache was poisoned by the previous mutation: r2.Source = %q, want \"stashdb_text\"", r2.Source)
	}
	if r2.Date != "" {
		t.Fatalf("cache was poisoned by the previous mutation: r2.Date = %q, want empty", r2.Date)
	}
	if r1 == r2 {
		t.Fatal("expected distinct pointers from separate cache hits, got the same object")
	}
	if calls != 1 {
		t.Fatalf("expected compute() to be called exactly once (second call should hit cache), got %d", calls)
	}

	// A third mutation, compounding. Must still not compound.
	r2.Source = "web+" + r2.Source
	r3, _ := c.getOrCompute("key1", compute)
	if r3.Source != "stashdb_text" {
		t.Fatalf("cache compounded across multiple mutations: r3.Source = %q", r3.Source)
	}
}

func TestResultCache_CachesNilAsNoMatch(t *testing.T) {
	c := newResultCache()
	calls := 0
	compute := func() (*MatchResult, error) {
		calls++
		return nil, nil
	}

	r1, err := c.getOrCompute("miss", compute)
	if err != nil || r1 != nil {
		t.Fatalf("expected nil, nil got r1=%v err=%v", r1, err)
	}
	r2, err := c.getOrCompute("miss", compute)
	if err != nil || r2 != nil {
		t.Fatalf("expected nil, nil got r2=%v err=%v", r2, err)
	}
	if calls != 1 {
		t.Fatalf("expected compute called once for a cached nil result, got %d calls", calls)
	}
}

func TestResultCache_DoesNotCacheErrors(t *testing.T) {
	c := newResultCache()
	calls := 0
	compute := func() (*MatchResult, error) {
		calls++
		if calls == 1 {
			return nil, assertErr
		}
		return &MatchResult{Title: "recovered"}, nil
	}

	_, err := c.getOrCompute("key", compute)
	if err == nil {
		t.Fatal("expected the first call's error to propagate")
	}
	r, err := c.getOrCompute("key", compute)
	if err != nil {
		t.Fatalf("unexpected error on retry: %v", err)
	}
	if r == nil || r.Title != "recovered" {
		t.Fatalf("expected the second (successful) compute to run and be returned, got %v", r)
	}
	if calls != 2 {
		t.Fatalf("expected compute called twice (error not cached), got %d", calls)
	}
}

var assertErr = &testError{"boom"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// Concurrency check: many goroutines hitting the same key simultaneously must
// never observe a torn/partial write, and mutations from one goroutine's copy
// must never leak into another's.
func TestResultCache_ConcurrentAccessIsSafe(t *testing.T) {
	c := newResultCache()
	compute := func() (*MatchResult, error) {
		return &MatchResult{Title: "T", Source: "stashdb_text"}, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.getOrCompute("shared-key", compute)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			r.Source = "mutated-by-this-goroutine" // must not affect other goroutines' copies
			if r.Source != "mutated-by-this-goroutine" {
				t.Errorf("mutation didn't even apply to the local copy")
			}
		}()
	}
	wg.Wait()

	final, _ := c.getOrCompute("shared-key", compute)
	if final.Source != "stashdb_text" {
		t.Fatalf("cache was corrupted by concurrent mutation: %q", final.Source)
	}
}
