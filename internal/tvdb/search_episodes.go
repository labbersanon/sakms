package tvdb

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

const maxEpisodeSearchSeries = 10

// EpisodeHit is one episode matched by title from a supported per-series
// episode listing. TheTVDB v4 does not expose a global episode-title search.
type EpisodeHit struct {
	EpisodeID     int
	Name          string
	SeasonNumber  int
	EpisodeNumber int
	SeriesID      int
}

// SearchEpisodes searches TheTVDB for episodes by title.
func (c *Client) SearchEpisodes(ctx context.Context, query string) ([]EpisodeHit, error) {
	return c.SearchEpisodesWithSeeds(ctx, query, nil)
}

// SearchEpisodesWithSeeds is SearchEpisodes plus extra series catalogs to
// scan. TVDB has no global episode-title search; SearchSeries(query) only
// finds shows named like the query, so "Duck Soup" never seeds Laurel &
// Hardy. extra is those parent ids (tracked anthologies) the caller already
// knows.
//
// Claude 2026-10-01: seed catalogs beyond SearchSeries(query).
// Reason: Rename's TVDB path always searched kind=episode; an episode title
//
//	does not match a series name, so the seed list was empty and every
//	search returned [].
//
// Troubleshooting: TVDB Rename Search shows "No results" for every query.
// Review if: TVDB adds a documented global episode-title search endpoint.
func (c *Client) SearchEpisodesWithSeeds(ctx context.Context, query string, extra []Result) ([]EpisodeHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []EpisodeHit{}, nil
	}

	// Claude 2026-08-12: replace unsupported /search?type=episode.
	// Reason: TVDB v4 search can filter only movie/series/person/company; the
	// real episode shape is available from /series/{id}/episodes/{season-type}.
	// Troubleshooting: TVDB Rename Search returning nothing for every episode
	// title despite tests passing against a hand-authored fake search payload.
	// Review if: TVDB adds a documented global episode-title search endpoint.
	series, err := c.SearchSeries(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("tvdb: episode search series seed %q: %w", query, err)
	}
	if len(series) > maxEpisodeSearchSeries {
		series = series[:maxEpisodeSearchSeries]
	}

	out := []EpisodeHit{}
	seenEp := make(map[int]bool)
	seenSeries := make(map[int]bool)
	for _, s := range series {
		hits, err := c.matchEpisodesInSeries(ctx, query, s.TVDBID, seenEp)
		if err != nil {
			return nil, fmt.Errorf("tvdb: episode search series %d: %w", s.TVDBID, err)
		}
		out = append(out, hits...)
		seenSeries[s.TVDBID] = true
	}
	for _, s := range extra {
		if s.TVDBID <= 0 || seenSeries[s.TVDBID] {
			continue
		}
		seenSeries[s.TVDBID] = true
		hits, err := c.matchEpisodesInSeries(ctx, query, s.TVDBID, seenEp)
		if err != nil {
			// Optional parent: one dead catalog must not blank the search.
			continue
		}
		out = append(out, hits...)
	}
	return out, nil
}

// SearchEpisodesIn scans only the given series catalogs for an episode title.
// It does not call SearchSeries. Use this when the parent is already known
// (TVDB id or an Advanced "Series" field).
func (c *Client) SearchEpisodesIn(ctx context.Context, query string, series []Result) ([]EpisodeHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []EpisodeHit{}, nil
	}
	out := []EpisodeHit{}
	seen := make(map[int]bool)
	for _, s := range series {
		if s.TVDBID <= 0 {
			continue
		}
		hits, err := c.matchEpisodesInSeries(ctx, query, s.TVDBID, seen)
		if err != nil {
			return nil, fmt.Errorf("tvdb: episode search series %d: %w", s.TVDBID, err)
		}
		out = append(out, hits...)
	}
	return out, nil
}

func (c *Client) matchEpisodesInSeries(ctx context.Context, query string, seriesID int, seen map[int]bool) ([]EpisodeHit, error) {
	episodes, err := c.SeriesEpisodes(ctx, seriesID, SeasonTypeOfficial)
	if err != nil {
		return nil, err
	}
	out := []EpisodeHit{}
	for _, ep := range episodes {
		if seen[ep.ID] || !episodeTitleSearchMatch(query, ep.Name) {
			continue
		}
		sid := ep.SeriesID
		if sid == 0 {
			sid = seriesID
		}
		out = append(out, EpisodeHit{
			EpisodeID:     ep.ID,
			Name:          ep.Name,
			SeasonNumber:  ep.SeasonNumber,
			EpisodeNumber: ep.Number,
			SeriesID:      sid,
		})
		seen[ep.ID] = true
	}
	return out, nil
}

func episodeTitleSearchMatch(query, title string) bool {
	q := normalizeEpisodeSearchText(query)
	t := normalizeEpisodeSearchText(title)
	if q == "" || t == "" {
		return false
	}
	return t == q || strings.Contains(t, q) || strings.Contains(q, t)
}

func normalizeEpisodeSearchText(s string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}
