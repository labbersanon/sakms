package rename

import (
	"testing"

	"github.com/labbersanon/sakms/internal/searchterm"
)

// TestTokenStopLists is the ANTI-SILENT-NO-OP GUARD for the tokenStop
// filename/title split. Without it, a mergeTokenStops that wrote into
// tokenStopUniversal instead of allocating would hand titleTokens all 32 words
// back — making the entire fix a no-op — and the WHOLE SUITE WOULD STILL BE
// GREEN, because no other fixture anywhere in this package puts a
// release-scene word into a catalog title.
//
// The three length assertions are NOT redundant with the membership loops
// below them, and that is the whole reason both halves are here: a mutating
// merge passes every membership loop trivially (both maps would then contain
// everything, so "present in tokenStopFilename" holds for every key) and fails
// ONLY on len(tokenStopUniversal) == 14. Conversely, lengths alone would not
// catch a mis-sorted word. Neither half subsumes the other; do not delete
// either as duplication.
//
// Assertions 1-3 also prove DISJOINTNESS arithmetically: 14 + 18 == 32 means
// the union lost nothing to an overlap.
// Context: see .omc/plans/autopilot-impl.md §6.1 and
// .omc/specs/deep-interview-sakms-tokenstop-filename-title-split.md
func TestTokenStopLists(t *testing.T) {
	if got := len(tokenStopUniversal); got != 14 {
		t.Errorf("len(tokenStopUniversal) = %d, want 14 — a mutating mergeTokenStops is the likely cause", got)
	}
	if got := len(tokenStopReleaseScene); got != 18 {
		t.Errorf("len(tokenStopReleaseScene) = %d, want 18", got)
	}
	if got := len(tokenStopFilename); got != 32 {
		t.Errorf("len(tokenStopFilename) = %d, want 32 (14 + 18, i.e. the two inputs are disjoint)", got)
	}

	for k := range tokenStopUniversal {
		if _, ok := tokenStopReleaseScene[k]; ok {
			t.Errorf("universal word %q also appears in tokenStopReleaseScene — the lists must be disjoint", k)
		}
		if _, ok := tokenStopFilename[k]; !ok {
			t.Errorf("universal word %q is missing from the tokenStopFilename union", k)
		}
	}
	for k := range tokenStopReleaseScene {
		// The mutation guard stated directly, per-word: a merge that wrote into
		// its first input would put every release-scene word here.
		if _, ok := tokenStopUniversal[k]; ok {
			t.Errorf("release-scene word %q leaked into tokenStopUniversal — mergeTokenStops mutated its input", k)
		}
		if _, ok := tokenStopFilename[k]; !ok {
			t.Errorf("release-scene word %q is missing from the tokenStopFilename union", k)
		}
	}

	// Spot-check the one word the whole production failure turns on. Note that
	// the spec's own AC5 enumeration OMITS "extended" — it lists 17 of the 18 —
	// so this assertion is deliberately explicit rather than left to the loops
	// above: TheTVDB's "A Chump at Oxford (Extended)" (S00E17) vs
	// "A Chump At Oxford" (S16E01) is the exact pair that collapsed.
	if _, ok := tokenStopReleaseScene["extended"]; !ok {
		t.Error(`"extended" must be in tokenStopReleaseScene`)
	}
	if _, ok := tokenStopUniversal["extended"]; ok {
		t.Error(`"extended" must NOT be in tokenStopUniversal — stripping it from a catalog title is the bug being fixed`)
	}
}

