package disc

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Claude 2026-10-02: unique duration → TMDB episode (Phase 5b).
// Reason: IFO has length and order, not episode names. Auto-assign only
//   when one work and one catalog episode share a duration no one else has.
//   Do not pair by count or filename tokens (03,01 is not E03-E01).
// Troubleshooting: no pre-fill — TMDB runtime is 0 or several episodes share
//   the same minute length (typical for shorts).
// Review if: IFO/ffmpeg starts exposing per-title names.

// DurationSlopS is how close a disc title must be to TMDB runtime (minutes×60).
const DurationSlopS = 25

// CatalogEpisode is one TMDB (or test) episode used for unique matching.
type CatalogEpisode struct {
	Season     int
	Episode    int
	Title      string
	RuntimeMin int
}

// MatchUniqueSlots maps work names to catalog episodes when the duration
// hit is unique in both directions. workDur is seconds per PlanWorks name.
func MatchUniqueSlots(workDur map[string]float64, catalog []CatalogEpisode) map[string]CatalogEpisode {
	if len(workDur) == 0 || len(catalog) == 0 {
		return nil
	}
	workHits := map[string][]int{}
	for name, dur := range workDur {
		if dur <= 0 {
			continue
		}
		for i, ep := range catalog {
			if durationHit(dur, ep.RuntimeMin) {
				workHits[name] = append(workHits[name], i)
			}
		}
	}
	claimed := map[int][]string{}
	for name, idxs := range workHits {
		if len(idxs) != 1 {
			continue
		}
		claimed[idxs[0]] = append(claimed[idxs[0]], name)
	}
	out := map[string]CatalogEpisode{}
	for idx, names := range claimed {
		if len(names) != 1 {
			continue
		}
		out[names[0]] = catalog[idx]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func durationHit(discS float64, runtimeMin int) bool {
	if runtimeMin <= 0 || discS <= 0 {
		return false
	}
	return math.Abs(discS-float64(runtimeMin)*60) <= DurationSlopS
}

// MatchUniqueTitles maps work names to catalog episodes when the normalized
// title is unique in both directions. Do not pair by disc order into the
// series catalog — Golden shorts are not TMDB airdate order.
func MatchUniqueTitles(workTitle map[string]string, catalog []CatalogEpisode) map[string]CatalogEpisode {
	if len(workTitle) == 0 || len(catalog) == 0 {
		return nil
	}
	workHits := map[string][]int{}
	for name, title := range workTitle {
		key := normTitle(title)
		if key == "" {
			continue
		}
		for i, ep := range catalog {
			if normTitle(ep.Title) == key {
				workHits[name] = append(workHits[name], i)
			}
		}
	}
	claimed := map[int][]string{}
	for name, idxs := range workHits {
		if len(idxs) != 1 {
			continue
		}
		claimed[idxs[0]] = append(claimed[idxs[0]], name)
	}
	out := map[string]CatalogEpisode{}
	for idx, names := range claimed {
		if len(names) != 1 {
			continue
		}
		out[names[0]] = catalog[idx]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// UniqueNamedHit is the index of want in titles when the normalized name
// matches exactly one entry. Remakes that share a title are not unique.
func UniqueNamedHit(want string, titles []string) (int, bool) {
	key := normTitle(want)
	if key == "" {
		return 0, false
	}
	found := -1
	for i, title := range titles {
		if normTitle(title) != key {
			continue
		}
		if found >= 0 {
			return 0, false
		}
		found = i
	}
	if found < 0 {
		return 0, false
	}
	return found, true
}

// MergeCatalogByTitle keeps one episode per normalized title. Override
// wins — TVDB official year-seasons replace the TMDB airdate slot when
// both catalogs name the same short.
func MergeCatalogByTitle(base, override []CatalogEpisode) []CatalogEpisode {
	byKey := map[string]CatalogEpisode{}
	var order []string
	add := func(ep CatalogEpisode, replace bool) {
		key := normTitle(ep.Title)
		if key == "" {
			key = ":" + strconv.Itoa(ep.Season) + ":" + strconv.Itoa(ep.Episode)
		}
		if _, ok := byKey[key]; !ok {
			order = append(order, key)
			byKey[key] = ep
			return
		}
		if replace {
			byKey[key] = ep
		}
	}
	for _, ep := range base {
		add(ep, false)
	}
	for _, ep := range override {
		add(ep, true)
	}
	out := make([]CatalogEpisode, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return out
}

func normTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "’", "'")
	s = strings.ReplaceAll(s, "‘", "'")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	s = strings.Join(strings.Fields(b.String()), " ")
	for _, p := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, p) {
			s = strings.TrimSpace(s[len(p):])
			break
		}
	}
	return s
}
