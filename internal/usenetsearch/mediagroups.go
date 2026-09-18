package usenetsearch

import (
	"context"
	"sort"
	"strings"

	"github.com/labbersanon/sakms/internal/usenet"
)

// MediaWildmats returns LIST ACTIVE wildmats for mode ("movies"|"series"|"adult").
func MediaWildmats(mode string) []string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "movies":
		return []string{
			"alt.binaries.movies*",
			"alt.binaries.multimedia*",
			"alt.binaries.x264*",
			"alt.binaries.bluray*",
			"alt.binaries.blu-ray*",
			"alt.binaries.uhd*",
			"alt.binaries.dvd*",
			"alt.binaries.warez*",
		}
	case "series":
		return []string{
			"alt.binaries.teevee*",
			"alt.binaries.tv*",
			"alt.binaries.hdtv*",
			"alt.binaries.series*",
			"alt.binaries.anime*",
			"alt.binaries.multimedia*",
		}
	case "adult":
		return []string{
			"alt.binaries.erotica*",
			"alt.binaries.boneless*",
			"alt.binaries.multimedia.erotica*",
			"alt.binaries.xxx*",
			"alt.binaries.pictures.erotica*",
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
