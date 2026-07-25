package adultnewest

import (
	"testing"
	"time"
)

func TestFeedHealth_Available_BrowseConfirmedAlwaysVisible(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()

	// A browse-confirmed row is visible unconditionally — no feed, an unhealthy
	// feed, a pruned feed, and a last-seen well beyond the TTL all still show it.
	// This is the THIS-revision BLOCKING-defect regression guard: a
	// browse-confirmed row must never be hidden by a feed going away.
	old := now.Add(-2 * feedAvailabilityTTL)
	cases := []struct {
		name              string
		feedID            int64
		lastConfirmedSeen time.Time
	}{
		{"no feed", 0, time.Time{}},
		{"feed with no health entry", 9, now},
		{"feed beyond TTL", 9, old},
	}
	for _, c := range cases {
		if !fh.Available(true, c.feedID, c.lastConfirmedSeen, now) {
			t.Errorf("%s: a browse-confirmed row must stay visible", c.name)
		}
	}
	// Even an explicitly-unhealthy feed can't hide a browse-confirmed row.
	fh.MarkUnhealthy(9)
	if !fh.Available(true, 9, now, now) {
		t.Error("a browse-confirmed row must stay visible even when its feed is unhealthy")
	}
}

func TestFeedHealth_Available_FeedOnlyGatesOnFreshness(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()

	// Cold start: no health entry → feed-only row gates out.
	if fh.Available(false, 7, now, now) {
		t.Error("a feed-only row must gate out when its feed has no health entry (cold start)")
	}

	// Healthy + within TTL (even when its key isn't in any current window) →
	// visible. This is the prior-BLOCKING-defect / RSS-window-rotation guard.
	fh.SetHealthy(7, now)
	rotatedButRecent := now.Add(-feedAvailabilityTTL / 2)
	if !fh.Available(false, 7, rotatedButRecent, now) {
		t.Error("a feed-only row on a healthy feed within TTL must be visible even if rotated off the window")
	}

	// Healthy but beyond TTL → gates out.
	beyondTTL := now.Add(-2 * feedAvailabilityTTL)
	if fh.Available(false, 7, beyondTTL, now) {
		t.Error("a feed-only row beyond the TTL must gate out")
	}

	// Marked unhealthy → gates out regardless of last-seen.
	fh.MarkUnhealthy(7)
	if fh.Available(false, 7, now, now) {
		t.Error("a feed-only row on an unhealthy feed must gate out")
	}
}

func TestFeedHealth_PruneToEnabled_DropsRemovedFeeds(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()
	fh.SetHealthy(1, now)
	fh.SetHealthy(2, now)

	// Feed 2 is no longer enabled → pruned → reads unhealthy → its feed-only
	// rows gate out; feed 1 survives.
	fh.PruneToEnabled([]int64{1})
	if !fh.Healthy(1) {
		t.Error("feed 1 should remain healthy after prune")
	}
	if fh.Healthy(2) {
		t.Error("feed 2 should be pruned (unhealthy) after it left the enabled set")
	}
	if fh.Available(false, 2, now, now) {
		t.Error("a feed-only row on a pruned feed must gate out")
	}
}

func TestFeedHealth_DirectGrabURL(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()
	const url = "http://feed/x.torrent"

	// Fresh feed → enclosure exposed.
	fh.SetHealthy(7, now)
	if got := fh.DirectGrabURL(url, 7, now, now); got != url {
		t.Errorf("expected the enclosure exposed on a fresh feed, got %q", got)
	}

	// Unhealthy feed → empty (Prowlarr fallback) even though the URL is stored.
	fh.MarkUnhealthy(7)
	if got := fh.DirectGrabURL(url, 7, now, now); got != "" {
		t.Errorf("expected empty enclosure on an unhealthy feed, got %q", got)
	}

	// Beyond TTL → empty even when healthy.
	fh.SetHealthy(7, now)
	if got := fh.DirectGrabURL(url, 7, now.Add(-2*feedAvailabilityTTL), now); got != "" {
		t.Errorf("expected empty enclosure beyond TTL, got %q", got)
	}

	// A browse-only row (no enclosure) → always empty.
	if got := fh.DirectGrabURL("", 0, time.Time{}, now); got != "" {
		t.Errorf("expected empty enclosure for a browse-only row, got %q", got)
	}
}

func TestFeedHealth_SurvivesRestartViaPersistedLastSeen(t *testing.T) {
	// The TTL clock is the row's persisted last_confirmed_seen (passed in), NOT
	// held in the holder — so a "restart" (a brand-new empty holder) re-gates a
	// feed-only row only until its feed is re-marked healthy, after which the
	// still-recent persisted last-seen makes it fresh again.
	now := time.Now()
	persistedLastSeen := now.Add(-feedAvailabilityTTL / 2) // recent, within TTL

	fresh := NewFeedHealth() // simulates a cold start after restart
	if fresh.Available(false, 7, persistedLastSeen, now) {
		t.Error("cold start: a feed-only row must gate out until the feed is re-polled")
	}
	fresh.SetHealthy(7, now) // the boot poll re-establishes health
	if !fresh.Available(false, 7, persistedLastSeen, now) {
		t.Error("after the boot poll, the persisted last-seen within TTL must make the row visible again")
	}
}
