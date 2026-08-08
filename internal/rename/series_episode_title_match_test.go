package rename

import "testing"

// TestEpisodeTitleMatches is the P1-P17 predicate table — pure function tests,
// no fake TMDB server and no fixture data needed.
//
// P1-P5 predate the discriminating-residual collapse fix and are unchanged by
// it. P6-P13 were added with that fix: P6/P11/P13 are the collapse rejections,
// P8/P9/P10 the genuinely-single-word controls that must keep matching, and
// P7/P12 the len(toks) >= 2 branch that the fix does not touch at all.
//
// P14-P17 were added with the tokenStop filename/title split. P14/P15 are the
// fix itself (the production file matches the plain catalog entry and not the
// Extended one); P16/P17 are its two ACCEPTED regressions, asserted on purpose
// — P16 the lost-match sub-class and P17 the wrong-slot redirect. Read P16's
// and P17's own comments before treating either as a defect. Every P1-P13
// fixture is inert under the split: none of their showTitle/episodeName values
// contains a release-scene word, so all thirteen produce byte-identical token
// sets before and after (plan §7.1).
// Context: see .omc/specs/deep-interview-sakms-episode-title-discriminating-residual-collapse-fix.md
// and .omc/plans/autopilot-impl.md §6.2
func TestEpisodeTitleMatches(t *testing.T) {
	cases := []struct {
		name        string
		basename    string
		showTitle   string
		episodeName string
		want        bool
	}{
		{
			// P1: an episode titled the same as its own show must NOT match
			// every file in that show's folder — the residual after
			// subtracting the show's own tokens is empty.
			name:        "P1 episode title equals show title — residual empty after show-token subtraction",
			basename:    "The Path S-less file.mkv",
			showTitle:   "The Path",
			episodeName: "The Path",
			want:        false,
		},
		{
			// P2: TMDB's un-titled-episode placeholder is rejected outright,
			// even though "episode" alone is a >=4-rune "strong" token that
			// would otherwise survive the residual/containment rule.
			name:        "P2 TMDB placeholder episode name rejected",
			basename:    "Red Skelton Episode 12.mp4",
			showTitle:   "The Red Skelton Show",
			episodeName: "Episode 12",
			want:        false,
		},
		{
			// P3: the motivating shape — same strings as web_authority_test.go's
			// HasTitleTokenOverlap fixture. Directional containment (every
			// discriminating episode token appears in the file title) is what
			// makes this match despite the file title carrying more tokens
			// than the episode name (exact equality would reject this).
			name:        "P3 motivating shape — file title is a superset of the episode title",
			basename:    "Red Skelton More Funny Faces.mp4",
			showTitle:   "The Red Skelton Show",
			episodeName: "More Funny Faces",
			want:        true,
		},
		{
			// P4: one shared token is enough for HasTitleTokenOverlap (step 1)
			// but the residual isn't fully CONTAINED in the file's own tokens
			// ("different" appears nowhere in the file title) — must reject.
			name:        "P4 one shared token, residual not contained — no match",
			basename:    "Red Skelton Something Else.mp4",
			showTitle:   "The Red Skelton Show",
			episodeName: "Something Different",
			want:        false,
		},
		{
			// P5: the Red Skelton digit-code population (§0.2/§1.5's worked
			// trace) must decline to zero, not one wrong, matches — a digit
			// code shares no token with any real episode title.
			name:        "P5 digit-code basename shares no token with a real episode title",
			basename:    "Red Skelton e1524.mp4",
			showTitle:   "The Red Skelton Show",
			episodeName: "More Funny Faces",
			want:        false,
		},
		{
			// P6: THE FIX. TheTVDB's real Laurel & Hardy episode "That's That"
			// tokenizes to the single token "that" from TWO source words ("That's"
			// and "That"), so it is 4 runes long but not distinctive. Pre-fix this
			// returned true and made "That's That" a competing candidate for the
			// genuinely-correct "That's My Wife" file.
			name:        "P6 single-token residual collapsed from two source words is not discriminating",
			basename:    "Laurel & Hardy - That's My Wife(B&W)-DVDRip.XviD-DIE-DVD09.mp4",
			showTitle:   "Laurel & Hardy",
			episodeName: "That's That",
			want:        false,
		},
		{
			// P7: the genuinely-discriminating sibling of P6, same file. Residual
			// {that, my, wife} takes the len(toks) >= 2 branch, which this change
			// does not touch at all.
			name:        "P7 That's My Wife still matches its own file",
			basename:    "Laurel & Hardy - That's My Wife(B&W)-DVDRip.XviD-DIE-DVD09.mp4",
			showTitle:   "Laurel & Hardy",
			episodeName: "That's My Wife",
			want:        true,
		},
		{
			// P8/P9/P10 are three INDEPENDENT single-word titles, deliberately not
			// folded into one case: each is a real title that shares "That's
			// That"'s code path (single-token residual, token >= 4 runes) and must
			// keep matching via the srcCounts[t] == 1 accept branch.
			name:        "P8 Liberty (S05E01) — one source word, one token, unaffected",
			basename:    "Laurel & Hardy - Liberty(B&W)-DVDRip.XviD-DIE-DVD05.mp4",
			showTitle:   "Laurel & Hardy",
			episodeName: "Liberty",
			want:        true,
		},
		{
			name:        "P9 Scram! (S08E06) — trailing punctuation is not a second word",
			basename:    "Laurel & Hardy - Scram!(B&W)-DVDRip.XviD-DIE-DVD11.mp4",
			showTitle:   "Laurel & Hardy",
			episodeName: "Scram!",
			want:        true,
		},
		{
			name:        "P10 Blotto (S06E02) — one source word, one token, unaffected",
			basename:    "Laurel & Hardy - Blotto(B&W)-DVDRip.XviD-DIE-DVD08.mp4",
			showTitle:   "Laurel & Hardy",
			episodeName: "Blotto",
			want:        true,
		},
		{
			// P11: the spec's explicitly accepted tradeoff. A file whose only
			// discriminating text IS "that's that" can no longer match via this
			// predicate — declining to guess beats today's false positive. It falls
			// through to AI-assisted recovery or manual repick.
			name:        "P11 a file literally named That's That no longer matches the That's That episode",
			basename:    "That's That.mp4",
			showTitle:   "Laurel & Hardy",
			episodeName: "That's That",
			want:        false,
		},
		{
			// P12: the multi-token branch's regression lock. Residual {that, wife}
			// has two tokens, so the multi-token branch returns true BEFORE any
			// collapse check runs — even though "that" here was contributed by two
			// source words. If a future edit hoists the collapse check above the
			// len(toks) >= 2 short-circuit, this is the case that fails.
			name:        "P12 multi-token residual accepts even with a collapsed token in it",
			basename:    "Laurel & Hardy - That's That Wife(B&W)-DVDRip.XviD-DIE-DVD09.mp4",
			showTitle:   "Laurel & Hardy",
			episodeName: "That's That Wife",
			want:        true,
		},
		{
			// P13: the subtraction-invariance lock. "Round and Round Path" is a
			// TWO-token title ({round: 2, path: 1}) that becomes a ONE-token residual
			// ({round}) only after the show's own "path" is subtracted. Its count is
			// still 2, because subtractTokens removes whole tokens and never
			// individual positions of a token it keeps. If a future edit ever moves
			// titleTokenCounts to AFTER the subtraction, the collapse becomes
			// invisible, this case returns true, and the false positive is back.
			//
			// Failure signature: if this goes red with got == true, the regression is
			// NOT in discriminating — it is that epCounts is being computed from the
			// residual (or from subtractTokens' output) instead of from the raw
			// episodeName. Fix the call site, not this assertion.
			name:        "P13 residual that collapses only after show-name subtraction is not discriminating",
			basename:    "The Path - Round and Round Path.mkv",
			showTitle:   "The Path",
			episodeName: "Round and Round Path",
			want:        false,
		},
		{
			// P14: THE FIX, at the predicate level. The real production
			// filename must NOT match the Extended variant. "extended" is now
			// RETAINED in the catalog title's residual ({chump, oxford,
			// extended}) and is absent from fileToks, so containment fails.
			//
			// If this flips to true, the two lists have been merged back
			// together and S00E17/S16E01 are indistinguishable again — which is
			// the exact live failure this whole change exists to fix.
			name:        "P14 the production file does NOT match the Extended catalog variant",
			basename:    "Laurel & Hardy - A Chump at Oxford(Colour)-DVDRip.XviD-DIE-DVD01.mp4",
			showTitle:   "Laurel & Hardy",
			episodeName: "A Chump at Oxford (Extended)",
			want:        false,
		},
		{
			// P15: the same file DOES match the plain variant. P14 and P15
			// together ARE the disambiguation — either one alone proves
			// nothing, because "matches neither" and "matches both" would each
			// satisfy exactly one of them.
			name:        "P15 the production file DOES match the plain catalog variant",
			basename:    "Laurel & Hardy - A Chump at Oxford(Colour)-DVDRip.XviD-DIE-DVD01.mp4",
			showTitle:   "Laurel & Hardy",
			episodeName: "A Chump At Oxford",
			want:        true,
		},
		{
			// P16: the accepted regression, LOST-MATCH sub-class. Pre-fix this
			// returned true. Attributable to filenameTokens specifically: "vs"
			// is one of the only THREE release-scene words searchterm.FromName
			// does not strip, so it reaches the tokenizer and is dropped there
			// on the file side while surviving on the catalog side.
			//
			// want:false is the CHOSEN outcome, not an accident — see the
			// plan's §4. Do not "fix" this to true by carving vs/feat/ft back
			// into the universal list; that is the spec's explicitly rejected
			// per-word judgment call, and it would not help the 15 words
			// searchterm.FromName already strips.
			//
			// Note also how this case exits: the retained "vs" pushes the
			// residual to {freddy, vs, jason}, which takes the len(toks) >= 2
			// branch and SKIPS the collapse check — then tokensSubsetOf
			// rejects, because fileToks cannot contain a release-scene word.
			// 96d27cd's protective outcome is preserved through a different
			// exit, not weakened (plan §7.2).
			name:        "P16 accepted regression, lost-match sub-class — a vs-bearing title no longer matches its own file",
			basename:    "Freddy vs Jason.mkv",
			showTitle:   "Horror Anthology",
			episodeName: "Freddy vs Jason",
			want:        false,
		},
		{
			// P17: the accepted regression, REDIRECT sub-class. basename is a
			// HYPOTHETICAL, CONSTRUCTED sibling of the real production file —
			// it does not exist on disk (confirmed absent from production
			// 2026-08-08, plan §8.1); do not go looking for it.
			//
			// want:true DOCUMENTS THE ACCEPTED REDIRECT RISK — it is NOT an
			// assertion of desired behaviour. This file genuinely belongs to
			// S00E17 "A Chump at Oxford (Extended)" and is matching S16E01's
			// plain entry instead. This is a KNOWN, RECORDED, ACCEPTED
			// consequence of the design (the spec's Round 3 decision: all 18
			// release-scene words move to filename-only, no exceptions) — see
			// the plan's §4.1 and §4.2.2. It is not a defect this plan
			// undertakes to fix. If this row ever flips to false, the redirect
			// stopped happening: find out why before "fixing" the test, because
			// something about the FromName/titleTokens vocabulary split
			// changed.
			//
			// Mechanism: "extended" is one of the 15 words FromName DOES strip,
			// so this file's tokens are identical to the plain file's, while
			// the correct S00E17 entry keeps the word and matches nothing.
			//
			// NO companion "== false" row asserting the (Extended) entry
			// stopped matching is needed: one row suffices for a two-sided
			// failure, because
			// TestSearchTVDBEpisodeByTitle_ExtendedVariantRedirectsToWrongSlot
			// already proves it via Found == 1 — were S00E17 still matching,
			// Found would be 2.
			name:        "P17 accepted regression, redirect sub-class — an Extended-shaped file lands on the WRONG slot",
			basename:    "Laurel & Hardy - A Chump at Oxford Extended(Colour)-DVDRip.XviD-DIE-DVD02.mp4",
			showTitle:   "Laurel & Hardy",
			episodeName: "A Chump At Oxford",
			want:        true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := episodeTitleMatches(tc.basename, tc.showTitle, tc.episodeName)
			if got != tc.want {
				t.Errorf("episodeTitleMatches(%q, %q, %q) = %v, want %v", tc.basename, tc.showTitle, tc.episodeName, got, tc.want)
			}
		})
	}
}

func TestIsPlaceholderEpisodeName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"Episode N", "Episode 12", true},
		{"Ep. N", "Ep. 3", true},
		{"Chapter N", "Chapter 4", true},
		{"Part N", "Part 2", true},
		{"real title", "More Funny Faces", false},
		{"real title containing a digit", "Skelton's Scrapbook", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPlaceholderEpisodeName(tc.in); got != tc.want {
				t.Errorf("isPlaceholderEpisodeName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
