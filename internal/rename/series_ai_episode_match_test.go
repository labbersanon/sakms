package rename

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/tvdb"
)

// ---------------------------------------------------------------------------
// autopilot-impl.md §4.2(b) — aiEpisodeRecoveryPass.
//
// READ THIS BEFORE ADDING A TEST HERE. No test in this file may use
// countingAI.calls to prove that a specific path made no AI call.
// sess.MainstreamAI has eleven-plus other consumers reachable from one scan
// (Movies GuessTitle, Kids classify.WithAI, ...), so a bare counter is not a
// discriminator: it fails spuriously when an unrelated consumer fires and
// proves nothing when it is 0. Every absence/presence assertion below is on
// captured prompt CONTENT, via seriesPromptCount.
//
// Most cases call aiEpisodeRecoveryPass DIRECTLY rather than driving
// ScanLibrarySeries. That is deliberate and is the opposite of the convention
// rename_library_series_test.go:2200 states for the anthology pass: two of
// this pass's five inputs — `resolved` and `handled` — are tvdbAnthologyPass
// RETURN VALUES that ScanLibrarySeries consumes internally, so a full-Scan
// test cannot set (or observe) either. The cases that CAN be driven end-to-end
// are, and they are the ones where the wiring itself is the claim:
// TestAIRecovery_WithinBatchCollisionAnnotatesTheSecondRow (§0.8's ordering) and
// TestAIRecovery_UsesShowFolderNameNotParentDir (§2.5.4's fail-open bug).
// ---------------------------------------------------------------------------

// seriesPromptCount reports how many captured prompts came from
// identify.ParseSeriesEpisode, identified by the marker every one of its
// prompts carries verbatim (identify.SeriesEpisodePromptMarker, pinned by
// TestParseSeriesEpisode_EveryPromptCarriesTheMarker).
//
//	"this path made no call"  -> seriesPromptCount(ai) == 0
//	"this path made one call" -> seriesPromptCount(ai) == 1
func seriesPromptCount(ai *countingAI) int {
	n := 0
	for _, p := range ai.prompts {
		if strings.Contains(p, identify.SeriesEpisodePromptMarker) {
			n++
		}
	}
	return n
}

// promptedForSeriesEpisode is the boolean face of seriesPromptCount, for the
// cases that only care whether the path ran at all. The int is the primitive
// because several cases must distinguish "one call" from "two".
func promptedForSeriesEpisode(c *countingAI) bool { return seriesPromptCount(c) > 0 }

// assertRowUnchanged is reflect.DeepEqual rather than ==: proposals.Proposal
// carries slice fields (ExtraEpisodeNumbers, Genres, Cast) and is not a
// comparable type.
func assertRowUnchanged(t *testing.T, got, want proposals.Proposal) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("row was modified:\n got: %+v\nwant: %+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const (
	// The foreign-language and typo'd Laurel & Hardy filenames are the REAL
	// target population (plan §0.3): a file carrying an episode NAME in another
	// language, which no deterministic path can place. Verbatim from
	// .omc/artifacts/series-after-20260806.psv.
	aiPolitiqueriasFile = "Laurel & Hardy - Politiquerias(B&W)-DVDRip.XviD-DIE-DVD16.mp4"
	aiLadronesFile      = "Laurel & Hardy - Ladrones(B&W)-DVDRip.XviD-DIE-DVD17.mp4"

	// The English titles TheTVDB actually catalogs those two under.
	aiChickensEpisode = "Chickens Come Home"
	aiNightOwlsInTVDB = "Night Owls"
)

// aiTVDBCatalog is the already-fetched catalog MODE 2 matches against —
// anthologyScanCatalog's four titles plus the two English titles the foreign
// filenames above resolve to.
func aiTVDBCatalog() []tvdb.Episode {
	return []tvdb.Episode{
		{SeriesID: anthologyTVDBSeriesID, SeasonNumber: 3, Number: 1, Name: "Duck Soup", Aired: "1927-03-13"},
		{SeriesID: anthologyTVDBSeriesID, SeasonNumber: 2, Number: 5, Name: "Berth Marks", Aired: "1929-06-01"},
		{SeriesID: anthologyTVDBSeriesID, SeasonNumber: 2, Number: 12, Name: "Big Business", Aired: "1929-04-20"},
		{SeriesID: anthologyTVDBSeriesID, SeasonNumber: 8, Number: 3, Name: "The Music Box", Aired: "1932-04-16"},
		{SeriesID: anthologyTVDBSeriesID, SeasonNumber: 3, Number: 2, Name: aiChickensEpisode, Aired: "1931-01-31"},
		{SeriesID: anthologyTVDBSeriesID, SeasonNumber: 2, Number: 20, Name: aiNightOwlsInTVDB, Aired: "1930-05-04"},
	}
}

// aiAnthologyPin is the pin tvdbAnthologyPass would publish for the Laurel &
// Hardy folder: a REAL TheTVDB id plus the negative synthetic TMDB id.
func aiAnthologyPin() anthologyPin {
	return anthologyPin{
		tvdbID:  anthologyTVDBSeriesID,
		synthID: anthologyTMDBID(anthologyTVDBSeriesID),
		title:   "Laurel & Hardy",
		year:    1921,
	}
}

// aiResolution builds the map entry tvdbAnthologyPass publishes for a resolved
// folder. candName defaults to the pin's title (the fresh-pin case, where the
// two genuinely coincide) — pass a different one to model the established-pin
// rescan, where pin.title is the TRACKED ROW's title and candName is TheTVDB's.
func aiResolution(pin anthologyPin, candName string) anthologyResolution {
	if candName == "" {
		candName = pin.title
	}
	return anthologyResolution{pin: pin, catalog: aiTVDBCatalog(), candName: candName}
}

// aiUnmatchedRow is the row proposeOneEpisodeLibrary's parse-failure branch
// produces — the ONLY shape that ever reaches this pass. Built from the same
// verbatim reason string assertUntouchedParseFailures asserts, so a "row
// unchanged" assertion here means the same thing it means there.
func aiUnmatchedRow(sourcePath, foundRoot string) proposals.Proposal {
	name := filepath.Base(sourcePath)
	return proposals.Proposal{
		Mode: mode.Series, Workflow: proposals.Rename,
		SourceName: name, SourcePath: sourcePath, RootFolderPath: foundRoot,
		Status: proposals.Unmatched,
		Reason: parseFailureReason(name),
	}
}

// aiGuess is the canned ChatJSON body countingAI replays for every call.
// seasonNumber/episodeNumber are populated on purpose in the cases that use
// them: they are §2.4's numeric sink and no caller may ever read them.
func aiGuess(show, episode string, season, episodeNum int) map[string]any {
	return map[string]any{
		"showTitle":     show,
		"episodeTitle":  episode,
		"seasonNumber":  float64(season),
		"episodeNumber": float64(episodeNum),
		"year":          float64(1921),
	}
}

// aiMode2Fixture is one resolved Laurel & Hardy folder holding one
// foreign-titled file, wired for a DIRECT aiEpisodeRecoveryPass call.
type aiMode2Fixture struct {
	root       string
	roots      []string
	key        string
	sess       *mode.Session
	tracked    map[episodeKey]bool
	seriesByID map[int]library.Series
	folderIDs  map[string]int
	resolved   map[string]anthologyResolution
	handled    map[int]bool
	candidates []int
	out        []proposals.Proposal
	ai         *countingAI
}

