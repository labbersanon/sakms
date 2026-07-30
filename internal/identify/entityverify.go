package identify

import (
	"context"
	"strings"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// performerMatchThreshold/studioMatchThreshold: person/studio names are
// short (often just 1-2 tokens), so the scene-title similarity threshold
// (0.4, tuned for long multi-word titles) is too loose here — a looser bar
// risks confidently "correcting" a name to a different, similarly-spelled
// real performer/studio. Bumped to require most/all tokens to actually
// overlap.
const (
	performerMatchThreshold = 0.6
	studioMatchThreshold    = 0.6

	// nameCollapseTightness is the bar a merged-row pair's two source names
	// (see PairByName in pair.go) must clear to collapse into a single
	// displayed name rather than surfacing both ("TPDB: X / StashDB: Y") for
	// operator review. Deliberately tighter than the 0.6 pairing thresholds
	// above: a pair can clear 0.6 (worth showing as one merged card) while
	// still differing enough that silently picking one name would hide a
	// possible false-positive merge from the operator. Single-sourced here,
	// next to the other tuned magic numbers in this package, rather than in
	// whatever caller consumes it.
	nameCollapseTightness = 0.9
)

// normalizedEqual reports whether a and b are equal after normalizeForSearch
// (the same separator normalization applied before every other
// search/similarity comparison in this package), compared case-insensitively.
// A fast, cheap check that's strictly weaker than nearExactName's full
// similarity check below.
func normalizedEqual(a, b string) bool {
	return strings.EqualFold(normalizeForSearch(a), normalizeForSearch(b))
}

// nearExactName reports whether a and b are close enough to be the same
// entity's name for collapse-to-one-name purposes (vs. surfacing both — see
// PairByName's caller in the merge-TPDB-StashDB plan's Q3). Asymmetry-safe:
// goes through maxSimilarity (pair.go) rather than a single fixed-order
// TitleSimilarity call, since TitleSimilarity's containment branch is
// arg-order-dependent (similarity.go:52).
func nearExactName(a, b string) bool {
	return normalizedEqual(a, b) || maxSimilarity(a, b) >= nameCollapseTightness
}

// NearExactName is nearExactName's exported form, for callers outside this
// package (e.g. a merged-row handler in internal/api deciding whether to
// collapse a paired TPDB/StashDB name to one displayed name or surface both —
// see PairByName's doc comment and the merge-TPDB-StashDB plan's Q3). Keeps
// the tuned nameCollapseTightness constant and the asymmetry-safe comparison
// single-sourced in this package rather than re-implemented by a caller.
func NearExactName(a, b string) bool {
	return nearExactName(a, b)
}

// normalizeForSearch replaces filename-style separators (dots, dashes,
// underscores) with spaces before using an AI-extracted guess as a search
// term. TitleSimilarity's own tokenizer already splits on dots/dashes, but
// NOT underscores (treated as part of a word character, see similarity.go's
// wordRe) — and a raw dotted/underscored string sent as a literal search
// term to StashDB/FansDB/TPDB's own search may not match as well
// server-side as a clean, space-separated one. A cheap, deterministic
// transform — not dependent on the AI reliably doing this itself (see
// ParseFilename's separator-normalization prompt guidance, which this
// backstops rather than replaces: prompt-level formatting proved unreliable
// under repeated live testing, see CHANGELOG.md).
func normalizeForSearch(s string) string {
	replacer := strings.NewReplacer(".", " ", "-", " ", "_", " ")
	return strings.Join(strings.Fields(replacer.Replace(s)), " ")
}

// bestMatch returns the candidate name with the highest TitleSimilarity to
// guess, if that score clears threshold — or ("", false) if nothing does.
func bestMatch(guess string, candidateNames []string, threshold float64) (string, bool) {
	bestName := ""
	bestScore := 0.0
	for _, name := range candidateNames {
		if score := TitleSimilarity(guess, name); score > bestScore {
			bestScore = score
			bestName = name
		}
	}
	if bestScore >= threshold {
		return bestName, true
	}
	return "", false
}

func performerNames(performers []stashbox.Performer) []string {
	out := make([]string, len(performers))
	for i, p := range performers {
		out[i] = p.Name
	}
	return out
}

func tpdbPerformerNames(performers []tpdbrest.Performer) []string {
	out := make([]string, len(performers))
	for i, p := range performers {
		out[i] = p.Name
	}
	return out
}

func tpdbSiteNames(sites []tpdbrest.Site) []string {
	out := make([]string, len(sites))
	for i, s := range sites {
		out[i] = s.Name
	}
	return out
}

// studioDenylistExact are known non-studio tokens the AI has been observed
// (via live testing against real filenames) to mistake for a studio name —
// content-rating placeholders, not real production companies. Matched
// case-insensitively.
var studioDenylistExact = map[string]bool{
	"xxx":  true,
	"xxxx": true,
}

// looksLikeReleaseGroupTag reports whether cleaned has the shape of a scene
// release-group tag (e.g. "WRB", "NBQ", "KTR") rather than a studio name:
// short and entirely uppercase letters. This is a real observed failure mode
// (see sakms_adult_ai_identification.md — the AI returned exactly this shape
// of token as "studio" on 2/8 real test files), but it only gates the
// last-resort fallback in verifyStudio below: a genuine short-acronym studio
// name would already have matched via StashDB/FansDB/TPDB earlier in that
// function, so this heuristic only ever fires for a guess that ALSO failed
// real-database verification.
func looksLikeReleaseGroupTag(cleaned string) bool {
	if len(cleaned) < 2 || len(cleaned) > 5 {
		return false
	}
	for _, r := range cleaned {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// rejectNonStudioGuess returns "" instead of a cleaned guess that looks like
// a content-rating tag or release-group tag rather than a real studio name —
// better to leave Studio empty than confidently wrong, the same "decline
// rather than fabricate" principle GuessTitle's escape valve already applies
// to mainstream titles.
func rejectNonStudioGuess(cleaned string) string {
	if studioDenylistExact[strings.ToLower(cleaned)] || looksLikeReleaseGroupTag(cleaned) {
		return ""
	}
	return cleaned
}

// verifyStudio checks an AI-guessed studio name (ParseFilename's raw
// extraction) against StashDB/FansDB/TPDB and returns the database's own
// canonical name where a confident match exists — correctness comes from
// real data instead of hoping the AI formats text perfectly. Falls back to
// a deterministically-cleaned version of the guess (still better than the
// AI's raw, possibly dot/underscore-separated text) if nothing matches,
// unless that guess itself looks like a content-rating or release-group tag
// (see rejectNonStudioGuess) — a completely wrong entity guess can't be
// fixed by fuzzy-matching against real studio names, so it's rejected
// outright rather than passed through. Every external call goes through the
// same per-host throttle every other StashDB/FansDB/TPDB call in this
// package uses, and the same FansDB fansite-hint gate as searchInternalDBs
// (see IsFansiteHinted's doc comment) — querying FansDB's mostly-generic-clip
// catalog for every mainstream file's studio/performers would be both
// wasteful and prone to spurious corrections.
func (id *Identifier) verifyStudio(ctx context.Context, guess, stem string) string {
	if guess == "" {
		return guess
	}
	cleaned := normalizeForSearch(guess)

	boxes := []string{"stashdb"}
	if IsFansiteHinted(stem, guess) {
		boxes = append(boxes, "fansdb")
	}
	for _, box := range boxes {
		client := id.Boxes.stashBoxes[box]
		if client == nil {
			continue
		}
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return rejectNonStudioGuess(cleaned)
		}
		studio, err := client.FindStudio(ctx, cleaned)
		if err != nil {
			continue // best-effort: try the next box rather than aborting verification
		}
		if studio != nil && studio.Name != "" {
			return studio.Name
		}
	}

	if id.Boxes.tpdb != nil {
		if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
			return rejectNonStudioGuess(cleaned)
		}
		if candidates, err := id.Boxes.tpdb.SearchSites(ctx, cleaned); err == nil {
			if best, ok := bestMatch(cleaned, tpdbSiteNames(candidates), studioMatchThreshold); ok {
				return best
			}
		}
	}

	return rejectNonStudioGuess(cleaned)
}

// verifyPerformers is verifyStudio's sibling for the performers array —
// each guess is independently checked/corrected the same way. studioGuess
// is the same fansite-hint signal searchInternalDBs and verifyStudio use
// (the raw AI studio guess, not yet DB-corrected — the hint only cares
// about keyword presence, not canonical spelling).
func (id *Identifier) verifyPerformers(ctx context.Context, guesses []string, stem, studioGuess string) []string {
	out := make([]string, len(guesses))
	for i, g := range guesses {
		out[i] = id.verifyOnePerformer(ctx, g, stem, studioGuess)
	}
	return out
}

func (id *Identifier) verifyOnePerformer(ctx context.Context, guess, stem, studioGuess string) string {
	if guess == "" {
		return guess
	}
	cleaned := normalizeForSearch(guess)

	boxes := []string{"stashdb"}
	if IsFansiteHinted(stem, studioGuess) {
		boxes = append(boxes, "fansdb")
	}
	for _, box := range boxes {
		client := id.Boxes.stashBoxes[box]
		if client == nil {
			continue
		}
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return cleaned
		}
		candidates, err := client.SearchPerformer(ctx, cleaned, 5)
		if err != nil {
			continue
		}
		if best, ok := bestMatch(cleaned, performerNames(candidates), performerMatchThreshold); ok {
			return best
		}
	}

	if id.Boxes.tpdb != nil {
		if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
			return cleaned
		}
		if candidates, err := id.Boxes.tpdb.SearchPerformers(ctx, cleaned); err == nil {
			if best, ok := bestMatch(cleaned, tpdbPerformerNames(candidates), performerMatchThreshold); ok {
				return best
			}
		}
	}

	return cleaned
}

