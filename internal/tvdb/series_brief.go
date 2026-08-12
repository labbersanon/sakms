package tvdb

import (
	"context"
	"fmt"
	"strconv"
)

// SeriesBrief returns a series' display name and year from GET /v4/series/{id}.
func (c *Client) SeriesBrief(ctx context.Context, seriesID int) (name string, year int, err error) {
	if seriesID <= 0 {
		return "", 0, fmt.Errorf("tvdb: invalid series id %d", seriesID)
	}
	var resp struct {
		Data struct {
			Name string `json:"name"`
			Year string `json:"year"`
		} `json:"data"`
	}
	path := fmt.Sprintf("/v4/series/%d", seriesID)
	if err := c.authedGet(ctx, path, nil, &resp); err != nil {
		return "", 0, err
	}
	year, _ = strconv.Atoi(resp.Data.Year)
	return resp.Data.Name, year, nil
}