// newAIMode2Fixture wires the MODE 2 path with a fatal TMDB client: sess.TMDB
// must be non-nil to clear the pass-level precondition (§2.5.1), and MODE 2
// must never actually call it — the fatal server asserts both at once.
func newAIMode2Fixture(t *testing.T, resp map[string]any, files ...string) *aiMode2Fixture {
	t.Helper()
	if len(files) == 0 {
		files = []string{aiPolitiqueriasFile}
	}
	root := "/library/Series"
	roots := []string{root}
	ai := &countingAI{resp: resp}

	f := &aiMode2Fixture{
		root: root, roots: roots, ai: ai,
		sess:       &mode.Session{Mode: mode.Series, MainstreamAI: ai, TMDB: fatalTMDBSeriesServer(t)},
		tracked:    map[episodeKey]bool{},
		seriesByID: map[int]library.Series{},
		folderIDs:  map[string]int{},
		resolved:   map[string]anthologyResolution{},
		handled:    map[int]bool{},
	}
	for i, name := range files {
		f.out = append(f.out, aiUnmatchedRow(filepath.Join(root, anthologyShowFolder, name), root))
		f.candidates = append(f.candidates, i)
	}
	f.key = showFolderKey(f.out[0].SourcePath, roots)
	f.resolved[f.key] = aiResolution(aiAnthologyPin(), "")
	return f
}

func (f *aiMode2Fixture) run(t *testing.T) {
	t.Helper()
	aiEpisodeRecoveryPass(context.Background(), f.sess, f.tracked, f.seriesByID, f.folderIDs,
		f.resolved, f.handled, f.roots, f.root, f.candidates, f.out)
}

// ---------------------------------------------------------------------------
// Preconditions (§2.5.1)
// ---------------------------------------------------------------------------

// TestAIRecovery_DisabledMakesZeroSeriesPrompts is AC4's first half, driven
// END-TO-END through ScanLibrarySeries so it covers the real wiring rather
// than a hand-built argument list.
//
// The enabled subtest is the positive control that makes the disabled one
// non-vacuous: without it, "zero prompts" would be equally true of a fixture
// that never reaches the pass at all.
func TestAIRecovery_DisabledMakesZeroSeriesPrompts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		wantStatus proposals.Status
	}{
		{name: "ai fallback enabled", enabled: true, wantStatus: proposals.Pending},
		{name: "ai fallback disabled (MainstreamAI nil)", enabled: false, wantStatus: proposals.Unmatched},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			seedAnthologyFiles(t, root, anthologyShowFolder,
				anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile,
				anthologyBigBusinessFile, aiPolitiqueriasFile)

			tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
				ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921",
				Catalog: aiScanCatalogWithChickens(),
			}})
			ai := &countingAI{resp: aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0)}
			sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}
			if tc.enabled {
				sess.MainstreamAI = ai
			}

			got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
				naming.Jellyfin, DefaultMatchConfig(), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			p := proposalByName(t, got, aiPolitiqueriasFile)
			if p.Status != tc.wantStatus {
				t.Fatalf("status = %v (%s), want %v", p.Status, p.Reason, tc.wantStatus)
			}
			if tc.enabled {
				if n := seriesPromptCount(ai); n != 1 {
					t.Errorf("seriesPromptCount = %d, want exactly 1", n)
				}
				if !strings.HasPrefix(p.Reason, aiEpisodeMatchReasonPrefix) {
					t.Errorf("reason = %q, want the %q prefix", p.Reason, aiEpisodeMatchReasonPrefix)
				}
				return
			}
			if n := seriesPromptCount(ai); n != 0 {
				t.Errorf("seriesPromptCount = %d, want 0 with the AI fallback disabled", n)
			}
			// The ORIGINAL reason must survive byte-for-byte — asserting only
			// "Unmatched" would pass even if the pass had rewritten it.
			if p.Reason != parseFailureReason(aiPolitiqueriasFile) {
				t.Errorf("reason = %q, want the verbatim parse-failure reason %q",
					p.Reason, parseFailureReason(aiPolitiqueriasFile))
			}
		})
	}
}

// aiScanCatalogWithChickens is anthologyScanCatalog plus the English title the
// Spanish-named file resolves to. The four original titles are what clears
// minCorroboratingFiles; the fifth is what MODE 2 then matches against.
func aiScanCatalogWithChickens() []fakeTVDBEpisode {
	return append(anthologyScanCatalog(),
		fakeTVDBEpisode{ID: 5, SeriesID: anthologyTVDBSeriesID, Name: aiChickensEpisode, Number: 2, SeasonNumber: 3, Aired: "1931-01-31"},
		fakeTVDBEpisode{ID: 6, SeriesID: anthologyTVDBSeriesID, Name: aiNightOwlsInTVDB, Number: 20, SeasonNumber: 2, Aired: "1930-05-04"},
	)
}

func TestAIRecovery_PassLevelPreconditionsMakeZeroPrompts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(f *aiMode2Fixture)
		wantErr string
	}{
		{name: "nil TMDB client", mutate: func(f *aiMode2Fixture) { f.sess.TMDB = nil }},
		{name: "no candidates", mutate: func(f *aiMode2Fixture) { f.candidates = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
			before := f.out[0]
			tc.mutate(f)
			f.run(t)
			if promptedForSeriesEpisode(f.ai) {
				t.Errorf("seriesPromptCount = %d, want 0", seriesPromptCount(f.ai))
			}
			assertRowUnchanged(t, f.out[0], before)
		})
	}

	t.Run("nil session", func(t *testing.T) {
		// Separate because it cannot share the fixture's assertion on
		// f.ai — there is no session to hang the client off.
		aiEpisodeRecoveryPass(context.Background(), nil, nil, nil, nil, nil, nil, nil, "", []int{0},
			[]proposals.Proposal{aiUnmatchedRow("/library/Series/Show/x.mkv", "/library/Series")})
	})
}

// TestIsDVDAuthoringName_BothStemForms pins BOTH branches of the predicate
// independently: neither the raw extension-stripped stem nor
// searchterm.FromName's output alone covers both real filenames, because they
// render the separator differently ("VTS_01_0" vs "VTS 01 0").
//
// Fix any failure here in isDVDAuthoringName, NEVER in junkStemRe
// (web_authority.go) — Movies and Adult both consume that shared predicate.
func TestIsDVDAuthoringName_BothStemForms(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		// The two REAL Candid Camera filenames (plan §0.3, §6.4).
		{name: "VIDEO_TS.VOB", want: true},
		{name: "VTS_01_0.VOB", want: true},
		// The same names as searchterm.FromName renders them.
		{name: "VIDEO TS.VOB", want: true},
		{name: "VTS 01 0.VOB", want: true},
		// Case and separator variants.
		{name: "video_ts.vob", want: true},
		{name: "vts-02-1.VOB", want: true},
		{name: "VTS_10_3.mkv", want: true},
		// Full paths — the predicate takes the base name.
		{name: "/media/Series/CANDID CAMERA/VIDEO_TS.VOB", want: true},
		// Negatives: real titles that merely LOOK similar.
		{name: "VTS Broadcast e601.mp4", want: false},
		{name: "Video Tsunami.mkv", want: false},
		{name: "The Video TS Story.mkv", want: false},
		{name: aiPolitiqueriasFile, want: false},
		{name: "Red Skelton More Funny Faces.mp4", want: false},
		{name: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDVDAuthoringName(tc.name); got != tc.want {
				t.Errorf("isDVDAuthoringName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}

	// The gap this guard exists to close, asserted rather than assumed: the
	// shared junk predicate says these two names are FINE, which is why a
	// Series-local guard is needed at all.
	for _, name := range []string{"VIDEO_TS.VOB", "VTS_01_0.VOB"} {
		if IsJunkRenameFilename(name) {
			t.Errorf("IsJunkRenameFilename(%q) = true — the premise of isDVDAuthoringName no longer holds; "+
				"if junkStemRe was widened for a Series-only problem, revert that instead", name)
		}
	}
}

func TestAIRecovery_ExcludedFilenamesMakeZeroPrompts(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		why  string
	}{
		{name: "dvd authoring VTS", file: "VTS_01_0.VOB", why: "isDVDAuthoringName"},
		{name: "dvd authoring VIDEO_TS", file: "VIDEO_TS.VOB", why: "isDVDAuthoringName"},
		{name: "dvd authoring spaced", file: "VTS 01 0.VOB", why: "isDVDAuthoringName"},
		{name: "junk stem", file: "RED_SKELTON_Title1.mp4", why: "IsJunkRenameFilename"},
		{name: "junk stem bare", file: "Video3.mkv", why: "IsJunkRenameFilename"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0), tc.file)
			before := f.out[0]
			f.run(t)
			if promptedForSeriesEpisode(f.ai) {
				t.Errorf("%s did not exclude %q: seriesPromptCount = %d, want 0",
					tc.why, tc.file, seriesPromptCount(f.ai))
			}
			assertRowUnchanged(t, f.out[0], before)
		})
	}
}

