package api

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/prowlarr"
)

// fakeReleaseMatchAI is a minimal identify.AIClient fake — counts calls and
// delegates each response to fn, so tests can script exactly what the
// (simulated) AI extracts per prompt without a real Ollama/OpenAI/etc. round
// trip. Mirrors the shape of internal/rename's countingAI fake. calls is
// atomic, not a plain int: aiEscalateTitleMatch now runs candidates
// concurrently (see aiEscalationConcurrency), so more than one goroutine can
// legitimately call ChatJSON on the same fake at once — a plain int here
// raced under -race the moment a test used more than one release.
type fakeReleaseMatchAI struct {
	fn    func(prompt string) (map[string]any, error)
	calls int32
}

func (f *fakeReleaseMatchAI) ChatJSON(ctx context.Context, prompt string) (map[string]any, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.fn != nil {
		return f.fn(prompt)
	}
	return nil, nil
}

// TestFilterReleases_FastPathTitleAndLanguage covers the plan's four
// deterministic (no-AI) cases: exact match, a heavily noisy scene-release
// title that still contains every target-title token, a foreign-language
// tag rejecting an otherwise-matching title, and an ambiguous/partial title
// match (shares only a generic word) that the fast path correctly rejects.
func TestFilterReleases_FastPathTitleAndLanguage(t *testing.T) {
	cases := []struct {
		name        string
		targetTitle string
		release     prowlarr.Release
		wantKept    bool
	}{
		{
			name:        "exact match",
			targetTitle: "The Dark Knight",
			release:     prowlarr.Release{GUID: "1", Title: "The.Dark.Knight.2008.1080p.BluRay.x264-GROUP"},
			wantKept:    true,
		},
		{
			name:        "noisy scene-release title (heavy tags, every target token still present)",
			targetTitle: "The Matrix Resurrections",
			release:     prowlarr.Release{GUID: "1", Title: "The.Matrix.Resurrections.2021.2160p.WEB-DL.DDP5.1.Atmos.HDR.DV.x265-GROUP"},
			wantKept:    true,
		},
		{
			name:        "foreign-language tag rejection (title matches, but FRENCH tag present)",
			targetTitle: "The Dark Knight",
			release:     prowlarr.Release{GUID: "1", Title: "The.Dark.Knight.2008.FRENCH.1080p.BluRay.x264-GROUP"},
			wantKept:    false,
		},
		{
			name:        "ambiguous/partial title match (shares only one generic word) rejected",
			targetTitle: "Show One Two Three",
			release:     prowlarr.Release{GUID: "1", Title: "Show.Four.Five.Six.2020-GROUP"},
			wantKept:    false,
		},
		{
			// Regression for a real "nothing is being found to grab" report:
			// identify.TitleSimilarity's containment shortcut requires >= 2
			// overlapping tokens, which a single-word title can NEVER satisfy
			// (inter can't exceed len(ta)=1) — Jaccard alone then gets
			// diluted by the release title's quality/tag tokens, landing
			// below titleSimilarityFloor even for an exact match. Covered by
			// singleWordTitleMatches, not TitleSimilarity itself.
			name:        "single-word title (Moana) matches via singleWordTitleMatches fallback",
			targetTitle: "Moana",
			release:     prowlarr.Release{GUID: "1", Title: "Moana.2016.1080p.BluRay.x264-GROUP"},
			wantKept:    true,
		},
		{
			name:        "single-word title (Moana) still rejects an unrelated release",
			targetTitle: "Moana",
			release:     prowlarr.Release{GUID: "1", Title: "Some.Other.Movie.2020.1080p.BluRay.x264-GROUP"},
			wantKept:    false,
		},
		{
			name:        "single-word title (Moana) still rejects on a language tag despite matching",
			targetTitle: "Moana",
			release:     prowlarr.Release{GUID: "1", Title: "Moana.2016.FRENCH.1080p.BluRay.x264-GROUP"},
			wantKept:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterReleases(context.Background(), []prowlarr.Release{tc.release}, tc.targetTitle, mode.Movies, nil)
			gotKept := len(got) == 1
			if gotKept != tc.wantKept {
				t.Errorf("FilterReleases(%q, %q) kept=%v, want kept=%v", tc.release.Title, tc.targetTitle, gotKept, tc.wantKept)
			}
		})
	}
}

