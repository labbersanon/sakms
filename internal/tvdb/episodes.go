package tvdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/labbersanon/sakms/internal/httpx"
)

// SeasonTypeOfficial is the season-type path segment this feature uses. It
// is the value the 2026-08-07 LIVE verification was actually performed
// against (series 73910, slug laurel-and-hardy): page=0 returned the full
// 158-episode catalog, and the two slots this feature's tests assert --
// S03E01 "Duck Soup" and S08E03 "The Music Box" -- are the numbers THAT
// ordering reports.
//
// The v4 spec documents the alternatives as: default, official, dvd,
// absolute, alternate, regional. "default" was NOT separately confirmed and
// is deliberately not used: the asserted (season, episode) pairs above were
// measured under "official", so shipping a different ordering would make
// this plan's own asserted values unverified again. See
// .omc/plans/autopilot-impl.md §0.2 and §8.4 (whose live test fetches
// "default" too, asserting only that it is non-empty -- enough to notice if
// the two ever diverge structurally, without pinning numbers nobody has
// measured).
const SeasonTypeOfficial = "official"

// maxEpisodePages bounds SeriesEpisodes' pagination. TheTVDB's documented
// page size is 500, so this admits 20,000 episodes -- far beyond any real
// series. Hitting it means pagination is not terminating the way this code
// assumes, which is a correctness failure, not a size problem: see
// ErrEpisodeListTruncated.
const maxEpisodePages = 40

// ErrEpisodeListTruncated is returned when pagination hits maxEpisodePages.
// It is an ERROR, not a partial result, because every caller of
// SeriesEpisodes uses the catalog to prove a title is UNIQUE across the
// whole show -- and a truncated catalog cannot prove that. Returning
// partial data here would silently downgrade the guarantee to "unique
// across the pages that happened to load."
var ErrEpisodeListTruncated = errors.New("tvdb: episode list exceeded maxEpisodePages")

// Episode is one episode from TheTVDB's per-series episode listing.
// Field names/types follow EpisodeBaseRecord in TheTVDB's published v4
// OpenAPI spec -- see this file's VERIFICATION STATUS block.
type Episode struct {
	ID           int
	SeriesID     int
	Name         string // "" for an untitled episode; callers must handle it
	Number       int    // episode number within SeasonNumber
	SeasonNumber int
	Aired        string // "YYYY-MM-DD", or "" if TheTVDB has no air date
	Runtime      int    // minutes; 0 if unknown
}

// SeriesEpisodes returns EVERY episode TheTVDB lists for seriesID under
// seasonType, following pagination to exhaustion.
//
// Fail-CLOSED: any page error, and pagination overrun, return an error with
// NO partial slice. See ErrEpisodeListTruncated.
//
// Items with a non-positive SeasonNumber-bearing shape are NOT filtered:
// season 0 (Specials) is deliberately included, mirroring
// searchEpisodeByTitle's own Season-0 decision
// (internal/rename/series_episode_title_match.go:113-118) -- the uniqueness
// rule is what protects against a Specials/regular-season title collision,
// and excluding season 0 would cost recall for nothing.
func (c *Client) SeriesEpisodes(ctx context.Context, seriesID int, seasonType string) ([]Episode, error) {
	if seriesID <= 0 {
		return nil, fmt.Errorf("tvdb: invalid series id %d", seriesID)
	}
	if seasonType == "" {
		seasonType = SeasonTypeOfficial
	}
	var out []Episode
	// page starts at 0 -- the v4 spec declares `page` required with
	// `default: 0`, and this was CONFIRMED live on 2026-08-07 (series
	// 73910, page=0 returns the full 158-episode catalog, not an empty
	// array). What is still unverified is pagination CONTINUATION -- that
	// catalog fit in one page, so the loop below has never had its second
	// iteration exercised against the real API, which is why termination is
	// on an empty page bounded by maxEpisodePages rather than on anything
	// in `links`.
	for page := 0; page < maxEpisodePages; page++ {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		var resp seriesEpisodesResponse
		path := fmt.Sprintf("/v4/series/%d/episodes/%s", seriesID, seasonType)
		if err := c.authedGet(ctx, path, q, &resp); err != nil {
			return nil, fmt.Errorf("tvdb: series %d episodes (page %d): %w", seriesID, page, err)
		}
		if len(resp.Data.Episodes) == 0 {
			return out, nil // exhausted
		}
		for _, e := range resp.Data.Episodes {
			if e.SeasonNumber < 0 || e.Number <= 0 {
				continue // structurally unusable slot -- cannot address an episode
			}
			out = append(out, Episode{
				ID: e.ID, SeriesID: e.SeriesID, Name: e.Name,
				Number: e.Number, SeasonNumber: e.SeasonNumber,
				Aired: e.Aired, Runtime: e.Runtime,
			})
		}
	}
	return nil, fmt.Errorf("tvdb: series %d: %w", seriesID, ErrEpisodeListTruncated)
}