// TestFilenameTokensStripsFullList proves the two stop lists genuinely DIFFER
// in behaviour, one release-scene word at a time.
//
// It must NOT call searchterm.FromName on the first two blocks, and that is
// the point rather than an oversight: FromName already strips 15 of these 18
// words from a filename before the tokenizer ever sees them, so a test written
// against FromName-processed input would pass identically with the stop lists
// merged back together — vacuous. Feeding RAW strings is what makes the
// assertion load-bearing.
//
// Carrier words "Oxford" and "Chump" are chosen because each is >= 4 runes,
// letters-only, non-roman, and in NEITHER stop list — so any failure here is
// unambiguously about the word under test, never about a carrier.
// Context: see .omc/plans/autopilot-impl.md §6.3 (this is AC5, done
// non-vacuously).
func TestFilenameTokensStripsFullList(t *testing.T) {
	// Block 1 — the 18 release-scene words: stripped by filenameTokens,
	// RETAINED by titleTokens. Both directions asserted as exact set equality
	// (size AND membership); "contains" would not catch an over-strip.
	for _, w := range []string{
		"vs", "feat", "ft", "av1", "x264", "x265", "hevc", "h264", "bluray",
		"webrip", "webdl", "hdtv", "remux", "proper", "repack", "extended",
		"unrated", "internal",
	} {
		t.Run("release-scene/"+w, func(t *testing.T) {
			in := "Oxford " + w + " Chump"
			assertTokenSet(t, "filenameTokens", in, filenameTokens(in), "oxford", "chump")
			assertTokenSet(t, "titleTokens", in, titleTokens(in), "oxford", w, "chump")
		})
	}

	// Block 2 — the 14 universal words: stripped by BOTH tokenizers. This is
	// the half that proves the split narrowed titleTokens without gutting it.
	//
	// "a" never actually reaches the stop map — the len(f) < 2 rule fires
	// first, so it would be dropped even if it were removed from the list. It
	// is retained in tokenStopUniversal only because the spec forbids changing
	// WHICH 14 words are universal, not because it is reachable.
	for _, w := range []string{
		"a", "an", "the", "and", "or", "of", "for",
		"to", "in", "on", "at", "with", "from", "by",
	} {
		t.Run("universal/"+w, func(t *testing.T) {
			in := "Oxford " + w + " Chump"
			assertTokenSet(t, "titleTokens", in, titleTokens(in), "oxford", "chump")
			assertTokenSet(t, "filenameTokens", in, filenameTokens(in), "oxford", "chump")
		})
	}

	// Block 3 — the documentation fixture, and the ONLY block that calls
	// FromName: the real production filename, end to end, proving the pipeline
	// this fix runs inside is unchanged on the FILENAME side.
	//
	// Provenance of every dropped token, since no two are dropped by the same
	// rule: "DVDRip" is removed by searchterm.FromName (NOT by either stop
	// list), "at" by the universal list, "a" by the len < 2 rule. "xvid",
	// "colour" and "die" are in neither list and survive — "die" survives the
	// short-roman-numeral filter specifically because "d" is not one of
	// i/v/x/l/c/m.
	const prod = "Laurel & Hardy - A Chump at Oxford(Colour)-DVDRip.XviD-DIE-DVD01.mp4"
	assertTokenSet(t, "filenameTokens(FromName)", prod,
		filenameTokens(searchterm.FromName(prod)),
		"laurel", "hardy", "chump", "oxford", "colour", "xvid", "die")
}

// assertTokenSet asserts exact set equality — size and membership, both
// directions. Local to this file's tokenizer tests.
func assertTokenSet(t *testing.T, fn, in string, got map[string]struct{}, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s(%q) = %v (%d tokens), want %v (%d tokens)", fn, in, got, len(got), want, len(want))
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("%s(%q) = %v, missing expected token %q", fn, in, got, w)
		}
	}
}

