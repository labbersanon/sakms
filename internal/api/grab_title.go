package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

func formatGrabCatalogTitle(title string, specified bool, season, episode int, episodeTitle string) string {
	// Claude 2026-10-22: Activity/Downloads label for series grabs.
	// Reason: Grab.Title is the show name; live Furious rows stored S01E05 but
	//   the UI printed only "Furious". Specials are season 0 with SeasonSpecified.
	// Troubleshooting: label missing SxxExx → SeasonSpecified false on the row.
	//   Missing · title → library_episodes.title empty. Review if: Grab.Title
	//   starts storing the formatted label.
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	out := title
	if specified {
		if episode > 0 {
			out = fmt.Sprintf("%s S%02dE%02d", title, season, episode)
		} else {
			out = fmt.Sprintf("%s S%02d", title, season)
		}
	}
	ep := strings.TrimSpace(episodeTitle)
	if ep != "" && !strings.EqualFold(ep, title) {
		return out + " · " + ep
	}
	return out
}

func catalogTitleFromGrab(g *grabs.Grab) string {
	if g == nil {
		return ""
	}
	return formatGrabCatalogTitle(g.Title, g.SeasonSpecified, g.SeasonNumber, g.EpisodeNumber, g.EpisodeTitle)
}

func attachEpisodeTitles(ctx context.Context, libStore *library.Store, list []grabs.Grab) {
	if libStore == nil || len(list) == 0 {
		return
	}
	cache := newEpisodeTitleCache(libStore)
	for i := range list {
		g := &list[i]
		if g.Mode != mode.Series || !g.SeasonSpecified || g.EpisodeNumber <= 0 {
			continue
		}
		g.EpisodeTitle = cache.title(ctx, g.TMDBID, g.TVDBID, g.SeasonNumber, g.EpisodeNumber)
	}
}

type episodeTitleCache struct {
	lib      *library.Store
	tmdb     map[int]*library.Series
	tvdb     map[int]*library.Series
	missTmdb map[int]bool
	missTvdb map[int]bool
	eps      map[int64]map[int]map[int]string
}

func newEpisodeTitleCache(libStore *library.Store) *episodeTitleCache {
	return &episodeTitleCache{
		lib:      libStore,
		tmdb:     map[int]*library.Series{},
		tvdb:     map[int]*library.Series{},
		missTmdb: map[int]bool{},
		missTvdb: map[int]bool{},
		eps:      map[int64]map[int]map[int]string{},
	}
}

func (c *episodeTitleCache) title(ctx context.Context, tmdbID, tvdbID, season, episode int) string {
	if c == nil || c.lib == nil || episode <= 0 {
		return ""
	}
	series := c.series(ctx, tmdbID, tvdbID)
	if series == nil {
		return ""
	}
	bySeason := c.episodes(ctx, series.ID)
	if bySeason == nil {
		return ""
	}
	return strings.TrimSpace(bySeason[season][episode])
}

func (c *episodeTitleCache) series(ctx context.Context, tmdbID, tvdbID int) *library.Series {
	if tmdbID != 0 {
		if s, ok := c.tmdb[tmdbID]; ok {
			return s
		}
		if !c.missTmdb[tmdbID] {
			s, err := c.lib.GetSeriesByTMDBID(ctx, tmdbID)
			if err == nil {
				c.tmdb[tmdbID] = s
				if s.TVDBID != 0 {
					c.tvdb[s.TVDBID] = s
				}
				return s
			}
			c.missTmdb[tmdbID] = true
		}
	}
	if tvdbID > 0 {
		if s, ok := c.tvdb[tvdbID]; ok {
			return s
		}
		if !c.missTvdb[tvdbID] {
			s, err := c.lib.GetSeriesByTVDBID(ctx, tvdbID)
			if err == nil {
				c.tvdb[tvdbID] = s
				if s.TMDBID != 0 {
					c.tmdb[s.TMDBID] = s
				}
				return s
			}
			c.missTvdb[tvdbID] = true
		}
	}
	return nil
}

func (c *episodeTitleCache) episodes(ctx context.Context, seriesID int64) map[int]map[int]string {
	if got, ok := c.eps[seriesID]; ok {
		return got
	}
	list, err := c.lib.ListEpisodes(ctx, seriesID)
	if err != nil {
		c.eps[seriesID] = nil
		return nil
	}
	bySeason := map[int]map[int]string{}
	for _, ep := range list {
		if strings.TrimSpace(ep.Title) == "" {
			continue
		}
		if bySeason[ep.SeasonNumber] == nil {
			bySeason[ep.SeasonNumber] = map[int]string{}
		}
		bySeason[ep.SeasonNumber][ep.EpisodeNumber] = ep.Title
	}
	c.eps[seriesID] = bySeason
	return bySeason
}
