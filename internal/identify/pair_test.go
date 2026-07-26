package identify

import "testing"

func TestPairByName_ExactMatchPairs(t *testing.T) {
	a := []string{"Jane Doe"}
	b := []string{"Jane Doe"}
	pairs := PairByName(a, b, 0.6)
	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d: %+v", len(pairs), pairs)
	}
	if pairs[0] != (Pair{AIndex: 0, BIndex: 0}) {
		t.Fatalf("expected {0,0}, got %+v", pairs[0])
	}
}

func TestPairByName_SubThresholdDoesNotPair(t *testing.T) {
	a := []string{"Alpha Beta"}
	b := []string{"Gamma Delta"}
	pairs := PairByName(a, b, 0.6)
	if len(pairs) != 0 {
		t.Fatalf("expected no pairs for unrelated names, got %+v", pairs)
	}
}

func TestPairByName_MutualBestCorrectness(t *testing.T) {
	// Two distinct-but-similar A-side names both resemble the same B-side
	// name to some degree, but only one of them is genuinely the B name's
	// best match in both directions — the other must stay unpaired rather
	// than both collapsing onto the shared name.
	a := []string{"Jane Smith", "Jane Smithson Extra Words Here"}
	b := []string{"Jane Smith"}

	pairs := PairByName(a, b, 0.6)
	if len(pairs) != 1 {
		t.Fatalf("expected exactly 1 mutual-best pair, got %d: %+v", len(pairs), pairs)
	}
	if pairs[0].AIndex != 0 || pairs[0].BIndex != 0 {
		t.Fatalf("expected the exact-match A[0] to win the pair, got %+v", pairs[0])
	}
}

func TestPairByName_SingleTokenNamesRequireEffectivelyExactOverlap(t *testing.T) {
	// Single-token names (tokenize -> 1 token) skip TitleSimilarity's
	// containment branch (requires >=2 overlapping tokens) and fall back to
	// plain Jaccard, so they only pair on near-total token overlap, not loose
	// similarity.
	a := []string{"Ana"}
	bExact := []string{"Ana"}
	if pairs := PairByName(a, bExact, 0.6); len(pairs) != 1 {
		t.Fatalf("expected single-token exact match to pair, got %+v", pairs)
	}

	bLoose := []string{"Anastasia"}
	if pairs := PairByName(a, bLoose, 0.6); len(pairs) != 0 {
		t.Fatalf("expected single-token loose similarity ('Ana' vs 'Anastasia') NOT to pair, got %+v", pairs)
	}
}

func TestPairByName_ExclusiveNamesSurviveUnpaired(t *testing.T) {
	a := []string{"TPDB Only Performer"}
	b := []string{"StashDB Only Performer"}
	pairs := PairByName(a, b, 0.6)
	if len(pairs) != 0 {
		t.Fatalf("expected both source-exclusive names to survive unpaired (no pairs), got %+v", pairs)
	}
}

func TestPairByName_AsymmetrySafe(t *testing.T) {
	// short is fully contained in long, so TitleSimilarity(short, long) hits
	// the containment branch and scores high (~1.0), while
	// TitleSimilarity(long, short) does NOT (containment is measured against
	// long's own much larger token count) and falls back to a low plain
	// Jaccard score — the exact same asymmetry TestTitleSimilarity_
	// ContainmentBypassesLengthPenalty documents in similarity_test.go.
	//
	// Placing the LONG name on the A side and the SHORT name on the B side
	// means a naive pairing loop that always calls TitleSimilarity(a, b) in
	// that fixed order would score low (TitleSimilarity(long, short)) and
	// fail to pair. Only an asymmetry-safe implementation that tries both
	// orders (max(TitleSimilarity(a,b), TitleSimilarity(b,a))) sees the high
	// TitleSimilarity(short, long) score and pairs correctly.
	long := "AnalOnly 24 12 27 Rebel Rhyder And Nicoluva Anal Rides XXX 2160p MP4-NBQ"
	short := "Anal Rides"

	naiveAB := TitleSimilarity(long, short)
	naiveBA := TitleSimilarity(short, long)
	if naiveAB >= 0.6 {
		t.Fatalf("test setup invalid: TitleSimilarity(long, short) = %f, expected it to be BELOW threshold to prove asymmetry-safety matters", naiveAB)
	}
	if naiveBA < 0.6 {
		t.Fatalf("test setup invalid: TitleSimilarity(short, long) = %f, expected it to be well ABOVE threshold", naiveBA)
	}

	pairs := PairByName([]string{long}, []string{short}, 0.6)
	if len(pairs) != 1 {
		t.Fatalf("expected the asymmetric pair to form via max(TitleSimilarity(a,b),TitleSimilarity(b,a)), got %+v", pairs)
	}
	if pairs[0] != (Pair{AIndex: 0, BIndex: 0}) {
		t.Fatalf("expected {0,0}, got %+v", pairs[0])
	}
}

