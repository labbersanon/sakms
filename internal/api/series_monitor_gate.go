package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// Claude 2026-10-02: tracked series grabs must name a monitored season.
// Reason: Requests row Grab/Search sent title+tmdbId with no season. That
//   downloaded whole-show packs (AHS 2153–2157) while only season 13 was
//   monitored. MissingEpisodes stays unfiltered for catalog sync; Requests
//   and grab paths now refuse unmonitored seasons.
// Troubleshooting: 409 on a series grab — seasonSpecified false, or the
//   season has no library_season_monitored=true row.
// Review if: operator Discover grab of an unmonitored season should be an
//   explicit override (it is not today).

var (
	errSeriesGrabNeedsSeason   = errors.New("this series is tracked — grab a monitored season, not the whole show")
	errSeriesSeasonUnmonitored = errors.New("that season is not monitored")
)

// monitoredMissingEpisodes is MissingEpisodes restricted to seasons the
// operator monitors. Absent/false rows stay unmonitored. MissingEpisodes
// itself is unchanged (catalog sync still needs the unfiltered list).
func monitoredMissingEpisodes(ctx context.Context, libStore *library.Store, seriesID int64) ([]library.Episode, error) {
	missing, err := libStore.MissingEpisodes(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	monitored, err := libStore.MonitoredSeasons(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	var out []library.Episode
	for _, ep := range missing {
		if monitored[ep.SeasonNumber] {
			out = append(out, ep)
		}
	}
	return out, nil
}

func refuseUnmonitoredSeriesGrab(ctx context.Context, libStore *library.Store, m mode.Mode, tmdbID int, season int, seasonSpecified bool) error {
	if m != mode.Series || libStore == nil || tmdbID <= 0 {
		return nil
	}
	series, err := libStore.GetSeriesByTMDBID(ctx, tmdbID)
	if err != nil {
		if errors.Is(err, library.ErrNotFound) {
			return nil
		}
		return err
	}
	if !seasonSpecified {
		return errSeriesGrabNeedsSeason
	}
	monitored, err := libStore.MonitoredSeasons(ctx, series.ID)
	if err != nil {
		return err
	}
	if !monitored[season] {
		return fmt.Errorf("%w (season %d)", errSeriesSeasonUnmonitored, season)
	}
	return nil
}
