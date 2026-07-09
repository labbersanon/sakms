// Package classify decides whether a candidate identification is
// kids-appropriate content, so Rename can route it to a mode's paired Kids
// root folder instead of the general one (see mode.Session.KidsRootPath).
//
// Structured metadata (certification + genre, from Sonarr/Radarr's own
// lookup response) is tried first; an AI fallback (see ai.go) is used only
// when that signal is missing or ambiguous.
package classify

import "strings"

// Signal is the rating/genre metadata available for a candidate
// identification, sourced from Sonarr/Radarr's own lookup response (see
// internal/servarr.LookupResult). Certification is only ever populated for
// movies — Sonarr's series/lookup has no certification field at all.
type Signal struct {
	Certification string
	Genres        []string
}

// Result is a kids/not-kids classification decision.
type Result struct {
	IsKids bool
	// Confident is false when the metadata signal was too weak/ambiguous to
	// trust — the caller should fall back to AI classification (see WithAI)
	// rather than act on this result directly.
	Confident bool
	Reason    string
}

// kidsCertifications are content ratings that unambiguously mean
// kids-appropriate (G/TV-Y family), sourced from the MPA and TV Parental
// Guidelines rating systems.
var kidsCertifications = map[string]bool{
	"g": true, "tv-y": true, "tv-y7": true, "tv-y7-fv": true, "tv-g": true,
}

// adultCertifications are ratings that unambiguously mean NOT kids content.
// Deliberately narrow: PG-13/TV-14 and similar "in between" ratings are
// common for perfectly ordinary family-adjacent content and are NOT treated
// as a confident adult signal — they fall through to the genre check below,
// and from there to the AI fallback if genres don't resolve it either.
var adultCertifications = map[string]bool{
	"r": true, "nc-17": true, "tv-ma": true, "x": true,
}

// kidsGenres are genre strings that alone are a confident kids signal.
var kidsGenres = map[string]bool{
	"kids": true, "children": true,
}

// FromMetadata makes a kids/not-kids decision from certification + genre
// alone, no AI involved.
func FromMetadata(sig Signal) Result {
	cert := strings.ToLower(strings.TrimSpace(sig.Certification))
	if kidsCertifications[cert] {
		return Result{IsKids: true, Confident: true, Reason: "certification " + sig.Certification}
	}
	if adultCertifications[cert] {
		return Result{IsKids: false, Confident: true, Reason: "certification " + sig.Certification}
	}

	hasFamily, hasAnimation := false, false
	for _, g := range sig.Genres {
		switch strings.ToLower(strings.TrimSpace(g)) {
		case "family":
			hasFamily = true
		case "animation":
			hasAnimation = true
		default:
			if kidsGenres[strings.ToLower(strings.TrimSpace(g))] {
				return Result{IsKids: true, Confident: true, Reason: "genre: " + g}
			}
		}
	}
	// Animation alone is NOT a confident kids signal (plenty of
	// adult-oriented animation exists) — only Family+Animation together is
	// treated as strong enough to act on without AI confirmation.
	if hasFamily && hasAnimation {
		return Result{IsKids: true, Confident: true, Reason: "genres: family + animation"}
	}

	return Result{IsKids: false, Confident: false, Reason: "no strong signal from certification/genres"}
}
