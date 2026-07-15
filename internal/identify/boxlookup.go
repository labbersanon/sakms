package identify

import (
	"context"
	"regexp"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/stashbox"
	"github.com/curtiswtaylorjr/sakms/internal/tpdbrest"
)

// fansiteHintRe: FansDB's text search is only worth consulting for fansite
// content — its catalog is millions of short, generic OnlyFans/ManyVids clip
// titles that spuriously match studio content at the 0.4 similarity threshold
// (e.g. "Wow Girls - Best Of Taylor Sands Scene 3" incorrectly matching
// "Best of Anal 3 / lunaokko (OnlyFans)"). Fingerprint lookups are exact and
// stay ungated; only the fuzzy text search is gated on these hints.
var fansiteHintRe = regexp.MustCompile(`(?i)onlyfans|manyvids|fansly|loyalfans|clips4sale|c4s|fansdb`)

func IsFansiteHinted(texts ...string) bool {
	for _, t := range texts {
		if t != "" && fansiteHintRe.MatchString(t) {
			return true
		}
	}
	return false
}

// BoxSearcher wraps StashDB/FansDB (stash-box protocol) and TPDB (REST) text
// search behind a shared, copy-on-return cache (see cache.go).
type BoxSearcher struct {
	stashBoxes map[string]*stashbox.Client // keyed by "stashdb", "fansdb" — a missing/nil entry means "not configured"
	tpdb       *tpdbrest.Client
	cache      *resultCache
}

func NewBoxSearcher(stashBoxes map[string]*stashbox.Client, tpdb *tpdbrest.Client) *BoxSearcher {
	return &BoxSearcher{stashBoxes: stashBoxes, tpdb: tpdb, cache: newResultCache()}
}

// SearchStashBox searches one stash-box (StashDB/FansDB) by title text.
// Returns the first candidate (of up to the first 5) whose title similarity
// is >= 0.4 AND whose studio doesn't contradict studio (if given): a
// zero-token-overlap studio mismatch is rejected, since a title-similar
// result from a completely different producer is a false match — but this bar
// is deliberately loose (zero-overlap only) so e.g. "TeamSkeet" still matches
// "TeamSkeet X Evil Angel".
func (b *BoxSearcher) SearchStashBox(ctx context.Context, box, title, studio string) (*MatchResult, error) {
	client := b.stashBoxes[box]
	if client == nil {
		return nil, nil
	}
	key := "stashbox\x00" + box + "\x00" + title + "\x00" + studio
	return b.cache.getOrCompute(key, func() (*MatchResult, error) {
		term := title
		if studio != "" {
			term = title + " " + studio
		}
		candidates, err := client.SearchScene(ctx, term)
		if err != nil {
			return nil, err
		}
		limit := len(candidates)
		if limit > 5 {
			limit = 5
		}
		for _, m := range candidates[:limit] {
			if TitleSimilarity(title, m.Title) < 0.4 {
				continue
			}
			if studio != "" && m.StudioName != "" && TitleSimilarity(studio, m.StudioName) == 0.0 {
				continue
			}
			return &MatchResult{
				Title: m.Title, Studio: m.StudioName, Date: m.ReleaseDate,
				Type: "scene", Source: box + "_text", SceneID: m.ID, Box: box,
				Image: m.ImageURL, Tags: strings.Join(m.Tags, ","), RuntimeSeconds: m.Duration,
			}, nil
		}
		return nil, nil
	})
}

// SearchTPDB searches ThePornDB by title text (REST). studio (if given)
// narrows server-side via the "site" param; there is no client-side studio
// gate here, unlike SearchStashBox.
func (b *BoxSearcher) SearchTPDB(ctx context.Context, title, studio string) (*MatchResult, error) {
	if b.tpdb == nil {
		return nil, nil
	}
	key := "tpdb\x00" + title + "\x00" + studio
	return b.cache.getOrCompute(key, func() (*MatchResult, error) {
		candidates, err := b.tpdb.SearchByTitle(ctx, title, studio)
		if err != nil {
			return nil, err
		}
		for _, m := range candidates {
			if TitleSimilarity(title, m.Title) >= 0.4 {
				tagNames := make([]string, len(m.Tags))
				for i, t := range m.Tags {
					tagNames[i] = t.Name
				}
				return &MatchResult{
					Title: m.Title, Studio: m.Site, Date: m.Date,
					Type: "scene", Source: "tpdb_text", SceneID: m.ID, Box: "tpdb",
					Image: m.Image, Tags: strings.Join(tagNames, ","), RuntimeSeconds: m.Duration,
				}, nil
			}
		}
		return nil, nil
	})
}

// SearchTPDBMovies searches ThePornDB's movie catalog by title text — the
// movie analogue of SearchTPDB, used by callers that need a movie identity
// specifically (e.g. internal/adultnewest's Movie row), not a scene. Movies
// share TPDB's Scene resource shape (see tpdbrest.Client.SearchMovies' doc
// comment), so this mirrors SearchTPDB's exact matching logic (>= 0.4 title
// similarity, first match wins) against c.tpdb.SearchMovies instead of
// SearchByTitle, and stamps Type: "movie" on the result.
func (b *BoxSearcher) SearchTPDBMovies(ctx context.Context, title string) (*MatchResult, error) {
	if b.tpdb == nil {
		return nil, nil
	}
	key := "tpdb_movie\x00" + title
	return b.cache.getOrCompute(key, func() (*MatchResult, error) {
		candidates, err := b.tpdb.SearchMovies(ctx, title)
		if err != nil {
			return nil, err
		}
		for _, m := range candidates {
			if TitleSimilarity(title, m.Title) >= 0.4 {
				tagNames := make([]string, len(m.Tags))
				for i, t := range m.Tags {
					tagNames[i] = t.Name
				}
				return &MatchResult{
					Title: m.Title, Studio: m.Site, Date: m.Date,
					Type: "movie", Source: "tpdb_text", SceneID: m.ID, Box: "tpdb",
					Image: m.Image, Tags: strings.Join(tagNames, ","), RuntimeSeconds: m.Duration,
				}, nil
			}
		}
		return nil, nil
	})
}

// SceneByID looks up a scene directly by its stash-box UUID (StashDB/FansDB).
func (b *BoxSearcher) SceneByID(ctx context.Context, box, sceneID string) (*MatchResult, error) {
	client := b.stashBoxes[box]
	if client == nil {
		return nil, nil
	}
	sc, err := client.FindScene(ctx, sceneID)
	if err != nil {
		return nil, err
	}
	if sc == nil {
		return nil, nil
	}
	return &MatchResult{
		Title: sc.Title, Studio: sc.StudioName, Date: sc.ReleaseDate,
		Type: "scene", Source: box + "_id", SceneID: sc.ID, Box: box,
		Image: sc.ImageURL, Tags: strings.Join(sc.Tags, ","), RuntimeSeconds: sc.Duration,
	}, nil
}
