package rename

import "testing"

func TestTitleSimilarity_ExactMatchIsPerfect(t *testing.T) {
	got := titleSimilarity("A Beautiful Mind", "A Beautiful Mind")
	if got != 1.0 {
		t.Errorf("expected 1.0 for an exact match, got %v", got)
	}
}

func TestTitleSimilarity_TolerantOfPunctuationAndCase(t *testing.T) {
	got := titleSimilarity("Mission Impossible", "Mission: Impossible")
	if got != 1.0 {
		t.Errorf("expected 1.0 once punctuation/case is normalized, got %v", got)
	}
}

func TestTitleSimilarity_TolerantOfExtraTokens(t *testing.T) {
	// searchterm.FromName often leaves a trailing year alongside the title —
	// that shouldn't tank similarity against a title with no year in it.
	got := titleSimilarity("American Pie 2 2001", "American Pie 2")
	if got < 0.8 {
		t.Errorf("expected high similarity despite the extra year token, got %v", got)
	}
}

func TestTitleSimilarity_UnrelatedTitlesScoreZero(t *testing.T) {
	got := titleSimilarity("FathersLLDVD", "Father's Day")
	if got != 0 {
		t.Errorf("expected 0 for a search term sharing no real word tokens with the title, got %v", got)
	}
}

func TestTitleSimilarity_EmptyInputScoresZero(t *testing.T) {
	if got := titleSimilarity("", "Anything"); got != 0 {
		t.Errorf("expected 0 for an empty search term, got %v", got)
	}
	if got := titleSimilarity("Anything", ""); got != 0 {
		t.Errorf("expected 0 for an empty match title, got %v", got)
	}
}

func TestExtractYear_PrefersParenthesizedForm(t *testing.T) {
	if got := extractYear("American Pie 2 (2001)"); got != 2001 {
		t.Errorf("expected 2001, got %d", got)
	}
}

func TestExtractYear_FallsBackToUnambiguousBareYear(t *testing.T) {
	if got := extractYear("American Pie 1999"); got != 1999 {
		t.Errorf("expected 1999, got %d", got)
	}
}

func TestExtractYear_AmbiguousMultipleBareYearsReturnsZero(t *testing.T) {
	if got := extractYear("1917 1984"); got != 0 {
		t.Errorf("expected 0 (ambiguous) for two candidate years, got %d", got)
	}
}

func TestExtractYear_NoYearReturnsZero(t *testing.T) {
	if got := extractYear("A Beautiful Mind"); got != 0 {
		t.Errorf("expected 0 for a name with no year, got %d", got)
	}
}

func TestMatchConfidence_StrongTitleAndYearMatchScoresHigh(t *testing.T) {
	got := matchConfidence("A Beautiful Mind 2001", "A Beautiful Mind", "2001-12-21")
	if got < 80 {
		t.Errorf("expected a high-confidence score, got %d", got)
	}
}

func TestMatchConfidence_YearMismatchHalvesScore(t *testing.T) {
	withMatchingYear := matchConfidence("Some Title 2001", "Some Title", "2001-01-01")
	withMismatchedYear := matchConfidence("Some Title 2001", "Some Title", "1995-01-01")
	if withMismatchedYear >= withMatchingYear {
		t.Errorf("expected a year mismatch to score lower than a year match: mismatched=%d matching=%d", withMismatchedYear, withMatchingYear)
	}
	// Sanity: the halving is real, not a rounding no-op.
	if withMismatchedYear > withMatchingYear/2+1 {
		t.Errorf("expected roughly half the score on year mismatch, got matching=%d mismatched=%d", withMatchingYear, withMismatchedYear)
	}
}

func TestMatchConfidence_UnknownMatchYearDoesNotPenalize(t *testing.T) {
	// Both calls use the identical search term, so titleSimilarity (and
	// therefore the token-overlap component of the score) is held constant
	// — isolating the year-penalty branch specifically, unlike a comparison
	// that also varies the search term's own token count.
	yearKnownAndMatching := matchConfidence("Some Title 2001", "Some Title", "2001-01-01")
	yearUnknown := matchConfidence("Some Title 2001", "Some Title", "") // unparseable release date -> matchYear == 0
	if yearUnknown != yearKnownAndMatching {
		t.Errorf("expected no penalty when the match's own year can't be determined: known=%d unknown=%d", yearKnownAndMatching, yearUnknown)
	}
}

func TestMatchConfidence_UnrelatedResultScoresBelowDefaultThreshold(t *testing.T) {
	// The exact real-world case this feature exists for: an opaque/garbled
	// search term that TMDB still returns *some* best-effort top result for.
	got := matchConfidence("FathersLLDVD", "Father's Day", "1997-05-09")
	if got >= DefaultConfidenceThreshold {
		t.Errorf("expected an unrelated result to score below the default threshold (%d), got %d", DefaultConfidenceThreshold, got)
	}
}

func TestMatchConfidence_NumericTitleYearFalsePositiveStillClearsThreshold(t *testing.T) {
	// Known, documented limitation (see matchConfidence's doc comment): a
	// numeric movie title makes extractYear return a value that isn't
	// actually a year marker, which can trigger an incorrect year-mismatch
	// halving. This test proves that halving alone doesn't drop a
	// genuinely correct, full-token-overlap match below the default
	// threshold — the case the doc comment claims is fine in practice.
	got := matchConfidence("2012", "2012", "2009-11-13") // real movie, released 2009
	if got < DefaultConfidenceThreshold {
		t.Errorf("expected the halved score to still clear the default threshold (%d), got %d", DefaultConfidenceThreshold, got)
	}
}
