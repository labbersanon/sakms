package identify

import "testing"

func TestTitleSimilarity_CamelCaseSplitting(t *testing.T) {
	// "MaxineX" should split into "Maxine" + "X" for tokenization purposes.
	sim := TitleSimilarity("MaxineX Sluts Vs Studs", "Maxine X Sluts Vs Studs Pt 3")
	if sim < 0.5 {
		t.Fatalf("expected high similarity after camelCase split, got %f", sim)
	}
}

func TestTitleSimilarity_ExactMatch(t *testing.T) {
	sim := TitleSimilarity("Some Title", "Some Title")
	if sim != 1.0 {
		t.Fatalf("expected 1.0 for exact match, got %f", sim)
	}
}

func TestTitleSimilarity_NoOverlap(t *testing.T) {
	sim := TitleSimilarity("Alpha Beta", "Gamma Delta")
	if sim != 0.0 {
		t.Fatalf("expected 0.0 for no overlap, got %f", sim)
	}
}

func TestTitleSimilarity_EmptyString(t *testing.T) {
	if sim := TitleSimilarity("", "Something"); sim != 0.0 {
		t.Fatalf("expected 0.0 for empty input, got %f", sim)
	}
	if sim := TitleSimilarity("Something", ""); sim != 0.0 {
		t.Fatalf("expected 0.0 for empty input, got %f", sim)
	}
}

func TestTitleSimilarity_ContainmentBypassesLengthPenalty(t *testing.T) {
	// The query title is short and fully contained in a much longer filename —
	// Jaccard alone would penalize this for length; containment should let it
	// through when containment>=0.7 and >=2 overlapping tokens.
	short := "Anal Rides"
	long := "AnalOnly 24 12 27 Rebel Rhyder And Nicoluva Anal Rides XXX 2160p MP4-NBQ"
	sim := TitleSimilarity(short, long)
	if sim < 0.7 {
		t.Fatalf("expected containment to dominate for a short title inside a long filename, got %f", sim)
	}
}

func TestTitleSimilarity_SingleGenericWordDoesNotBypassPenalty(t *testing.T) {
	// A single overlapping generic word should NOT trigger the containment
	// bypass (requires >=2 overlapping tokens) — guards against spurious
	// matches on common words like "scene" or "part".
	sim := TitleSimilarity("Scene", "Some Totally Different Long Title About A Scene")
	// containment for "Scene" alone would be 1.0 (fully contained), but with
	// only 1 overlapping token the bypass must not apply — falls back to
	// plain (low) Jaccard.
	if sim >= 0.7 {
		t.Fatalf("expected the single-word containment bypass to be blocked, got %f", sim)
	}
}

func TestTitleSimilarity_UnicodeWordCharacters(t *testing.T) {
	// Go's \w is ASCII-only; verifies the \p{L}\p{N}_ substitution keeps
	// accented words intact as a single token rather than fragmenting them.
	sim := TitleSimilarity("Café Scene", "Café Scene Part 2")
	if sim < 0.5 {
		t.Fatalf("expected accented word to tokenize correctly and match, got %f", sim)
	}
}
