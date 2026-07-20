package api

import (
	"context"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
)

// titleSimilarityFloor mirrors internal/identify.ExtractFromSearch's own
// reject threshold (< 0.2) — the same deterministic similarity cut already
// tuned for exactly this "messy scene-name vs. known canonical title"
// comparison, not a new number invented here.
const titleSimilarityFloor = 0.2

// AI-escalation bounds — found missing entirely in an earlier version of
// this function, which caused a real production hang: a plain sequential
// loop over EVERY raw release (no count cap, no concurrency, no phase
// deadline beyond the shared httpClient's already-generous 15s per-call
// timeout) meant a search returning 20-30 releases with a fast-path miss
// could block the request for several minutes, and the frontend's fetch
// wrapper has no client-side timeout either — a real "selecting titles
// hangs checking availability" bug, not a hypothetical one. These three
// bounds compose to cap the whole phase's worst case regardless of how
// many releases came back or how slow any individual AI call is:
// ceil(maxAIEscalationCandidates/aiEscalationConcurrency) batches, each
// bounded by aiEscalationTimeout (which cancels in-flight HTTP requests
// via context — every AIClient implementation already uses
// http.NewRequestWithContext, so this isn't just a client-side abandon).
const (
	// maxAIEscalationCandidates bounds how many raw releases ever get an AI
	// call — escalating every release in a large result set defeats "keep
	// the common case fast." Only the first N (Prowlarr's own relevance
	// ordering) are checked; this is a documented tradeoff, not a claim that
	// AI-checking more would never help.
	maxAIEscalationCandidates = 10
	// aiEscalationConcurrency bounds how many AI calls run at once.
	aiEscalationConcurrency = 4
)

// aiEscalationTimeout is a hard ceiling on the WHOLE escalation phase —
// independent of the two bounds above, so even a pathological case (every
// call slow) can't block the request past this. A var, not a const, purely
// so a test can shrink it (save/restore) to prove the deadline is actually
// enforced without a real ~20s test run.
var aiEscalationTimeout = 20 * time.Second

// languageTagPattern is a small, explicit, deterministic token list marking
// a release title as carrying a non-English language tag — English is the
// assumed unmarked default (the same convention scene-release naming already
// uses), so a release is only rejected when one of these tags is actually
// present in the title, never guessed absent. This is NOT a user-facing
// preference/setting (the plan is explicit: "don't build speculative config
// ahead of proven need") — easy to make configurable later if it's ever
// wrong for someone. Word-boundary matched, case-insensitive, mirroring
// internal/release.Parse's own regexp convention for title-token matching.
//
// Deliberately does NOT include "multi": an earlier version of this list
// did, which was a real bug (found via a "nothing is being found to grab"
// report) — MULTI in scene-release naming means "multiple audio tracks
// bundled," not "no English track." For the English-original content this
// app targets (TMDB movies/shows), a MULTI release routinely still includes
// English as one of the bundled tracks, unlike FRENCH/GERMAN/etc., which
// really do mean "this release's only audio is that other language."
// Treating MULTI the same as those was silently excluding good releases.
var languageTagPattern = regexp.MustCompile(`(?i)\b(french|german|spanish|italian|vostfr|russian|hindi|korean|japanese)\b`)

// hasLanguageTag reports whether title carries one of languageTagPattern's
// non-English tags.
func hasLanguageTag(title string) bool {
	return languageTagPattern.MatchString(title)
}