// TestAIRecovery_EmptySourceNameMakesZeroPrompts covers precondition 4.
// ParseSeriesEpisode's only internal gate is client != nil, so an empty
// filename still makes a real AI call there — the precondition here is what
// stops it.
func TestAIRecovery_EmptySourceNameMakesZeroPrompts(t *testing.T) {
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
	f.out[0].SourceName = "   "
	f.run(t)
	if promptedForSeriesEpisode(f.ai) {
		t.Errorf("seriesPromptCount = %d, want 0 for a blank SourceName", seriesPromptCount(f.ai))
	}
}

func TestAIRecovery_IneligibleRowStatesMakeZeroPrompts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(p *proposals.Proposal)
	}{
		{name: "already Pending", mutate: func(p *proposals.Proposal) { p.Status = proposals.Pending }},
		{name: "Unmatched but already carries a TMDB id", mutate: func(p *proposals.Proposal) { p.TMDBID = 555 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
			tc.mutate(&f.out[0])
			before := f.out[0]
			f.run(t)
			if promptedForSeriesEpisode(f.ai) {
				t.Errorf("seriesPromptCount = %d, want 0", seriesPromptCount(f.ai))
			}
			assertRowUnchanged(t, f.out[0], before)
		})
	}
}

// TestAIRecovery_HandledRowIsSkipped is the CONSUMER side of §2.3, run across
// ALL THREE of tvdbAnthologyPass's write branches.
//
// Branches 1 and 2 are the ones that matter and the ones a weaker test misses:
// both leave the row Unmatched with TMDBID == 0, so precondition 3 lets both
// through and handled[i] is the ONLY thing that stops them. Asserting "the row
// is still Unmatched" would pass even while this pass silently overwrote
// anthology's precise reason with its own vaguer one — so every case asserts
// the reason is BYTE-IDENTICAL.
func TestAIRecovery_HandledRowIsSkipped(t *testing.T) {
	pin := aiAnthologyPin()
	for _, tc := range []struct {
		name   string
		branch string
		mutate func(p *proposals.Proposal)
	}{
		{
			name: "branch 1 — ambiguity decline", branch: "res.Found >= 2",
			mutate: func(p *proposals.Proposal) {
				p.Reason = fmt.Sprintf(
					"%q matches more than one episode of %q on TheTVDB (S%02dE%02d %q and S%02dE%02d %q) — ambiguous, leaving in place for manual review",
					p.SourceName, pin.title, 3, 1, "Duck Soup", 3, 2, aiChickensEpisode)
			},
		},
		{
			// LOAD-BEARING: this mutate sets p.Reason ALONE. Status/TMDBID stay
			// at newAIMode2Fixture's base values (Unmatched / 0), which is what
			// keeps the row eligible past the :160 gate and makes the
			// byte-identity assertion below non-vacuous — this case is the only
			// genuine handled[i] coverage in the suite (plan §5.2.5/§9.7.1). Do
			// NOT "correct" them to a real post-softening row's Pending/synthID.
			name: "branch 2 — tracked-slot alternate", branch: "tracked[episodeKey{pin.synthID,...}]",
			mutate: func(p *proposals.Proposal) {
				p.Reason = fmt.Sprintf("alternate: %q S%02dE%02d already has a file in the library — apply will fold as primary or alternate by quality",
					pin.title, 3, 2)
			},
		},
		{
			name: "branch 3 — the Pending emit", branch: "the Pending emit",
			mutate: func(p *proposals.Proposal) {
				p.Status = proposals.Pending
				p.Title, p.TMDBID, p.TVDBID, p.Year = pin.title, pin.synthID, pin.tvdbID, pin.year
				p.SeasonNumber, p.EpisodeNumber = 3, 1
				p.Reason = fmt.Sprintf("%s %q -> S%02dE%02d (tvdb %d)", tvdbAnthologyReasonPrefix, "Duck Soup", 3, 1, pin.tvdbID)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
			tc.mutate(&f.out[0])
			f.handled[0] = true
			before := f.out[0]

			f.run(t)

			if promptedForSeriesEpisode(f.ai) {
				t.Errorf("%s: seriesPromptCount = %d, want 0 — a handled row must never reach the model",
					tc.branch, seriesPromptCount(f.ai))
			}
			if f.out[0].Reason != before.Reason {
				t.Errorf("%s: anthology's reason was OVERWRITTEN\n got: %q\nwant: %q", tc.branch, f.out[0].Reason, before.Reason)
			}
			assertRowUnchanged(t, f.out[0], before)
		})
	}
}

// ---------------------------------------------------------------------------
// Mode selection (§2.5.3)
// ---------------------------------------------------------------------------

// TestAIRecovery_Mode3IsNotImplemented pins BOTH the deferral (§2.6) and the
// ORDERING that makes it free (§2.5.3): mode selection is a pure map lookup
// that runs BEFORE the AI call, so a row in neither map costs zero prompts.
// Had selection run after the call, the expected count would be 1.
//
// The fixture deliberately contains a SECOND, RESOLVED folder. Without it, a
// mutation from a per-key presence test to a "did anything resolve at all"
// test (len(resolved) > 0) would still pass here.
func TestAIRecovery_Mode3IsNotImplemented(t *testing.T) {
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))

	// Row 0 moves to an UNRESOLVED, UNPINNED folder — mode 3.
	f.out[0] = aiUnmatchedRow(filepath.Join(f.root, "Some Other Show", aiPolitiqueriasFile), f.root)
	orphanKey := showFolderKey(f.out[0].SourcePath, f.roots)
	if _, present := f.resolved[orphanKey]; present {
		t.Fatalf("fixture error: %q must be absent from resolved", orphanKey)
	}
	if _, present := f.folderIDs[orphanKey]; present {
		t.Fatalf("fixture error: %q must be absent from folderIDs", orphanKey)
	}
	// ... while the Laurel & Hardy folder IS still resolved (f.resolved[f.key]),
	// so "some folder resolved" is true and "THIS folder resolved" is false.
	if len(f.resolved) == 0 {
		t.Fatal("fixture error: another folder must remain resolved")
	}
	before := f.out[0]

	f.run(t)

	if promptedForSeriesEpisode(f.ai) {
		t.Errorf("seriesPromptCount = %d, want 0 — a MODE 3 row must cost zero AI calls, which is only "+
			"achievable because mode selection runs BEFORE the call", seriesPromptCount(f.ai))
	}
	assertRowUnchanged(t, f.out[0], before)
}

