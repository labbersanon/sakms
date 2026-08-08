package rename

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/tvdb"
)

// TestAnthologyTMDBID pins the four properties library_series.tmdb_id's
// NOT NULL / UNIQUE constraint depends on: the id is deterministic, strictly
// negative, never the 0 sentinel, and distinct per distinct TheTVDB series id.
//
// The collision sample is deliberately bounded and deterministic. The fold is
// a 31-bit space (~2.1e9 slots), so a birthday collision becomes likely well
// before 1e5 inputs -- a larger or randomised sample would produce a flaky
// test whose only "fix" is changing the derivation, which is exactly what
// this feature must not do (the shared range with WebAuthorityTMDBID is an
// accepted, documented risk, not a defect to engineer around).
func TestAnthologyTMDBID(t *testing.T) {
	// Real-world-shaped TheTVDB series ids plus boundary values.
	ids := []int{0, 1, 2, 71663, 73141, 78804, 121361, 305288, 355567, 424435, -1}

	t.Run("stable across calls", func(t *testing.T) {
		for _, id := range ids {
			first := anthologyTMDBID(id)
			for i := 0; i < 5; i++ {
				if got := anthologyTMDBID(id); got != first {
					t.Fatalf("anthologyTMDBID(%d) not stable: call 0 = %d, call %d = %d", id, first, i+1, got)
				}
			}
		}
	})

	t.Run("strictly negative and never zero", func(t *testing.T) {
		for _, id := range ids {
			got := anthologyTMDBID(id)
			if got >= 0 {
				t.Errorf("anthologyTMDBID(%d) = %d, want strictly negative", id, got)
			}
			if got == 0 {
				t.Errorf("anthologyTMDBID(%d) = 0, which collides with pinFolderID's sticky-ambiguous sentinel", id)
			}
		}
	})

	t.Run("output stays inside the negative 31-bit range", func(t *testing.T) {
		// mask -> [0, 2^31-1], +1 -> [1, 2^31], negate -> [-2^31, -1].
		const lo = -(1 << 31)
		for _, id := range ids {
			got := anthologyTMDBID(id)
			if got < lo || got > -1 {
				t.Errorf("anthologyTMDBID(%d) = %d, want within [%d, -1]", id, got, lo)
			}
		}
	})

	t.Run("distinct ids produce distinct synthetic ids", func(t *testing.T) {
		seen := make(map[int]int, 1000)
		for id := 1; id <= 1000; id++ {
			got := anthologyTMDBID(id)
			if prev, dup := seen[got]; dup {
				t.Fatalf("collision: anthologyTMDBID(%d) == anthologyTMDBID(%d) == %d", prev, id, got)
			}
			seen[got] = id
		}
		for _, id := range ids {
			got := anthologyTMDBID(id)
			if prev, dup := seen[got]; dup && prev != id {
				t.Fatalf("collision: anthologyTMDBID(%d) == anthologyTMDBID(%d) == %d", prev, id, got)
			}
			seen[got] = id
		}
	})
}

// TestAnthologyEpisodeCatalogSearch covers the plan's §8.2 table (S1-S7) for
// searchTVDBEpisodeByTitle. Pure: the function takes an already-fetched
// []tvdb.Episode, so none of this needs an httptest server, and no TMDB cache
// reset is involved because nothing here touches internal/tmdb at all.
//
// The show name and the two filenames are the REAL production strings from
// .omc/artifacts/series-after-20260806.psv, verbatim -- these cases assert
// that the reused-unmodified episodeTitleMatches predicate works on the actual
// library population, not on a convenient synthetic shape.
func TestAnthologyEpisodeCatalogSearch(t *testing.T) {
	const show = "Laurel & Hardy"
	const duckSoupFile = "Laurel & Hardy - Duck Soup(B&W)-DVDRip.XviD-DIE-DVD12.mp4"
	const musicBoxFile = "Laurel & Hardy - The Music Box(Colour)-DVDRip.XviD-DIE-DVD14.mkv"

	ep := func(season, number int, name string) tvdb.Episode {
		return tvdb.Episode{SeriesID: 71663, SeasonNumber: season, Number: number, Name: name, Aired: "1927-03-13"}
	}

	t.Run("S1 unique slot matches the real filename", func(t *testing.T) {
		got := searchTVDBEpisodeByTitle([]tvdb.Episode{
			ep(1, 7, "Duck Soup"),
			ep(1, 8, "Slipping Wives"),
		}, show, duckSoupFile)
		if got.Found != 1 {
			t.Fatalf("Found = %d, want 1 (result %+v)", got.Found, got)
		}
		if got.Match == nil {
			t.Fatal("Match = nil, want the Duck Soup slot")
		}
		if got.Match.season != 1 || got.Match.episode != 7 || got.Match.name != "Duck Soup" {
			t.Errorf("Match = S%02dE%02d %q, want S01E07 %q", got.Match.season, got.Match.episode, got.Match.name, "Duck Soup")
		}
		if got.Second != nil {
			t.Errorf("Second = %+v, want nil on a unique match", got.Second)
		}
	})

	t.Run("S2 same title in two seasons is ambiguous", func(t *testing.T) {
		// CONSTRUCTED duplicate: this is a claim about the matcher's uniqueness
		// rule, NOT a claim that TheTVDB's real Laurel & Hardy catalog lists
		// "Duck Soup" twice.
		got := searchTVDBEpisodeByTitle([]tvdb.Episode{
			ep(1, 7, "Duck Soup"),
			ep(3, 2, "Duck Soup"),
		}, show, duckSoupFile)
		if got.Found != 2 {
			t.Fatalf("Found = %d, want 2", got.Found)
		}
		if got.Match != nil {
			t.Errorf("Match = %+v, want nil the INSTANT a second slot is found", got.Match)
		}
		if got.First == nil || got.Second == nil {
			t.Fatalf("First = %+v, Second = %+v, want both set for the ambiguity reason string", got.First, got.Second)
		}
		if got.First.season != 1 || got.First.episode != 7 || got.Second.season != 3 || got.Second.episode != 2 {
			t.Errorf("slots = S%02dE%02d and S%02dE%02d, want S01E07 and S03E02",
				got.First.season, got.First.episode, got.Second.season, got.Second.episode)
		}
	})

	t.Run("S3 no matching title", func(t *testing.T) {
		got := searchTVDBEpisodeByTitle([]tvdb.Episode{
			ep(1, 1, "Slipping Wives"),
			ep(1, 2, "Love 'em and Weep"),
		}, show, duckSoupFile)
		if got.Found != 0 || got.Match != nil {
			t.Fatalf("Found = %d, Match = %+v, want 0 and nil", got.Found, got.Match)
		}
	})

	t.Run("S4 episode titled with the show's own name never matches", func(t *testing.T) {
		// Pins the residual-subtraction false-positive vector: the recovered
		// file title repeats the show name, so without the show-token
		// subtraction this slot would match every file in the folder.
		got := searchTVDBEpisodeByTitle([]tvdb.Episode{ep(1, 1, show)}, show, duckSoupFile)
		if got.Found != 0 || got.Match != nil {
			t.Fatalf("Found = %d, Match = %+v, want 0 and nil", got.Found, got.Match)
		}
	})

	t.Run("S5 placeholder and empty episode names are rejected", func(t *testing.T) {
		got := searchTVDBEpisodeByTitle([]tvdb.Episode{
			ep(1, 12, "Episode 12"),
			ep(0, 1, ""),
		}, show, duckSoupFile)
		if got.Found != 0 || got.Match != nil {
			t.Fatalf("Found = %d, Match = %+v, want 0 and nil", got.Found, got.Match)
		}
	})

	t.Run("S6 empty catalog", func(t *testing.T) {
		got := searchTVDBEpisodeByTitle(nil, show, duckSoupFile)
		if got.Found != 0 || got.Match != nil || got.First != nil || got.Second != nil {
			t.Fatalf("got %+v, want the zero value on an empty catalog", got)
		}
	})

	t.Run("S7 neighbours in one catalog do not cross-match", func(t *testing.T) {
		// The whole-catalog uniqueness rule is only meaningful if two DIFFERENT
		// episode titles in the SAME catalog stay distinguishable. Both real
		// filenames are run against the same two-slot catalog and each must
		// land uniquely on its own slot -- if either predicate call leaked, one
		// of these would report Found == 2 instead.
		catalog := []tvdb.Episode{
			ep(1, 7, "Duck Soup"),
			ep(4, 3, "The Music Box"),
		}

		music := searchTVDBEpisodeByTitle(catalog, show, musicBoxFile)
		if music.Found != 1 || music.Match == nil {
			t.Fatalf("music box: Found = %d, Match = %+v, want 1 and non-nil", music.Found, music.Match)
		}
		if music.Match.season != 4 || music.Match.episode != 3 || music.Match.name != "The Music Box" {
			t.Errorf("music box matched S%02dE%02d %q, want S04E03 %q", music.Match.season, music.Match.episode, music.Match.name, "The Music Box")
		}

		duck := searchTVDBEpisodeByTitle(catalog, show, duckSoupFile)
		if duck.Found != 1 || duck.Match == nil {
			t.Fatalf("duck soup: Found = %d, Match = %+v, want 1 and non-nil", duck.Found, duck.Match)
		}
		if duck.Match.season != 1 || duck.Match.episode != 7 || duck.Match.name != "Duck Soup" {
			t.Errorf("duck soup matched S%02dE%02d %q, want S01E07 %q", duck.Match.season, duck.Match.episode, duck.Match.name, "Duck Soup")
		}
	})
}