// TestFilterReleases_AIEscalation_NilClientDegradesCleanly is the plan's
// explicit nil-safety requirement: when the fast path keeps nothing AND no
// AI client is configured, the filter must return zero candidates cleanly —
// never panic, never error.
func TestFilterReleases_AIEscalation_NilClientDegradesCleanly(t *testing.T) {
	releases := []prowlarr.Release{
		{GUID: "1", Title: "xXx.RandomRelease.Whatever.2020-GROUP"},
	}
	got := FilterReleases(context.Background(), releases, "Obscure Title Nobody Knows", mode.Movies, nil)
	if len(got) != 0 {
		t.Fatalf("expected zero candidates with a nil AI client, got %+v", got)
	}
}

// TestFilterReleases_AIEscalation_SkippedWhenFastPathMatches proves the
// "only escalate when the fast path kept ZERO" rule: a configured AI client
// must never be called when the deterministic pass already found a match —
// keeps the common case fast (no AI round-trip per candidate), per the plan.
func TestFilterReleases_AIEscalation_SkippedWhenFastPathMatches(t *testing.T) {
	ai := &fakeReleaseMatchAI{fn: func(prompt string) (map[string]any, error) {
		return map[string]any{"title": "should not be used"}, nil
	}}
	releases := []prowlarr.Release{
		{GUID: "1", Title: "The.Dark.Knight.2008.1080p.BluRay.x264-GROUP"},
	}
	got := FilterReleases(context.Background(), releases, "The Dark Knight", mode.Movies, ai)
	if len(got) != 1 {
		t.Fatalf("expected the fast-path match to survive, got %+v", got)
	}
	if ai.calls != 0 {
		t.Errorf("expected zero AI calls when the fast path already matched, got %d", ai.calls)
	}
}

// TestFilterReleases_AIEscalation_MoviesGuessTitleFindsMatch is the
// AI-escalation path for Movies/Series: a release title too abbreviated for
// the deterministic fast path is recovered once identify.GuessTitle cleans
// it.
func TestFilterReleases_AIEscalation_MoviesGuessTitleFindsMatch(t *testing.T) {
	ai := &fakeReleaseMatchAI{fn: func(prompt string) (map[string]any, error) {
		return map[string]any{"title": "The Dark Knight"}, nil
	}}
	releases := []prowlarr.Release{
		{GUID: "1", Title: "tdk.2008.rip-XYZ"}, // too abbreviated for the fast path
	}
	got := FilterReleases(context.Background(), releases, "The Dark Knight", mode.Movies, ai)
	if len(got) != 1 {
		t.Fatalf("expected AI-escalation (GuessTitle) to recover the match, got %+v", got)
	}
	if ai.calls != 1 {
		t.Errorf("expected exactly one AI call (one candidate), got %d", ai.calls)
	}
}

// TestFilterReleases_AIEscalation_AdultParseFilenameFindsMatch is the
// AI-escalation path for Adult: identify.ParseFilename (the same
// scene-filename-parse prompt already used elsewhere) cleans the release
// title instead of GuessTitle.
func TestFilterReleases_AIEscalation_AdultParseFilenameFindsMatch(t *testing.T) {
	ai := &fakeReleaseMatchAI{fn: func(prompt string) (map[string]any, error) {
		return map[string]any{"studio": "Some Studio", "title": "Wild Scene Title", "performers": []any{}}, nil
	}}
	releases := []prowlarr.Release{
		{GUID: "1", Title: "somestudio.wld.scn.ttl.2020.mp4"},
	}
	got := FilterReleases(context.Background(), releases, "Wild Scene Title", mode.Adult, ai)
	if len(got) != 1 {
		t.Fatalf("expected AI-escalation (ParseFilename) to recover the match, got %+v", got)
	}
}

