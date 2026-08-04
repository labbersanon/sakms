package pruning

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Subject is the mode-agnostic shape Match/MatchAny reason about. Each of
// Purge's three Scan paths projects its own library row into one of these
// (Series aggregates across its episodes; Movies/Adult read Item/Scene
// fields directly), which keeps this engine free of any library-type
// import fan-out.
type Subject struct {
	CreatedAt   string // RFC3339 from the library row; "" if never captured.
	SizeBytes   int64  // 0 == not captured (backfill has not reached it).
	QualityTier string // "" == not captured; "unknown" == backfill could not infer.
}

// tierRank orders the four real quality tiers low -> lossless for the
// "floor" comparison. The two sentinel values ("" = never captured, the
// literal "unknown" = backfill could not infer) are DELIBERATELY absent: a
// rank lookup miss means the tier condition does not match. This is the
// fail-closed direction — see matchesTier's doc for why the other direction
// would be a data-loss bug.
var tierRank = map[string]int{"low": 0, "medium": 1, "high": 2, "lossless": 3}

// Match reports whether subj satisfies EVERY configured condition on r
// (AND-within-rule), and returns the human-readable Reason fragment for a
// match. A rule with no configured conditions can never reach here in
// practice (the store rejects it at Create/Update), but Match returns
// false for one anyway rather than vacuously matching everything.
func Match(r Rule, subj Subject, now time.Time) (bool, string) {
	hasAge := r.AgeDays != 0
	hasSize := r.SizeBytes != 0
	hasTier := r.QualityTierFloor != ""
	if !hasAge && !hasSize && !hasTier {
		return false, ""
	}

	var parts []string

	if hasAge {
		ageDays, ok := matchesAge(subj.CreatedAt, r.AgeDays, now)
		if !ok {
			return false, ""
		}
		parts = append(parts, fmt.Sprintf("%d days old", ageDays))
	}

	if hasSize {
		// subj.SizeBytes == 0 means "not captured yet," but this is safe by
		// construction: the comparison is >=, so an uncaptured 0 can only
		// satisfy a rule whose SizeBytes is also 0 — and 0 is that field's
		// own "not configured" sentinel, so hasSize would be false and this
		// branch would never run. Do not flip this comparison to <= without
		// re-checking that invariant.
		if subj.SizeBytes < r.SizeBytes {
			return false, ""
		}
		parts = append(parts, humanBytes(subj.SizeBytes))
	}

	if hasTier {
		if !matchesTier(subj.QualityTier, r.QualityTierFloor) {
			return false, ""
		}
		parts = append(parts, fmt.Sprintf("tier: %s", subj.QualityTier))
	}

	return true, fmt.Sprintf("Matched rule '%s': %s", r.Name, strings.Join(parts, ", "))
}

// MatchAny evaluates rules against subj with OR semantics and returns the
// Reason fragment for EVERY rule that matched, in rules order. An empty
// (nil) result means no rule matched. Callers pass only enabled rules for
// the item's own mode (Store.ListEnabledForMode).
func MatchAny(rules []Rule, subj Subject, now time.Time) []string {
	var reasons []string
	for _, r := range rules {
		if ok, reason := Match(r, subj, now); ok {
			reasons = append(reasons, reason)
		}
	}
	return reasons
}

// Claude 2026-08-03: added BestTier for Purge's Series aggregation (B4, plan
// §2.4).
// Reason: a Series row has no quality_tier of its own, so ScanLibrarySeries
// must aggregate one from its episodes — and the "best tier wins" rule needs
// the same ladder Match uses. Exporting this one helper keeps tierRank the
// single source of truth instead of internal/purge growing a second copy that
// could drift.
// Review if: library_series ever gains its own quality_tier column, at which
// point the aggregation (and this helper's only caller) disappears.

// BestTier returns the highest-ranked real quality tier among tiers, or "" if
// none of them is one. The ""/"unknown" sentinels are skipped rather than
// ranked (they are absent from tierRank on purpose — see matchesTier), so a
// set containing only sentinels aggregates to "", which can never satisfy a
// tier condition. BEST, not worst, is deliberate for Purge's Series path: one
// high-quality episode protects the whole show from a bulk delete (plan §2.4).
func BestTier(tiers []string) string {
	best := ""
	bestRank := -1
	for _, t := range tiers {
		rank, ok := tierRank[t]
		if !ok {
			continue
		}
		if rank > bestRank {
			best, bestRank = t, rank
		}
	}
	return best
}

// matchesAge reports whether createdAt is at least thresholdDays old as of
// now, along with the actual computed age in days for the Reason string.
//
// An empty or unparseable createdAt means the age condition does NOT
// match — same fail-closed direction as matchesTier's sentinel handling.
// created_at is written by SQLite as sakms_now(),
// which time.RFC3339 parses fine (Go's time.Parse accepts the fractional
// seconds even though the RFC3339 layout constant doesn't spell them out).
func matchesAge(createdAt string, thresholdDays int, now time.Time) (int, bool) {
	if createdAt == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return 0, false
	}
	ageDays := int(now.Sub(t) / (24 * time.Hour))
	if ageDays < thresholdDays {
		return ageDays, false
	}
	return ageDays, true
}

// matchesTier reports whether subjectTier is at or below floorTier on the
// quality ladder ("low or below" is more prune-worthy).
//
// subjectTier's two non-tier sentinel values in live data — "" (row
// predates the size/tier backfill) and library.TierUnknown ("unknown",
// backfill ran but could not infer a tier, an accepted permanent state) —
// are not present in tierRank, so the lookup misses and this returns
// false. If it instead treated a lookup miss as "satisfies the broadest
// possible floor," every un-backfilled or un-inferrable row in the library
// would match a "lossless or below" rule and get staged for deletion. Do
// not change this direction; it is pinned by dedicated tests.
func matchesTier(subjectTier, floorTier string) bool {
	subjRank, ok := tierRank[subjectTier]
	if !ok {
		return false
	}
	floorRank, ok := tierRank[floorTier]
	if !ok {
		return false
	}
	return subjRank <= floorRank
}

// humanBytes renders n as a compact operator-facing size (the "8.2GB" in a
// matched rule's Reason). 1024-based; byte counts print as a whole number,
// KB and above print with one decimal place, trailing ".0" trimmed (so an
// exact power of 1024 reads as "1KB"/"1MB" rather than "1.0KB"/"1.0MB").
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	val := float64(n)
	unit := units[0]
	for _, u := range units {
		val /= 1024
		unit = u
		if val < 1024 {
			break
		}
	}
	s := strings.TrimSuffix(strconv.FormatFloat(val, 'f', 1, 64), ".0")
	return s + unit
}
