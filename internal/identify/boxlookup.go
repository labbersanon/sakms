package identify

import (
	"context"
	"regexp"
	"strings"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
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

// resolveTPDBDuration returns duration if it's already non-zero, otherwise
// falls back to a confirming GET /scenes/{id} re-fetch. Found live
// (2026-07-15): TPDB's search/list endpoint (GET /scenes?q=) sometimes
// returns duration:0 for a scene that genuinely has a real duration on
// file — confirmed by comparing SearchByTitle's response against
// GetSceneByID's for the same known-affected scene, both hitting real
// production TPDB credentials; GetSceneByID reliably had it. Root cause on
// TPDB's side isn't confirmed (a live diagnostic showed both endpoints
// correct hours later, consistent with transient flakiness rather than a
// permanent shape difference) — this fallback is correct either way, since
// it only pays the extra round-trip when the cheap path already came back
// suspicious (0). Silently keeps 0 if the re-fetch also comes back empty or
// errors — best-effort, matches this file's other TPDB calls.
func (b *BoxSearcher) resolveTPDBDuration(ctx context.Context, id string, duration int) int {
	if duration > 0 {
		return duration
	}
	full, err := b.tpdb.GetSceneByID(ctx, id)
	if err != nil || full == nil {
		return duration
	}
	return full.Duration
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
					Image: m.Image, Tags: strings.Join(tagNames, ","),
					Performers:     strings.Join(m.Performers, ","),
					RuntimeSeconds: b.resolveTPDBDuration(ctx, m.ID, m.Duration),
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
					Image: m.Image, Tags: strings.Join(tagNames, ","),
					Performers:     strings.Join(m.Performers, ","),
					RuntimeSeconds: b.resolveTPDBDuration(ctx, m.ID, m.Duration),
				}, nil
			}
		}
		return nil, nil
	})
}

// maxSceneCandidates bounds ListSceneCandidates' unioned result set — see its
// doc comment.
const maxSceneCandidates = 50

// SceneCandidate is one pickable scene from a multi-box text search — the
// LIST counterpart to SearchStashBox/SearchTPDB's single best *MatchResult.
// It exists because a "pick the right scene" UI cannot be built on a
// collapsed best-guess.
type SceneCandidate struct {
	Box             string
	SceneID         string
	Title           string
	Studio          string
	Date            string
	ImageURL        string
	DurationSeconds int
}

// ListSceneCandidates fans a title query across every configured stash box in
// order plus TPDB, returning the raw union with NO similarity filtering —
// the operator is the filter here, unlike the automatic identification path
// (SearchStashBox/SearchTPDB above). Per-box failures are returned as strings
// rather than aborting the search: one unreachable box must not blank the
// whole result. Deliberately NOT cached: the automatic path's copy-on-return
// cache (cache.go) is keyed on a collapsed MatchResult and would serve stale/
// incompatible entries here.
func (b *BoxSearcher) ListSceneCandidates(ctx context.Context, title string, order []DatabaseRef) (items []SceneCandidate, softErrs []string) {
	refs := order
	if len(refs) == 0 {
		refs = legacyCascade
	}

	for _, ref := range refs {
		client := b.stashBoxes[ref.Name]
		if client == nil {
			continue
		}
		scenes, err := client.SearchScene(ctx, title)
		if err != nil {
			softErrs = append(softErrs, ref.Name+": "+err.Error())
			continue
		}
		for _, sc := range scenes {
			if len(items) >= maxSceneCandidates {
				return items, softErrs
			}
			items = append(items, SceneCandidate{
				Box:             ref.Name,
				SceneID:         sc.ID,
				Title:           sc.Title,
				Studio:          sc.StudioName,
				Date:            sc.ReleaseDate,
				ImageURL:        sc.ImageURL,
				DurationSeconds: sc.Duration,
			})
		}
	}

	if b.tpdb != nil {
		scenes, err := b.tpdb.SearchByTitle(ctx, title, "")
		if err != nil {
			softErrs = append(softErrs, "tpdb: "+err.Error())
		} else {
			for _, sc := range scenes {
				if len(items) >= maxSceneCandidates {
					break
				}
				items = append(items, SceneCandidate{
					Box:             "tpdb",
					SceneID:         sc.ID,
					Title:           sc.Title,
					Studio:          sc.Site,
					Date:            sc.Date,
					ImageURL:        sc.Image,
					DurationSeconds: sc.Duration,
				})
			}
		}
	}

	return items, softErrs
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
