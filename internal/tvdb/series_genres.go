package tvdb

import (
	"context"
	"fmt"
	"strings"
)

// SeriesGenres returns genre names from GET /v4/series/{id} data.genres[].name.
// Claude 2026-10-13: Animation-floor fallback when TMDB is missing or synthetic.
// Reason: Looney Tunes year-seasons use AnthologyTMDBID; TVDB 7266 has genres.
// Troubleshooting: 480p cartoon still on live Medium@480 — TVDB client nil or 404.
// Review if: TVDB GenreBaseRecord drops name in favor of slug-only.
func (c *Client) SeriesGenres(ctx context.Context, seriesID int) ([]string, error) {
	if seriesID <= 0 {
		return nil, fmt.Errorf("tvdb: invalid series id %d", seriesID)
	}
	var resp struct {
		Data struct {
			Genres []struct {
				Name string `json:"name"`
			} `json:"genres"`
		} `json:"data"`
	}
	if err := c.authedGet(ctx, fmt.Sprintf("/v4/series/%d", seriesID), nil, &resp); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.Data.Genres))
	for _, g := range resp.Data.Genres {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}
