package usenetsearch

import (
	"context"
	"sort"
	"strings"

	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-16: mode-scoped LIST ACTIVE wildmats = crawl membership.
// Reason: every provider group matching these patterns applies to the mode;
//   packages.json is badges only. Scene bins added so movies/series are not
//   stuck on movies*/teevee* alone.
// Troubleshooting: a wanted group missing → add a wildmat here (not seeds).
// Review if: a pattern pulls warez/spam hierarchies (warez* was dropped).

// sceneBinWildmats carry both movies and series but are not matched by the
// mode-named hierarchies (movies*/moovee*/teevee*).
var sceneBinWildmats = []string{
	"alt.binaries.inner-sanctum*",
	"alt.binaries.ath*",
	"alt.binaries.cores*",
	"alt.binaries.ghosts*",
	"alt.binaries.ftn*",
	"alt.binaries.amazing*",
	"alt.binaries.bloaf*",
	"alt.binaries.u4e*",
}

// MediaWildmats returns LIST ACTIVE wildmats for mode ("movies"|"series"|"adult").
func MediaWildmats(mode string) []string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "movies":
		// Claude 2026-10-15: drop warez*; add moovee (exact-friendly wildmat).
		// Reason: warez is software noise; moovee is a primary movie binary group
		//   missing from the old list. Recommended seeds filter against LIST ACTIVE.
		// Troubleshooting: empty Movies picker — provider wildmat miss.
		// Review if: Discover titles replace wildmat LIST ACTIVE entirely.
		return append([]string{
			"alt.binaries.movies*",
			"alt.binaries.moovee*",
			"alt.binaries.multimedia*",
			"alt.binaries.x264*",
			"alt.binaries.hdtv.x264*",
			"alt.binaries.bluray*",
			"alt.binaries.blu-ray*",
			"alt.binaries.uhd*",
			"alt.binaries.dvd*",
		}, sceneBinWildmats...)
	case "series":
		return append([]string{
			"alt.binaries.teevee*",
			"alt.binaries.tv*",
			"alt.binaries.hdtv*",
			"alt.binaries.series*",
			"alt.binaries.anime*",
			"alt.binaries.multimedia*",
		}, sceneBinWildmats...)
	case "adult":
		return []string{
			"alt.binaries.erotica*",
			"alt.binaries.boneless*",
			"alt.binaries.multimedia.erotica*",
			"alt.binaries.xxx*",
			"alt.binaries.pictures.erotica*",
			"alt.binaries.mom*",
			"alt.binaries.adult*",
		}
	default:
		return nil
	}
}

func IsMediaGroup(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, "control.") {
		return false
	}
	low := strings.ToLower(name)
	return strings.Contains(low, "binaries") || strings.Contains(low, "multimedia")
}

func ListMediaGroups(ctx context.Context, src usenet.HeaderSource, mode string) ([]string, error) {
	if src == nil {
		return nil, nil
	}
	wildmats := MediaWildmats(mode)
	if len(wildmats) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, wm := range wildmats {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		names, err := src.ListActive(ctx, wm)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if !IsMediaGroup(n) || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func FormatGroups(groups []string) string {
	return strings.Join(groups, "\n")
}

func MergeGroups(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, g := range list {
			g = strings.TrimSpace(g)
			if g == "" || seen[g] {
				continue
			}
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}
