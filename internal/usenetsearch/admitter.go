package usenetsearch

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// Claude 2026-10-15: admitter — the Service-owned state admission.go's pure
// functions need across the per-pass Crawler rebuild.
// Reason: Service.runProbeAndCrawl builds a fresh *Crawler every pass
//   (service.go), so a cached PolicySnapshot or a per-release STAT verdict
//   stored on Crawler would never survive past one pass. admitter lives on
//   Service instead (SetAdmissionPolicy), and the Crawler for one pass just
//   borrows a pointer to it.
// Troubleshooting: a release re-STATed every pass despite an unchanged
//   verdict → check admitter.verdicts eviction (verdictCacheMax) or whether
//   SetAdmissionPolicy was called again (which replaces the whole admitter,
//   losing the cache — only Invalidate should be used for a prefs change).
// Related files: usenetsearch/admission.go (pure core), crawler.go (caller),
//   service.go (SetAdmissionPolicy/InvalidateAdmissionPolicy)

// statReleasesPerChunk bounds how many releases get a sampled STAT in one
// OVER chunk (≤3 msgids each, so ≤120 STATs — about 6s worst case added to
// a chunk at statPause=50ms spacing). Releases past the budget are indexed
// as stat=deferred; the liveness monitor checks them later.
// Review if: crawl catch-up lag vs. download throughput needs a different split.
const statReleasesPerChunk = 40

// verdictCacheMax caps the in-memory STAT-verdict LRU (a few MB at this
// size: group+release string keys plus a bool/string per entry).
// Review if: RSS growth on long uptime shows this is too large.
const verdictCacheMax = 100_000

// admissionPolicyRefresh is how often admitter.Snapshot re-fetches from
// AdmissionPolicy absent an explicit Invalidate (prefs PUT/DELETE).
// Review if: prefs change frequently enough that 5m polling feels stale.
const admissionPolicyRefresh = 5 * time.Minute

// admitter caches the admission PolicySnapshot and every per-release STAT
// verdict decided so far in this process. Safe for concurrent use (Crawler
// calls it from crawlGroup; Service may call Invalidate from an HTTP
// handler goroutine concurrently).
type admitter struct {
	mu     sync.Mutex
	policy AdmissionPolicy

	snapshot *PolicySnapshot
	snapAt   time.Time

	ll *list.List               // front = most recently used
	m  map[string]*list.Element // group\x00release -> element holding *verdictEntry
}

type verdictEntry struct {
	key   string
	keep  bool
	label string
}

func newAdmitter(policy AdmissionPolicy) *admitter {
	return &admitter{
		policy: policy,
		ll:     list.New(),
		m:      make(map[string]*list.Element),
	}
}

func verdictKey(group, release string) string {
	return group + "\x00" + release
}

// getVerdict returns a previously-cached STAT decision for group/release,
// promoting it to most-recently-used. ok is false on a cache miss — the
// caller must STAT (budget/busy permitting) rather than assume a verdict.
func (a *admitter) getVerdict(group, release string) (keep bool, label string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	el, found := a.m[verdictKey(group, release)]
	if !found {
		return false, "", false
	}
	a.ll.MoveToFront(el)
	ent := el.Value.(*verdictEntry)
	return ent.keep, ent.label, true
}

// putVerdict records a freshly-decided STAT verdict, evicting the
// least-recently-used entry once verdictCacheMax is exceeded.
func (a *admitter) putVerdict(group, release string, keep bool, label string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := verdictKey(group, release)
	if el, found := a.m[key]; found {
		ent := el.Value.(*verdictEntry)
		ent.keep, ent.label = keep, label
		a.ll.MoveToFront(el)
		return
	}
	el := a.ll.PushFront(&verdictEntry{key: key, keep: keep, label: label})
	a.m[key] = el
	if a.ll.Len() > verdictCacheMax {
		if back := a.ll.Back(); back != nil {
			a.ll.Remove(back)
			delete(a.m, back.Value.(*verdictEntry).key)
		}
	}
}

// Snapshot returns the cached PolicySnapshot, refreshing it from the
// underlying AdmissionPolicy when stale or absent. A refresh error with a
// still-usable stale snapshot logs nothing and serves the stale copy — a
// transient Settings/DB hiccup must not stall the whole crawl pass.
func (a *admitter) Snapshot(ctx context.Context) (*PolicySnapshot, error) {
	a.mu.Lock()
	policy := a.policy
	snap := a.snapshot
	stale := a.snapshot == nil || time.Since(a.snapAt) >= admissionPolicyRefresh
	a.mu.Unlock()
	if policy == nil {
		return nil, nil
	}
	if !stale {
		return snap, nil
	}
	fresh, err := policy.Snapshot(ctx)
	if err != nil {
		if snap != nil {
			return snap, nil
		}
		return nil, err
	}
	a.mu.Lock()
	a.snapshot = fresh
	a.snapAt = time.Now()
	a.mu.Unlock()
	return fresh, nil
}

// Invalidate forces the next Snapshot call to refresh immediately —
// called on a title/mode quality-prefs PUT or DELETE (Step 4).
func (a *admitter) Invalidate() {
	a.mu.Lock()
	a.snapshot = nil
	a.mu.Unlock()
}