// StudioImage looks up an already-corrected studio name's poster art across
// configured boxes (StashDB/FansDB first, then TPDB) — a display-only
// follow-up for callers (e.g. internal/adultnewest's Studio row) that already
// have a name from verifyStudio, which discards the image after using it
// purely for name correction. Returns the image URL and which box it came
// from ("stashdb" | "fansdb" | "tpdb") — the source matters to callers that
// build an external link from it (a wrong source label would build a link to
// the wrong site), so this deliberately doesn't collapse box identity away
// the way a bare image-URL-only return would. Best-effort: returns ("", "")
// if nothing found, no box is configured, or the throttle wait is cancelled
// — never an error, since a missing poster shouldn't fail the caller's whole
// match.
func (id *Identifier) StudioImage(ctx context.Context, name string) (image, source string) {
	if name == "" {
		return "", ""
	}
	for _, box := range []string{"stashdb", "fansdb"} {
		client := id.Boxes.stashBoxes[box]
		if client == nil {
			continue
		}
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return "", ""
		}
		studio, err := client.FindStudio(ctx, name)
		if err == nil && studio != nil && studio.ImageURL != "" {
			return studio.ImageURL, box
		}
	}
	if id.Boxes.tpdb != nil {
		if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
			return "", ""
		}
		if candidates, err := id.Boxes.tpdb.SearchSites(ctx, name); err == nil {
			for _, s := range candidates {
				if s.Name == name && s.Image != "" {
					return s.Image, "tpdb"
				}
			}
		}
	}
	return "", ""
}

