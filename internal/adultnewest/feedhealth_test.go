package adultnewest

import (
	"testing"
	"time"
)

// Claude 2026-08-11: updated all tests to pass protocol to Available/DirectGrabURL
// and replaced feedAvailabilityTTL references with TorrentReleaseTTL (the short
// window — a safe substitute for "definitely stale" checks, since any age beyond
// TorrentReleaseTTL is also beyond every shorter window). Added ProtocolTTL_*
// tests per plan §9.1 / A8.

func TestFeedHealth_Available_BrowseConfirmedAlwaysVisible(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()

	// A browse-confirmed row is visible unconditionally — no feed, an unhealthy
	// feed, a pruned feed, and a last-seen well beyond the TTL all still show it.
	// This is the THIS-revision BLOCKING-defect regression guard: a
	// browse-confirmed row must never be hidden by a feed going away.
	old := now.Add(-2 * TorrentReleaseTTL)
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
		if !fh.Available(true, "torrent", c.feedID, c.lastConfirmedSeen, now) {
			t.Errorf("%s: a browse-confirmed row must stay visible", c.name)
		}
	}
	// Even an explicitly-unhealthy feed can't hide a browse-confirmed row.
	fh.MarkUnhealthy(9)
	if !fh.Available(true, "torrent", 9, now, now) {
		t.Error("a browse-confirmed row must stay visible even when its feed is unhealthy")
	}
}