// titleWordPattern splits a title into word tokens for singleWordTitleMatches
// — deliberately a plain local regex rather than reusing
// internal/identify's unexported tokenize, since that package's tokenizer
// also does camelCase-boundary splitting this simpler check doesn't need.
var titleWordPattern = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// singleWordTitleMatches is a narrow fallback for a real "nothing is being
// found to grab" report (a search for "Moana" — a single-word title —
// returned nothing at all, even for an exact-title release). The root
// cause: identify.TitleSimilarity's containment shortcut requires >= 2
// overlapping tokens by design (see its doc comment and
// TestTitleSimilarity_SingleGenericWordDoesNotBypassPenalty), specifically
// to stop a single GENERIC connector word (its own test uses "Scene") from
// spuriously matching an unrelated title inside that package's real use
// case: comparing an AI-guessed title against a raw, noisy filename stem,
// where "Scene"/"Part"/"Vol"-type tokens are common structural artifacts.
// A single-token target title can never satisfy "inter >= 2" no matter how
// distinctive the word is, so "Moana" was structurally unmatchable via that
// path — not a tuning issue, a hard ceiling.
//
// That guard is correct and stays untouched for identify.TitleSimilarity
// itself. This function is a SEPARATE, narrower rule scoped only to this
// package's actual context, which has a materially different risk profile:
// targetTitle here is always a real, canonical, database-sourced title
// (TMDB movie/show title, or a TPDB/StashDB scene title) that the operator
// explicitly selected — never an AI guess or a raw filename fragment. A
// movie/show/scene whose ENTIRE canonical title is a single, genuinely
// generic word (the exact failure mode the shared guard defends against) is
// vanishingly rare in that context, so a plain whole-word, case-insensitive
// containment check is safe here without needing a stopword list.
func singleWordTitleMatches(targetTitle, releaseTitle string) bool {
	targetWords := titleWordPattern.FindAllString(targetTitle, -1)
	if len(targetWords) != 1 {
		return false
	}
	// Reuses titleWordPattern on both sides (rather than compiling a fresh
	// per-call regex from the dynamic word) so this stays cheap when called
	// once per raw release in FilterReleases' fast-path loop.
	target := strings.ToLower(targetWords[0])
	for _, w := range titleWordPattern.FindAllString(releaseTitle, -1) {
		if strings.ToLower(w) == target {
			return true
		}
	}
	return false
}

// FilterReleases applies the Discover detail-popup plan's title-match +
// language filter pass to raw Prowlarr releases before any tier/protocol
// grading — the popup's search needs this because a raw title/ID-scoped
// Prowlarr search returns "widely varied" results (wrong language, loosely-
// matched titles), and without this pass the availability signal would be
// noisy garbage (see the plan's Context section).
//
// Two-stage title match, built on internal/identify's already-existing
// pieces rather than a fresh regex title-cleaner:
//
//  1. Fast path (always runs, no AI call): internal/identify.TitleSimilarity
//     against targetTitle vs. each candidate's raw release title — already
//     tested, already tuned for this exact comparison.
//  2. AI-escalation path (only reached when the fast path kept ZERO
//     candidates): mirrors internal/identify.Identify's own cheap-first,
//     AI-as-escalation structure. Runs ONLY when aiClient is non-nil — a nil
//     client (AI features not configured, the tolerant-nil convention every
//     mode.Session client already follows) degrades cleanly to "no
//     candidates," never an error.
//
// Argument order for TitleSimilarity calls follows
// internal/identify.ExtractFromSearch's own established convention
// (TitleSimilarity(extracted.Title, stem)): the shorter, canonical/clean
// title first, the longer/noisier scene-release-style title second — this
// is what lets TitleSimilarity's containment shortcut (see its doc comment)
// recognize "every token of the canonical title appears somewhere in this
// noisy release title" rather than falling back to a plain Jaccard score
// that a short canonical title against a long noisy one would otherwise
// score low on.
//
// Then, regardless of which title-match stage produced the surviving set, a
// deterministic language-tag filter (hasLanguageTag) drops any release
// carrying a non-English tag. Order preserved; prowlarr.Release fields are
// passed through unchanged so the caller can still pair filtered releases'
// indices 1:1 with a derived []autograb.Candidate slice (see
// buildAutoGrabCandidates's existing index-pairing convention, which this
// filter's output feeds into unchanged).
func FilterReleases(ctx context.Context, releases []prowlarr.Release, targetTitle string, m mode.Mode, aiClient identify.AIClient) []prowlarr.Release {
	fastMatched := make([]prowlarr.Release, 0, len(releases))
	for _, rel := range releases {
		if identify.TitleSimilarity(targetTitle, rel.Title) >= titleSimilarityFloor || singleWordTitleMatches(targetTitle, rel.Title) {
			fastMatched = append(fastMatched, rel)
		}
	}

	matched := fastMatched
	if len(matched) == 0 && aiClient != nil {
		log.Printf("discover availability: FilterReleases(mode=%s, title=%q) — fast path matched 0/%d raw releases, escalating to AI", m, targetTitle, len(releases))
		matched = aiEscalateTitleMatch(ctx, releases, targetTitle, m, aiClient)
	} else if len(matched) == 0 {
		log.Printf("discover availability: FilterReleases(mode=%s, title=%q) — fast path matched 0/%d raw releases, no AI client configured, giving up", m, targetTitle, len(releases))
	} else {
		log.Printf("discover availability: FilterReleases(mode=%s, title=%q) — fast path matched %d/%d raw releases", m, targetTitle, len(matched), len(releases))
	}

	out := make([]prowlarr.Release, 0, len(matched))
	for _, rel := range matched {
		if !hasLanguageTag(rel.Title) {
			out = append(out, rel)
		}
	}
	if len(out) != len(matched) {
		log.Printf("discover availability: FilterReleases(mode=%s, title=%q) — language filter dropped %d/%d matched releases", m, targetTitle, len(matched)-len(out), len(matched))
	}
	return out
}