// TestEpisodeTitleCollapse_ProductionCatalog is the CATALOG-level half of the
// discriminating-residual collapse fix. Two of that fix's acceptance criteria
// are statements about a whole catalog's match SET, which no single
// episodeTitleMatches call can express:
//
//   - (a) the real production pair must resolve to exactly ONE slot. Pre-fix
//     this reported Found == 2 with First = S00E43 "That's That" -- byte for
//     byte the live ambiguity-decline reason observed on the production
//     Laurel & Hardy folder, because "That's That" collapses to the single
//     token "that", which IS present in the "That's My Wife" file.
//   - (b) a bare "That's That" file must match NOTHING and, just as
//     importantly, must not silently slide onto some OTHER wrong episode.
//     Pre-fix this reported Found == 1 on S00E43.
//
// The catalog is widened past the two slots the defect needs so that (b)'s
// Found == 0 is a claim about a populated catalog rather than a two-element
// one. Mirrors TestAnthologyEpisodeCatalogSearch deliberately: pure, no
// httptest server, no TMDB cache reset. That test's `ep` helper is a closure
// inside its own function body and is NOT reachable from here (see
// tvdbEpisodesFrom's note), so this declares its own.
// Context: see .omc/specs/deep-interview-sakms-episode-title-discriminating-residual-collapse-fix.md
func TestEpisodeTitleCollapse_ProductionCatalog(t *testing.T) {
	const show = "Laurel & Hardy"
	const thatsMyWifeFile = "Laurel & Hardy - That's My Wife(B&W)-DVDRip.XviD-DIE-DVD09.mp4"
	const bareThatsThatFile = "That's That.mp4"

	ep := func(season, number int, name string) tvdb.Episode {
		return tvdb.Episode{SeriesID: 71663, SeasonNumber: season, Number: number, Name: name, Aired: "1929-02-23"}
	}
	catalog := []tvdb.Episode{
		ep(0, 43, "That's That"),
		ep(5, 3, "That's My Wife"),
		ep(1, 7, "Duck Soup"),
		ep(5, 4, "Below Zero"),
		ep(5, 1, "Liberty"),
	}

	t.Run("the real production pair resolves to exactly one slot", func(t *testing.T) {
		got := searchTVDBEpisodeByTitle(catalog, show, thatsMyWifeFile)
		if got.Found != 1 {
			t.Fatalf("Found = %d, want 1 (result %+v)", got.Found, got)
		}
		if got.Match == nil {
			t.Fatal("Match = nil, want the That's My Wife slot")
		}
		if got.Match.season != 5 || got.Match.episode != 3 || got.Match.name != "That's My Wife" {
			t.Errorf("Match = S%02dE%02d %q, want S05E03 %q", got.Match.season, got.Match.episode, got.Match.name, "That's My Wife")
		}
		if got.Second != nil {
			t.Errorf("Second = %+v, want nil -- a non-nil Second here IS the live production ambiguity", got.Second)
		}
	})

	t.Run("a bare That's That file matches nothing and mismatches nothing", func(t *testing.T) {
		// The "That's That" slot is rejected by the collapse check; the
		// "That's My Wife" slot is rejected by containment (residual
		// {that, my, wife} is not a subset of the file's {that}); the three
		// widening slots share no token with the file at all. So the outcome
		// is a clean zero, not a silent slide onto the wrong episode.
		got := searchTVDBEpisodeByTitle(catalog, show, bareThatsThatFile)
		if got.Found != 0 {
			t.Fatalf("Found = %d, want 0 (result %+v)", got.Found, got)
		}
		if got.Match != nil || got.First != nil || got.Second != nil {
			t.Errorf("got %+v, want the zero value -- any non-nil slot here is a silent mismatch", got)
		}
	})
}

// ---------------------------------------------------------------------------
// autopilot-impl.md §2.3 / §4.2(b2) -- PRODUCER-side coverage of the `handled`
// return value.
// ---------------------------------------------------------------------------

// handledFixtureRoot is a SYNTHETIC path prefix, never a real directory.
// tvdbAnthologyPass touches no filesystem at all -- it does pure path math
// (showFolderKey/showFolderName) over out[i].SourcePath plus HTTP against the
// TheTVDB fake -- so seeding real files here would add a t.TempDir() that
// nothing reads.
const handledFixtureRoot = "/lib"

