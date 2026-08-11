package adultnewest

import (
	"testing"
	"time"
)

func TestProtocolTTL_Windows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		protocol string
		want     time.Duration
	}{
		{"usenet", NZBReleaseTTL},
		{"torrent", TorrentReleaseTTL},
		// Unrecognized values fall to the short (torrent) window — fail-short.
		{"", TorrentReleaseTTL},
		{"nzb", TorrentReleaseTTL},
		{"USENET", TorrentReleaseTTL}, // case-sensitive match; uppercase is unrecognized
		{"garbage", TorrentReleaseTTL},
	} {
		got := ProtocolTTL(tc.protocol)
		if got != tc.want {
			t.Errorf("ProtocolTTL(%q) = %v, want %v", tc.protocol, got, tc.want)
		}
	}

	// Sanity: usenet window is much larger than torrent window.
	if NZBReleaseTTL <= TorrentReleaseTTL {
		t.Errorf("NZBReleaseTTL (%v) must be larger than TorrentReleaseTTL (%v)", NZBReleaseTTL, TorrentReleaseTTL)
	}
}

func TestReleaseFresh_Boundaries(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name     string
		protocol string
		at       time.Time
		want     bool
	}{
		{"usenet fresh at TTL-1s", "usenet", now.Add(-NZBReleaseTTL + time.Second), true},
		{"usenet stale at TTL", "usenet", now.Add(-NZBReleaseTTL), false},
		{"torrent fresh at TTL-1s", "torrent", now.Add(-TorrentReleaseTTL + time.Second), true},
		{"torrent stale at TTL", "torrent", now.Add(-TorrentReleaseTTL), false},
		{"usenet still fresh at 30d", "usenet", now.AddDate(0, 0, -30), true},
		{"torrent stale at 30d", "torrent", now.AddDate(0, 0, -30), false},
		// Unrecognized protocol gets the torrent window (fail-short).
		{"unrecognized stale at 30d", "", now.AddDate(0, 0, -30), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ReleaseFresh(tc.protocol, tc.at, now)
			if got != tc.want {
				t.Errorf("ReleaseFresh(%q, at=%v) = %v, want %v", tc.protocol, tc.at, got, tc.want)
			}
		})
	}
}