// TestFilterReleases_AIEscalation_PerCandidateErrorSkipsOnlyThatCandidate
// proves a single candidate's AI failure never fails the whole filter — it
// just drops that one candidate, matching the "degrade cleanly" requirement
// even for the multi-candidate escalation case. Keyed on the release title
// appearing in the prompt (GuessTitle embeds it via %q — see
// mainstream_prompts.go), not on call order: escalation now runs
// concurrently (see aiEscalationConcurrency), so which candidate's HTTP
// call reaches the fake first is not guaranteed to match slice order.
func TestFilterReleases_AIEscalation_PerCandidateErrorSkipsOnlyThatCandidate(t *testing.T) {
	ai := &fakeReleaseMatchAI{fn: func(prompt string) (map[string]any, error) {
		if strings.Contains(prompt, "bad-release") {
			return nil, fmt.Errorf("simulated AI failure")
		}
		return map[string]any{"title": "The Dark Knight"}, nil
	}}
	releases := []prowlarr.Release{
		{GUID: "1", Title: "bad-release-XYZ"},
		{GUID: "2", Title: "good-release-XYZ"},
	}
	got := FilterReleases(context.Background(), releases, "The Dark Knight", mode.Movies, ai)
	if len(got) != 1 || got[0].GUID != "2" {
		t.Fatalf("expected only the second (successfully-cleaned) candidate to survive, got %+v", got)
	}
}

// TestSingleWordTitleMatches is a direct table-driven check of the
// single-word fallback, independent of FilterReleases' full pipeline —
// including the multi-word no-op case, so a change to this function can't
// silently start affecting FilterReleases' existing ambiguous/partial-match
// protection (which relies on singleWordTitleMatches returning false for
// any target title with more than one word).
func TestSingleWordTitleMatches(t *testing.T) {
	cases := []struct {
		name         string
		targetTitle  string
		releaseTitle string
		want         bool
	}{
		{"exact single word", "Moana", "Moana.2016.1080p.BluRay.x264-GROUP", true},
		{"case-insensitive", "moana", "MOANA.2016.1080p.BluRay.x264-GROUP", true},
		{"unrelated release", "Moana", "Some.Other.Movie.2020.1080p-GROUP", false},
		{"multi-word target never uses this fallback", "The Dark Knight", "The.Dark.Knight.2008-GROUP", false},
		{"short real title (Up)", "Up", "Up.2009.1080p.BluRay.x264-GROUP", true},
		{"substring but not a whole word must not match", "Up", "Update.2009.1080p-GROUP", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := singleWordTitleMatches(tc.targetTitle, tc.releaseTitle); got != tc.want {
				t.Errorf("singleWordTitleMatches(%q, %q) = %v, want %v", tc.targetTitle, tc.releaseTitle, got, tc.want)
			}
		})
	}
}