func TestNearExactName_TrueForNearIdentical(t *testing.T) {
	if !nearExactName("Jane Doe", "Jane Doe") {
		t.Fatal("expected identical names to be near-exact")
	}
	if !nearExactName("Jane.Doe", "Jane Doe") {
		t.Fatal("expected separator-normalized-equal names to be near-exact")
	}
}

func TestNearExactName_FalseForPairedButDivergentBeyondTightness(t *testing.T) {
	// Four of five tokens overlap (differs only in the last name), which
	// clears the 0.6 pairing threshold (maxSimilarity 0.8, via
	// TitleSimilarity's containment branch: inter=4 >= 2, containment =
	// 4/5 = 0.8 >= 0.7) but stays below the 0.9 collapse tightness — the
	// exact "genuinely fuzzy pairing, not the same entity" case Q3 requires
	// nearExactName to reject so the caller surfaces both names instead of
	// silently collapsing to one.
	a := "Jane Marie Rose Elizabeth Smith"
	b := "Jane Marie Rose Elizabeth Anderson"

	score := maxSimilarity(a, b)
	if score < performerMatchThreshold {
		t.Fatalf("test setup invalid: maxSimilarity(a, b) = %f, expected it to clear the 0.6 pairing threshold", score)
	}
	if score >= nameCollapseTightness {
		t.Fatalf("test setup invalid: maxSimilarity(a, b) = %f, expected it below nameCollapseTightness (%f)", score, nameCollapseTightness)
	}
	if nearExactName(a, b) {
		t.Fatal("expected a 0.6-pairable but tightness-divergent pair to be reported as NOT near-exact")
	}
}

func TestNearExactName_ExportedMatchesUnexported(t *testing.T) {
	cases := [][2]string{
		{"Jane Doe", "Jane Doe"},
		{"Jane Marie Rose Elizabeth Smith", "Jane Marie Rose Elizabeth Anderson"},
		{"Alpha Beta", "Gamma Delta"},
	}
	for _, c := range cases {
		if got, want := NearExactName(c[0], c[1]), nearExactName(c[0], c[1]); got != want {
			t.Fatalf("NearExactName(%q,%q) = %v, want %v (nearExactName)", c[0], c[1], got, want)
		}
	}
}

func TestPairPerformersByName_UsesPerformerThreshold(t *testing.T) {
	got := PairPerformersByName([]string{"Jane Doe"}, []string{"Jane Doe"})
	want := PairByName([]string{"Jane Doe"}, []string{"Jane Doe"}, performerMatchThreshold)
	if len(got) != 1 || len(want) != 1 || got[0] != want[0] {
		t.Fatalf("PairPerformersByName = %+v, want %+v", got, want)
	}
}

func TestPairStudiosByName_UsesStudioThreshold(t *testing.T) {
	got := PairStudiosByName([]string{"Some Studio"}, []string{"Some Studio"})
	want := PairByName([]string{"Some Studio"}, []string{"Some Studio"}, studioMatchThreshold)
	if len(got) != 1 || len(want) != 1 || got[0] != want[0] {
		t.Fatalf("PairStudiosByName = %+v, want %+v", got, want)
	}
}