// anthologyHandledCatalog is anthologyScanCatalog plus ONE title deliberately
// duplicated across two seasons ("Below Zero"), which is what drives a file to
// the ambiguity-decline branch. Kept local rather than folded into the shared
// helper: every existing case A-O expectation table is written against the
// four-entry catalog, and adding a fifth title there would change what those
// cases assert.
func anthologyHandledCatalog() []fakeTVDBEpisode {
	c := anthologyScanCatalog()
	return append(c,
		fakeTVDBEpisode{ID: 5, SeriesID: anthologyTVDBSeriesID, Name: "Below Zero", Number: 4, SeasonNumber: 5, Aired: "1930-04-26"},
		fakeTVDBEpisode{ID: 6, SeriesID: anthologyTVDBSeriesID, Name: "Below Zero", Number: 9, SeasonNumber: 6, Aired: "1930-04-26"},
	)
}

// TestTVDBAnthologyPass_HandledCoversEveryWrittenIndex is the ONE deliberate
// exception to rename_library_series_test.go:2200's "every case drives the
// REAL ScanLibrarySeries, not tvdbAnthologyPass directly" convention, and the
// exception is forced: `handled` is a RETURN VALUE that ScanLibrarySeries
// consumes internally, so no end-to-end test can observe it at all. The 14
// existing TestScanLibrarySeries_TVDBAnthology_* cases each exercise one of
// these branches through the real scan; none of them can see this bit.
//
// The property asserted is a BICONDITIONAL, in both directions:
//
//	handled[i] == true  <=>  out[i] differs from its pre-call snapshot
//
// The reverse direction is the one that catches the likely regression -- a
// future edit hoisting handled[i] = true to the top of the step-7 loop, which
// would mark every file in a resolved folder including the ones the pass
// deliberately leaves alone. That is why the fixture carries a SIXTH file
// ("Towed in a Hole") matching nothing in the catalog: with only the five
// files the branch enumeration needs, every index is written and the reverse
// direction is vacuously true.
//
// Fixture details that are not obvious:
//   - THREE clean files are required, not one. minCorroboratingFiles is 3 and
//     counts DISTINCT SLOTS from Found == 1 files only, so the ambiguous file
//     (Found == 2) and the tracked-slot file contribute nothing toward
//     qualification -- the tracked check runs well AFTER it.
//   - the tracked key uses the SYNTHETIC NEGATIVE id, anthologyTMDBID(...),
//     because the branch keys on pin.synthID. A real positive id never fires.
//   - folderIDs is EMPTY (a present key skips the folder outright at step 2)
//     and seriesByTVDBID is EMPTY (a hit there would drop the threshold to 1).
func TestTVDBAnthologyPass_HandledCoversEveryWrittenIndex(t *testing.T) {
	const (
		belowZeroFile = "Laurel & Hardy - Below Zero(B&W)-DVDRip.XviD-DIE-DVD07.mp4"
		towedFile     = "Laurel & Hardy - Towed in a Hole(B&W)-DVDRip.XviD-DIE-DVD19.mp4"
	)
	// wantWritten is the EXPECTED branch outcome per file, declared up front so
	// the assertion below cannot be quietly rewritten to match whatever the
	// code did.
	files := []struct {
		name        string
		wantWritten bool
		branch      string
	}{
		{anthologyDuckSoupFile, true, "3 (Pending emit)"},
		{anthologyBerthMarksFile, true, "3 (Pending emit)"},
		{anthologyBigBusinessFile, true, "3 (Pending emit)"},
		{belowZeroFile, true, "1 (ambiguity decline)"},
		{anthologyMusicBoxFile, true, "2 (tracked-slot decline)"},
		{towedFile, false, "none -- Found == 0, left untouched"},
	}

	out := make([]proposals.Proposal, 0, len(files))
	candidates := make([]int, 0, len(files))
	for i, f := range files {
		out = append(out, proposals.Proposal{
			Mode:           mode.Series,
			Workflow:       "rename",
			Status:         proposals.Unmatched,
			SourceName:     f.name,
			SourcePath:     filepath.Join(handledFixtureRoot, anthologyShowFolder, f.name),
			RootFolderPath: handledFixtureRoot,
			Reason:         fmt.Sprintf("could not determine season/episode from %q", f.name),
		})
		candidates = append(candidates, i)
	}
	snapshot := append([]proposals.Proposal(nil), out...)

	tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyHandledCatalog(),
	}})
	// fatalTMDBSeriesServer, not nil: this pass must never reach TMDB, and a
	// nil client would make that pass vacuously rather than assert it.
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	tracked := map[episodeKey]bool{
		{tmdbID: anthologyTMDBID(anthologyTVDBSeriesID), season: 8, episode: 3}: true,
	}

	resolved, handled := tvdbAnthologyPass(context.Background(), sess, tracked,
		map[int]library.Series{},
		// no tracked-episode corroboration in this fixture — a nil map indexes to
		// a nil slice and the helper returns false, i.e. pre-fix behaviour.
		nil,
		map[string]int{},
		[]string{handledFixtureRoot}, handledFixtureRoot, candidates, out)

	if handled == nil || resolved == nil {
		t.Fatalf("both return maps must be non-nil (handled[i] = true panics on a nil map): resolved=%v handled=%v", resolved, handled)
	}

	key := showFolderKey(out[0].SourcePath, []string{handledFixtureRoot})
	got, ok := resolved[key]
	if !ok {
		t.Fatalf("folder %q resolved to one candidate but published no resolution: %+v", key, resolved)
	}
	// candName is the CATALOG's own show name, which is what the pass feeds to
	// searchTVDBEpisodeByTitle -- never pin.title, which the established-pin
	// shortcut can overwrite with a tracked row's title.
	if got.candName != "Laurel & Hardy" {
		t.Errorf("resolved[%q].candName = %q, want %q", key, got.candName, "Laurel & Hardy")
	}
	if got.pin.tvdbID != anthologyTVDBSeriesID || got.pin.synthID != anthologyTMDBID(anthologyTVDBSeriesID) {
		t.Errorf("resolved[%q].pin = %+v, want tvdbID %d / synthID %d", key, got.pin, anthologyTVDBSeriesID, anthologyTMDBID(anthologyTVDBSeriesID))
	}
	if len(got.catalog) != len(anthologyHandledCatalog()) {
		t.Errorf("resolved[%q].catalog has %d episodes, want the whole %d-episode fetch carried out of the pass",
			key, len(got.catalog), len(anthologyHandledCatalog()))
	}

	for i, f := range files {
		// reflect.DeepEqual, not ==: proposals.Proposal carries slice fields
		// (ExtraEpisodeNumbers, Genres, Cast) and is not comparable.
		written := !reflect.DeepEqual(out[i], snapshot[i])
		if written != f.wantWritten {
			t.Errorf("%q (branch %s): out[%d] written = %v, want %v (reason now %q)",
				f.name, f.branch, i, written, f.wantWritten, out[i].Reason)
		}
		if handled[i] != written {
			t.Errorf("%q (branch %s): handled[%d] = %v but out[%d] written = %v -- handled must cover EXACTLY the written indices, in both directions",
				f.name, f.branch, i, handled[i], i, written)
		}
	}
}