// PerformerImage is StudioImage's performer analogue, with one addition:
// gender is threaded from the matched box object's own Gender field
// (normalized via normalizeGender), since a performer identity carries
// gender where a studio never does — StudioImage is deliberately left
// unchanged (studios have no gender concept). Every return path — including
// the two "nothing found" failure paths — returns an empty gender alongside
// the empty image/source, matching image/source's own all-empty-on-failure
// contract.
func (id *Identifier) PerformerImage(ctx context.Context, name string) (image, source, gender string) {
	if name == "" {
		return "", "", ""
	}
	for _, box := range []string{"stashdb", "fansdb"} {
		client := id.Boxes.stashBoxes[box]
		if client == nil {
			continue
		}
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return "", "", ""
		}
		candidates, err := client.SearchPerformer(ctx, name, 5)
		if err == nil {
			for _, p := range candidates {
				if p.Name == name && p.ImageURL != "" {
					return p.ImageURL, box, normalizeGender(p.Gender)
				}
			}
		}
	}
	if id.Boxes.tpdb != nil {
		if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
			return "", "", ""
		}
		if candidates, err := id.Boxes.tpdb.SearchPerformers(ctx, name); err == nil {
			for _, p := range candidates {
				if p.Name == name && p.Image != "" {
					return p.Image, "tpdb", normalizeGender(p.Gender)
				}
			}
		}
	}
	return "", "", ""
}

