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

// --- Tags condition (the 4th AND'd condition) -----------------------------
//
// Claude 2026-08-11: the six matchedTags tests below MOVED here verbatim in
// meaning from internal/purge/purge_test.go, which is deleted.
// Reason: exact-tag-match semantics must be implemented in exactly ONE place.
// The primitive moved from internal/purge (MatchesAny/MatchedEntries) into
// this package because internal/purge imports internal/pruning, so the
// dependency could not run the other way. Moving the tests WITH it is the
// drift guard — there must never be a window where the semantic lives in two
// files.
// Review if: a second tag-matching implementation ever appears anywhere.

// tagAllowlist is the full curated tag set these tests match against,
// mirroring the list the retired global Purge allowlist shipped with. Kept as
// a local literal (it was one in internal/purge too) so this test depends on
// no config package.
var tagAllowlist = []string{
	"BDSM", "Bondage", "Bondage Blowjob", "Bondage Collar", "Bondage Sex",
	"Dungeon", "Latina Trans", "Trans Fucked by Female", "Trans Fucked by Male",
	"Trans Fucks Female", "Trans Fucks Male", "Trans Fucks Trans", "Transgender",
	"Transgender (Female)", "Twosome (Trans)",
	"Bound", "Bound Wrists", "Bound Arms", "Bound Legs", "Chained", "Rope",
	"Crotch Rope", "Shibari", "Ribbon Bondage", "Breast Bondage", "Ball Gag",
	"Bit Gag", "Tape Gag", "Improvised Gag", "Whip", "Slave", "Dominatrix",
	"Spiked Collar", "Metal Collar", "Animal Collar",
	"Shemale", "She-male", "Chicks with Dicks", "Trannies", "Tgirls", "T-Girl",
	"Transmasculine", "Trans Women", "Trans Men", "Transgender Erotica",
	"FTM Gay Porn", "Queer Porn", "Feminist Porn", "Nonbinary", "Genderqueer",
	"Gender Variant Media",
	"Futanari", "Futa with Female", "Futa with Male", "Implied Futanari",
	"Crossdressing",
}

// tagMatches is the old purge.MatchesAny in matchedTags' terms: does this ONE
// item tag hit any entry in the configured list?
func tagMatches(itemTag string, ruleTags []string) bool {
	return len(matchedTags(ruleTags, []string{itemTag})) > 0
}

func TestMatchedTags_AllKnownLiveTagsStillMatch(t *testing.T) {
	known := []string{
		"BDSM", "Bondage", "Bondage Blowjob", "Bondage Collar", "Bondage Sex",
		"Dungeon", "Latina Trans", "Trans Fucked by Female", "Trans Fucked by Male",
		"Trans Fucks Female", "Trans Fucks Male", "Trans Fucks Trans", "Transgender",
		"Transgender (Female)", "Twosome (Trans)",
	}
	for _, tag := range known {
		t.Run(tag, func(t *testing.T) {
			if !tagMatches(tag, tagAllowlist) {
				t.Errorf("expected %q to match (regression against live data)", tag)
			}
		})
	}
}

// Transgender and Transformation are the case that breaks word-boundary
// regex matching (see matchedTags' doc comment) — exact matching has no such
// ambiguity.
func TestMatchedTags_TransgenderVsTransformation(t *testing.T) {
	if !tagMatches("Transgender", tagAllowlist) {
		t.Fatal("Transgender must match — it's an explicit configured tag")
	}
	if tagMatches("Transformation", tagAllowlist) {
		t.Fatal("Transformation must NOT match — not configured, and exact matching has no substring ambiguity")
	}
}

func TestMatchedTags_UnrelatedTagsNeverMatch(t *testing.T) {
	cases := []string{
		"Transformation", "Transatlantic", "Translator", "Transcript",
		"Bondage-Free", "Vanilla Romance", "Blonde", "Anal", "Threesome",
		"Chainsaw", "Collarbone", "Sailor Collar",
	}
	for _, tag := range cases {
		t.Run(tag, func(t *testing.T) {
			if tagMatches(tag, tagAllowlist) {
				t.Errorf("expected %q NOT to match — not a configured tag", tag)
			}
		})
	}
}

func TestMatchedTags_CaseInsensitive(t *testing.T) {
	if !tagMatches("bdsm", tagAllowlist) {
		t.Fatal("expected case-insensitive match for lowercase 'bdsm'")
	}
	if !tagMatches("SHEMALE", tagAllowlist) {
		t.Fatal("expected case-insensitive match for uppercase 'SHEMALE'")
	}
}

func TestMatchedTags_ReportsWhichTagFired(t *testing.T) {
	got := matchedTags(tagAllowlist, []string{"latina trans"}) // case-insensitive input
	if len(got) != 1 || got[0] != "Latina Trans" {
		t.Fatalf("expected [\"Latina Trans\"], got %v", got)
	}
}

