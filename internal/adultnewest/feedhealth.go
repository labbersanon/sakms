package adultnewest

import (
	"sync"
	"time"
)

// feedAvailabilityTTL is how long a feed-sourced cache row stays "fresh" after
// the last successful poll that confirmed its enclosure present. It is the
// absence-based safety net, deliberately sized between two other clocks:
//
//   - ≫ the feed poll interval (default 30 min, D7) so a continuously-present
//     item — whose last_confirmed_seen is refreshed every poll — never
//     accidentally TTL-expires; and
//   - ≪ PurgeStale's 6-month cutoff so a genuinely-gone feed-only item (dropped
//     from every poll) disappears from Discover long before it is purged.
//
// 14 days (≈672× the interval, ≈13× shorter than PurgeStale) gives an item that
// rotates off a feed's recent-N RSS window a two-week grace period in which its
// still-valid enclosure remains direct-grabbable — matching the observed reality
// that indexer download retention far outlives the RSS listing window. A runtime
// settings key is a trivial later upgrade; a constant suffices now.
const feedAvailabilityTTL = 14 * 24 * time.Hour

// feedHealthEntry is one feed's in-memory liveness. lastPollAt exists for
// observability/debugging; freshness is gated on healthy + the row's persisted
// last_confirmed_seen, not on lastPollAt.
type feedHealthEntry struct {
	healthy    bool
	lastPollAt time.Time
}

// FeedHealth is the concurrency-safe holder for per-feed liveness that gates
// Adult Discover's feed-sourced availability. The feed pass (scan.go's
// runFeedCycle) is the SOLE writer (SetHealthy/MarkUnhealthy/PruneToEnabled);
// the read handlers are readers (Available/DirectGrabURL). It is constructed
// once in cmd/sakms/main.go and injected into both.
//
// The last-seen TTL clock itself is NOT held here — it is the persisted
// adult_newest_releases.last_confirmed_seen column, so it survives restarts.
// This holder only tracks whether each feed's most recent poll succeeded.
type FeedHealth struct {
	mu      sync.RWMutex
	entries map[int64]feedHealthEntry
}

// NewFeedHealth builds an empty holder — every feed reads as unhealthy until its
// first successful poll (fail-closed cold start).
func NewFeedHealth() *FeedHealth {
	return &FeedHealth{entries: make(map[int64]feedHealthEntry)}
}

// SetHealthy records a successful poll of feedID at now.
func (h *FeedHealth) SetHealthy(feedID int64, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries[feedID] = feedHealthEntry{healthy: true, lastPollAt: now}
}

// MarkUnhealthy records a failed poll of feedID — its enclosures stop being
// exposed for direct-grab, and its feed-only rows gate out, until a later poll
// succeeds. The entry is kept (not deleted) so lastPollAt survives for
// debugging; only PruneToEnabled removes entries.
func (h *FeedHealth) MarkUnhealthy(feedID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entries[feedID]
	e.healthy = false
	h.entries[feedID] = e
}

// PruneToEnabled deletes every entry whose feedID is not in the current
// enabled-adult-feed set, so a feed disabled or deleted since the last cycle
// stops being reported healthy (a missing entry reads as unhealthy via
// Healthy). Detection lag is ≤ one poll interval.
func (h *FeedHealth) PruneToEnabled(enabledIDs []int64) {
	keep := make(map[int64]bool, len(enabledIDs))
	for _, id := range enabledIDs {
		keep[id] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range h.entries {
		if !keep[id] {
			delete(h.entries, id)
		}
	}
}

// Healthy reports whether feedID's most recent poll succeeded. It returns false
// for any feedID absent from the map — a single rule that covers cold start
// (empty map), a pruned disabled/deleted feed, and a feed marked unhealthy, so
// all three fail closed through one code path.
func (h *FeedHealth) Healthy(feedID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	e, ok := h.entries[feedID]
	return ok && e.healthy
}

// feedFresh is the single freshness predicate backing BOTH the visibility gate
// and the direct-grab exposure decision, so they can never drift apart: a feed
// source is fresh iff it exists (feedID != 0), its feed's last poll succeeded,
// and it was confirmed present within feedAvailabilityTTL.
func (h *FeedHealth) feedFresh(feedID int64, lastConfirmedSeen, now time.Time) bool {
	return feedID != 0 && h.Healthy(feedID) && now.Sub(lastConfirmedSeen) < feedAvailabilityTTL
}

// Available is the request-time VISIBILITY gate every adult_newest_releases read
// path applies: a row is shown iff the browse pass currently confirms its entity
// OR its feed source is fresh. A browse-confirmed row is therefore shown
// unconditionally — independent of every feed's health, disablement, or deletion
// — which is what makes "augment = zero regression" literally true: deleting or
// downing a feed can only ever drop a both-sourced row back to Prowlarr-grabbable
// (see DirectGrabURL), never hide it.
func (h *FeedHealth) Available(browseConfirmed bool, feedID int64, lastConfirmedSeen, now time.Time) bool {
	return browseConfirmed || h.feedFresh(feedID, lastConfirmedSeen, now)
}

// DirectGrabURL is the request-time GRAB-PATH exposure decision every
// card-building path applies: it returns downloadURL iff the enclosure exists
// and its feed source is currently fresh, else "". A card's DTO downloadUrl is
// populated ONLY from this helper, never from the raw row.download_url — so a
// both-sourced row whose feed is unhealthy/pruned/beyond-TTL is still shown (via
// browse_confirmed) but carries an empty enclosure, falling back to the
// pre-existing Prowlarr GrabDialog path rather than offering a stale enclosure.
// The enclosure is kept stored on the row throughout, so recovery (a later
// successful poll) re-exposes it for free.
func (h *FeedHealth) DirectGrabURL(downloadURL string, feedID int64, lastConfirmedSeen, now time.Time) string {
	if downloadURL != "" && h.feedFresh(feedID, lastConfirmedSeen, now) {
		return downloadURL
	}
	return ""
}
