package adultnewest

import "time"

// Claude 2026-08-11: per-protocol release freshness policy for the Adult release cache.
// Reason: replaces the flat 14-day feedAvailabilityTTL with differentiated windows —
// usenet retention outlives torrent seeder availability by orders of magnitude.
// Troubleshooting: an expired-looking NZB cache row is likely within PurgeStale's
// 6-month entity ceiling (adult_newest_releases), which is separate from this table's
// own TTL-based purge. See plan §0.5 and R-7.
// Review if: operator feedback shows torrent seeders evaporate faster or slower than 1w.

const (
	// NZBReleaseTTL is the freshness window for usenet (NZB) releases.
	// Usenet retention outlives indexer listings by years — a 2-year window ensures
	// a cached NZB stays fresh for the full practical life of the article.
	NZBReleaseTTL = 2 * 365 * 24 * time.Hour

	// TorrentReleaseTTL is the freshness window for torrent releases.
	// Seeders evaporate quickly; a weekly re-verify ensures stale peers aren't
	// dispatched. 14d→7d narrowing is intentional (A8: torrent narrowing).
	TorrentReleaseTTL = 7 * 24 * time.Hour
)

// ProtocolTTL maps a Prowlarr protocol string onto its freshness window.
// An unrecognized or empty protocol gets the SHORT (torrent) window deliberately —
// fail-short, never fail-long: an unknown row must not be trusted for two years.
func ProtocolTTL(protocol string) time.Duration {
	switch protocol {
	case "usenet":
		return NZBReleaseTTL
	default:
		return TorrentReleaseTTL
	}
}

// ReleaseFresh reports whether a release persisted at time at is still within its
// protocol-specific freshness window as of now. This is the single freshness predicate
// for BOTH the persisted-release cache and the RSS feed mechanism, so the two can
// never drift apart.
func ReleaseFresh(protocol string, at, now time.Time) bool {
	return now.Sub(at) < ProtocolTTL(protocol)
}