// TestTVDBAnthologyPass_ResolvedCandNameIsNotPinTitle pins the ONE property
// that makes candName a separate field rather than something a consumer could
// read off pin.title: the established-pin shortcut OVERWRITES pin.title with
// the TRACKED ROW's title, while candName must keep the CATALOG's own show
// name.
//
// That distinction is invisible on the ordinary path -- with no established
// row the two strings are byte-identical, so
// TestTVDBAnthologyPass_HandledCoversEveryWrittenIndex above cannot catch a
// consumer (or a future edit) sourcing candName from the pin. This is the
// fixture where they genuinely diverge.
//
// It matters because candName is the show-title argument
// searchTVDBEpisodeByTitle takes, and episodeTitleMatches SUBTRACTS that
// title's tokens from every episode name before comparing. Feed it the tracked
// row's title instead and the residual computation is done against a string
// the catalog was never written in.
func TestTVDBAnthologyPass_ResolvedCandNameIsNotPinTitle(t *testing.T) {
	// Deliberately spelled "and" where TheTVDB spells the show "&", so the two
	// strings are distinguishable while still clearing the folder-name gate.
	const trackedTitle = "Laurel and Hardy"

	out := []proposals.Proposal{{
		Mode:           mode.Series,
		Workflow:       "rename",
		Status:         proposals.Unmatched,
		SourceName:     anthologyDuckSoupFile,
		SourcePath:     filepath.Join(handledFixtureRoot, anthologyShowFolder, anthologyDuckSoupFile),
		RootFolderPath: handledFixtureRoot,
		Reason:         "could not determine season/episode",
	}}

	tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	// TMDBID MUST be the synthetic id: the established-pin shortcut refuses a
	// row whose TMDBID is anything else (a real TMDB-tracked show landing here
	// would mint a second library_series row for the same folder).
	seriesByTVDBID := map[int]library.Series{
		anthologyTVDBSeriesID: {
			TMDBID: anthologyTMDBID(anthologyTVDBSeriesID), TVDBID: anthologyTVDBSeriesID,
			Title: trackedTitle, Year: 1921, RootFolderPath: handledFixtureRoot,
		},
	}

	// ONE file only: with an established row the threshold drops from
	// minCorroboratingFiles to 1, which is what makes this fixture a rescan
	// rather than a fresh pin.
	resolved, handled := tvdbAnthologyPass(context.Background(), sess, map[episodeKey]bool{},
		seriesByTVDBID,
		// no tracked-episode corroboration in this fixture — a nil map indexes to
		// a nil slice and the helper returns false, i.e. pre-fix behaviour.
		nil,
		map[string]int{},
		[]string{handledFixtureRoot}, handledFixtureRoot, []int{0}, out)

	key := showFolderKey(out[0].SourcePath, []string{handledFixtureRoot})
	got, ok := resolved[key]
	if !ok {
		t.Fatalf("established-pin rescan published no resolution for %q: %+v", key, resolved)
	}
	if got.pin.title != trackedTitle {
		t.Fatalf("pin.title = %q, want the TRACKED row's title %q -- the established-pin shortcut did not fire, so this test proves nothing",
			got.pin.title, trackedTitle)
	}
	if got.candName != "Laurel & Hardy" {
		t.Errorf("candName = %q, want the CATALOG's own name %q (it must NOT be sourced from pin.title, which is %q here)",
			got.candName, "Laurel & Hardy", got.pin.title)
	}
	if !handled[0] {
		t.Errorf("handled[0] = false, want true -- the row was emitted as %v with reason %q", out[0].Status, out[0].Reason)
	}
}

// TestTVDBAnthologyPass_AmbiguousFolderPublishesNoResolution is the REVERSE
// direction for `resolved`, and it is the test that pins WHERE the write
// happens rather than merely THAT it happens.
//
// TestTVDBAnthologyPass_HandledCoversEveryWrittenIndex above proves a resolved
// folder gets an entry. It cannot prove the entry is written at the
// winner-selection point, because with one qualifying candidate the forbidden
// per-candidate write point produces byte-identical contents. Two qualifying
// candidates is the only fixture where the two points differ:
//
//	winner-selection point (correct) -> no entry, the folder declined
//	per-candidate point   (forbidden) -> an entry for a CONTESTED show
//
// The forbidden variant is the specific bug autopilot-impl.md §2.3 fix 5
// exists to prevent: publishing a resolution for a folder this pass
// deliberately REFUSED to resolve lets a downstream AI-assisted pass match a
// file against a catalog the deterministic pass would not commit to,
// inverting the deterministic-first precedence order.
//
// The two show NAMES are lifted verbatim from
// TestScanLibrarySeries_TVDBAnthology_ShowLevelAmbiguityDeclines
// (rename_library_series_test.go) rather than invented: both must clear
// gate.allowsTitle against the "Laurel and Hardy" FOLDER seed, and a name that
// fails Check A would silently collapse this back into a one-candidate test
// that passes vacuously. Both are served the same catalog so both clear the
// 3-slot threshold; maxAnthologyCandidates is 3, so both are evaluated.
func TestTVDBAnthologyPass_AmbiguousFolderPublishesNoResolution(t *testing.T) {
	names := []string{anthologyDuckSoupFile, anthologyBerthMarksFile, anthologyBigBusinessFile, anthologyMusicBoxFile}
	out := make([]proposals.Proposal, 0, len(names))
	candidates := make([]int, 0, len(names))
	for i, name := range names {
		out = append(out, proposals.Proposal{
			Mode:           mode.Series,
			Workflow:       "rename",
			Status:         proposals.Unmatched,
			SourceName:     name,
			SourcePath:     filepath.Join(handledFixtureRoot, anthologyShowFolder, name),
			RootFolderPath: handledFixtureRoot,
			Reason:         fmt.Sprintf("could not determine season/episode from %q", name),
		})
		candidates = append(candidates, i)
	}
	snapshot := append([]proposals.Proposal(nil), out...)

	tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{
		{ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog()},
		{ID: 99999, Name: "Laurel and Hardy Collection", Year: "1930", Catalog: anthologyScanCatalog()},
	})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	resolved, handled := tvdbAnthologyPass(context.Background(), sess, map[episodeKey]bool{},
		map[int]library.Series{},
		// no tracked-episode corroboration in this fixture — a nil map indexes to
		// a nil slice and the helper returns false, i.e. pre-fix behaviour.
		nil,
		map[string]int{},
		[]string{handledFixtureRoot}, handledFixtureRoot, candidates, out)

	// BOTH catalogs fetched == both candidates genuinely evaluated and both
	// qualified. Without this the case cannot distinguish "declined because
	// TWO qualified" from "declined because ZERO did" — and only the former
	// puts the forbidden write point in reach at all.
	// 4 == two candidates x one paginated fetch (page 0 plus the terminating
	// empty page 1).
	if n := counts.Episodes.Load(); n != 4 {
		t.Fatalf("SeriesEpisodes requests = %d, want 4 (2 candidates x 1 paginated fetch) — both candidates must qualify or this test proves nothing", n)
	}
	if len(resolved) != 0 {
		t.Errorf("resolved = %+v, want EMPTY: two candidates qualified, so the folder declined and must publish NOTHING. A non-empty map here means resolved[key] is written inside the candidate loop instead of at the winner-selection point (autopilot-impl.md §2.3 fix 5)", resolved)
	}
	if len(handled) != 0 {
		t.Errorf("handled = %+v, want EMPTY: a declined folder writes to no row at all", handled)
	}
	for i, name := range names {
		if !reflect.DeepEqual(out[i], snapshot[i]) {
			t.Errorf("%q: out[%d] was rewritten (status %v, reason %q), want byte-identical to the pre-call snapshot", name, i, out[i].Status, out[i].Reason)
		}
	}
}

