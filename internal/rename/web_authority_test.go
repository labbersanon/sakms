package rename

import "testing"

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
