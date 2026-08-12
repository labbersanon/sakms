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
	seen := make(map[int]bool)
	for _, s := range series {
		episodes, err := c.SeriesEpisodes(ctx, s.TVDBID, SeasonTypeOfficial)
		if err != nil {
			return nil, fmt.Errorf("tvdb: episode search series %d: %w", s.TVDBID, err)
		}
		for _, ep := range episodes {
			if seen[ep.ID] || !episodeTitleSearchMatch(query, ep.Name) {
				continue
			}
			seriesID := ep.SeriesID
			if seriesID == 0 {
				seriesID = s.TVDBID
			}
			out = append(out, EpisodeHit{
				EpisodeID:     ep.ID,
				Name:          ep.Name,
				SeasonNumber:  ep.SeasonNumber,
				EpisodeNumber: ep.Number,
				SeriesID:      seriesID,
			})
			seen[ep.ID] = true
		}
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