// PerformerGender is PerformerImage's gender analogue, but with the one
// distinction the gender backfill (internal/adultnewest) needs and
// PerformerImage cannot provide: PerformerImage collapses ALL THREE outcomes
// (throttle-abort, box error, genuine reached-but-no-match) to ("","",""), so
// a caller can't tell a transient box outage apart from "this performer has no
// gender on file." That ambiguity is fatal for a backfill that persists a
// result: writing "" on a transient failure would burn a retryable row
// permanently. So this resolver reports the two cases distinguishably:
//
//	(g,  nil)  g possibly "" → a box was REACHED and returned that gender
//	                           ("" == reached, but no gender on file for this name)
//	("", err)                → throttle-abort / box error / every box unreachable.
//	                           NOT a negative answer — the caller MUST leave the row NULL.
//
// Per-box behavior mirrors PerformerImage's box-iteration loop exactly, only
// the outcome semantics differ: a Throttle.Wait error returns ("", err)
// immediately (a cancelled context must abort, not be read as "no gender"); a
// Search error is remembered as lastErr and the next box is tried; a
// name-matched candidate returns (normalizeGender(p.Gender), nil) — matched on
// name ALONE (unlike PerformerImage, which additionally requires a non-empty
// image, since gender, not art, is what this resolver is after); all boxes
// reached with no match returns ("", nil); and if no box was ever reached
// (every configured box errored) the remembered lastErr is returned so the
// caller leaves the row NULL. The reached flag is what separates "reached, no
// gender" (write "") from "never reached" (leave NULL): a mix (one box reached
// with no match, another errored) counts as reached and returns ("", nil), and
// the vacuous zero-configured-boxes case likewise returns ("", nil).
func (id *Identifier) PerformerGender(ctx context.Context, name string) (gender string, err error) {
	if name == "" {
		return "", nil
	}
	var lastErr error
	reached := false
	for _, box := range []string{"stashdb", "fansdb"} {
		client := id.Boxes.stashBoxes[box]
		if client == nil {
			continue
		}
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return "", err
		}
		candidates, err := client.SearchPerformer(ctx, name, 5)
		if err != nil {
			lastErr = err
			continue // best-effort: remember and try the next box
		}
		reached = true
		for _, p := range candidates {
			if p.Name == name {
				return normalizeGender(p.Gender), nil
			}
		}
	}
	if id.Boxes.tpdb != nil {
		if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
			return "", err
		}
		candidates, err := id.Boxes.tpdb.SearchPerformers(ctx, name)
		if err != nil {
			lastErr = err
		} else {
			reached = true
			for _, p := range candidates {
				if p.Name == name {
					return normalizeGender(p.Gender), nil
				}
			}
		}
	}
	if !reached && lastErr != nil {
		return "", lastErr
	}
	return "", nil
}

// normalizeGender maps a raw upstream gender token (TPDB extras.gender free
// string, or StashDB GenderEnum name) to "female" or "male" — everything
// else (unset, empty, "transgender_female", "transgender_male", "intersex",
// "non_binary", "non-binary", "trans female", "unknown", or any unrecognized
// token) normalizes to "" and is excluded from any gender-split view by
// design (documented simplification, not a silent guess). Lifted
// byte-for-byte from internal/adultmerge.normalizeGender (that package is
// deleted by a parallel US-2 story in this same redesign effort — this
// helper is what replaces it) so downstream values match what the old
// merged cards produced — same EXACT-MATCH-after-lower+trim contract, never
// substring/Contains (see that function's original doc comment for the
// "TRANSGENDER_FEMALE contains female" trap this guards against).
func normalizeGender(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "female":
		return "female"
	case "male":
		return "male"
	default:
		return ""
	}
}