// TestAIRecovery_EmptyShowFolderKeyDeclines covers the `key == ""` arm: a file
// sitting directly in a root has no show folder to resolve against.
func TestAIRecovery_EmptyShowFolderKeyDeclines(t *testing.T) {
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
	f.out[0] = aiUnmatchedRow(filepath.Join(f.root, aiPolitiqueriasFile), f.root)
	if key := showFolderKey(f.out[0].SourcePath, f.roots); key != "" {
		t.Fatalf("fixture error: showFolderKey = %q, want empty", key)
	}
	before := f.out[0]

	f.run(t)

	if promptedForSeriesEpisode(f.ai) {
		t.Errorf("seriesPromptCount = %d, want 0", seriesPromptCount(f.ai))
	}
	assertRowUnchanged(t, f.out[0], before)
}

// TestAIRecovery_EmptyEpisodeTitleDeclines is the single decline point after
// the call (§2.5.4): the model produced no usable episode title.
func TestAIRecovery_EmptyEpisodeTitleDeclines(t *testing.T) {
	for _, tc := range []struct{ name, episode string }{
		{name: "empty", episode: ""},
		{name: "whitespace only", episode: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", tc.episode, 0, 0))
			before := f.out[0]
			f.run(t)
			if n := seriesPromptCount(f.ai); n != 1 {
				t.Errorf("seriesPromptCount = %d, want exactly 1 — the call IS made, its RESULT is declined", n)
			}
			assertRowUnchanged(t, f.out[0], before)
		})
	}
}

// ---------------------------------------------------------------------------
// MODE 1 — folder pinned to a tracked TMDB show (§2.5.5)
// ---------------------------------------------------------------------------

// aiMode1Fixture wires the MODE 1 path: a real tracked TMDB show, a real TMDB
// episode-title server, and a filename whose OWN tokens match nothing in the
// catalog — only the AI's recovered title does. That is what makes this a test
// of the AI pass rather than of tryEpisodeTitleMatchSeries' normal call site.
type aiMode1Fixture struct {
	root       string
	roots      []string
	sess       *mode.Session
	tracked    map[episodeKey]bool
	seriesByID map[int]library.Series
	folderIDs  map[string]int
	candidates []int
	out        []proposals.Proposal
	ai         *countingAI
	tmdbReqs   int
}

// aiSpanishSkeltonFile carries no token of any episode title in the fixture
// catalog, so every deterministic path declines it and only the AI's recovered
// "More Funny Faces" can place it.
const aiSpanishSkeltonFile = "Red Skelton Caras Divertidas.mp4"

func newAIMode1Fixture(t *testing.T, resp map[string]any, foundRoot string) *aiMode1Fixture {
	t.Helper()
	root := "/library/Series"
	roots := []string{root}
	ai := &countingAI{resp: resp}
	f := &aiMode1Fixture{root: root, roots: roots, ai: ai}
	f.sess = &mode.Session{
		Mode:         mode.Series,
		MainstreamAI: ai,
		TMDB: fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, redSkeltonShowTitle,
			redSkeltonEpisodeFixture(), -1, &f.tmdbReqs),
	}
	f.tracked = map[episodeKey]bool{}
	f.seriesByID = map[int]library.Series{redSkeltonShowID: {
		TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root,
	}}
	path := filepath.Join(root, redSkeltonShowTitle, aiSpanishSkeltonFile)
	f.folderIDs = map[string]int{showFolderKey(path, roots): redSkeltonShowID}
	f.out = []proposals.Proposal{aiUnmatchedRow(path, foundRoot)}
	f.candidates = []int{0}
	return f
}

func (f *aiMode1Fixture) run(t *testing.T) {
	t.Helper()
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)
	aiEpisodeRecoveryPass(context.Background(), f.sess, f.tracked, f.seriesByID, f.folderIDs,
		map[string]anthologyResolution{}, map[int]bool{}, f.roots, f.root, f.candidates, f.out)
}

// TestAIRecovery_Mode1PinnedFolderProducesPending is the MODE 1 happy path,
// and it also pins §2.5.5's "prefix only, never parse or reconstruct": the
// reason must END with exactly what tryEpisodeTitleMatchSeries emits on its
// own, unmodified.
func TestAIRecovery_Mode1PinnedFolderProducesPending(t *testing.T) {
	f := newAIMode1Fixture(t, aiGuess(redSkeltonShowTitle, "More Funny Faces", 0, 0), "/library/Series")
	f.run(t)

	if n := seriesPromptCount(f.ai); n != 1 {
		t.Fatalf("seriesPromptCount = %d, want exactly 1", n)
	}
	p := f.out[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending", p.Status, p.Reason)
	}
	if p.Status == proposals.Applied {
		t.Fatal("this path must never produce Applied")
	}
	if p.TMDBID != redSkeltonShowID || p.SeasonNumber != 2 || p.EpisodeNumber != 2 {
		t.Errorf("got tmdb %d S%02dE%02d, want %d S02E02", p.TMDBID, p.SeasonNumber, p.EpisodeNumber, redSkeltonShowID)
	}
	// Title/Year come from the PINNED ROW only — inherited from
	// tryEpisodeTitleMatchSeries, and the split-provenance defect §0.3 exists
	// to prevent.
	if p.Title != redSkeltonShowTitle || p.Year != 1951 {
		t.Errorf("title/year = %q/%d, want %q/1951", p.Title, p.Year, redSkeltonShowTitle)
	}
	// SourceName/SourcePath survive — i.e. out[i], not a fresh Proposal{}, was
	// handed to tryEpisodeTitleMatchSeries as its base.
	if p.SourceName != aiSpanishSkeltonFile || p.SourcePath == "" {
		t.Errorf("base proposal was not carried through: SourceName=%q SourcePath=%q", p.SourceName, p.SourcePath)
	}

	wantPrefix := fmt.Sprintf("%s recovered episode title %q from %q; ",
		aiEpisodeMatchReasonPrefix, "More Funny Faces", aiSpanishSkeltonFile)
	if !strings.HasPrefix(p.Reason, wantPrefix) {
		t.Errorf("reason = %q, want prefix %q", p.Reason, wantPrefix)
	}
	// The underlying reason must be PRESERVED VERBATIM, not rebuilt.
	wantTail := fmt.Sprintf("%s %q -> S%02dE%02d", episodeTitleMatchReasonPrefix, "More Funny Faces", 2, 2)
	if !strings.HasSuffix(p.Reason, wantTail) {
		t.Errorf("reason = %q, want it to END with tryEpisodeTitleMatchSeries' own unmodified reason %q", p.Reason, wantTail)
	}
}

