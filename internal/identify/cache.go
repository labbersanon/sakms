package identify

import "sync"

// resultCache memoizes a (box,title,studio)-keyed text-search lookup for the
// duration of a run.
//
// Every Get returns an independent copy of the cached value (or nil for a
// cached "no match"), never a shared pointer. Returning a shared mutable
// object would let one caller's mutation of the result (e.g. appending to
// Source) corrupt the cached value for every subsequent caller, including
// concurrent goroutines.
type resultCache struct {
	mu       sync.Mutex
	cache    map[string]*MatchResult // nil value = cached "no match"; missing key = not yet computed
	inflight map[string]*inflightCall
}

// inflightCall lets concurrent callers for the same key wait on a single
// compute() instead of each issuing their own redundant lookup against
// StashDB/FansDB/TPDB (which are separately rate-limited).
type inflightCall struct {
	done   chan struct{}
	result *MatchResult
	err    error
}

func newResultCache() *resultCache {
	return &resultCache{cache: make(map[string]*MatchResult), inflight: make(map[string]*inflightCall)}
}

// getOrCompute returns a cached result (as an independent copy) if key was
// already computed, otherwise calls compute, caches its result, and returns a
// copy of that. Errors are never cached — a transient failure shouldn't
// permanently poison the cache for that key. Concurrent callers for the same
// uncached key share a single in-flight compute() call.
func (c *resultCache) getOrCompute(key string, compute func() (*MatchResult, error)) (*MatchResult, error) {
	c.mu.Lock()
	if cached, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return copyResult(cached), nil
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		if call.err != nil {
			return nil, call.err
		}
		return copyResult(call.result), nil
	}
	call := &inflightCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	result, err := compute()

	c.mu.Lock()
	delete(c.inflight, key)
	if err == nil {
		c.cache[key] = result
	}
	c.mu.Unlock()

	call.result, call.err = result, err
	close(call.done)

	if err != nil {
		return nil, err
	}
	return copyResult(result), nil
}

func copyResult(r *MatchResult) *MatchResult {
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}