// aiEscalateTitleMatch is FilterReleases' AI-assisted fallback, only ever
// reached when the deterministic fast path kept nothing. Each release title
// is cleaned by AI (internal/identify.GuessTitle for Movies/Series,
// internal/identify.ParseFilename for Adult — the SAME prompt already used
// for scene-release filenames, which is exactly what a Prowlarr release
// title also looks like) and the cleaned title is re-compared via
// TitleSimilarity. A per-candidate AI failure (a real error, OR
// GuessTitle/ParseFilename's own "declined to guess" empty-title result)
// just drops that one candidate — it never fails the whole filter, matching
// the "no candidates" degrade-cleanly requirement.
//
// Bounded on three axes at once (see the consts' doc comment above) so this
// phase's worst-case wall-clock time is predictable regardless of how many
// releases Prowlarr returned: at most maxAIEscalationCandidates are ever
// checked, at most aiEscalationConcurrency run at a time, and the whole
// phase is cut off at aiEscalationTimeout even if individual calls are slow.
func aiEscalateTitleMatch(ctx context.Context, releases []prowlarr.Release, targetTitle string, m mode.Mode, aiClient identify.AIClient) []prowlarr.Release {
	ctx, cancel := context.WithTimeout(ctx, aiEscalationTimeout)
	defer cancel()

	candidates := releases
	if len(candidates) > maxAIEscalationCandidates {
		candidates = candidates[:maxAIEscalationCandidates]
	}

	matched := make([]bool, len(candidates))
	sem := make(chan struct{}, aiEscalationConcurrency)
	var wg sync.WaitGroup
	for i, rel := range candidates {
		wg.Add(1)
		go func(i int, rel prowlarr.Release) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			cleaned, err := cleanReleaseTitle(ctx, rel.Title, m, aiClient)
			if err != nil {
				log.Printf("discover availability: AI escalation error for release %q: %v", rel.Title, err)
				return
			}
			if cleaned == "" {
				log.Printf("discover availability: AI declined to guess a title for release %q", rel.Title)
				return
			}
			sim := identify.TitleSimilarity(targetTitle, cleaned)
			if sim >= titleSimilarityFloor {
				matched[i] = true
			}
			log.Printf("discover availability: AI escalation for release %q — cleaned to %q, similarity to target %q = %.3f (floor %.2f), matched=%v",
				rel.Title, cleaned, targetTitle, sim, titleSimilarityFloor, matched[i])
		}(i, rel)
	}
	wg.Wait()

	out := make([]prowlarr.Release, 0, len(candidates))
	for i, rel := range candidates {
		if matched[i] {
			out = append(out, rel)
		}
	}
	log.Printf("discover availability: AI escalation for target %q checked %d candidates (of %d raw releases), matched %d",
		targetTitle, len(candidates), len(releases), len(out))
	return out
}

// cleanReleaseTitle asks AI to extract a cleaned title from a raw release
// title — GuessTitle for Movies/Series (the mainstream title-guess prompt),
// ParseFilename for Adult (the scene-filename parse prompt; releaseTitle
// plays the role of the filename stem, with no parent-folder context since a
// Prowlarr release has none).
func cleanReleaseTitle(ctx context.Context, releaseTitle string, m mode.Mode, aiClient identify.AIClient) (string, error) {
	if m == mode.Adult {
		parsed, err := identify.ParseFilename(ctx, aiClient, releaseTitle, "")
		if err != nil {
			return "", err
		}
		return parsed.Title, nil
	}
	return identify.GuessTitle(ctx, aiClient, releaseTitle)
}