// TestAIRecovery_Mode1PassesFoundRootNotGeneralRoot pins the argument order a
// prior critic finding caught: transposing foundRoot and generalRoot breaks
// Kids routing SILENTLY — the proposal still forms, it just lands under the
// wrong root.
//
// With foundRoot == KidsRootPath the correct code takes
// tryEpisodeTitleMatchSeries' FIRST switch branch ("where the file was found
// wins"). Passing generalRoot in that position makes branch 1 false, branch 2
// false (pin.root is the general root here), and the file silently lands under
// the general root instead.
func TestAIRecovery_Mode1PassesFoundRootNotGeneralRoot(t *testing.T) {
	const kidsRoot = "/library/Kids Series"
	f := newAIMode1Fixture(t, aiGuess(redSkeltonShowTitle, "More Funny Faces", 0, 0), kidsRoot)
	f.sess.KidsRootPath = kidsRoot

	f.run(t)

	p := f.out[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending", p.Status, p.Reason)
	}
	if p.RootFolderPath != kidsRoot {
		t.Errorf("RootFolderPath = %q, want %q — foundRoot (out[i].RootFolderPath, read BEFORE the "+
			"rewrite) must be passed in tryEpisodeTitleMatchSeries' foundRoot position, not generalRoot",
			p.RootFolderPath, kidsRoot)
	}
}

// TestAIRecovery_Mode1TrackedSlotDeclines proves MODE 1 genuinely INHERITS
// tryEpisodeTitleMatchSeries' tracked-slot handling rather than merely being
// claimed to (§2.5.5). It reaches SITE 5 through aiEpisodeMode1's verbatim
// reuse — it never enters aiEpisodeMode2, so it is NOT Site 7 coverage
// (TestAIRecovery_Mode2TrackedSlotDeclines is; plan §9.5).
//
// Unlike MODE 2's key, this one is the REAL POSITIVE TMDB id — MODE 1 resolves
// against a tracked TMDB show, not an anthology synthetic.
//
// Softened outcome (plan §9.7.1(B), B3): a match onto an already-tracked slot
// is now a legitimate ALTERNATE, so the row is Pending and legitimately CARRIES
// its placement data — Apply folds it in against live DB state rather than
// overwriting the tracked row. The reason assertion is Contains, NOT HasPrefix:
// aiEpisodeMode1 wraps EVERY non-nil return from tryEpisodeTitleMatchSeries in
// aiEpisodeMatchReasonPrefix, so the row reads "<aiPrefix> …; alternate: …".
// The aiEpisodeMatchReasonPrefix assertion below stays — it pins that wrapping,
// which this work does not change (contrast Site 7, which loses the prefix).
func TestAIRecovery_Mode1TrackedSlotDeclines(t *testing.T) {
	f := newAIMode1Fixture(t, aiGuess(redSkeltonShowTitle, "More Funny Faces", 0, 0), "/library/Series")
	f.tracked[episodeKey{tmdbID: redSkeltonShowID, season: 2, episode: 2}] = true

	f.run(t)

	p := f.out[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending-as-alternate onto the tracked slot",
			p.Status, p.Reason)
	}
	if p.TMDBID != redSkeltonShowID || p.SeasonNumber != 2 || p.EpisodeNumber != 2 {
		t.Errorf("row is missing the placement data a softened alternate must carry: tmdb=%d S%02dE%02d, want tmdb=%d S02E02",
			p.TMDBID, p.SeasonNumber, p.EpisodeNumber, redSkeltonShowID)
	}
	if !strings.Contains(p.Reason, "alternate:") {
		t.Errorf("reason = %q, want it to contain the softened %q clause", p.Reason, "alternate:")
	}
	if !strings.HasPrefix(p.Reason, aiEpisodeMatchReasonPrefix) {
		t.Errorf("reason = %q, want the %q prefix", p.Reason, aiEpisodeMatchReasonPrefix)
	}
}

// TestAIRecovery_Mode1IncompleteCatalogFailsClosed proves MODE 1 inherits
// searchEpisodeByTitle's fail-CLOSED posture: a season that cannot be read
// might have held the second match that would have made the result ambiguous,
// so an unreadable season aborts the WHOLE search rather than downgrading the
// guarantee to "unique across the seasons that happened to load".
func TestAIRecovery_Mode1IncompleteCatalogFailsClosed(t *testing.T) {
	root := "/library/Series"
	roots := []string{root}
	ai := &countingAI{resp: aiGuess(redSkeltonShowTitle, "More Funny Faces", 0, 0)}
	var reqs int
	// Season 1 404s. The unique match lives in season 2 and would otherwise be
	// found — so a PASSING assertion here means the hole, not the match, won.
	sess := &mode.Session{Mode: mode.Series, MainstreamAI: ai,
		TMDB: fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, redSkeltonShowTitle, redSkeltonEpisodeFixture(), 1, &reqs)}
	path := filepath.Join(root, redSkeltonShowTitle, aiSpanishSkeltonFile)
	out := []proposals.Proposal{aiUnmatchedRow(path, root)}
	before := out[0]

	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)
	aiEpisodeRecoveryPass(context.Background(), sess, map[episodeKey]bool{},
		map[int]library.Series{redSkeltonShowID: {TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root}},
		map[string]int{showFolderKey(path, roots): redSkeltonShowID},
		map[string]anthologyResolution{}, map[int]bool{}, roots, root, []int{0}, out)

	if n := seriesPromptCount(ai); n != 1 {
		t.Errorf("seriesPromptCount = %d, want exactly 1 — the call IS made, its RESULT is declined", n)
	}
	assertRowUnchanged(t, out[0], before)
}

// TestAIRecovery_Mode1AmbiguousTitleStaysUnmatched proves the reused
// episodeTitleMatches uniqueness rule still governs an AI-recovered title:
// two seasons carrying the same episode name decline rather than guess.
func TestAIRecovery_Mode1AmbiguousTitleStaysUnmatched(t *testing.T) {
	root := "/library/Series"
	roots := []string{root}
	ai := &countingAI{resp: aiGuess(redSkeltonShowTitle, "More Funny Faces", 0, 0)}
	var reqs int
	// CONSTRUCTED duplicate: a claim about the matcher's uniqueness rule, not
	// about TMDB's real Red Skelton catalog.
	dup := map[int][]tmdb.SeasonEpisode{
		1: {{EpisodeNumber: 4, Name: "More Funny Faces", AirDate: "1951-10-14"}},
		2: {{EpisodeNumber: 2, Name: "More Funny Faces", AirDate: "1952-10-05"}},
	}
	sess := &mode.Session{Mode: mode.Series, MainstreamAI: ai,
		TMDB: fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, redSkeltonShowTitle, dup, -1, &reqs)}
	path := filepath.Join(root, redSkeltonShowTitle, aiSpanishSkeltonFile)
	out := []proposals.Proposal{aiUnmatchedRow(path, root)}

	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)
	aiEpisodeRecoveryPass(context.Background(), sess, map[episodeKey]bool{},
		map[int]library.Series{redSkeltonShowID: {TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root}},
		map[string]int{showFolderKey(path, roots): redSkeltonShowID},
		map[string]anthologyResolution{}, map[int]bool{}, roots, root, []int{0}, out)

	if out[0].Status != proposals.Pending && out[0].TMDBID != 0 {
		t.Fatalf("unexpected: %+v", out[0])
	}
	if out[0].Status != proposals.Unmatched {
		t.Fatalf("status = %v (%s), want Unmatched on an ambiguous title", out[0].Status, out[0].Reason)
	}
	if !strings.Contains(out[0].Reason, "ambiguous") {
		t.Errorf("reason = %q, want the distinct ambiguity reason", out[0].Reason)
	}
	// Prefixed, not rewritten — the AI provenance survives on the decline too.
	if !strings.HasPrefix(out[0].Reason, aiEpisodeMatchReasonPrefix) {
		t.Errorf("reason = %q, want the %q prefix", out[0].Reason, aiEpisodeMatchReasonPrefix)
	}
}

// ---------------------------------------------------------------------------
// MODE 2 — folder resolved by tvdbAnthologyPass this scan (§2.5.6)
// ---------------------------------------------------------------------------