// authedGet issues an authenticated GET against the v4 API and decodes the
// JSON body into out. It duplicates doSearch's ensureToken + Bearer-header
// preamble rather than refactoring doSearch to share it: this repo's
// no-premature-abstraction convention (CLAUDE.md, "Established engineering
// conventions") prefers a sibling over hoisting a shared helper for two
// callers, and leaving client.go untouched keeps `git diff --stat` able to
// prove SearchSeries/SearchMovies/Ping are unchanged.
func (c *Client) authedGet(ctx context.Context, path string, q url.Values, out any) error {
	if err := c.ensureToken(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()

	u := c.cfg.BaseURL + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("tvdb: building request for %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, out)
}

// VERIFICATION STATUS, per this project's honesty-about-unverified-assumptions
// convention and mirroring this package's own /search UNVERIFIED ASSUMPTION
// block (client.go:12-17):
//
// The shapes below were transcribed from TheTVDB's PUBLISHED v4 OpenAPI
// specification (docs/swagger.yml in github.com/thetvdb/v4-api), read during
// planning on 2026-08-07, and then CONFIRMED AGAINST A LIVE API RESPONSE the
// same day. What was confirmed, precisely:
//
//	GET /series/73910/episodes/official?page=0
//	  -- series id 73910, slug "laurel-and-hardy"
//	  -- returns the FULL 158-episode catalog in ONE page
//	  -- envelope is {status, data:{series, episodes[]}, links}, exactly the
//	     object-valued `data` shape decoded below (NOT /search's array shape)
//	  -- numeric fields arrive as JSON NUMBERS, not strings, exactly as the
//	     episodeItem struct below declares them
//	  -- links.total_items = 158, links.page_size = 500
//	  -- S03E01 "Duck Soup" (aired 1927-03-13) and S08E03 "The Music Box"
//	     (aired 1932-04-16) both present and correct
//
// So the two things this block previously flagged as unproven now differ:
//
//  1. `page` indexing -- RESOLVED. `page=0` is correct (0-indexed) and is
//     NOT empty. This is no longer a silent-no-op hazard.
//  2. The `links` envelope -- STILL UNVERIFIED, and deliberately still
//     untrusted. The spec $ref's components/schemas/Links but does not
//     DEFINE it, and the live catalog fit in a single page (158 of a 500
//     page size), so pagination CONTINUATION got zero live exercise. Nothing
//     here decodes or trusts `links`; pagination terminates on an empty
//     episodes array, bounded by maxEpisodePages. Do not "improve" this into
//     a links.next-driven loop on the strength of the verification above --
//     it verified nothing about that field.
//
// If TheTVDB changes these field names, episodes decode to zero values and
// are dropped by SeriesEpisodes' Number<=0 filter, degrading to "empty
// catalog" (which every caller treats as no-match) rather than to wrong data.
type seriesEpisodesResponse struct {
	Status string `json:"status"`
	Data   struct {
		Series struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Year string `json:"year"` // string in v4, same as searchItem.Year
		} `json:"series"`
		Episodes []episodeItem `json:"episodes"`
	} `json:"data"`
}

// episodeItem is one EpisodeBaseRecord. NOTE the numeric fields are JSON
// NUMBERS here, unlike searchItem's string-encoded tvdb_id/year -- do not
// copy searchItem's `string` + strconv.Atoi shape into this struct.
type episodeItem struct {
	ID           int    `json:"id"`
	SeriesID     int    `json:"seriesId"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	SeasonNumber int    `json:"seasonNumber"`
	Aired        string `json:"aired"`
	Runtime      int    `json:"runtime"`
}
