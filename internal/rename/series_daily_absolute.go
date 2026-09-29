package rename

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// Claude 2026-09-28: daily air-date + anime absolute identify.
// Reason: files like "Show.2024.03.15.mkv" and "One.Piece.1089.mkv" have no
//   SxxExx. Runs after compact-code, before episode-title match. Maps the
//   token onto a TMDB season/episode; Apply still relocates via RelocateEpisode.
// Troubleshooting: unmatched daily/anime — parser ok=false, TMDB slots
//   empty/ambiguous, or unpinned title search not unique.
// Review if: AniDB / TMDB absolute episode groups replace SlotByAbsolute.

type showEpisodeCache struct {
	mu   sync.Mutex
	byID map[int][]tmdb.EpisodeSlot
}

func ensureSlotCache(cfg *MatchConfig) *showEpisodeCache {
	if cfg.slots == nil {
		cfg.slots = &showEpisodeCache{byID: map[int][]tmdb.EpisodeSlot{}}
	}
	return cfg.slots
}

func (c *showEpisodeCache) load(ctx context.Context, client *tmdb.Client, tmdbID int) ([]tmdb.EpisodeSlot, error) {
	c.mu.Lock()
	if slots, ok := c.byID[tmdbID]; ok {
		c.mu.Unlock()
		return slots, nil
	}
	c.mu.Unlock()
	slots, err := client.EpisodeSlots(ctx, tmdbID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.byID == nil {
		c.byID = map[int][]tmdb.EpisodeSlot{}
	}
	c.byID[tmdbID] = slots
	c.mu.Unlock()
	return slots, nil
}

func tryDailyOrAbsoluteSeries(
	ctx context.Context, sess *mode.Session, tracked map[episodeKey]bool,
	pin pinnedShow, generalRoot, foundRoot, videoPath string,
	cfg MatchConfig, base proposals.Proposal,
) *proposals.Proposal {
	if sess == nil || sess.TMDB == nil {
		return nil
	}
	name := filepath.Base(videoPath)
	parent := filepath.Dir(videoPath)
	date, hasDate := library.ParseEpisodeAirDateLoose(name, parent)
	abs, hasAbs := 0, false
	if !hasDate {
		abs, hasAbs = library.ParseAbsoluteEpisodeLoose(name, parent)
	}
	if !hasDate && !hasAbs {
		return nil
	}

	tmdbID := pin.tmdbID
	title := pin.title
	year := pin.year
	root := pin.root
	if root == "" {
		root = generalRoot
	}
	if tmdbID <= 0 {
		id, show, y, ok := searchShowForDaily(ctx, sess, name, parent)
		if !ok {
			return nil
		}
		tmdbID, title, year = id, show, y
		if sess.KidsRootPath != "" && foundRoot == sess.KidsRootPath {
			root = foundRoot
		}
	}
	if title == "" {
		return nil
	}

	slots, err := ensureSlotCache(&cfg).load(ctx, sess.TMDB, tmdbID)
	if err != nil || len(slots) == 0 {
		return nil
	}
	var season, episode int
	var ok bool
	if hasDate {
		season, episode, ok = tmdb.SlotByAirDate(slots, date)
	} else {
		season, episode, ok = tmdb.SlotByAbsolute(slots, abs)
	}
	if !ok {
		q := base
		q.Status = proposals.Unmatched
		if hasDate {
			q.Reason = fmt.Sprintf("air date %s is missing or ambiguous on %q", date, title)
		} else {
			q.Reason = fmt.Sprintf("absolute episode %d is past the end of %q", abs, title)
		}
		return &q
	}

	p := base
	p.Status = proposals.Pending
	p.Title = title
	p.TMDBID = tmdbID
	p.Year = year
	p.SeasonNumber = season
	p.EpisodeNumber = episode
	p.RootFolderPath = root
	if tracked[episodeKey{tmdbID: tmdbID, season: season, episode: episode}] {
		acceptDuplicatePendingEpisode(&p, title, season, episode)
	}
	return &p
}

func searchShowForDaily(ctx context.Context, sess *mode.Session, name, parent string) (id int, title string, year int, ok bool) {
	q := library.StripDailyOrAbsolute(name)
	if q == "" || q == name {
		q = library.StripDailyOrAbsolute(filepath.Base(parent))
	}
	if q == "" || !hasWordToken(q) {
		return 0, "", 0, false
	}
	hits, err := sess.TMDB.SearchTV(ctx, searchterm.FromName(q))
	if err != nil || len(hits) == 0 {
		return 0, "", 0, false
	}
	var match tmdb.Item
	found := 0
	for _, h := range hits {
		if !HasTitleTokenOverlap(q, h.Title) {
			continue
		}
		found++
		match = h
		if found > 1 {
			return 0, "", 0, false
		}
	}
	if found != 1 {
		return 0, "", 0, false
	}
	return match.ID, match.Title, yearFromReleaseDate(match.ReleaseDate), true
}