// TestAIRecovery_Mode2ResolvesForeignTitle is the case this whole story exists
// for: a Laurel & Hardy short whose filename carries the SPANISH episode title
// "Politiquerias", resolving against TheTVDB's English "Chickens Come Home".
//
// Laurel & Hardy has no TMDB TV entry at all, which is why MODE 1 cannot reach
// this population and why MODE 2 is the mode that matters.
func TestAIRecovery_Mode2ResolvesForeignTitle(t *testing.T) {
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
	f.run(t)

	if n := seriesPromptCount(f.ai); n != 1 {
		t.Fatalf("seriesPromptCount = %d, want exactly 1", n)
	}
	p := f.out[0]
	pin := aiAnthologyPin()
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending", p.Status, p.Reason)
	}
	if p.Status == proposals.Applied {
		t.Fatal("this path must never produce Applied")
	}
	if p.SeasonNumber != 3 || p.EpisodeNumber != 2 {
		t.Errorf("got S%02dE%02d, want S03E02", p.SeasonNumber, p.EpisodeNumber)
	}
	if p.TMDBID != pin.synthID || p.TVDBID != pin.tvdbID {
		t.Errorf("ids = tmdb %d / tvdb %d, want the synthetic %d / the real %d",
			p.TMDBID, p.TVDBID, pin.synthID, pin.tvdbID)
	}
	if p.TMDBID >= 0 {
		t.Errorf("TMDBID = %d, want a strictly negative synthetic id", p.TMDBID)
	}
	if p.Title != pin.title || p.Year != pin.year {
		t.Errorf("title/year = %q/%d, want %q/%d (from the pin)", p.Title, p.Year, pin.title, pin.year)
	}
	if p.RootFolderPath != f.root {
		t.Errorf("RootFolderPath = %q, want %q", p.RootFolderPath, f.root)
	}
	// No Genres/Cast enrichment: there is no TMDB id to enrich against, and
	// inventing one would be the split-provenance defect (§2.5.6 step 4).
	if len(p.Genres) != 0 || len(p.Cast) != 0 {
		t.Errorf("Genres=%v Cast=%v, want both EMPTY — MODE 2 has no TMDB id to enrich against", p.Genres, p.Cast)
	}
	if len(p.ExtraEpisodeNumbers) != 0 {
		t.Errorf("ExtraEpisodeNumbers = %v, want none (a title match resolves exactly one slot)", p.ExtraEpisodeNumbers)
	}
	wantReason := fmt.Sprintf("%s recovered episode title %q from %q -> %q S%02dE%02d (tvdb %d)",
		aiEpisodeMatchReasonPrefix, aiChickensEpisode, aiPolitiqueriasFile, aiChickensEpisode, 3, 2, pin.tvdbID)
	if p.Reason != wantReason {
		t.Errorf("reason = %q, want %q", p.Reason, wantReason)
	}
}

// TestAIRecovery_Mode2MakesZeroCatalogCalls pins the "zero new catalog calls"
// claim by COUNTING requests rather than asserting it in prose.
//
// sess.TVDB is a REAL working client backed by a counting httptest fake, so a
// catalog fetch would succeed rather than panic — which is what makes the
// counter the discriminator. sess.TMDB is the fatal server, covering the other
// direction. MODE 2 must match entirely against resolved[key].catalog, which
// tvdbAnthologyPass already fetched and this pass consumes for free.
func TestAIRecovery_Mode2MakesZeroCatalogCalls(t *testing.T) {
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
	tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: aiScanCatalogWithChickens(),
	}})
	f.sess.TVDB = tvdbClient

	f.run(t)

	if f.out[0].Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending — the pass must have matched against the "+
			"already-fetched catalog", f.out[0].Status, f.out[0].Reason)
	}
	if n := counts.Episodes.Load(); n != 0 {
		t.Errorf("catalog fetches = %d, want 0 — MODE 2 must reuse resolved[key].catalog", n)
	}
	if n := counts.Total.Load(); n != 0 {
		t.Errorf("TheTVDB requests = %d, want 0 of ANY kind (search included)", n)
	}
}

// TestAIRecovery_Mode2AmbiguousEpisodeTitleDeclines: a model-recovered title
// matching two catalog slots is ambiguity, not evidence. It declines SILENTLY
// — unlike tvdbAnthologyPass's own ambiguity branch, the ambiguous string here
// is a confabulation candidate, so presenting it in a rewritten reason would
// dress a guess up as a finding.
func TestAIRecovery_Mode2AmbiguousEpisodeTitleDeclines(t *testing.T) {
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
	r := f.resolved[f.key]
	r.catalog = append(r.catalog, tvdb.Episode{
		SeriesID: anthologyTVDBSeriesID, SeasonNumber: 7, Number: 4, Name: aiChickensEpisode, Aired: "1935-02-01",
	})
	f.resolved[f.key] = r
	before := f.out[0]

	f.run(t)

	if n := seriesPromptCount(f.ai); n != 1 {
		t.Errorf("seriesPromptCount = %d, want exactly 1", n)
	}
	assertRowUnchanged(t, f.out[0], before)
}

// TestAIRecovery_Mode2TrackedSlotDeclines reaches SITE 7 (aiEpisodeMode2) and
// is Site 7's ONLY test — the sibling Mode-1 test above reaches Site 5 through
// tryEpisodeTitleMatchSeries and must never be recorded as Site 7 coverage
// (plan §9.5).
//
// The tracked key uses the SYNTHETIC NEGATIVE id, not a real positive one —
// that is what the emit path keys on.
//
// Softened outcome (plan §9.7.1(B), B4): the row is Pending-as-alternate and
// legitimately carries its placement data. Note what is DELETED rather than
// relaxed here: Site 7 built its reason inline INCLUDING
// aiEpisodeMatchReasonPrefix, and acceptDuplicatePendingEpisode writes Reason
// WHOLESALE — so the AI prefix is lost on this path, deliberately. Asserting it
// would be asserting something false. Site 5 via Mode 1 keeps its prefix
// because that caller wraps rather than replaces; the two AI modes genuinely
// end up with different reason shapes.
func TestAIRecovery_Mode2TrackedSlotDeclines(t *testing.T) {
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
	pin := aiAnthologyPin()
	f.tracked[episodeKey{tmdbID: pin.synthID, season: 3, episode: 2}] = true

	f.run(t)

	p := f.out[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending-as-alternate onto the tracked slot",
			p.Status, p.Reason)
	}
	// TMDBID is the negative synthetic id, so the check is != 0, never <= 0.
	if p.TMDBID != pin.synthID || p.SeasonNumber != 3 || p.EpisodeNumber != 2 {
		t.Errorf("row is missing the placement data a softened alternate must carry: tmdb=%d S%02dE%02d, want tmdb=%d S03E02",
			p.TMDBID, p.SeasonNumber, p.EpisodeNumber, pin.synthID)
	}
	wantPrefix := fmt.Sprintf("alternate: %q S03E02 already has a file in the library", pin.title)
	if !strings.HasPrefix(p.Reason, wantPrefix) {
		t.Errorf("reason = %q, want the softened alternate reason (prefix %q)", p.Reason, wantPrefix)
	}
}