// ---------------------------------------------------------------------------
// Anthology established-pin corroboration (plan §6.1-§6.4). The four cases
// below cover the second corroboration channel establishedPinCorroborated
// adds: an already-established pin qualifying on ALREADY-TRACKED episodes when
// this scan's own shrinking candidate group can no longer corroborate it.
// ---------------------------------------------------------------------------

// tvdbEpisodesFrom converts the httptest fixture shape into the shape
// searchTVDBEpisodeByTitle takes, so a direct-call precondition assertion and
// the catalog the fake server actually serves can never drift apart. Deriving
// BOTH from one anthologyScanCatalog() call is the point — two hand-written
// parallel catalog literals is exactly the silent-drift hazard this avoids.
//
// Note that TestAnthologyEpisodeCatalogSearch's `ep` helper is NOT reachable
// here: it is a closure declared inside that test function, not a package-level
// helper, so calling it from a sibling test is a compile error.
func tvdbEpisodesFrom(eps []fakeTVDBEpisode) []tvdb.Episode {
	out := make([]tvdb.Episode, 0, len(eps))
	for _, e := range eps {
		out = append(out, tvdb.Episode{
			ID: e.ID, SeriesID: e.SeriesID, Name: e.Name, Number: e.Number,
			SeasonNumber: e.SeasonNumber, Aired: e.Aired, Runtime: e.Runtime,
		})
	}
	return out
}

// anthologyUnmatchableFile is a real production row from the reverting six —
// a foreign-language title absent from anthologyScanCatalog(), so it yields
// Found == 0 and contributes NO slot. It is the shrinking pool's residue in
// fixture form: the file class an established pin is left holding once every
// self-corroborating file has been applied away.
//
// Deliberately reuses series_ai_episode_match_test.go's constant rather than
// declaring a second near-identical literal. Despite the `ai` prefix (it was
// introduced for the AI-recovery pass) it is a plain package-level filename
// constant, and TestAIRecovery already pins its Found == 0 property. Every
// case below re-asserts that precondition locally anyway, so a future edit to
// that constant surfaces here as a precondition failure rather than as a
// silently vacuous test.
const anthologyUnmatchableFile = aiPolitiqueriasFile

// anthologyOneFileFixture builds the single-candidate-file shape §6.1, §6.2 and
// §6.4 all share: one unmatchable file in the un-applied on-disk show folder,
// carrying the verbatim parse-failure reason ScanLibrarySeries would have left
// on it.
func anthologyOneFileFixture() ([]proposals.Proposal, []proposals.Proposal) {
	out := []proposals.Proposal{{
		Mode:           mode.Series,
		Workflow:       "rename",
		Status:         proposals.Unmatched,
		SourceName:     anthologyUnmatchableFile,
		SourcePath:     filepath.Join(handledFixtureRoot, anthologyShowFolder, anthologyUnmatchableFile),
		RootFolderPath: handledFixtureRoot,
		Reason:         fmt.Sprintf("could not determine season/episode from %q", anthologyUnmatchableFile),
	}}
	return out, append([]proposals.Proposal(nil), out...)
}

// TestTVDBAnthologyPass_EstablishedPinCorroboratesFromTrackedEpisodes is the
// plan's §6.1, the primary acceptance test for the whole change.
//
// The fixture is the production shape the fix exists for: an established
// anthology pin whose scan group has shrunk to ONE file that matches nothing
// in the catalog, so len(slots) == 0 and the retired `threshold = 1` rule
// declined the folder outright. The already-tracked episodes — rows that only
// EXIST because earlier files were applied — are what qualify it now.
//
// Assertion 5 is the payload: the file itself stays byte-identical, which
// keeps it eligible for aiEpisodeRecoveryPass, while its folder now carries a
// `resolved` entry — the difference between that pass running in MODE 2
// (catalog-backed) and MODE 3.
func TestTVDBAnthologyPass_EstablishedPinCorroboratesFromTrackedEpisodes(t *testing.T) {
	// The TRACKED row's title, deliberately spelled "and" where TheTVDB spells
	// the show "&", so pin.title and candName are distinguishable strings.
	const trackedTitle = "Laurel and Hardy"

	// Precondition: without this the fixture could silently become a
	// len(slots) == 1 test and prove nothing about the tracked-episode channel.
	catalog := tvdbEpisodesFrom(anthologyScanCatalog())
	if r := searchTVDBEpisodeByTitle(catalog, "Laurel & Hardy", anthologyUnmatchableFile); r.Found != 0 {
		t.Fatalf("precondition: searchTVDBEpisodeByTitle(%q) Found = %d, want 0 — the candidate file must contribute NO slot",
			anthologyUnmatchableFile, r.Found)
	}
	// Per-show uniqueness of each corroboration title in the catalog THIS show
	// is served: establishedPinCorroborated demands Found == 1, so a title the
	// catalog happens to list twice abstains and this test would fail against a
	// correct implementation.
	for _, title := range []string{"Duck Soup", "Berth Marks"} {
		if r := searchTVDBEpisodeByTitle(catalog, "Laurel & Hardy", title); r.Found != 1 {
			t.Fatalf("precondition: tracked title %q is Found = %d in its own show's catalog, want exactly 1", title, r.Found)
		}
	}

	seriesByTVDBID := map[int]library.Series{
		anthologyTVDBSeriesID: {
			TMDBID: anthologyTMDBID(anthologyTVDBSeriesID), TVDBID: anthologyTVDBSeriesID,
			Title: trackedTitle, Year: 1921, RootFolderPath: handledFixtureRoot,
		},
	}

	t.Run("tracked episodes corroborate the established pin", func(t *testing.T) {
		out, snapshot := anthologyOneFileFixture()
		tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
			ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
		}})
		sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

		resolved, handled := tvdbAnthologyPass(context.Background(), sess, map[episodeKey]bool{},
			seriesByTVDBID,
			map[int][]library.Episode{anthologyTVDBSeriesID: {
				{SeasonNumber: 3, EpisodeNumber: 1, Title: "Duck Soup", FilePath: "/lib/Laurel & Hardy/Season 03/applied-duck-soup.mp4"},
				{SeasonNumber: 2, EpisodeNumber: 5, Title: "Berth Marks", FilePath: "/lib/Laurel & Hardy/Season 02/applied-berth-marks.mp4"},
			}},
			map[string]int{},
			[]string{handledFixtureRoot}, handledFixtureRoot, []int{0}, out)

		key := showFolderKey(out[0].SourcePath, []string{handledFixtureRoot})
		got, ok := resolved[key]
		if !ok {
			t.Fatalf("no resolution published for %q: %+v — the established pin found zero corroborating files in this scan and must have qualified on its TRACKED episodes", key, resolved)
		}
		if got.pin.title != trackedTitle {
			t.Errorf("pin.title = %q, want the TRACKED row's title %q", got.pin.title, trackedTitle)
		}
		if got.candName != "Laurel & Hardy" {
			t.Errorf("candName = %q, want the CATALOG's own name %q", got.candName, "Laurel & Hardy")
		}
		if got.pin.tvdbID != anthologyTVDBSeriesID || got.pin.synthID != anthologyTMDBID(anthologyTVDBSeriesID) {
			t.Errorf("pin = %+v, want tvdbID %d / synthID %d", got.pin, anthologyTVDBSeriesID, anthologyTMDBID(anthologyTVDBSeriesID))
		}
		// THE POINT OF THE FIX: the row itself is untouched (Found == 0, so step
		// 7 leaves it alone) and therefore still eligible for
		// aiEpisodeRecoveryPass — but its folder now has a resolved entry, which
		// is what makes that pass MODE 2 instead of MODE 3.
		if len(handled) != 0 {
			t.Errorf("handled = %+v, want EMPTY: the candidate file matched nothing, so no row is spoken for", handled)
		}
		if !reflect.DeepEqual(out[0], snapshot[0]) {
			t.Errorf("out[0] was rewritten (status %v, reason %q), want byte-identical to the pre-call snapshot", out[0].Status, out[0].Reason)
		}
	})

	t.Run("tracked episodes with no title corroborate nothing", func(t *testing.T) {
		// §0.4: library_episodes.title is populated fail-SOFT at Apply time, so
		// "" is a reachable, benign state — and an empty title is the ABSENCE of
		// a match input, never a match.
		out, snapshot := anthologyOneFileFixture()
		tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
			ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
		}})
		sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

		resolved, handled := tvdbAnthologyPass(context.Background(), sess, map[episodeKey]bool{},
			seriesByTVDBID,
			map[int][]library.Episode{anthologyTVDBSeriesID: {
				{SeasonNumber: 3, EpisodeNumber: 1, Title: "", FilePath: "/lib/Laurel & Hardy/Season 03/applied-duck-soup.mp4"},
				{SeasonNumber: 2, EpisodeNumber: 5, Title: "   ", FilePath: "/lib/Laurel & Hardy/Season 02/applied-berth-marks.mp4"},
			}},
			map[string]int{},
			[]string{handledFixtureRoot}, handledFixtureRoot, []int{0}, out)

		if len(resolved) != 0 {
			t.Errorf("resolved = %+v, want EMPTY: titleless tracked rows carry no match input", resolved)
		}
		if len(handled) != 0 {
			t.Errorf("handled = %+v, want EMPTY", handled)
		}
		if !reflect.DeepEqual(out[0], snapshot[0]) {
			t.Errorf("out[0] was rewritten, want byte-identical to the pre-call snapshot")
		}
	})
}

