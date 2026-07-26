package identify

// maxSimilarity is TitleSimilarity made asymmetry-safe. TitleSimilarity's
// containment branch is arg-order-dependent (containment =
// inter/len(tokenize(first-arg)), see similarity.go:52), so
// TitleSimilarity(a,b) != TitleSimilarity(b,a) in general. Every comparison
// in this file goes through maxSimilarity rather than calling TitleSimilarity
// directly with a single fixed argument order — see PairByName's doc comment
// for why that footgun matters here specifically.
func maxSimilarity(a, b string) float64 {
	ab, ba := TitleSimilarity(a, b), TitleSimilarity(b, a)
	if ab > ba {
		return ab
	}
	return ba
}

// Pair is a single matched (A,B) index pair produced by PairByName, indexing
// back into the caller's original aNames/bNames slices.
type Pair struct {
	AIndex int
	BIndex int
}

// PairByName performs asymmetry-safe mutual-best greedy pairing between two
// name lists (e.g. TPDB performer/studio names vs StashDB performer/studio
// names for a merged Discover row — see the merge-TPDB-StashDB plan's Q1/Q5).
//
// Both lists are normalized via normalizeForSearch first (same
// separator-normalization every other name comparison in this package
// applies before search/similarity work). For each name on one side, the
// best-scoring candidate on the other side is found using
// maxSimilarity(a,b) — never a single fixed TitleSimilarity(a,b) call, since
// that function is arg-order-dependent (see maxSimilarity's doc comment). A
// pair is kept only when each side is ALSO the other's best match (mutual
// best): this stops one popular/generic name from absorbing several
// genuinely distinct entities on the other side, at the cost of a safe
// false-negative (two entities that both prefer a third over each other stay
// unpaired, shown separately) rather than a false-positive merge.
//
// threshold is the caller-supplied pairing bar — pass
// performerMatchThreshold/studioMatchThreshold (0.6, see the const block in
// entityverify.go) to reuse the exact tolerance already tuned there for the
// identical false-positive hazard (short person/studio names risk
// "confidently correcting" to a different real entity).
func PairByName(aNames, bNames []string, threshold float64) []Pair {
	if len(aNames) == 0 || len(bNames) == 0 {
		return nil
	}

	normA := make([]string, len(aNames))
	for i, a := range aNames {
		normA[i] = normalizeForSearch(a)
	}
	normB := make([]string, len(bNames))
	for j, b := range bNames {
		normB[j] = normalizeForSearch(b)
	}

	// bestForA[i] = index into normB of A[i]'s best candidate (score wins
	// with a strict '>' so ties keep the first-seen candidate, mirroring
	// bestMatch's tie-breaking in entityverify.go), or -1 if none clears
	// threshold.
	bestForA := make([]int, len(normA))
	for i, a := range normA {
		best, bestScore := -1, 0.0
		for j, b := range normB {
			if score := maxSimilarity(a, b); score > bestScore {
				bestScore, best = score, j
			}
		}
		if bestScore < threshold {
			best = -1
		}
		bestForA[i] = best
	}

	bestForB := make([]int, len(normB))
	for j, b := range normB {
		best, bestScore := -1, 0.0
		for i, a := range normA {
			if score := maxSimilarity(a, b); score > bestScore {
				bestScore, best = score, i
			}
		}
		if bestScore < threshold {
			best = -1
		}
		bestForB[j] = best
	}

	var pairs []Pair
	for i, j := range bestForA {
		if j >= 0 && bestForB[j] == i {
			pairs = append(pairs, Pair{AIndex: i, BIndex: j})
		}
	}
	return pairs
}

// PairPerformersByName is PairByName pinned to performerMatchThreshold
// (0.6) — the typed convenience wrapper callers outside this package (e.g. a
// merged-row handler in internal/api) should use so the tuned threshold
// stays single-sourced here rather than hardcoded/duplicated by a caller.
func PairPerformersByName(aNames, bNames []string) []Pair {
	return PairByName(aNames, bNames, performerMatchThreshold)
}

// PairStudiosByName is PairPerformersByName's studio-name sibling, pinned to
// studioMatchThreshold (0.6).
func PairStudiosByName(aNames, bNames []string) []Pair {
	return PairByName(aNames, bNames, studioMatchThreshold)
}
