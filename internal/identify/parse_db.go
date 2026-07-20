package identify

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"github.com/labbersanon/sakms/internal/parseentity"
)

// ParseFilenameDB is a deterministic, zero-AI filename parser that extracts
// studio, performers, title, and year from a filename stem by matching tokens
// against the entity cache (internal/parseentity). It is the primary parser
// in the identification pipeline when an entity store is configured; ParseFilename
// (the AI path) runs only as an optional BYOAI fallback when key fields are
// still empty after this returns.
//
// Algorithm:
//  1. Year extracted deterministically (reuses ExtractYearFromToken).
//  2. Stem tokenized on [._\-\s]+, noise tokens stripped.
//  3. parentName tried as studio hint (high confidence).
//  4. Studio matched from start of tokens, sliding window 1–4.
//  5. Performers matched greedily in remaining tokens, window 1–3.
//  6. Title = unconsumed tokens, title-cased.
func ParseFilenameDB(ctx context.Context, stem, parentName string, store parseentity.EntityStore) (ParsedFilename, error) {
	// Extract year from raw split tokens BEFORE noise-stripping: stripNoiseTokens
	// removes year-shaped tokens (reYear strips ^(19|20)\d{2}$), so scanning
	// per-token on the raw list avoids both false matches on catalog-number
	// substrings and premature year removal.
	tokens := splitTokens(stem)
	year := extractYearFromTokens(tokens)
	if year == "" {
		year = extractYearFromTokens(splitTokens(parentName))
	}
	tokens = stripNoiseTokens(tokens)

	// Try parentName as a studio hint first (folder name = studio is reliable).
	studio := ""
	if parentName != "" {
		if name, ok, _ := store.LookupStudio(ctx, parseentity.NormName(parentName)); ok {
			studio = name
		}
	}

	remaining := tokens
	if studio == "" {
		// Try sliding window from the start of the token list (largest first).
		for winSize := 4; winSize >= 1; winSize-- {
			if len(tokens) < winSize {
				continue
			}
			key := parseentity.NormName(strings.Join(tokens[:winSize], ""))
			if name, ok, _ := store.LookupStudio(ctx, key); ok {
				studio = name
				remaining = tokens[winSize:]
				break
			}
		}
	} else {
		// parentName matched; still try to strip a leading studio prefix from
		// the stem tokens so it doesn't become part of the title.
		studioNorm := parseentity.NormName(studio)
		for winSize := 4; winSize >= 1; winSize-- {
			if len(tokens) < winSize {
				continue
			}
			if parseentity.NormName(strings.Join(tokens[:winSize], "")) == studioNorm {
				remaining = tokens[winSize:]
				break
			}
		}
	}

	// Match performers greedily in remaining tokens; consume matched windows.
	consumed := make([]bool, len(remaining))
	var performers []string
	for i := 0; i < len(remaining); i++ {
		for winSize := 3; winSize >= 1; winSize-- {
			if i+winSize > len(remaining) {
				continue
			}
			key := parseentity.NormName(strings.Join(remaining[i:i+winSize], ""))
			if name, ok, _ := store.LookupPerformer(ctx, key); ok {
				performers = append(performers, name)
				for j := i; j < i+winSize; j++ {
					consumed[j] = true
				}
				i += winSize - 1
				break
			}
		}
	}

	// Title = remaining unconsumed tokens.
	var titleTokens []string
	for i, t := range remaining {
		if !consumed[i] {
			titleTokens = append(titleTokens, t)
		}
	}
	// Drop conjunction tokens that only existed as performer separators.
	titleTokens = dropConjunctions(titleTokens)
	title := toTitleCase(strings.Join(titleTokens, " "))

	return ParsedFilename{
		Studio:     studio,
		Title:      title,
		Year:       year,
		Performers: performers,
	}, nil
}

// extractYearFromTokens scans individual tokens for the first date-bearing one
// and returns its 4-digit year string, or "" if none is found. Operating on
// discrete tokens (rather than the whole raw stem) prevents catalog numbers
// like "ep20191" from matching the year pattern as a substring.
func extractYearFromTokens(tokens []string) string {
	for _, t := range tokens {
		if y := ExtractYearFromToken(t); y != "" {
			return y
		}
	}
	return ""
}

// splitTokens splits a filename stem on dots, underscores, hyphens, and
// spaces. Empty tokens are discarded.
func splitTokens(s string) []string {
	return reSplit.Split(s, -1)
}

var reSplit = regexp.MustCompile(`[._\-\s]+`)

var (
	reResolution = regexp.MustCompile(`(?i)^(2160|1080|720|480|4k|uhd|fhd|hd)p?$`)
	// Numeric IDs: pure digits, or short letter prefix + 3+ digits (bb13671, ep01, s01e02).
	reNumericID = regexp.MustCompile(`(?i)^[a-z]{0,4}\d{3,}$`)
	// TLD fragments that appear when a domain-style studio prefix is split on dots.
	reTLD     = regexp.MustCompile(`(?i)^(com|net|org|to|me|xxx|tv|co)$`)
	reQuality = regexp.MustCompile(`(?i)^(x264|x265|hevc|avc|aac|mp4|mkv|avi|wmv|mov|wmv)$`)
	// 4-digit year tokens are handled by ExtractYearFromToken; strip them from
	// the token list so they don't pollute the title.
	reYear = regexp.MustCompile(`^(19|20)\d{2}$`)
)

var noiseWords = map[string]bool{
	"xxx": true, "web": true, "dl": true, "rip": true,
	"bluray": true, "bdrip": true, "dvdrip": true, "hdrip": true,
	"fullhd": true, "uncensored": true,
}

// conjunctionWords are dropped from the unconsumed title tokens when they
// appear adjacent to consumed performer windows — they only existed as
// separators between performer names.
var conjunctionWords = map[string]bool{
	"and": true, "with": true, "feat": true, "ft": true,
}

func stripNoiseTokens(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for i, t := range tokens {
		tl := strings.ToLower(t)
		switch {
		case reResolution.MatchString(t):
		case reNumericID.MatchString(t):
		case reTLD.MatchString(t) && i < 3: // domain fragment near start only
		case reQuality.MatchString(t):
		case reYear.MatchString(t):
		case noiseWords[tl]:
		default:
			out = append(out, t)
		}
	}
	return out
}

func dropConjunctions(tokens []string) []string {
	// Only drop if all remaining tokens are conjunctions — avoids stripping
	// "with" from a real title like "With You Tonight".
	if len(tokens) == 0 {
		return tokens
	}
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if !conjunctionWords[strings.ToLower(t)] {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return tokens // don't wipe a title that was entirely conjunctions
	}
	return out
}

// toTitleCase title-cases each word, lower-casing the rest of the letters.
// strings.Title is deprecated; this avoids the golang.org/x/text dependency.
func toTitleCase(s string) string {
	if s == "" {
		return ""
	}
	words := strings.Fields(s)
	for i, w := range words {
		runes := []rune(w)
		if len(runes) == 0 {
			continue
		}
		runes[0] = unicode.ToUpper(runes[0])
		for j := 1; j < len(runes); j++ {
			runes[j] = unicode.ToLower(runes[j])
		}
		words[i] = string(runes)
	}
	return strings.Join(words, " ")
}