// TestTVDBAnthologyPass_EstablishedPinDeclinesWhenTrackedEpisodesAlsoFail is
// the plan's §6.2 — the fail-closed half of §6.1's fixture.
//
// The tracked titles are real Laurel & Hardy shorts deliberately absent from
// anthologyScanCatalog(), which is what TheTVDB reassigning a series id would
// look like from here. Neither channel corroborates, so the outcome must be
// byte-for-byte today's decline: no resolution, no rows written, no partial
// state.
func TestTVDBAnthologyPass_EstablishedPinDeclinesWhenTrackedEpisodesAlsoFail(t *testing.T) {
	catalog := tvdbEpisodesFrom(anthologyScanCatalog())
	if r := searchTVDBEpisodeByTitle(catalog, "Laurel & Hardy", anthologyUnmatchableFile); r.Found != 0 {
		t.Fatalf("precondition: the candidate file is Found = %d, want 0", r.Found)
	}
	for _, title := range []string{"A Chump at Oxford", "Night Owls"} {
		if r := searchTVDBEpisodeByTitle(catalog, "Laurel & Hardy", title); r.Found != 0 {
			t.Fatalf("precondition: tracked title %q is Found = %d in the served catalog, want 0 — it must NOT corroborate or this test asserts the wrong branch", title, r.Found)
		}
	}

	out, snapshot := anthologyOneFileFixture()
	tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	resolved, handled := tvdbAnthologyPass(context.Background(), sess, map[episodeKey]bool{},
		map[int]library.Series{
			anthologyTVDBSeriesID: {
				TMDBID: anthologyTMDBID(anthologyTVDBSeriesID), TVDBID: anthologyTVDBSeriesID,
				Title: "Laurel and Hardy", Year: 1921, RootFolderPath: handledFixtureRoot,
			},
		},
		map[int][]library.Episode{anthologyTVDBSeriesID: {
			{SeasonNumber: 4, EpisodeNumber: 2, Title: "A Chump at Oxford", FilePath: "/lib/Laurel & Hardy/Season 04/applied-chump.mp4"},
			{SeasonNumber: 2, EpisodeNumber: 20, Title: "Night Owls", FilePath: "/lib/Laurel & Hardy/Season 02/applied-night-owls.mp4"},
		}},
		map[string]int{},
		[]string{handledFixtureRoot}, handledFixtureRoot, []int{0}, out)

	// The catalog WAS fetched, so the decline came from the qualification
	// branch and not from gate.allowsTitle vetoing the candidate earlier.
	if n := counts.Episodes.Load(); n == 0 {
		t.Fatalf("SeriesEpisodes requests = 0 — the candidate was never evaluated, so this test proves nothing about the corroboration branch")
	}
	if len(resolved) != 0 {
		t.Errorf("resolved = %+v, want EMPTY: neither this scan's group nor the tracked episodes corroborate", resolved)
	}
	if len(handled) != 0 {
		t.Errorf("handled = %+v, want EMPTY", handled)
	}
	if !reflect.DeepEqual(out[0], snapshot[0]) {
		t.Errorf("out[0] was rewritten (status %v, reason %q), want byte-identical to the pre-call snapshot — a fail-closed decline has no side effects",
			out[0].Status, out[0].Reason)
	}
}

