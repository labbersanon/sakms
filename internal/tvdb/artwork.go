package tvdb

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// ArtworkBaseURL is where relative TVDB banner/poster paths are hosted.
// UNVERIFIED ASSUMPTION against live CDN host naming — if TVDB moves hosts,
// absolute image URLs from artworks that already include https:// still work;
// only relative paths need this base.
var ArtworkBaseURL = "https://artworks.thetvdb.com"

// artworkTypePoster is TVDB v4's type id for poster art on series/movies.
// Confirmed against published OpenAPI (type 2 = Poster); soft-fail if the
// endpoint returns other types only — we still accept image URLs that look
// like posters by path when type is missing.
const artworkTypePoster = 2

// SeriesPosterURL returns an absolute https poster URL for a TVDB series id,
// or "" when none is found. Prefers type=poster artworks; falls back to the
// series image field on /v4/series/{id}.
// Claude 2026-09-22: artwork for Library poster fallback after TMDB miss.
// Reason: some tracked titles have empty/404 TMDB posters but live on TVDB.
// Troubleshooting: letter tiles for TMDB-empty series that TVDB has art for.
// Review if: TVDB artwork type ids change or CDN base moves.
func (c *Client) SeriesPosterURL(ctx context.Context, seriesID int) (string, error) {
	if seriesID <= 0 {
		return "", fmt.Errorf("tvdb: invalid series id %d", seriesID)
	}
	if u, err := c.posterFromArtworks(ctx, fmt.Sprintf("/v4/series/%d/artworks", seriesID)); err != nil {
		return "", err
	} else if u != "" {
		return u, nil
	}
	var resp struct {
		Data struct {
			Image string `json:"image"`
		} `json:"data"`
	}
	if err := c.authedGet(ctx, fmt.Sprintf("/v4/series/%d", seriesID), nil, &resp); err != nil {
		return "", err
	}
	return absolutizeArtwork(resp.Data.Image), nil
}

// MoviePosterURL returns an absolute https poster URL for a TVDB movie id.
func (c *Client) MoviePosterURL(ctx context.Context, movieID int) (string, error) {
	if movieID <= 0 {
		return "", fmt.Errorf("tvdb: invalid movie id %d", movieID)
	}
	if u, err := c.posterFromArtworks(ctx, fmt.Sprintf("/v4/movies/%d/artworks", movieID)); err != nil {
		return "", err
	} else if u != "" {
		return u, nil
	}
	var resp struct {
		Data struct {
			Image    string `json:"image"`
			ImageURL string `json:"image_url"`
		} `json:"data"`
	}
	if err := c.authedGet(ctx, fmt.Sprintf("/v4/movies/%d", movieID), nil, &resp); err != nil {
		return "", err
	}
	if u := absolutizeArtwork(resp.Data.ImageURL); u != "" {
		return u, nil
	}
	return absolutizeArtwork(resp.Data.Image), nil
}

func (c *Client) posterFromArtworks(ctx context.Context, path string) (string, error) {
	q := url.Values{}
	// lang filter is optional; omit to accept any language poster.
	var resp struct {
		Data []struct {
			Type  int    `json:"type"`
			Image string `json:"image"`
		} `json:"data"`
	}
	if err := c.authedGet(ctx, path, q, &resp); err != nil {
		// Some ids 404 on /artworks; treat as no art rather than hard-fail the
		// whole poster chain (series image fallback still runs for series).
		if strings.Contains(err.Error(), "404") {
			return "", nil
		}
		return "", err
	}
	var fallback string
	for _, a := range resp.Data {
		u := absolutizeArtwork(a.Image)
		if u == "" {
			continue
		}
		if a.Type == artworkTypePoster {
			return u, nil
		}
		if fallback == "" {
			fallback = u
		}
	}
	return fallback, nil
}

func absolutizeArtwork(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "https://") {
		return raw
	}
	if strings.HasPrefix(raw, "http://") {
		// Image proxy is https-only; skip plaintext.
		return ""
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	return strings.TrimRight(ArtworkBaseURL, "/") + raw
}