// TestHasLanguageTag is a direct table-driven check of the deterministic
// language-tag token list, independent of title-similarity scoring.
func TestHasLanguageTag(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"Some.Movie.2020.1080p.BluRay.x264-GROUP", false},
		{"Some.Movie.2020.FRENCH.1080p.BluRay.x264-GROUP", true},
		{"Some.Movie.2020.GERMAN.1080p-GROUP", true},
		// MULTI means "multiple audio tracks bundled" (usually including
		// English for English-original content), not "no English track" —
		// must NOT be rejected. See languageTagPattern's doc comment.
		{"Some.Movie.2020.MULTI.1080p-GROUP", false},
		{"Some.Movie.2020.VOSTFR.1080p-GROUP", true},
		{"FrenchConnection.2020.1080p-GROUP", false}, // "French" is not a whole word here
		{"Some.Movie.2020.JAPANESE.1080p-GROUP", true},
		{"Some.Movie.2020.KOREAN.1080p-GROUP", true},
		{"Some.Movie.2020.HINDI.1080p-GROUP", true},
		{"Some.Movie.2020.RUSSIAN.1080p-GROUP", true},
		{"Some.Movie.2020.ITALIAN.1080p-GROUP", true},
		{"Some.Movie.2020.SPANISH.1080p-GROUP", true},
	}
	for _, tc := range cases {
		if got := hasLanguageTag(tc.title); got != tc.want {
			t.Errorf("hasLanguageTag(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

// delayedFakeAI is a ctx-aware AIClient fake for proving the three
// AI-escalation bounds actually hold: it sleeps `delay` per call (respecting
// ctx cancellation the same way a real HTTP client bound to ctx would, via
// http.NewRequestWithContext — see aiEscalationTimeout's doc comment), and
// tracks total calls plus the highest number of calls observed in flight at
// once.
type delayedFakeAI struct {
	delay         time.Duration
	calls         int32
	current       int32
	maxConcurrent int32
	mu            sync.Mutex
}

func (f *delayedFakeAI) ChatJSON(ctx context.Context, prompt string) (map[string]any, error) {
	atomic.AddInt32(&f.calls, 1)
	cur := atomic.AddInt32(&f.current, 1)
	defer atomic.AddInt32(&f.current, -1)

	f.mu.Lock()
	if cur > f.maxConcurrent {
		f.maxConcurrent = cur
	}
	f.mu.Unlock()

	select {
	case <-time.After(f.delay):
		return map[string]any{"title": "irrelevant, never matches"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestFilterReleases_AIEscalation_CapsCandidateCount proves the count bound:
// far more raw releases than maxAIEscalationCandidates must never translate
// into that many AI calls — escalating every release in a large result set
// is exactly the unbounded-loop bug this cap exists to prevent.
func TestFilterReleases_AIEscalation_CapsCandidateCount(t *testing.T) {
	ai := &delayedFakeAI{}
	releases := make([]prowlarr.Release, maxAIEscalationCandidates+5)
	for i := range releases {
		releases[i] = prowlarr.Release{GUID: fmt.Sprintf("%d", i), Title: fmt.Sprintf("release-%d-XYZ", i)}
	}
	FilterReleases(context.Background(), releases, "Obscure Title Nobody Knows", mode.Movies, ai)
	if got := atomic.LoadInt32(&ai.calls); got != maxAIEscalationCandidates {
		t.Errorf("expected exactly %d AI calls (the cap), got %d for %d input releases", maxAIEscalationCandidates, got, len(releases))
	}
}

// TestFilterReleases_AIEscalation_BoundsConcurrency proves the concurrency
// bound: with an artificial per-call delay long enough that overlapping
// calls are essentially guaranteed if unbounded, the observed max-in-flight
// must never exceed aiEscalationConcurrency.
func TestFilterReleases_AIEscalation_BoundsConcurrency(t *testing.T) {
	ai := &delayedFakeAI{delay: 50 * time.Millisecond}
	releases := make([]prowlarr.Release, maxAIEscalationCandidates)
	for i := range releases {
		releases[i] = prowlarr.Release{GUID: fmt.Sprintf("%d", i), Title: fmt.Sprintf("release-%d-XYZ", i)}
	}
	FilterReleases(context.Background(), releases, "Obscure Title Nobody Knows", mode.Movies, ai)
	if ai.maxConcurrent > aiEscalationConcurrency {
		t.Errorf("observed %d AI calls in flight at once, want <= %d", ai.maxConcurrent, aiEscalationConcurrency)
	}
	if ai.maxConcurrent < aiEscalationConcurrency {
		t.Logf("observed max concurrency %d, expected exactly %d (not a failure, but weaker evidence the bound is exercised)", ai.maxConcurrent, aiEscalationConcurrency)
	}
}

// TestFilterReleases_AIEscalation_RespectsOverallTimeout is the regression
// test for the actual reported bug ("selecting titles hangs checking
// availability"): every AI call sleeps far longer than a shrunk
// aiEscalationTimeout, so if the phase deadline were NOT enforced (the old,
// buggy behavior — a plain unbounded sequential loop with no phase timeout
// at all), this test would take calls*delay to return. With the deadline
// enforced, it must return close to the shrunk timeout instead, and with
// zero matches (every call is cut off before producing a usable result).
func TestFilterReleases_AIEscalation_RespectsOverallTimeout(t *testing.T) {
	orig := aiEscalationTimeout
	aiEscalationTimeout = 100 * time.Millisecond
	defer func() { aiEscalationTimeout = orig }()

	ai := &delayedFakeAI{delay: 5 * time.Second} // far longer than the shrunk timeout
	releases := make([]prowlarr.Release, maxAIEscalationCandidates)
	for i := range releases {
		releases[i] = prowlarr.Release{GUID: fmt.Sprintf("%d", i), Title: fmt.Sprintf("release-%d-XYZ", i)}
	}

	start := time.Now()
	got := FilterReleases(context.Background(), releases, "Obscure Title Nobody Knows", mode.Movies, ai)
	elapsed := time.Since(start)

	// Generous upper bound (not tied tightly to the 100ms deadline) so this
	// stays robust under CI/scratch-instance scheduling jitter — the point is
	// "did NOT wait anywhere close to 5s", not "waited exactly 100ms".
	if elapsed > 2*time.Second {
		t.Errorf("FilterReleases took %s with a 100ms phase deadline and 5s per-call delay — the deadline is not being enforced (this is the exact shape of the reported hang)", elapsed)
	}
	if len(got) != 0 {
		t.Errorf("expected zero matches (every call cut off before returning a result), got %+v", got)
	}
}