// TestTVDBAnthologyPass_NewPinIgnoresTrackedEpisodeCorroboration is the plan's
// §6.3: the corroboration channel must NOT leak into the NEW-pin branch.
//
// READ THE FIXTURE SIZE BEFORE EDITING IT. With seriesByTVDBID empty, step 3's
// pre-filter (`len(group) < minCorroboratingFiles && len(seriesByTVDBID) == 0`,
// series_tvdb_episode_match.go) skips the folder outright, so a 2-FILE fixture
// never reaches the candidate loop and reports green against a correct
// implementation, a broken one AND a leaky one. Every sub-case here therefore
// seeds at least THREE files, and asserts counts.Episodes.Load() > 0 to prove
// the catalog was genuinely fetched — the same anti-vacuity technique
// TestTVDBAnthologyPass_AmbiguousFolderPublishesNoResolution uses above.
//
// TestScanLibrarySeries_TVDBAnthology_BelowThresholdIsInert already pins "2
// slots declines" end to end and reaches the same place by the other available
// route (a truncated catalog rather than a non-matching filename); this test
// is not a duplicate of it — its subject is the tracked-episode map, which is
// only readable from inside the candidate loop.
func TestTVDBAnthologyPass_NewPinIgnoresTrackedEpisodeCorroboration(t *testing.T) {
	// A FULLY corroborating tracked map: both titles are unique in the served
	// catalog, so both would genuinely corroborate if the established-pin
	// channel leaked into this branch. It is the leak detector, not evidence —
	// which is why overlap with the candidate filenames is not forbidden here.
	trackedEpisodes := map[int][]library.Episode{anthologyTVDBSeriesID: {
		{SeasonNumber: 2, EpisodeNumber: 12, Title: "Big Business", FilePath: "/lib/Laurel & Hardy/Season 02/applied-big-business.mp4"},
		{SeasonNumber: 8, EpisodeNumber: 3, Title: "The Music Box", FilePath: "/lib/Laurel & Hardy/Season 08/applied-music-box.mkv"},
	}}
	catalog := tvdbEpisodesFrom(anthologyScanCatalog())
	for _, title := range []string{"Big Business", "The Music Box"} {
		if r := searchTVDBEpisodeByTitle(catalog, "Laurel & Hardy", title); r.Found != 1 {
			t.Fatalf("precondition: tracked title %q is Found = %d, want 1 — it must be capable of corroborating, or the leak detector cannot detect a leak", title, r.Found)
		}
	}

	// slots counts DISTINCT (season, episode) PAIRS, not matching files, so the
	// bracketing sub-case below needs three titles landing on three distinct
	// pairs. Verified against anthologyScanCatalog(): Duck Soup S03E01,
	// Berth Marks S02E05, Big Business S02E12 — note the last two share a
	// SEASON and are still distinct pairs.
	run := func(t *testing.T, names []string) (map[string]anthologyResolution, map[int]bool, []proposals.Proposal, []proposals.Proposal, *fakeTVDBAnthologyCounts) {
		t.Helper()
		out := make([]proposals.Proposal, 0, len(names))
		candidates := make([]int, 0, len(names))
		for i, name := range names {
			out = append(out, proposals.Proposal{
				Mode:           mode.Series,
				Workflow:       "rename",
				Status:         proposals.Unmatched,
				SourceName:     name,
				SourcePath:     filepath.Join(handledFixtureRoot, anthologyShowFolder, name),
				RootFolderPath: handledFixtureRoot,
				Reason:         fmt.Sprintf("could not determine season/episode from %q", name),
			})
			candidates = append(candidates, i)
		}
		snapshot := append([]proposals.Proposal(nil), out...)

		tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
			ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
		}})
		sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

		resolved, handled := tvdbAnthologyPass(context.Background(), sess, map[episodeKey]bool{},
			// EMPTY seriesByTVDBID — isEstablished is false, which is the whole
			// point of this test.
			map[int]library.Series{},
			trackedEpisodes,
			map[string]int{},
			[]string{handledFixtureRoot}, handledFixtureRoot, candidates, out)
		return resolved, handled, out, snapshot, counts
	}

	t.Run("2 slots still declines despite a fully corroborating tracked map", func(t *testing.T) {
		names := []string{anthologyDuckSoupFile, anthologyBerthMarksFile, anthologyUnmatchableFile}
		resolved, handled, out, snapshot, counts := run(t, names)

		// THE ANTI-VACUITY ASSERTION, and the reason the third file exists. It
		// proves the catalog was fetched — i.e. the candidate loop genuinely ran
		// and the decline came from step 4's threshold, not from step 3's cheap
		// pre-filter skipping the folder before any of this was evaluated.
		// One candidate costs TWO requests (page 0 plus the terminating empty
		// page 1), never one.
		if n := counts.Episodes.Load(); n == 0 {
			t.Fatalf("SeriesEpisodes requests = 0 — step 3's pre-filter skipped the folder, so nothing here was evaluated and this test asserts nothing")
		}
		if len(resolved) != 0 {
			t.Errorf("resolved = %+v, want EMPTY: 2 distinct slots is below minCorroboratingFiles, and the tracked-episode channel must NOT rescue a NEW pin", resolved)
		}
		if len(handled) != 0 {
			t.Errorf("handled = %+v, want EMPTY", handled)
		}
		for i, name := range names {
			if !reflect.DeepEqual(out[i], snapshot[i]) {
				t.Errorf("%q: out[%d] was rewritten (status %v, reason %q), want byte-identical", name, i, out[i].Status, out[i].Reason)
			}
		}
	})

	t.Run("3 slots qualifies the new pin the ordinary way", func(t *testing.T) {
		// Bracketing sub-case, and it needs its OWN assertion set: this folder
		// RESOLVES, so copying the decline sub-case's "rows byte-identical /
		// handled empty" assertions here would fail a CORRECT implementation.
		// trackedEpisodes stays populated deliberately — the point is that three
		// real slots, not the tracked map, are what qualified it.
		names := []string{anthologyDuckSoupFile, anthologyBerthMarksFile, anthologyBigBusinessFile, anthologyUnmatchableFile}
		resolved, handled, out, _, counts := run(t, names)

		if n := counts.Episodes.Load(); n == 0 {
			t.Fatalf("SeriesEpisodes requests = 0 — the candidate loop never ran")
		}
		if len(resolved) != 1 {
			t.Fatalf("resolved = %+v, want exactly 1 entry: three distinct slots clears minCorroboratingFiles unchanged", resolved)
		}
		key := showFolderKey(out[0].SourcePath, []string{handledFixtureRoot})
		got, ok := resolved[key]
		if !ok {
			t.Fatalf("no resolution for %q: %+v", key, resolved)
		}
		if got.pin.tvdbID != anthologyTVDBSeriesID {
			t.Errorf("pin.tvdbID = %d, want %d", got.pin.tvdbID, anthologyTVDBSeriesID)
		}
		if len(handled) == 0 {
			t.Fatalf("handled is EMPTY, want the three matched rows written")
		}
		for i, name := range names[:3] {
			if out[i].Status != proposals.Pending {
				t.Errorf("%q: status = %v (%s), want Pending", name, out[i].Status, out[i].Reason)
			}
			if !handled[i] {
				t.Errorf("%q: handled[%d] = false, want true", name, i)
			}
		}
		if handled[3] {
			t.Errorf("%q: handled[3] = true, want false — it matches nothing and must be left for the AI recovery pass", names[3])
		}
	})
}

