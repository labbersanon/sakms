package tvdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/labbersanon/sakms/internal/httpx"
)

// EpisodeHit is one episode from GET /v4/search?type=episode.
type EpisodeHit struct {
	EpisodeID     int
	Name          string
	SeasonNumber  int
	EpisodeNumber int
	SeriesID      int
}

// SearchEpisodes searches TheTVDB for episodes by title.
func (c *Client) SearchEpisodes(ctx context.Context, query string) ([]EpisodeHit, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()

	q := url.Values{}
	q.Set("query", query)
	q.Set("type", "episode")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.cfg.BaseURL+"/v4/search?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("tvdb: building episode search request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &envelope); err != nil {
		return nil, fmt.Errorf("tvdb: episode search %q: %w", query, err)
	}

	out := make([]EpisodeHit, 0, len(envelope.Data))
	for _, raw := range envelope.Data {
		if hit, ok := decodeEpisodeHit(raw); ok {
			out = append(out, hit)
		}
	}
	return out, nil
}

type flexibleEpisode struct {
	ID           int              `json:"id"`
	Name         string           `json:"name"`
	SeasonNumber int              `json:"seasonNumber"`
	Number       int              `json:"number"`
	SeriesID     int              `json:"seriesId"`
	Episode      *flexibleEpisode `json:"episode"`
}

func decodeEpisodeHit(raw json.RawMessage) (EpisodeHit, bool) {
	var f flexibleEpisode
	if err := json.Unmarshal(raw, &f); err != nil {
		return EpisodeHit{}, false
	}
	if f.Episode != nil {
		f = *f.Episode
	}
	if f.ID <= 0 || f.SeriesID <= 0 || f.Name == "" || f.Number < 1 {
		return EpisodeHit{}, false
	}
	return EpisodeHit{
		EpisodeID:     f.ID,
		Name:          f.Name,
		SeasonNumber:  f.SeasonNumber,
		EpisodeNumber: f.Number,
		SeriesID:      f.SeriesID,
	}, true
}