// TestAIRecovery_Mode2KidsRoutingHonoursUnconfiguredKidsRoot is the prior
// code-review finding §2.5.6 names, asserted from the outside.
//
// With sess.KidsRootPath == "" and pin.root == "", dropping the
// `sess.KidsRootPath != ""` guard from the SECOND switch branch makes two
// empty strings compare equal, the branch fires, and RootFolderPath silently
// becomes "" — after which RelocateEpisodeRange builds a RELATIVE destination
// instead of refusing.
//
// out[i].RootFolderPath is deliberately NON-EMPTY here, as a real Scan always
// produces (proposeOneEpisodeLibrary seeds it with foundRoot). An empty one
// would make the FIRST branch fire on "" == "" — which is byte-identical to
// both sibling paths and must not be "fixed".
func TestAIRecovery_Mode2KidsRoutingHonoursUnconfiguredKidsRoot(t *testing.T) {
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
	f.sess.KidsRootPath = "" // unconfigured
	r := f.resolved[f.key]
	r.pin.root = "" // degenerate/legacy pin with no recorded root
	f.resolved[f.key] = r

	f.run(t)

	p := f.out[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending", p.Status, p.Reason)
	}
	if p.RootFolderPath == "" {
		t.Fatal("RootFolderPath = \"\" — the sess.KidsRootPath != \"\" guard on the second Kids branch was dropped")
	}
	if p.RootFolderPath != f.root {
		t.Errorf("RootFolderPath = %q, want the general root %q", p.RootFolderPath, f.root)
	}
}

// TestAIRecovery_Mode2KidsRoutingFollowsTheFoundRoot is the positive control
// for the case above: where the file was FOUND wins, matching acceptSeries.
func TestAIRecovery_Mode2KidsRoutingFollowsTheFoundRoot(t *testing.T) {
	const kidsRoot = "/library/Kids Series"
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
	f.sess.KidsRootPath = kidsRoot
	f.out[0].RootFolderPath = kidsRoot

	f.run(t)

	if f.out[0].RootFolderPath != kidsRoot {
		t.Errorf("RootFolderPath = %q, want %q — foundRoot must be read from out[i] BEFORE the rewrite",
			f.out[0].RootFolderPath, kidsRoot)
	}
}

// TestAIRecovery_Mode2UsesCandNameNotPinTitle pins §2.5.6's correctness
// requirement: searchTVDBEpisodeByTitle's show-title argument feeds
// episodeTitleMatches' SUBTRACTION RESIDUAL, so passing a different string
// than the catalog was scanned with yields a DIFFERENT match set, silently.
//
// The fixture models the established-pin rescan, where the two genuinely
// differ: pin.title is the TRACKED ROW's title (overwritten at
// series_tvdb_episode_match.go's established-pin shortcut) while candName is
// TheTVDB's own. Here the tracked title is chosen so that subtracting IT from
// "Chickens Come Home" empties the residual and the match is lost — which is
// exactly the silent failure the separate candName field exists to prevent.
func TestAIRecovery_Mode2UsesCandNameNotPinTitle(t *testing.T) {
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
	r := f.resolved[f.key]
	// The tracked row's own title. It still shares tokens with the show folder
	// name, so gate Check A passes either way and the ONLY thing this case can
	// fail on is the searchTVDBEpisodeByTitle argument — but subtracting it
	// from "Chickens Come Home" EMPTIES the residual, so a pin.title call finds
	// nothing while a candName call finds S03E02.
	r.pin.title = "Laurel & Hardy: Chickens Come Home Collection"
	r.candName = "Laurel & Hardy" // TheTVDB's own, which the catalog was scanned with
	f.resolved[f.key] = r

	f.run(t)

	p := f.out[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending — searchTVDBEpisodeByTitle must be called with "+
			"resolved[key].candName, NEVER resolved[key].pin.title", p.Status, p.Reason)
	}
	if p.SeasonNumber != 3 || p.EpisodeNumber != 2 {
		t.Errorf("got S%02dE%02d, want S03E02", p.SeasonNumber, p.EpisodeNumber)
	}
	// pin.title is still the folder-naming authority — a different job, and it
	// is correct everywhere it appears in the emit block.
	if p.Title != "Laurel & Hardy: Chickens Come Home Collection" {
		t.Errorf("Title = %q, want the PIN's title — only the searchTVDBEpisodeByTitle argument changes", p.Title)
	}
}

// TestAIRecovery_Mode2GateIsSeededWithTheShowFolderName pins §2.5.6 step 3.
//
// The filename here shares ZERO tokens with the show's title — which is the
// normal shape for this population, not an edge case: a short's own name
// rarely repeats "Laurel & Hardy". Re-seeding the gate with it makes Check A
// compare "Politiquerias" against "Laurel & Hardy", find no overlap, and veto
// every legitimate match. Seeding with the show folder name is what the
// anthology pass does, and it is the comparison allowsTitle was designed for.
//
// This is the plausible-looking edit that breaks the whole feature: "the gate
// evaluates true by construction, so make it meaningful by seeding it with the
// filename." Do not.
func TestAIRecovery_Mode2GateIsSeededWithTheShowFolderName(t *testing.T) {
	const bareForeignFile = "Politiquerias.mp4"
	f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0), bareForeignFile)
	// Fixture premise, asserted so a future tokenizer change cannot make this
	// case silently vacuous.
	if HasTitleTokenOverlap(bareForeignFile, aiAnthologyPin().title) {
		t.Fatalf("fixture error: %q must share no tokens with %q", bareForeignFile, aiAnthologyPin().title)
	}

	f.run(t)

	p := f.out[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending — the gate must be seeded with the SHOW FOLDER NAME, "+
			"never with the filename", p.Status, p.Reason)
	}
	if p.SeasonNumber != 3 || p.EpisodeNumber != 2 {
		t.Errorf("got S%02dE%02d, want S03E02", p.SeasonNumber, p.EpisodeNumber)
	}
}

// ---------------------------------------------------------------------------
// The numeric sink (§2.4 / spec deviation #3)
// ---------------------------------------------------------------------------

// TestAIRecovery_ModelNumbersAreNeverRead asserts the numeric-sink guarantee
// rather than arguing it, in BOTH modes: the model is fed a plausible-looking
// but WRONG season/episode alongside a good episode title, and the
// CATALOG-derived slot must win every time.
//
// This is the guarantee that makes a wrong model answer recoverable: a wrong
// TITLE simply fails to find a catalog match and the file stays Unmatched
// (fail-closed), while a trusted wrong NUMBER would produce a confident
// Pending on the wrong slot.
func TestAIRecovery_ModelNumbersAreNeverRead(t *testing.T) {
	t.Run("mode 1", func(t *testing.T) {
		f := newAIMode1Fixture(t, aiGuess(redSkeltonShowTitle, "More Funny Faces", 99, 99), "/library/Series")
		f.run(t)
		if f.out[0].Status != proposals.Pending {
			t.Fatalf("status = %v (%s), want Pending", f.out[0].Status, f.out[0].Reason)
		}
		if f.out[0].SeasonNumber != 2 || f.out[0].EpisodeNumber != 2 {
			t.Errorf("got S%02dE%02d, want the CATALOG's S02E02 — the model's 99/99 must never be read",
				f.out[0].SeasonNumber, f.out[0].EpisodeNumber)
		}
	})

	t.Run("mode 2", func(t *testing.T) {
		f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 99, 99))
		f.run(t)
		if f.out[0].Status != proposals.Pending {
			t.Fatalf("status = %v (%s), want Pending", f.out[0].Status, f.out[0].Reason)
		}
		if f.out[0].SeasonNumber != 3 || f.out[0].EpisodeNumber != 2 {
			t.Errorf("got S%02dE%02d, want the CATALOG's S03E02 — the model's 99/99 must never be read",
				f.out[0].SeasonNumber, f.out[0].EpisodeNumber)
		}
	})
}