// TestTVDBAnthologyPass_TwoEstablishedCandidatesStillDecline is the plan's
// §6.4: Step 5's show-level uniqueness rule is UNMODIFIED by this change.
//
// TestTVDBAnthologyPass_AmbiguousFolderPublishesNoResolution above covers the
// OLD shape (two candidates each clearing 3 slots). This covers the shape only
// this change can produce — two candidates each qualifying on ZERO slots and
// its own tracked episodes.
//
// MANDATORY per-show fixture requirement, without which all four assertions of
// the primary case hold for the WRONG reason: counts.Episodes.Load() == 4
// proves both catalogs were FETCHED, not that both candidates QUALIFIED. If
// either show's corroboration title were absent from — or duplicated within —
// ITS OWN served catalog, that show would abstain, zero candidates would
// qualify, and the case would still report green while measuring "declined
// because NEITHER corroborated". Each title is therefore verified unique in
// that same show's own catalog below, and the two shows use DIFFERENT titles so
// the per-show verification is visible in the fixture rather than implicit.
//
// The bracketing sub-case is what actually discriminates "each corroborates
// alone, so together they decline" from "neither ever corroborates".
func TestTVDBAnthologyPass_TwoEstablishedCandidatesStillDecline(t *testing.T) {
	// Both names are lifted verbatim from
	// TestTVDBAnthologyPass_AmbiguousFolderPublishesNoResolution: both are
	// already proven to clear Check A against the "Laurel and Hardy" folder
	// seed, and a name that failed it would silently collapse this into a
	// one-candidate test.
	const (
		secondShowID   = 99999
		secondShowName = "Laurel and Hardy Collection"
	)
	// anthologyScanCatalog() is served to BOTH shows — its four titles are all
	// distinct. Do NOT serve anthologyHandledCatalog() here: it duplicates
	// "Below Zero" deliberately, and any tracked title colliding with a
	// duplicated catalog title returns Found == 2 and abstains.
	catalog := tvdbEpisodesFrom(anthologyScanCatalog())
	if r := searchTVDBEpisodeByTitle(catalog, "Laurel & Hardy", "Duck Soup"); r.Found != 1 {
		t.Fatalf("precondition: show %d's corroboration title %q is Found = %d in ITS OWN catalog, want 1", anthologyTVDBSeriesID, "Duck Soup", r.Found)
	}
	if r := searchTVDBEpisodeByTitle(catalog, secondShowName, "Berth Marks"); r.Found != 1 {
		t.Fatalf("precondition: show %d's corroboration title %q is Found = %d in ITS OWN catalog, want 1", secondShowID, "Berth Marks", r.Found)
	}
	if r := searchTVDBEpisodeByTitle(catalog, "Laurel & Hardy", anthologyUnmatchableFile); r.Found != 0 {
		t.Fatalf("precondition: the candidate file is Found = %d, want 0 — slots must be empty for BOTH shows", r.Found)
	}

	// ONE file suffices, and §6.3's 3-file minimum does NOT apply: step 3's
	// condition is a conjunction and seriesByTVDBID is non-empty here, so the
	// folder survives regardless of group size.
	seriesByTVDBID := map[int]library.Series{
		anthologyTVDBSeriesID: {
			TMDBID: anthologyTMDBID(anthologyTVDBSeriesID), TVDBID: anthologyTVDBSeriesID,
			Title: "Laurel and Hardy", Year: 1921, RootFolderPath: handledFixtureRoot,
		},
		// Its OWN synthetic id, never anthologyTMDBID(anthologyTVDBSeriesID):
		// the deviation guard in step 4 flips isEstablished false on a mismatch,
		// which would drop this show to the new-pin branch with zero slots and
		// silently turn the primary case into a one-candidate test.
		secondShowID: {
			TMDBID: anthologyTMDBID(secondShowID), TVDBID: secondShowID,
			Title: secondShowName, Year: 1930, RootFolderPath: handledFixtureRoot,
		},
	}

	run := func(t *testing.T, tracked map[int][]library.Episode) (map[string]anthologyResolution, map[int]bool, []proposals.Proposal, []proposals.Proposal, *fakeTVDBAnthologyCounts) {
		t.Helper()
		out, snapshot := anthologyOneFileFixture()
		tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{
			{ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog()},
			{ID: secondShowID, Name: secondShowName, Year: "1930", Catalog: anthologyScanCatalog()},
		})
		sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

		resolved, handled := tvdbAnthologyPass(context.Background(), sess, map[episodeKey]bool{},
			seriesByTVDBID, tracked, map[string]int{},
			[]string{handledFixtureRoot}, handledFixtureRoot, []int{0}, out)
		return resolved, handled, out, snapshot, counts
	}

	t.Run("both established candidates corroborate, so the folder declines", func(t *testing.T) {
		resolved, handled, out, snapshot, counts := run(t, map[int][]library.Episode{
			anthologyTVDBSeriesID: {{SeasonNumber: 3, EpisodeNumber: 1, Title: "Duck Soup", FilePath: "/lib/Laurel & Hardy/Season 03/applied-duck-soup.mp4"}},
			secondShowID:          {{SeasonNumber: 2, EpisodeNumber: 5, Title: "Berth Marks", FilePath: "/lib/Laurel and Hardy Collection/Season 02/applied-berth-marks.mp4"}},
		})

		// Necessary but NOT sufficient on its own — see this test's doc comment
		// and the bracketing sub-case below. 4 == two candidates x one paginated
		// fetch (page 0 plus the terminating empty page 1).
		if n := counts.Episodes.Load(); n != 4 {
			t.Fatalf("SeriesEpisodes requests = %d, want 4 (2 candidates x 1 paginated fetch)", n)
		}
		if len(resolved) != 0 {
			t.Errorf("resolved = %+v, want EMPTY: two qualifying candidates DECLINE the whole folder — the corroboration channel narrows how much evidence ONE candidate needs, never how many candidates may win", resolved)
		}
		if len(handled) != 0 {
			t.Errorf("handled = %+v, want EMPTY", handled)
		}
		if !reflect.DeepEqual(out[0], snapshot[0]) {
			t.Errorf("out[0] was rewritten (status %v, reason %q), want byte-identical", out[0].Status, out[0].Reason)
		}
	})

	t.Run("only one candidate corroborates, so the folder resolves to it", func(t *testing.T) {
		// SAME single folder, SAME two candidates — one folder with two
		// candidates, not two folders. Show 99999 gets no tracked entry at all,
		// so it is genuinely evaluated and genuinely declines.
		resolved, _, out, _, counts := run(t, map[int][]library.Episode{
			anthologyTVDBSeriesID: {{SeasonNumber: 3, EpisodeNumber: 1, Title: "Duck Soup", FilePath: "/lib/Laurel & Hardy/Season 03/applied-duck-soup.mp4"}},
		})

		// Still required: both catalogs must be fetched, or the case cannot show
		// that 99999 reached step 4 at all.
		if n := counts.Episodes.Load(); n != 4 {
			t.Fatalf("SeriesEpisodes requests = %d, want 4 — show %d must be genuinely evaluated, not skipped", n, secondShowID)
		}
		if len(resolved) != 1 {
			t.Fatalf("resolved = %+v, want exactly 1 entry: with len(slots) == 0 for both, the folder can only resolve if show %d qualified through the TRACKED-EPISODE channel", resolved, anthologyTVDBSeriesID)
		}
		key := showFolderKey(out[0].SourcePath, []string{handledFixtureRoot})
		got, ok := resolved[key]
		if !ok {
			t.Fatalf("no resolution for %q: %+v", key, resolved)
		}
		if got.pin.tvdbID != anthologyTVDBSeriesID {
			t.Errorf("pin.tvdbID = %d, want %d — the CORROBORATING show must win, not the other one", got.pin.tvdbID, anthologyTVDBSeriesID)
		}
		if got.pin.synthID != anthologyTMDBID(anthologyTVDBSeriesID) {
			t.Errorf("pin.synthID = %d, want %d", got.pin.synthID, anthologyTMDBID(anthologyTVDBSeriesID))
		}
	})
}