// TestFilenameTokensMatchesFilenameTokenCounts is the filename-side mirror of
// TestTitleTokensMatchesTitleTokenCounts: the count table's KEY SET must be
// exactly the set filenameTokens returns.
//
// Like its sibling, the parity half is TAUTOLOGICAL as long as filenameTokens
// is literally tokenSetFrom(filenameTokenCounts(s)) — and that is the point.
// It is a drift lock against a future session re-forking the filter loop, not
// a claim that today's code could fail it. All four tokenizer entry points now
// route through the single tokenCountsWithStop loop; a fifth filter rule
// belongs THERE, never in a wrapper.
//
// This test is also filenameTokenCounts' only direct consumer. That function
// has no production caller by design — it is the delegation target that keeps
// filenameTokens from forking the loop — so it must be excluded from any
// mechanical unused-symbol sweep.
// Context: see .omc/plans/autopilot-impl.md §6.4
func TestFilenameTokensMatchesFilenameTokenCounts(t *testing.T) {
	fixtures := []struct {
		name string
		in   string
	}{
		{"a collapse -- two source words, one token", "That's That"},
		{"a non-collapse single-word control", "Liberty"},
		{"a stopword drop (and)", "Love 'em and Weep"},
		{"a short roman-numeral drop (III)", "Funny Faces III"},
		{"a len < 2 drop (the s of That's)", "That's My Wife"},
		{"a digit token", "Episode 12"},
		{"empty string", ""},
		{"punctuation only", "!!! --- ???"},
		// The two fixtures this test has that its title-side sibling cannot:
		// release-scene words, which only the filename pair strips.
		{"a release-scene drop (bluray)", "Oxford bluray Chump"},
		{"a release-scene drop (vs) -- P16's own fixture", "Freddy vs Jason"},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			set := filenameTokens(f.in)
			counts := filenameTokenCounts(f.in)
			if len(set) != len(counts) {
				t.Fatalf("filenameTokens(%q) has %d tokens, filenameTokenCounts has %d: %v vs %v", f.in, len(set), len(counts), set, counts)
			}
			for tok := range set {
				if _, ok := counts[tok]; !ok {
					t.Errorf("token %q is in filenameTokens(%q) but missing from filenameTokenCounts", tok, f.in)
				}
			}
			for tok := range counts {
				if _, ok := set[tok]; !ok {
					t.Errorf("token %q is in filenameTokenCounts(%q) but missing from filenameTokens", tok, f.in)
				}
			}
		})
	}

	// The COUNT VALUE the key-set check above cannot reach — the counts form
	// needs at least one direct value assertion, same as its title-side
	// sibling. Do NOT weaken this to ">= 1": out[f]++ starts from a zero value,
	// so any key present at all necessarily has a value of at least 1 and that
	// assertion cannot fail.
	if got := filenameTokenCounts("That's That")["that"]; got != 2 {
		t.Errorf(`filenameTokenCounts("That's That")["that"] = %d, want 2`, got)
	}
}

func TestIsJunkRenameFilename(t *testing.T) {
	junk := []string{
		"RED_SKELTON_Title1.mp4",
		"RED_SKELTON_Title4.mp4",
		"Title.mkv",
		"Movie2.mp4",
		"Video.avi",
		"Clip9.mkv",
		"Track1.mp4",
	}
	for _, n := range junk {
		if !IsJunkRenameFilename(n) {
			t.Errorf("expected junk: %q", n)
		}
	}
	ok := []string{
		"Red Skelton More Funny Faces.mp4",
		"Red Skelton Funny Faces III - AV1.mp4",
		"A Beautiful Mind (2001).mkv",
	}
	for _, n := range ok {
		if IsJunkRenameFilename(n) {
			t.Errorf("expected not junk: %q", n)
		}
	}
}

func TestHasTitleTokenOverlap(t *testing.T) {
	if !HasTitleTokenOverlap("Red Skelton More Funny Faces.mp4", "More Funny Faces") {
		t.Fatal("expected overlap on Funny/Faces/More/Skelton/Red")
	}
	if HasTitleTokenOverlap("RED_SKELTON_Title1.mp4", "Some Random Film") {
		t.Fatal("Title1 must not overlap invented title")
	}
	if !HasTitleTokenOverlap("JoJo Dancer 2007.mkv", "Jo Jo Dancer, Your Life Is Calling") {
		t.Fatal("expected JoJo / Dancer overlap")
	}
	// Weak single short token must not accept Bing's Red (2010) for Skelton files.
	if HasTitleTokenOverlap("Red Skelton Funny Faces III - AV1.mp4", "Red") {
		t.Fatal("single short token Red must not count as overlap")
	}
}

func TestWebAuthorityQueries_DropsTrailingSequel(t *testing.T) {
	qs := webAuthorityQueries("Red Skelton Funny Faces III - AV1.mp4", "Red Skelton Funny Faces III")
	if len(qs) < 2 {
		t.Fatalf("expected multiple queries, got %#v", qs)
	}
	if qs[0] != "Red Skelton Funny Faces" {
		t.Fatalf("expected sequel-stripped first, got %q (all %#v)", qs[0], qs)
	}
	foundFull := false
	for _, q := range qs {
		if q == "Red Skelton Funny Faces III" {
			foundFull = true
		}
	}
	if !foundFull {
		t.Fatalf("expected full sequel query present, got %#v", qs)
	}
}

func TestWebAuthorityTMDBID_StableNegative(t *testing.T) {
	a := WebAuthorityTMDBID("More Funny Faces", 1982)
	b := WebAuthorityTMDBID("More Funny Faces", 1982)
	c := WebAuthorityTMDBID("More Funny Faces", 1984)
	if a >= 0 || b >= 0 {
		t.Fatalf("expected negative ids, got %d %d", a, b)
	}
	if a != b {
		t.Fatalf("expected stable id, got %d vs %d", a, b)
	}
	if a == c {
		t.Fatalf("different years should differ")
	}
}