// TestAIRecovery_NeverProducesApplied: every proposal either mode produces is
// Pending or Unmatched. Structural (Apply is a separate operator-initiated
// HTTP route nothing in internal/rename can reach), asserted anyway.
func TestAIRecovery_NeverProducesApplied(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T) []proposals.Proposal
	}{
		{name: "mode 1 match", run: func(t *testing.T) []proposals.Proposal {
			f := newAIMode1Fixture(t, aiGuess(redSkeltonShowTitle, "More Funny Faces", 0, 0), "/library/Series")
			f.run(t)
			return f.out
		}},
		{name: "mode 2 match", run: func(t *testing.T) []proposals.Proposal {
			f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
			f.run(t)
			return f.out
		}},
		{name: "mode 2 tracked-slot decline", run: func(t *testing.T) []proposals.Proposal {
			f := newAIMode2Fixture(t, aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0))
			f.tracked[episodeKey{tmdbID: aiAnthologyPin().synthID, season: 3, episode: 2}] = true
			f.run(t)
			return f.out
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range tc.run(t) {
				if p.Status == proposals.Applied {
					t.Errorf("%q reached Applied: %+v", p.SourceName, p)
				}
				if p.Status != proposals.Pending && p.Status != proposals.Unmatched {
					t.Errorf("%q has status %v, want Pending or Unmatched", p.SourceName, p.Status)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// End-to-end wiring (the two claims a direct call cannot make)
// ---------------------------------------------------------------------------

// aiScanFixture seeds a real Laurel & Hardy folder under sub, wires a real
// TheTVDB fake, and runs the REAL ScanLibrarySeries.
func aiScanFixture(t *testing.T, sub string, ai *countingAI, files ...string) []proposals.Proposal {
	t.Helper()
	root := t.TempDir()
	seedAnthologyFiles(t, root, sub, files...)
	tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: aiScanCatalogWithChickens(),
	}})
	sess := &mode.Session{Mode: mode.Series, MainstreamAI: ai,
		TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
		naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return got
}

// TestAIRecovery_WithinBatchCollisionAnnotatesTheSecondRow pins §0.8's
// ordering constraint FROM THE OUTSIDE, where a future reorder would actually
// be caught: the pass must run BEFORE the `seen` collision loop, so the Pending
// rows it creates inherit the within-batch episodeKey guard for free.
//
// Two foreign-titled files, one canned AI response, therefore one slot. If the
// pass ran AFTER the `seen` loop, NEITHER row would carry the guard's
// annotation. The ordering is unchanged; only the guard's outcome softened
// (plan §9.7.1(B), B5) — nothing is "demoted" any more, hence the rename from
// ..._DemotesTheSecondRow. Both rows stay Pending and the second carries the
// "alternate:" annotation, so which row is which is discriminated on the
// ANNOTATION, not on status (status no longer distinguishes them at all).
func TestAIRecovery_WithinBatchCollisionAnnotatesTheSecondRow(t *testing.T) {
	ai := &countingAI{resp: aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0)}
	got := aiScanFixture(t, anthologyShowFolder, ai,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile,
		aiPolitiqueriasFile, aiLadronesFile)

	if n := seriesPromptCount(ai); n != 2 {
		t.Fatalf("seriesPromptCount = %d, want 2 (one per foreign-titled file; the four "+
			"anthology-handled rows must make none)", n)
	}

	first := proposalByName(t, got, aiPolitiqueriasFile)
	second := proposalByName(t, got, aiLadronesFile)
	// Which of the two the walk reaches first is a filesystem-ordering detail,
	// so discriminate on the `seen` guard's own annotation. Both rows are
	// Pending now, so a status-based pick would silently always choose `second`.
	wantAnnotation := fmt.Sprintf(
		"alternate: another file in this scan also claims %q S%02dE%02d — apply will fold as primary or alternate by quality.",
		aiAnthologyPin().title, 3, 2)
	pending, annotated := first, second
	if strings.HasPrefix(first.Reason, wantAnnotation) {
		pending, annotated = second, first
	}
	if !strings.HasPrefix(annotated.Reason, wantAnnotation) {
		t.Fatalf("neither AI-resolved row carries the `seen` guard's annotation (%q): %q=%q, %q=%q",
			wantAnnotation, first.SourceName, first.Reason, second.SourceName, second.Reason)
	}
	if strings.HasPrefix(pending.Reason, wantAnnotation) {
		t.Fatalf("BOTH rows carry the `seen` guard's annotation — only the second claimant may")
	}
	if pending.Status != proposals.Pending {
		t.Fatalf("the primary claimant has status %v (%s), want Pending", pending.Status, pending.Reason)
	}
	if pending.SeasonNumber != 3 || pending.EpisodeNumber != 2 {
		t.Errorf("Pending row is S%02dE%02d, want S03E02", pending.SeasonNumber, pending.EpisodeNumber)
	}
	if annotated.Status != proposals.Pending {
		t.Fatalf("second row on the same slot has status %v (%s), want Pending-as-alternate via the `seen` guard",
			annotated.Status, annotated.Reason)
	}
}

// TestAIRecovery_UsesShowFolderNameNotParentDir is §2.5.4's fail-open bug,
// the one tonight's earlier TVDB anthology work already had to fix once: for
// a file nested one level below the show folder,
// filepath.Base(filepath.Dir(path)) yields the SUBDIRECTORY name, which
// carries no title information and actively misleads the model.
func TestAIRecovery_UsesShowFolderNameNotParentDir(t *testing.T) {
	ai := &countingAI{resp: aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0)}
	got := aiScanFixture(t, filepath.Join(anthologyShowFolder, "Uncompressed"), ai,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile,
		aiPolitiqueriasFile)

	if n := seriesPromptCount(ai); n != 1 {
		t.Fatalf("seriesPromptCount = %d, want 1", n)
	}
	var prompt string
	for _, p := range ai.prompts {
		if strings.Contains(p, identify.SeriesEpisodePromptMarker) {
			prompt = p
		}
	}
	if !strings.Contains(prompt, anthologyShowFolder) {
		t.Errorf("prompt does not carry the SHOW FOLDER name %q:\n%s", anthologyShowFolder, prompt)
	}
	if strings.Contains(prompt, "Uncompressed") {
		t.Errorf("prompt carries the nested subdirectory name %q — showFolderName was replaced by "+
			"filepath.Base(filepath.Dir(...)):\n%s", "Uncompressed", prompt)
	}
	if p := proposalByName(t, got, aiPolitiqueriasFile); p.Status != proposals.Pending {
		t.Errorf("status = %v (%s), want Pending", p.Status, p.Reason)
	}
}

// TestScanLibraryMoviesMakesZeroSeriesPrompts is spec Non-Goal 4's structural
// guarantee. Note this is exactly the assertion a bare countingAI.calls could
// NOT make: Movies legitimately calls GuessTitle, so .calls is non-zero by
// design and an equality-to-zero assertion on it would fail spuriously.
func TestScanLibraryMoviesMakesZeroSeriesPrompts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Some Opaque Movie File.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ai := &countingAI{resp: map[string]any{"title": "Some Opaque Movie File"}}
	sess := &mode.Session{Mode: mode.Movies, MainstreamAI: ai,
		TMDB: fakeTMDBSearch(t, map[string]string{})}

	if _, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), root,
		naming.Jellyfin, DefaultMatchConfig(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := seriesPromptCount(ai); n != 0 {
		t.Errorf("seriesPromptCount = %d, want 0 — Movies must never reach the Series episode prompt", n)
	}
}
