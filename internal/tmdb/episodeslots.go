package tmdb

import (
	"context"
	"sort"
)

// Claude 2026-09-28: flatten a show's TMDB episodes for daily/absolute resolve.
// Reason: date-named dailies and absolute-numbered anime have no SxxExx;
//   Rename/import map those tokens onto a season/episode slot.
// Troubleshooting: Incomplete (empty slots) — TVDetails or a SeasonDetails
//   call failed; uniqueness is unprovable so callers must refuse.
// Review if: AniDB or TMDB episode-group type Absolute replaces this walk.

// EpisodeSlot is one TMDB episode with the air date used for daily matching.
type EpisodeSlot struct {
	Season  int
	Episode int
	AirDate string
}

// EpisodeSlots returns every episode TMDB lists for tmdbID, seasons in
// ascending order. A TVDetails error, empty Seasons, or any SeasonDetails
// error returns nil — fail-closed, same as rename's episode-title search.
func (c *Client) EpisodeSlots(ctx context.Context, tmdbID int) ([]EpisodeSlot, error) {
	det, err := c.TVDetails(ctx, tmdbID)
	if err != nil || len(det.Seasons) == 0 {
		return nil, err
	}
	seasons := make([]int, 0, len(det.Seasons))
	for _, s := range det.Seasons {
		seasons = append(seasons, s.SeasonNumber)
	}
	sort.Ints(seasons)
	var out []EpisodeSlot
	for _, season := range seasons {
		eps, err := c.SeasonDetails(ctx, tmdbID, season)
		if err != nil {
			return nil, err
		}
		for _, ep := range eps {
			out = append(out, EpisodeSlot{
				Season:  season,
				Episode: ep.EpisodeNumber,
				AirDate: ep.AirDate,
			})
		}
	}
	return out, nil
}

// SlotByAirDate returns the unique episode that aired on date (YYYY-MM-DD).
func SlotByAirDate(slots []EpisodeSlot, date string) (season, episode int, ok bool) {
	if date == "" {
		return 0, 0, false
	}
	found := 0
	for _, s := range slots {
		if s.AirDate != date {
			continue
		}
		found++
		season, episode = s.Season, s.Episode
		if found > 1 {
			return 0, 0, false
		}
	}
	return season, episode, found == 1
}

// SlotByAbsolute maps 1-based absolute numbering onto seasons >= 1 in
// (season, episode) order. Season 0 / Specials are skipped, matching Sonarr.
func SlotByAbsolute(slots []EpisodeSlot, abs int) (season, episode int, ok bool) {
	if abs < 1 {
		return 0, 0, false
	}
	n := 0
	for _, s := range slots {
		if s.Season < 1 {
			continue
		}
		n++
		if n == abs {
			return s.Season, s.Episode, true
		}
	}
	return 0, 0, false
}