func TestFeedHealth_Available_FeedOnlyGatesOnFreshness(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()

	// Cold start: no health entry → feed-only row gates out.
	if fh.Available(false, "torrent", 7, now, now) {
		t.Error("a feed-only row must gate out when its feed has no health entry (cold start)")
	}

	// Healthy + within TTL (even when its key isn't in any current window) →
	// visible. This is the prior-BLOCKING-defect / RSS-window-rotation guard.
	fh.SetHealthy(7, now)
	rotatedButRecent := now.Add(-TorrentReleaseTTL / 2)
	if !fh.Available(false, "torrent", 7, rotatedButRecent, now) {
		t.Error("a feed-only row on a healthy feed within TTL must be visible even if rotated off the window")
	}

	// Healthy but beyond TTL → gates out.
	beyondTTL := now.Add(-2 * TorrentReleaseTTL)
	if fh.Available(false, "torrent", 7, beyondTTL, now) {
		t.Error("a feed-only row beyond the TTL must gate out")
	}

	// Marked unhealthy → gates out regardless of last-seen.
	fh.MarkUnhealthy(7)
	if fh.Available(false, "torrent", 7, now, now) {
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
	if fh.Available(false, "torrent", 2, now, now) {
		t.Error("a feed-only row on a pruned feed must gate out")
	}
}

func TestFeedHealth_DirectGrabURL(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()
	const url = "http://feed/x.torrent"

	// Fresh feed → enclosure exposed.
	fh.SetHealthy(7, now)
	if got := fh.DirectGrabURL(url, "torrent", 7, now, now); got != url {
		t.Errorf("expected the enclosure exposed on a fresh feed, got %q", got)
	}

	// Unhealthy feed → empty (Prowlarr fallback) even though the URL is stored.
	fh.MarkUnhealthy(7)
	if got := fh.DirectGrabURL(url, "torrent", 7, now, now); got != "" {
		t.Errorf("expected empty enclosure on an unhealthy feed, got %q", got)
	}

	// Beyond TTL → empty even when healthy.
	fh.SetHealthy(7, now)
	if got := fh.DirectGrabURL(url, "torrent", 7, now.Add(-2*TorrentReleaseTTL), now); got != "" {
		t.Errorf("expected empty enclosure beyond TTL, got %q", got)
	}

	// A browse-only row (no enclosure) → always empty.
	if got := fh.DirectGrabURL("", "torrent", 0, time.Time{}, now); got != "" {
		t.Errorf("expected empty enclosure for a browse-only row, got %q", got)
	}
}

func TestFeedHealth_SurvivesRestartViaPersistedLastSeen(t *testing.T) {
	// The TTL clock is the row's persisted last_confirmed_seen (passed in), NOT
	// held in the holder — so a "restart" (a brand-new empty holder) re-gates a
	// feed-only row only until its feed is re-marked healthy, after which the
	// still-recent persisted last-seen makes it fresh again.
	now := time.Now()
	persistedLastSeen := now.Add(-TorrentReleaseTTL / 2) // recent, within TTL

	fresh := NewFeedHealth() // simulates a cold start after restart
	if fresh.Available(false, "torrent", 7, persistedLastSeen, now) {
		t.Error("cold start: a feed-only row must gate out until the feed is re-polled")
	}
	fresh.SetHealthy(7, now) // the boot poll re-establishes health
	if !fresh.Available(false, "torrent", 7, persistedLastSeen, now) {
		t.Error("after the boot poll, the persisted last-seen within TTL must make the row visible again")
	}
}

// TestFeedHealth_ProtocolTTL_NZBLongWindow: a usenet row that is 60 days old —
// well beyond TorrentReleaseTTL (7d) — must remain fresh because NZBReleaseTTL
// is 2yr. A torrent row of the same age must gate out.
func TestFeedHealth_ProtocolTTL_NZBLongWindow(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()
	fh.SetHealthy(1, now)

	sixtyDaysAgo := now.Add(-60 * 24 * time.Hour)

	// NZB (usenet) — 60d is within the 2yr NZBReleaseTTL.
	if !fh.Available(false, "usenet", 1, sixtyDaysAgo, now) {
		t.Error("usenet: a row 60d old must still be available (NZBReleaseTTL is 2yr)")
	}
	const nzbURL = "http://index/x.nzb"
	if got := fh.DirectGrabURL(nzbURL, "usenet", 1, sixtyDaysAgo, now); got != nzbURL {
		t.Errorf("usenet: DirectGrabURL must expose a 60d-old row's enclosure, got %q", got)
	}

	// Torrent — 60d is well beyond TorrentReleaseTTL (7d) → gate out.
	if fh.Available(false, "torrent", 1, sixtyDaysAgo, now) {
		t.Error("torrent: a row 60d old must gate out (TorrentReleaseTTL is 7d)")
	}
	if got := fh.DirectGrabURL("http://feed/x.torrent", "torrent", 1, sixtyDaysAgo, now); got != "" {
		t.Errorf("torrent: DirectGrabURL must return empty for a 60d-old row, got %q", got)
	}
}

// TestFeedHealth_ProtocolTTL_TorrentSevenDay: a torrent row is fresh up to 7d
// and stale just beyond — confirming the A8 14d→7d narrowing.
func TestFeedHealth_ProtocolTTL_TorrentSevenDay(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()
	fh.SetHealthy(2, now)

	sixDaysAgo := now.Add(-6 * 24 * time.Hour)
	if !fh.Available(false, "torrent", 2, sixDaysAgo, now) {
		t.Error("torrent: a row 6d old must be within the 7d TorrentReleaseTTL")
	}

	eightDaysAgo := now.Add(-8 * 24 * time.Hour)
	if fh.Available(false, "torrent", 2, eightDaysAgo, now) {
		t.Error("torrent: a row 8d old must gate out (beyond TorrentReleaseTTL)")
	}
}

// TestFeedHealth_ProtocolTTL_Mixed: in a scenario where a feed carries both NZB
// and torrent rows, each gets its own independent TTL — an expired torrent does
// not affect an active NZB, and vice versa.
func TestFeedHealth_ProtocolTTL_Mixed(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()
	fh.SetHealthy(3, now)

	tenDaysAgo := now.Add(-10 * 24 * time.Hour)

	// 10d ago: torrent window (7d) expired → gate out.
	if fh.Available(false, "torrent", 3, tenDaysAgo, now) {
		t.Error("mixed: torrent row 10d old must gate out")
	}
	// 10d ago: NZB window (2yr) not expired → available.
	if !fh.Available(false, "usenet", 3, tenDaysAgo, now) {
		t.Error("mixed: usenet row 10d old must still be available")
	}
}

// TestFeedHealth_ProtocolTTL_UnknownProtocolFallsShort: an unrecognized or empty
// protocol receives the torrent (short) window — fail-short policy ensures an
// unknown row type is never trusted for two years.
func TestFeedHealth_ProtocolTTL_UnknownProtocolFallsShort(t *testing.T) {
	fh := NewFeedHealth()
	now := time.Now()
	fh.SetHealthy(4, now)

	tenDaysAgo := now.Add(-10 * 24 * time.Hour)

	// Empty protocol → short window → gate out at 10d.
	if fh.Available(false, "", 4, tenDaysAgo, now) {
		t.Error("unknown protocol: row 10d old must gate out (fail-short — unknown → TorrentReleaseTTL)")
	}
	// Unrecognized protocol → short window → gate out at 10d.
	if fh.Available(false, "carrier-pigeon", 4, tenDaysAgo, now) {
		t.Error("unrecognized protocol: row 10d old must gate out (fail-short)")
	}
}
