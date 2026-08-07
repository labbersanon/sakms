package rename

import (
	"testing"

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
