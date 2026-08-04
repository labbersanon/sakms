package pruning

import (
	"testing"
	"time"
)

// fixedNow is the deterministic "now" every age test evaluates against, so
// none of these depend on the wall clock.
var fixedNow = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

// daysAgo returns an RFC3339 CreatedAt exactly n days before fixedNow.
func daysAgo(n int) string {
	return fixedNow.Add(-time.Duration(n) * 24 * time.Hour).Format(time.RFC3339)
}

// --- AND-within-rule ---------------------------------------------------

func TestMatch_AllThreeConditions_AllSatisfied_Matches(t *testing.T) {
	r := Rule{Name: "Old low-quality movies", AgeDays: 400, SizeBytes: 1_000_000, QualityTierFloor: "low"}
	subj := Subject{CreatedAt: daysAgo(412), SizeBytes: 8_804_682_956, QualityTier: "low"}

	ok, reason := Match(r, subj, fixedNow)
	if !ok {
		t.Fatalf("expected match, got false (reason=%q)", reason)
	}
	want := "Matched rule 'Old low-quality movies': 412 days old, 8.2GB, tier: low"
	if reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestMatch_AllThreeConditions_OneFails_DoesNotMatch(t *testing.T) {
	base := Rule{Name: "R", AgeDays: 400, SizeBytes: 1_000_000, QualityTierFloor: "low"}
	cases := []struct {
		name string
		subj Subject
	}{
		{"age fails", Subject{CreatedAt: daysAgo(10), SizeBytes: 8_000_000, QualityTier: "low"}},
		{"size fails", Subject{CreatedAt: daysAgo(500), SizeBytes: 100, QualityTier: "low"}},
		{"tier fails", Subject{CreatedAt: daysAgo(500), SizeBytes: 8_000_000, QualityTier: "high"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ok, reason := Match(base, tc.subj, fixedNow); ok {
				t.Errorf("expected no match, got true (reason=%q)", reason)
			}
		})
	}
}

func TestMatch_AgeOnlyRule_IgnoresSizeAndTier(t *testing.T) {
	r := Rule{Name: "Stale", AgeDays: 100}
	subj := Subject{CreatedAt: daysAgo(200), SizeBytes: 0, QualityTier: ""}

	ok, reason := Match(r, subj, fixedNow)
	if !ok {
		t.Fatalf("expected match, got false (reason=%q)", reason)
	}
	want := "Matched rule 'Stale': 200 days old"
	if reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestMatch_SizeOnlyRule_IgnoresAgeAndTier(t *testing.T) {
	r := Rule{Name: "Big", SizeBytes: 1_000_000}
	subj := Subject{CreatedAt: "", SizeBytes: 2_000_000, QualityTier: ""}

	ok, reason := Match(r, subj, fixedNow)
	if !ok {
		t.Fatalf("expected match, got false (reason=%q)", reason)
	}
	want := "Matched rule 'Big': 1.9MB"
	if reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestMatch_TierOnlyRule_IgnoresAgeAndSize(t *testing.T) {
	r := Rule{Name: "LowQuality", QualityTierFloor: "medium"}
	subj := Subject{CreatedAt: "", SizeBytes: 0, QualityTier: "low"}

	ok, reason := Match(r, subj, fixedNow)
	if !ok {
		t.Fatalf("expected match, got false (reason=%q)", reason)
	}
	want := "Matched rule 'LowQuality': tier: low"
	if reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestMatch_RuleWithNoConditions_NeverMatches(t *testing.T) {
	r := Rule{Name: "Empty"}
	subj := Subject{CreatedAt: daysAgo(10000), SizeBytes: 999_999_999_999, QualityTier: "low"}

	if ok, reason := Match(r, subj, fixedNow); ok {
		t.Errorf("expected a rule with no configured conditions to never match, got true (reason=%q)", reason)
	}
}

// --- OR-across-rules -----------------------------------------------------

func TestMatchAny_ItemMatchingOneOfThreeRules_IsFlagged(t *testing.T) {
	rules := []Rule{
		{Name: "A", AgeDays: 10000},
		{Name: "B", AgeDays: 100},
		{Name: "C", QualityTierFloor: "low"},
	}
	subj := Subject{CreatedAt: daysAgo(200), SizeBytes: 0, QualityTier: "high"}

	reasons := MatchAny(rules, subj, fixedNow)
	if len(reasons) != 1 {
		t.Fatalf("expected exactly 1 matched reason, got %d: %v", len(reasons), reasons)
	}
	want := "Matched rule 'B': 200 days old"
	if reasons[0] != want {
		t.Errorf("reason = %q, want %q", reasons[0], want)
	}
}

func TestMatchAny_ItemMatchingNoRules_ReturnsEmpty(t *testing.T) {
	rules := []Rule{
		{Name: "A", AgeDays: 10000},
		{Name: "B", SizeBytes: 999_999_999_999},
	}
	subj := Subject{CreatedAt: daysAgo(1), SizeBytes: 1, QualityTier: "unknown"}

	if reasons := MatchAny(rules, subj, fixedNow); len(reasons) != 0 {
		t.Errorf("expected no matched reasons, got %v", reasons)
	}
}

func TestMatchAny_ItemMatchingTwoRules_ReturnsBothReasons(t *testing.T) {
	rules := []Rule{
		{Name: "A", AgeDays: 100},
		{Name: "B", QualityTierFloor: "low"},
		{Name: "C", AgeDays: 10000},
	}
	subj := Subject{CreatedAt: daysAgo(200), SizeBytes: 0, QualityTier: "low"}

	reasons := MatchAny(rules, subj, fixedNow)
	if len(reasons) != 2 {
		t.Fatalf("expected exactly 2 matched reasons, got %d: %v", len(reasons), reasons)
	}
	if reasons[0] != "Matched rule 'A': 200 days old" {
		t.Errorf("reasons[0] = %q", reasons[0])
	}
	if reasons[1] != "Matched rule 'B': tier: low" {
		t.Errorf("reasons[1] = %q", reasons[1])
	}
}

func TestMatchAny_EmptyRuleSet_ReturnsEmpty(t *testing.T) {
	subj := Subject{CreatedAt: daysAgo(10000), SizeBytes: 999_999_999_999, QualityTier: "low"}
	if reasons := MatchAny(nil, subj, fixedNow); len(reasons) != 0 {
		t.Errorf("expected no matched reasons for an empty rule set, got %v", reasons)
	}
}

// --- Sentinel safety — the highest-value tests in the feature -----------

func TestMatch_EmptyQualityTier_NeverSatisfiesTierCondition(t *testing.T) {
	// The broadest possible floor — an un-backfilled row must NOT match.
	r := Rule{Name: "Broadest", QualityTierFloor: "lossless"}
	subj := Subject{QualityTier: ""}

	if ok, reason := Match(r, subj, fixedNow); ok {
		t.Errorf("expected an empty QualityTier to never satisfy a tier condition, got match (reason=%q)", reason)
	}
}

func TestMatch_UnknownQualityTier_NeverSatisfiesTierCondition(t *testing.T) {
	r := Rule{Name: "Broadest", QualityTierFloor: "lossless"}
	subj := Subject{QualityTier: "unknown"}

	if ok, reason := Match(r, subj, fixedNow); ok {
		t.Errorf("expected %q QualityTier to never satisfy a tier condition, got match (reason=%q)", "unknown", reason)
	}
}

func TestMatch_ZeroSize_DoesNotMatchConfiguredSizeThreshold(t *testing.T) {
	r := Rule{Name: "AnySize", SizeBytes: 1}
	subj := Subject{SizeBytes: 0}

	if ok, reason := Match(r, subj, fixedNow); ok {
		t.Errorf("expected a zero (uncaptured) size to never satisfy a configured size threshold, got match (reason=%q)", reason)
	}
}

func TestMatch_UnparseableCreatedAt_DoesNotSatisfyAgeCondition(t *testing.T) {
	r := Rule{Name: "Stale", AgeDays: 1}
	subj := Subject{CreatedAt: "not-a-date"}

	if ok, reason := Match(r, subj, fixedNow); ok {
		t.Errorf("expected an unparseable CreatedAt to never satisfy an age condition, got match (reason=%q)", reason)
	}
}

func TestMatch_EmptyCreatedAt_DoesNotSatisfyAgeCondition(t *testing.T) {
	r := Rule{Name: "Stale", AgeDays: 1}
	subj := Subject{CreatedAt: ""}

	if ok, reason := Match(r, subj, fixedNow); ok {
		t.Errorf("expected an empty CreatedAt to never satisfy an age condition, got match (reason=%q)", reason)
	}
}

// --- Boundary semantics (>= / <= are inclusive) --------------------------

func TestMatch_AgeExactlyAtThreshold_Matches(t *testing.T) {
	r := Rule{Name: "Stale", AgeDays: 100}
	subj := Subject{CreatedAt: daysAgo(100)}

	ok, reason := Match(r, subj, fixedNow)
	if !ok {
		t.Fatalf("expected age exactly at threshold to match (inclusive >=)")
	}
	if want := "Matched rule 'Stale': 100 days old"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestMatch_SizeExactlyAtThreshold_Matches(t *testing.T) {
	r := Rule{Name: "Big", SizeBytes: 1_000_000}
	subj := Subject{SizeBytes: 1_000_000}

	if ok, _ := Match(r, subj, fixedNow); !ok {
		t.Errorf("expected size exactly at threshold to match (inclusive >=)")
	}
}

func TestMatch_TierExactlyAtFloor_Matches(t *testing.T) {
	r := Rule{Name: "LowFloor", QualityTierFloor: "medium"}
	subj := Subject{QualityTier: "medium"}

	if ok, _ := Match(r, subj, fixedNow); !ok {
		t.Errorf("expected tier exactly at floor to match (inclusive <=)")
	}
}

// --- Reason-text generation (§9.2) ---------------------------------------

func TestReason_AllThreeConditions_MatchesSpecExample(t *testing.T) {
	r := Rule{Name: "Old low-quality movies", AgeDays: 400, SizeBytes: 1_000_000, QualityTierFloor: "low"}
	subj := Subject{CreatedAt: daysAgo(412), SizeBytes: 8_804_682_956, QualityTier: "low"}

	_, reason := Match(r, subj, fixedNow)
	want := "Matched rule 'Old low-quality movies': 412 days old, 8.2GB, tier: low"
	if reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestReason_AgeOnlyRule_OmitsSizeAndTierFragments(t *testing.T) {
	r := Rule{Name: "Stale", AgeDays: 412}
	subj := Subject{CreatedAt: daysAgo(412), SizeBytes: 8_804_682_956, QualityTier: "low"}

	_, reason := Match(r, subj, fixedNow)
	want := "Matched rule 'Stale': 412 days old"
	if reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestReason_FragmentOrderIsAgeSizeTier(t *testing.T) {
	// Configure all three but in a rule constructed with fields out of
	// "order" in the struct literal — the Reason's fragment order must
	// still be age, size, tier regardless of field declaration order.
	r := Rule{Name: "R", QualityTierFloor: "low", SizeBytes: 1, AgeDays: 1}
	subj := Subject{CreatedAt: daysAgo(5), SizeBytes: 2048, QualityTier: "low"}

	_, reason := Match(r, subj, fixedNow)
	want := "Matched rule 'R': 5 days old, 2KB, tier: low"
	if reason != want {
		t.Errorf("reason = %q, want %q (fragment order must be age, size, tier)", reason, want)
	}
}

func TestReason_UsesActualItemValuesNotRuleThresholds(t *testing.T) {
	// Threshold is 100 days / 1MB / floor "medium", but the actual item is
	// much older, much bigger, and a better ("low") tier than the floor —
	// the Reason must report the item's own values, not the rule's.
	r := Rule{Name: "R", AgeDays: 100, SizeBytes: 1_000_000, QualityTierFloor: "medium"}
	subj := Subject{CreatedAt: daysAgo(412), SizeBytes: 8_804_682_956, QualityTier: "low"}

	_, reason := Match(r, subj, fixedNow)
	want := "Matched rule 'R': 412 days old, 8.2GB, tier: low"
	if reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1KB"},
		{1536, "1.5KB"},
		{1_048_576, "1MB"},
		{8_804_682_956, "8.2GB"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.n); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