func TestMatchedTags_NoMatch(t *testing.T) {
	got := matchedTags(tagAllowlist, []string{"Vanilla"})
	if len(got) != 0 {
		t.Fatalf("expected no matches, got %v", got)
	}
}

func TestMatch_TagsConditionMatchesWhenATagHits(t *testing.T) {
	r := Rule{Name: "Flagged", Tags: []string{"BDSM", "Rope"}}
	ok, reason := Match(r, Subject{Tags: []string{"Drama", "rope"}}, fixedNow)
	if !ok {
		t.Fatal("expected a match: the item carries 'rope', which the rule configures")
	}
	if want := "Matched rule 'Flagged': tags: Rope"; reason != want {
		t.Errorf("reason = %q, want %q (reports the tag that FIRED, not the whole configured list)", reason, want)
	}
}

// Fail-closed, the same direction as matchesTier: an item with NO tags can
// never satisfy a configured tags condition. The opposite direction would
// stage every untagged item in the library for deletion.
func TestMatch_TagsFailClosedOnItemWithNoTags(t *testing.T) {
	r := Rule{Name: "Flagged", Tags: []string{"BDSM"}}
	for name, subj := range map[string]Subject{
		"nil tags":   {Tags: nil},
		"empty tags": {Tags: []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			if ok, _ := Match(r, subj, fixedNow); ok {
				t.Error("expected NO match — an untagged item must never satisfy a tags condition")
			}
		})
	}
}

func TestMatch_TagsConfiguredButNoneHit(t *testing.T) {
	r := Rule{Name: "Flagged", Tags: []string{"BDSM"}}
	if ok, _ := Match(r, Subject{Tags: []string{"Drama", "Comedy"}}, fixedNow); ok {
		t.Error("expected NO match — none of the item's tags is configured")
	}
}

func TestMatch_TagsAreANDedWithTheOtherThree(t *testing.T) {
	r := Rule{Name: "R", AgeDays: 100, SizeBytes: 1_000_000, QualityTierFloor: "medium", Tags: []string{"BDSM"}}

	all := Subject{CreatedAt: daysAgo(412), SizeBytes: 8_804_682_956, QualityTier: "low", Tags: []string{"BDSM"}}
	if ok, _ := Match(r, all, fixedNow); !ok {
		t.Fatal("expected a match when all four conditions are satisfied")
	}

	// Each single failure must sink the whole rule.
	for name, subj := range map[string]Subject{
		"age fails":  {CreatedAt: daysAgo(5), SizeBytes: 8_804_682_956, QualityTier: "low", Tags: []string{"BDSM"}},
		"size fails": {CreatedAt: daysAgo(412), SizeBytes: 10, QualityTier: "low", Tags: []string{"BDSM"}},
		"tier fails": {CreatedAt: daysAgo(412), SizeBytes: 8_804_682_956, QualityTier: "lossless", Tags: []string{"BDSM"}},
		"tags fail":  {CreatedAt: daysAgo(412), SizeBytes: 8_804_682_956, QualityTier: "low", Tags: []string{"Drama"}},
	} {
		t.Run(name, func(t *testing.T) {
			if ok, _ := Match(r, subj, fixedNow); ok {
				t.Error("expected NO match — every configured condition is AND'd")
			}
		})
	}
}

// A tags-only rule ignores the other three entirely: an item with no
// CreatedAt, no size and no tier still matches on its tags alone.
func TestMatch_TagsOnlyRuleIgnoresTheOtherThree(t *testing.T) {
	r := Rule{Name: "Legacy allowlist", Tags: []string{"Trailer"}}
	ok, reason := Match(r, Subject{Tags: []string{"Trailer"}}, fixedNow)
	if !ok {
		t.Fatal("expected a tags-only rule to match on tags alone")
	}
	if want := "Matched rule 'Legacy allowlist': tags: Trailer"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

func TestReason_FragmentOrderIsAgeSizeTierTags(t *testing.T) {
	// All four configured, declared out of "order" in the struct literal —
	// the Reason's fragment order must still be age, size, tier, tags.
	r := Rule{Name: "R", Tags: []string{"BDSM"}, QualityTierFloor: "low", SizeBytes: 1, AgeDays: 1}
	subj := Subject{CreatedAt: daysAgo(5), SizeBytes: 2048, QualityTier: "low", Tags: []string{"BDSM"}}

	_, reason := Match(r, subj, fixedNow)
	want := "Matched rule 'R': 5 days old, 2KB, tier: low, tags: BDSM"
	if reason != want {
		t.Errorf("reason = %q, want %q (fragment order must be age, size, tier, tags)", reason, want)
	}
}
