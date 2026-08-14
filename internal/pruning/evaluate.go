package pruning

import (
	"fmt"
	"math"
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
	CreatedAt   string   // RFC3339 from the library row; "" if never captured.
	SizeBytes   int64    // 0 == not captured (backfill has not reached it).
	QualityTier string   // "" == not captured; "unknown" == backfill could not infer.
	Tags        []string // The item's own tags, verbatim from libStore; nil == none.
	Rating      int      // Operator 1–5 stars; 0 == unset (fail-closed for min-rating).
}

// matchedTags returns which of ruleTags exactly match (case-insensitive) any
// entry in itemTags, in ruleTags order.
//
// EXACT matching, deliberately not substring or word-boundary: a tag like
// "Transgender" and one like "Transformation" must be distinguishable with
// zero false positives.
//
// Ported from internal/purge's MatchesAny/MatchedEntries (themselves ported
// unchanged from stash-whisparr-sort), which this replaces. The primitive had
// to MOVE rather than be called: internal/purge imports internal/pruning, so
// the dependency cannot run the other way. Keeping a copy behind in
// internal/purge would put the exact-match semantic in two files at once,
// which is precisely the drift this consolidation exists to prevent — there
// is deliberately no compatibility shim there.
func matchedTags(ruleTags, itemTags []string) []string {
	var out []string
	for _, rt := range ruleTags {
		for _, it := range itemTags {
			if strings.EqualFold(rt, it) {
				out = append(out, rt)
				break
			}
		}
	}
	return out
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
// match.
//
// Claude 2026-08-14: when Criteria is non-empty, only those rows are
// evaluated (AND, in row order). The five scalar fields are ignored so two
// age rows or contains+notContains tags are expressible. Empty Criteria
// keeps the pre-0014 five-field path so Go tests that construct
// Rule{AgeDays: 365} stay green.
// Review if: the scalar columns are dropped or the frontend stops sending
// criteria.
func Match(r Rule, subj Subject, now time.Time) (bool, string) {
	if len(r.Criteria) > 0 {
		return matchCriteria(r, subj, now)
	}
	return matchLegacy(r, subj, now)
}

func matchLegacy(r Rule, subj Subject, now time.Time) (bool, string) {
	hasAge := r.AgeDays != 0
	hasSize := r.SizeBytes != 0
	hasTier := r.QualityTierFloor != ""
	hasTags := len(r.Tags) > 0
	hasRating := r.MinRating != 0
	if !hasAge && !hasSize && !hasTier && !hasTags && !hasRating {
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

	if hasTags {
		// Fail-closed, the same direction as matchesTier (see its doc for why
		// the opposite direction is a data-loss bug): an item with no tags, or
		// with no tag matching this rule's, does NOT match. An empty subj.Tags
		// can never satisfy a configured tags condition.
		//
		// The fragment reports the tags that ACTUALLY fired, not the rule's
		// configured list — the same "actual item values, not rule thresholds"
		// convention the other three fragments follow.
		hits := matchedTags(r.Tags, subj.Tags)
		if len(hits) == 0 {
			return false, ""
		}
		parts = append(parts, fmt.Sprintf("tags: %s", strings.Join(hits, ", ")))
	}

	if hasRating {
		// Fail-closed: unrated (0) never satisfies a min-rating condition.
		// MinRating is the keep-floor — match when the operator scored the
		// title below that floor (rating < MinRating). Do not treat 0 as
		// "worse than 1 star" or every unrated row would stage for delete.
		if subj.Rating <= 0 || subj.Rating >= r.MinRating {
			return false, ""
		}
		parts = append(parts, fmt.Sprintf("rated %d/5", subj.Rating))
	}

	return true, fmt.Sprintf("Matched rule '%s': %s", r.Name, strings.Join(parts, ", "))
}

// matchCriteria ANDs every Criteria row in order. One miss fails the rule.
// Fragments follow row order (the operator-authored order), not the legacy
// age/size/tier/tags/rating fixed order.
func matchCriteria(r Rule, subj Subject, now time.Time) (bool, string) {
	var parts []string
	for _, c := range r.Criteria {
		ok, fragment := matchCriterion(c, subj, now)
		if !ok {
			return false, ""
		}
		parts = append(parts, fragment)
	}
	if len(parts) == 0 {
		return false, ""
	}
	return true, fmt.Sprintf("Matched rule '%s': %s", r.Name, strings.Join(parts, ", "))
}

func matchCriterion(c Criterion, subj Subject, now time.Time) (bool, string) {
	switch c.Field {
	case FieldAge:
		ageDays, ok := subjectAgeDays(subj.CreatedAt, now)
		if !ok {
			return false, ""
		}
		threshold, err := parseNonNegInt(c.Value)
		if err != nil || !compareInt(ageDays, threshold, c.Op) {
			return false, ""
		}
		return true, fmt.Sprintf("%d days old", ageDays)
	case FieldSize:
		// Uncaptured size (0) never matches any size op, including lt —
		// otherwise every not-yet-backfilled row would match "less than 1 GB".
		if subj.SizeBytes == 0 {
			return false, ""
		}
		threshold, ok := criterionSizeBytes(c.Value, c.Unit)
		if !ok || !compareInt64(subj.SizeBytes, threshold, c.Op) {
			return false, ""
		}
		return true, humanBytes(subj.SizeBytes)
	case FieldQuality:
		subjRank, ok := tierRank[strings.ToLower(subj.QualityTier)]
		if !ok {
			return false, ""
		}
		wantRank, ok := tierRank[strings.ToLower(strings.TrimSpace(c.Value))]
		if !ok || !compareInt(subjRank, wantRank, c.Op) {
			return false, ""
		}
		return true, fmt.Sprintf("tier: %s", subj.QualityTier)
	case FieldTag:
		// Claude 2026-08-14: Values + matchMode any/all (De Morgan on notContains).
		// Reason: one tag row holds a chip list; Any = OR, All = AND. Untagged
		//   fails contains (fail-closed) and matches both notContains modes.
		// Troubleshooting: empty Values falls back to Value so pre-0015 JSON
		//   and Go tests that set Criterion{Value: "BDSM"} stay green.
		// Review if: substring tag matching is added (must not; EqualFold only).
		values := tagCriterionValuesForMatch(c)
		mode := tagMatchModeForMatch(c)
		if len(values) == 0 || mode == "" {
			return false, ""
		}
		hits := matchedTags(values, subj.Tags)
		switch c.Op {
		case OpContains:
			switch mode {
			case MatchModeAny:
				if len(hits) == 0 {
					return false, ""
				}
			case MatchModeAll:
				if len(hits) != len(values) {
					return false, ""
				}
			default:
				return false, ""
			}
			return true, fmt.Sprintf("tags: %s", strings.Join(hits, ", "))
		case OpNotContains:
			switch mode {
			case MatchModeAny:
				if len(hits) != 0 {
					return false, ""
				}
			case MatchModeAll:
				if len(hits) >= len(values) {
					return false, ""
				}
			default:
				return false, ""
			}
			if mode == MatchModeAll {
				return true, fmt.Sprintf("tags: not all of %s", strings.Join(values, ", "))
			}
			return true, fmt.Sprintf("tags: not %s", strings.Join(values, ", "))
		default:
			return false, ""
		}
	case FieldRating:
		// Unrated (0) never matches any rating op, including lt.
		if subj.Rating <= 0 {
			return false, ""
		}
		threshold, err := parseNonNegInt(c.Value)
		if err != nil || !compareInt(subj.Rating, threshold, c.Op) {
			return false, ""
		}
		return true, fmt.Sprintf("rated %d/5", subj.Rating)
	default:
		return false, ""
	}
}

// Claude 2026-08-14: hasExactTag retired — tag match now uses matchedTags
//   over Values (or a one-element Value fallback) plus matchMode.
// Reason: a single-value helper hid Any/All and would drift from the table.
// Troubleshooting: pre-0015 single-tag rows still match via
//   tagCriterionValuesForMatch falling back to Value.
// Review if: a one-tag helper is needed again for a different field.
// func hasExactTag(itemTags []string, want string) bool {
// 	return len(matchedTags([]string{want}, itemTags)) > 0
// }

func compareInt(actual, threshold int, op string) bool {
	switch op {
	case OpGT:
		return actual > threshold
	case OpLT:
		return actual < threshold
	case OpEQ:
		return actual == threshold
	default:
		return false
	}
}

func compareInt64(actual, threshold int64, op string) bool {
	switch op {
	case OpGT:
		return actual > threshold
	case OpLT:
		return actual < threshold
	case OpEQ:
		return actual == threshold
	default:
		return false
	}
}

func subjectAgeDays(createdAt string, now time.Time) (int, bool) {
	if createdAt == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return 0, false
	}
	return int(now.Sub(t) / (24 * time.Hour)), true
}

// criterionSizeBytes converts a free-fill size value + unit into bytes
// (1024-based, matching humanBytes / GB_BYTES). Used by Validate and Match.
func criterionSizeBytes(value, unit string) (int64, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	var mul float64
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case UnitKB:
		mul = 1024
	case UnitMB:
		mul = 1024 * 1024
	case UnitGB:
		mul = 1024 * 1024 * 1024
	case UnitTB:
		mul = 1024 * 1024 * 1024 * 1024
	default:
		return 0, false
	}
	return int64(math.Round(n * mul)), true
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
