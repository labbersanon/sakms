package api

// releaseDedupKey is the minimal shape the shared dedup/winner-selection reduces
// any release-like item to. downloadURL and normalizedTitle are matched as TWO
// INDEPENDENT criteria (not title-as-URL-empty-fallback); seeders drives
// winner selection.
type releaseDedupKey struct {
	downloadURL     string
	normalizedTitle string
	seeders         int
}

// dedupeReleases collapses items that represent the same release, keeping the
// single highest-seeder survivor of each duplicate group. Two items are the same
// release when EITHER their (non-empty) downloadURL matches OR their (non-empty)
// normalizedTitle matches. It ONLY ever selects whole items — it never mutates
// or reconstructs any field of a survivor — which is what makes grab-safety
// (ReleaseTitle/DownloadURL/Protocol/SizeBytes preserved) automatic. Order is
// stable: a survivor holds the position of the first occurrence of its group;
// ties (equal seeders) keep the first occurrence. Always returns a non-nil slice
// so an empty result JSON-encodes as [] not null.
//
// Load-bearing premise: a non-empty downloadURL uniquely identifies one release.
// This is what prevents a bridge item {urlA, titleB} from coexisting with a
// distinct {urlA, titleA} and chaining unrelated items together, and is also what
// makes the distinct-quality-variant invariant airtight — distinct-quality
// releases share neither URL nor normalized title, so no ordering (including
// replacement-promotion) can merge them.
func dedupeReleases[T any](items []T, keyOf func(T) releaseDedupKey) []T {
	out := make([]T, 0, len(items))
	survivors := make([]releaseDedupKey, 0, len(items)) // parallel to out
	byURL := map[string]int{}                           // non-empty downloadURL -> index in out
	byTitle := map[string]int{}                         // non-empty normalizedTitle -> index in out

	for _, item := range items {
		k := keyOf(item)
		idx := -1
		if k.downloadURL != "" {
			if j, ok := byURL[k.downloadURL]; ok {
				idx = j
			}
		}
		if idx == -1 && k.normalizedTitle != "" {
			if j, ok := byTitle[k.normalizedTitle]; ok {
				idx = j
			}
		}

		switch {
		case idx == -1: // new group
			out = append(out, item)
			survivors = append(survivors, k)
			i := len(out) - 1
			if k.downloadURL != "" {
				byURL[k.downloadURL] = i
			}
			if k.normalizedTitle != "" {
				byTitle[k.normalizedTitle] = i
			}
		case k.seeders > survivors[idx].seeders: // strict > : ties keep first
			out[idx] = item
			survivors[idx] = k
			// The winner may carry a different URL/title, so a later cross-post
			// matching only the winner's fields is still caught.
			if k.downloadURL != "" {
				byURL[k.downloadURL] = idx
			}
			if k.normalizedTitle != "" {
				byTitle[k.normalizedTitle] = idx
			}
			// else: drop (lower-or-equal seeders)
		}
	}

	return out
}