// TestHasWordToken pins the compact-code seed cascade's step-3 predicate. Three
// simpler predicates all silently fail on these rows — see hasWordToken's own
// doc comment — so each row here is a guard against a specific weakening.
//
// The {720x480} row is the one PRODUCTION actually sees: step 3 runs on the seed
// step 2 already stripped, so for "e614_720x480.mp4" the input is
// "_720x480.mp4" -> searchterm.FromName -> "720x480" -> {720x480}, not the
// pre-strip {e614, 720x480}. Both forms are kept: the pre-strip one is still
// reachable whenever step 2 no-ops (a blocklisted code), and both must be false.
func TestHasWordToken(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"stripped compact-code seed keeps real title tokens", "RED SKELTON .mp4", true},
		{"pre-strip seed is all codes", "e614_720x480.mp4", false},
		{"post-strip seed is all codes — the production form", "_720x480.mp4", false},
		{"empty seed", "", false},
		{"an ordinary title is unaffected", "Some.Show.Name.mkv", true},
	}
	for _, c := range cases {
		if got := hasWordToken(c.in); got != c.want {
			t.Errorf("%s: hasWordToken(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// TestTitleTokensMatchesTitleTokenCounts pins the ONE invariant the
// titleTokens/titleTokenCounts split rests on: the count table's KEY SET is
// exactly the set titleTokens returns. If the two ever desynchronize, the
// collapse check in discriminating() counts source positions for tokens the
// residual never had -- a silent wrong answer with no crash and no other
// failing test.
//
// The parity half is TAUTOLOGICAL as long as titleTokens is literally
// tokenSetFrom(titleTokenCounts(s)), and that is the point: it is a drift lock
// against a future session re-forking the two filter loops, not a claim that
// today's code could fail it. Fixtures cover every filter rule at least once.
//
// There is now a SIBLING for the filename pair,
// TestFilenameTokensMatchesFilenameTokenCounts — both pairs delegate to the one
// tokenCountsWithStop loop, so a new filter rule must be added THERE and never
// to either wrapper.
// Context: see .omc/specs/deep-interview-sakms-episode-title-discriminating-residual-collapse-fix.md
func TestTitleTokensMatchesTitleTokenCounts(t *testing.T) {
	fixtures := []struct {
		name string
		in   string
	}{
		{"a collapse -- two source words, one token", "That's That"},
		{"a non-collapse single-word control", "Liberty"},
		{"a stopword drop (and)", "Love 'em and Weep"},
		{"a short roman-numeral drop (III)", "Funny Faces III"},
		{"a len < 2 drop (the s of That's)", "That's My Wife"},
		{"a digit token", "Episode 12"},
		{"empty string", ""},
		{"punctuation only", "!!! --- ???"},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			set := titleTokens(f.in)
			counts := titleTokenCounts(f.in)
			if len(set) != len(counts) {
				t.Fatalf("titleTokens(%q) has %d tokens, titleTokenCounts has %d: %v vs %v", f.in, len(set), len(counts), set, counts)
			}
			for tok := range set {
				if _, ok := counts[tok]; !ok {
					t.Errorf("token %q is in titleTokens(%q) but missing from titleTokenCounts", tok, f.in)
				}
			}
			for tok := range counts {
				if _, ok := set[tok]; !ok {
					t.Errorf("token %q is in titleTokenCounts(%q) but missing from titleTokens", tok, f.in)
				}
			}
		})
	}

	// The COUNT VALUES the key-set check above cannot reach. These two are the
	// exact pair the whole collapse mechanism turns on -- 2 for the collapsed
	// case, 1 for the genuinely-single-word case -- and this is the only place
	// in the suite where they are checked directly rather than inferred from
	// P6's and P11's pass/fail outcome. Do NOT weaken these to "every count
	// >= 1": out[f]++ starts from a zero value, so any key present at all
	// necessarily has a value of at least 1 and that assertion cannot fail.
	if got := titleTokenCounts("That's That")["that"]; got != 2 {
		t.Errorf(`titleTokenCounts("That's That")["that"] = %d, want 2`, got)
	}
	if got := titleTokenCounts("Liberty")["liberty"]; got != 1 {
		t.Errorf(`titleTokenCounts("Liberty")["liberty"] = %d, want 1`, got)
	}
}
