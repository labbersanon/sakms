package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"

	"golang.org/x/sync/errgroup"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/omdb"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/trakt"
)

// maxPrefetchedSeasons caps how many seasons discoverDetailHandler eagerly
// fetches episodes for on one request. Seasons past the cap are absent from the
// response ENTIRELY — not present-with-empty-episodes — which is the point:
// a soft-failed placeholder would still cost a cache slot and still gate the
// response, hiding the problem instead of avoiding it.
//
// 30 covers essentially every scripted show; the tail it excludes is soaps,
// daily news, and a few long-running anime. Below the cap nothing changes at
// all.
//
// KNOWN, ACCEPTED LIMITATION, stated plainly because it is a real UI dead end,
// not merely "less eager": a show with more than 30 seasons has NO path to
// grabbing seasons 31+ from the picker. The picker's season grid renders
// exactly what this response carries and has no lazy-fetch for a season it
// cannot find (SeasonEpisodePicker.tsx's `current()` is a find over that one
// list), and its degraded free-text S/E fallback does not help either — that
// only renders when the season list is EMPTY, and a capped response is not
// empty. Accepted for now: the alternative is a per-season lazy fetch, which is
// real scope this finding did not ask for. If a >30-season show ever needs
// grabbing here, that lazy fetch is the fix — do NOT just raise the cap.
// Claude 2026-08-02, Phase-4 review FIX 5 (code-reviewer, not in the plan).
// Review if: the picker gains a lazy per-season fetch, or the TMDB cache's
// 512-entry capacity changes materially.
const maxPrefetchedSeasons = 30

// discoverDetailHandler backs GET
// /api/modes/{mode}/discover/detail?tmdbId=N[&sections=seasons] —
// the Seerr-parity Discover detail popup's one on-demand, per-click enrichment
// fetch (Movies/Series only; Adult has no TMDB id, so it 400s and keeps its
// existing performers/genres popup unchanged). It fans the independent TMDB
// sub-calls (extended details, full credits, keywords, watch providers,
// recommendations) out IN PARALLEL and SOFT-FAILS each one independently: any
// single sub-call failing yields an empty section in the response, never a 500
// for the whole popup. This is one explicit-click, per-title TMDB fetch (same
// trigger shape as discoverTrailerHandler/discoverAvailabilityHandler) — NOT
// the banned automatic per-card page-load probe, and it never touches Prowlarr
// (see CLAUDE.md's "Discover never queries Prowlarr" note).
//
// Soft-fail mechanics (mirrors discoverrefresh.FilterByUSRelease, which moved
// out of discover.go when the scheduled Discover row cache landed): each
// goroutine captures its own result + error into its own local variable, logs
// on error, and ALWAYS returns nil — so a plain errgroup.Group (not
// errgroup.WithContext, which would cancel the group's shared context on the
// first non-nil return) is deliberately used: with every goroutine returning
// nil, there is no cancellation to reason about, and the sibling sub-calls
// all complete even when one fails.
//
// `sections=seasons` scopes the response instead of adding an endpoint: it runs
// ONLY the extended-details call (which is where the season list itself comes
// from) plus the per-season episode fan-out, and never starts the credits/
// keywords/providers/recommendations goroutines. That's for the card-level
// season/episode picker, which wants a lightweight season-only fetch; the
// detail popup omits the param and gets the full bundle plus seasons in one
// request. Any OTHER value degrades to "give them everything" rather than
// 400ing, matching this handler's existing soft-fail posture.
func discoverDetailHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, traktStore *trakt.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		if m != mode.Movies && m != mode.Series {
			http.Error(w, "detail lookup is only supported for movies/series", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tmdbID, err := strconv.Atoi(r.URL.Query().Get("tmdbId"))
		if err != nil || tmdbID <= 0 {
			http.Error(w, "tmdbId query parameter is required and must be a positive integer", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		isTV := m == mode.Series
		seasonsOnly := r.URL.Query().Get("sections") == "seasons"

		// Each sub-call writes only its own captured var below — disjoint, so no
		// data race across the parallel goroutines. Assembled into the DTO after
		// g.Wait().
		var (
			ext          titleExtended
			credits      tmdb.Credits
			keywords     []string
			providers    []tmdb.WatchProvider
			recs         []tmdb.Item
			traktRatings trakt.Ratings
		)

		g := new(errgroup.Group)
		g.Go(func() error {
			var e error
			if isTV {
				var d tmdb.TVDetails
				d, e = sess.TMDB.TVDetails(ctx, tmdbID)
				if e == nil {
					ext = extendedFromTV(d)
				}
			} else {
				var d tmdb.MovieDetails
				d, e = sess.TMDB.MovieDetails(ctx, tmdbID)
				if e == nil {
					ext = extendedFromMovie(d)
				}
			}
			if e != nil {
				log.Printf("discover detail: extended details failed for mode=%s tmdbId=%d, degrading to empty section: %v", m, tmdbID, e)
			}
			return nil
		})
		// The four goroutines below are the bundle `sections=seasons` skips
		// entirely — zero requests to those endpoints, not fetched-then-discarded.
		// The extended-details goroutine above always runs: it is what supplies
		// ext.Seasons, which the season fan-out after g.Wait() iterates.
		if !seasonsOnly {
			g.Go(func() error {
				var e error
				if isTV {
					credits, e = sess.TMDB.TVAggregateFullCredits(ctx, tmdbID)
				} else {
					credits, e = sess.TMDB.MovieFullCredits(ctx, tmdbID)
				}
				if e != nil {
					log.Printf("discover detail: credits failed for mode=%s tmdbId=%d, degrading to empty section: %v", m, tmdbID, e)
				}
				return nil
			})
			g.Go(func() error {
				var e error
				if isTV {
					keywords, e = sess.TMDB.TVKeywords(ctx, tmdbID)
				} else {
					keywords, e = sess.TMDB.MovieKeywords(ctx, tmdbID)
				}
				if e != nil {
					log.Printf("discover detail: keywords failed for mode=%s tmdbId=%d, degrading to empty section: %v", m, tmdbID, e)
				}
				return nil
			})
			g.Go(func() error {
				var e error
				if isTV {
					providers, e = sess.TMDB.TVWatchProviders(ctx, tmdbID)
				} else {
					providers, e = sess.TMDB.MovieWatchProviders(ctx, tmdbID)
				}
				if e != nil {
					log.Printf("discover detail: watch providers failed for mode=%s tmdbId=%d, degrading to empty section: %v", m, tmdbID, e)
				}
				return nil
			})
			g.Go(func() error {
				var e error
				if isTV {
					recs, e = sess.TMDB.TVRecommendations(ctx, tmdbID, 1)
				} else {
					recs, e = sess.TMDB.MovieRecommendations(ctx, tmdbID, 1)
				}
				if e != nil {
					log.Printf("discover detail: recommendations failed for mode=%s tmdbId=%d, degrading to empty section: %v", m, tmdbID, e)
				}
				return nil
			})
			// Claude 2026-08-14: Trakt public ratings ride the same click
			// fan-out as the TMDB bundle. Reason: icon+score row is
			// detail-only; GET /tracked must not grow catalog scores.
			// Review if: Quality Over Time (season chart) is added later.
			if traktStore != nil {
				g.Go(func() error {
					conn, e := traktStore.Get(ctx)
					if e != nil {
						if !errors.Is(e, trakt.ErrNotConfigured) {
							log.Printf("discover detail: trakt credentials failed for tmdbId=%d, omitting trakt rating: %v", tmdbID, e)
						}
						return nil
					}
					if conn.ClientID == "" {
						return nil
					}
					kind := "movies"
					if isTV {
						kind = "shows"
					}
					c := trakt.New(trakt.Config{BaseURL: trakt.DefaultBaseURL, ClientID: conn.ClientID}, httpClient)
					var re error
					traktRatings, re = c.Ratings(ctx, kind, tmdbID)
					if re != nil {
						log.Printf("discover detail: trakt ratings failed for mode=%s tmdbId=%d, omitting trakt rating: %v", m, tmdbID, re)
					}
					return nil
				})
			}
		}
		_ = g.Wait() // every goroutine returns nil — see the soft-fail note above.

		var omdbTitle omdb.Title
		ratings := []apidto.OfficialRating{}
		if !seasonsOnly {
			omdbTitle = lookupOMDbTitle(ctx, httpClient, connStore, sess.TMDB, isTV, tmdbID, ext.IMDBID)
			ratings = assembleOfficialRatings(ext.VoteAverage, ext.VoteCount, traktRatings, omdbTitle)
		}

		// Season/episode prefetch — a SECOND, SEQUENTIAL-AFTER group, deliberately
		// not a sixth sibling of the one above: the season list itself arrives with
		// the extended-details call, so nothing here can start until that group has
		// finished. Every season this response carries is fetched eagerly and in
		// parallel on this one request, which is what makes the picker's episode
		// grid instant on season click and keeps the browser at ONE request
		// instead of N. "Every season this response carries" is no longer "every
		// season TMDB reports" — see maxPrefetchedSeasons.
		//
		// SetLimit(6) bounds CONCURRENCY; maxPrefetchedSeasons bounds TOTAL work.
		// Two different limits for two different costs, and the first alone was
		// never sufficient. This is TMDB, once per explicit operator action, for
		// one title — not the banned automatic per-card Prowlarr probe (CLAUDE.md,
		// "Discover never queries Prowlarr"); both limits are politeness toward
		// TMDB's rate limit and the response cache, not rule compliance.
		//
		// Soft-fails PER SEASON, mirroring this handler's existing posture and the
		// codebase's per-item convention (retryDueGrabs/syncSeriesCatalog): one
		// season's fetch failing leaves that season with an empty episode list and
		// its siblings untouched, never a failed popup.
		//
		// Claude 2026-08-02: sort ascending and cap at maxPrefetchedSeasons BEFORE
		// the fan-out (see that const), so an over-cap season is never fetched at
		// all rather than fetched and hidden.
		// Reason: SetLimit(6) bounds concurrency but NOT total work — a 40-season
		// soap fired 40 sequential-ish TMDB calls and could evict ~8% of the
		// 512-entry cache on one popup open, crowding out the Discover-row entries
		// the cache mostly exists to serve, while the response waited on all 40.
		// The sort moved UP here from where it used to run on the assembled DTO:
		// this is what makes "the first N" mean the lowest season numbers rather
		// than an arbitrary N of TMDB's own ordering.
		// Troubleshooting: Phase-4 review FIX 5 (code-reviewer, not in the plan).
		// Review if: the cache capacity or the picker's data model changes.
		//
		// INVARIANT: nothing may reorder or resize ext.Seasons below this point.
		// seasonEps is indexed POSITIONALLY against it, so a later sort would pair
		// every season with another season's episodes — silently, and no current
		// test would catch it. That is precisely why the sort lives here and not
		// next to the DTO assembly, which is where it looks like it belongs.
		sort.Slice(ext.Seasons, func(i, j int) bool { return ext.Seasons[i].SeasonNumber < ext.Seasons[j].SeasonNumber })
		if len(ext.Seasons) > maxPrefetchedSeasons {
			log.Printf("discover detail: tmdbId=%d reports %d seasons, prefetching the first %d — later seasons are omitted from this response", tmdbID, len(ext.Seasons), maxPrefetchedSeasons)
			ext.Seasons = ext.Seasons[:maxPrefetchedSeasons]
		}

		seasonEps := make([][]tmdb.SeasonEpisode, len(ext.Seasons))
		sg := new(errgroup.Group)
		sg.SetLimit(6)
		for i, s := range ext.Seasons {
			sg.Go(func() error {
				eps, e := sess.TMDB.SeasonDetails(ctx, tmdbID, s.SeasonNumber)
				if e != nil {
					log.Printf("discover detail: season %d episodes failed for tmdbId=%d, degrading to season-only: %v", s.SeasonNumber, tmdbID, e)
					return nil
				}
				seasonEps[i] = eps
				return nil
			})
		}
		_ = sg.Wait()

		seasons := make([]apidto.SeasonSummary, 0, len(ext.Seasons))
		for i, s := range ext.Seasons {
			seasons = append(seasons, apidto.SeasonSummary{
				SeasonNumber: s.SeasonNumber,
				Name:         s.Name,
				AirDate:      s.AirDate,
				EpisodeCount: s.EpisodeCount,
				PosterPath:   s.PosterPath,
				Episodes:     mapSeasonEpisodes(seasonEps[i]),
			})
		}
		// `seasons` is already ascending: ext.Seasons was sorted before the
		// fan-out (see the INVARIANT above) and this loop preserves that order.
		// TMDB returns seasons[] in its own order, so the sort is still required —
		// it just happens earlier now. Judgment call, unchanged: ascending puts
		// Specials (season 0) FIRST, where Sonarr/Seerr conventionally put them
		// last — chosen for consistency with airdatemonitor.go's seasonStates sort
		// and because it needs no special case. Flipping it is a one-line change
		// (up there, not here — re-sorting at this point is safe for the DTO but
		// would tempt the next reader to move the whole sort back down, which is
		// not).

		detail := apidto.TitleDetail{
			Status:                ext.Status,
			OriginalLanguage:      ext.OriginalLanguage,
			ProductionCountry:     ext.ProductionCountry,
			ProductionCountryCode: ext.ProductionCountryCode,
			CollectionName:        ext.CollectionName,
			CollectionID:          ext.CollectionID,
			Networks:              nonNilSlice(ext.Networks),
			Studios:               nonNilSlice(ext.Studios),
			Runtime:               ext.Runtime,
			ReleaseDates:          nonNilSlice(ext.ReleaseDates),
			Genres:                nonNilSlice(ext.Genres),
			Keywords:              nonNilSlice(keywords),
			Cast:                  mapCast(credits.Cast),
			Crew:                  mapCrew(credits.Crew),
			WatchProviders:        mapWatchProviders(providers),
			Recommendations:       mapDiscoverItems(recs),
			Overview:              ext.Overview,
			PosterPath:            ext.PosterPath,
			Ratings:               ratings,
			// `"seasons":[]` and never null — for a Movie (which never populates
			// ext.Seasons), for a Series whose TVDetails call soft-failed, and for
			// a Series TMDB reports zero seasons for. The zero-length make() above
			// is what actually guarantees that; nonNilSlice is belt-and-braces and
			// keeps this field's shape consistent with every other slice on the
			// type (repo's never-null-array convention — the generated TS type is
			// non-nullable).
			Seasons: nonNilSlice(seasons),
		}
		writeJSON(w, detail)
	}
}

// titleExtended normalizes the movie-vs-TV extended-details response into one
// shape the handler assembles from — the two TMDB detail types differ enough
// (Collection is Movies-only; Networks is Series-only) that a small
// intermediate struct is cleaner than branching the DTO assembly.
type titleExtended struct {
	Status                string
	OriginalLanguage      string
	ProductionCountry     string
	ProductionCountryCode string
	CollectionName        string
	CollectionID          int
	Runtime               int
	Genres                []string
	Networks              []string
	Studios               []string
	ReleaseDates          []apidto.ReleaseDateEntry
	Overview              string
	PosterPath            string
	IMDBID                string
	VoteAverage           float64
	VoteCount             int
	// Seasons is Series-only (nil for a Movie) — TMDB's seasons[] as /tv/{id}
	// returns it, in TMDB's own order. It is what the handler's per-season
	// episode fan-out iterates.
	Seasons []tmdb.TVSeason
}

func extendedFromMovie(d tmdb.MovieDetails) titleExtended {
	return titleExtended{
		Status:                d.Status,
		OriginalLanguage:      d.OriginalLanguage,
		ProductionCountry:     d.ProductionCountry,
		ProductionCountryCode: d.ProductionCountryCode,
		CollectionName:        d.Collection.Name,
		CollectionID:          d.Collection.ID,
		Runtime:               d.Runtime,
		Genres:                d.Genres,
		Studios:               d.Studios,
		ReleaseDates:          mapReleaseDates(d.ReleaseDates),
		Overview:              d.Overview,
		PosterPath:            d.PosterPath,
		IMDBID:                d.IMDBID,
		VoteAverage:           d.VoteAverage,
		VoteCount:             d.VoteCount,
	}
}

func extendedFromTV(d tmdb.TVDetails) titleExtended {
	return titleExtended{
		Status:                d.Status,
		OriginalLanguage:      d.OriginalLanguage,
		ProductionCountry:     d.ProductionCountry,
		ProductionCountryCode: d.ProductionCountryCode,
		Runtime:               d.Runtime,
		Genres:                d.Genres,
		Networks:              d.Networks,
		Seasons:               d.Seasons,
		Overview:              d.Overview,
		PosterPath:            d.PosterPath,
		VoteAverage:           d.VoteAverage,
		VoteCount:             d.VoteCount,
	}
}

// mapSeasonEpisodes maps one season's TMDB episodes to the picker's episode
// grid DTO. Same shape as mapCast/mapCrew, and for the same reason: the
// zero-length make() means a soft-failed season's Episodes serializes as [],
// never null.
func mapSeasonEpisodes(in []tmdb.SeasonEpisode) []apidto.EpisodeSummary {
	out := make([]apidto.EpisodeSummary, 0, len(in))
	for _, e := range in {
		out = append(out, apidto.EpisodeSummary{
			EpisodeNumber: e.EpisodeNumber,
			Name:          e.Name,
			AirDate:       e.AirDate,
			Runtime:       e.Runtime,
			StillPath:     e.StillPath,
		})
	}
	return out
}

// releaseTypeLabels maps TMDB's numeric release-date "type" enum to the human
// labels the sidebar shows (see tmdb's releaseTypeDigital/Physical consts and
// the documented 1–6 enum). An unknown type falls back to "Release".
var releaseTypeLabels = map[int]string{
	1: "Premiere",
	2: "Theatrical (limited)",
	3: "Theatrical",
	4: "Digital",
	5: "Physical",
	6: "TV",
}

func mapReleaseDates(in []tmdb.ReleaseDate) []apidto.ReleaseDateEntry {
	out := make([]apidto.ReleaseDateEntry, 0, len(in))
	for _, rd := range in {
		label := releaseTypeLabels[rd.Type]
		if label == "" {
			label = "Release"
		}
		out = append(out, apidto.ReleaseDateEntry{Type: label, Date: rd.Date})
	}
	return out
}

// nonNilSlice returns s unchanged unless it is nil, in which case it returns an
// empty (non-nil) slice — so a soft-failed or type-absent section serializes as
// a JSON [] rather than null, matching this repo's never-null-array convention
// (see grabs.Store.List) and the generated TS type's non-nullable array shape.
func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func mapCast(in []tmdb.CreditPerson) []apidto.CastMember {
	out := make([]apidto.CastMember, 0, len(in))
	for _, p := range in {
		out = append(out, apidto.CastMember{Name: p.Name, Character: p.Character, ProfilePath: p.ProfilePath})
	}
	return out
}

func mapCrew(in []tmdb.CreditPerson) []apidto.CrewMember {
	out := make([]apidto.CrewMember, 0, len(in))
	for _, p := range in {
		out = append(out, apidto.CrewMember{Name: p.Name, Job: p.Job, ProfilePath: p.ProfilePath})
	}
	return out
}

func mapWatchProviders(in []tmdb.WatchProvider) []apidto.WatchProviderDTO {
	out := make([]apidto.WatchProviderDTO, 0, len(in))
	for _, p := range in {
		out = append(out, apidto.WatchProviderDTO{Name: p.Name, LogoPath: p.LogoPath})
	}
	return out
}

// mapDiscoverItems maps tmdb.Item to apidto.DiscoverItem — the same
// field-for-field shape discoverHandler already encodes tmdb.Item as directly
// (identical JSON), re-expressed as the generated DTO type for the detail
// popup's "More like this" rail.
func mapDiscoverItems(in []tmdb.Item) []apidto.DiscoverItem {
	out := make([]apidto.DiscoverItem, 0, len(in))
	for _, it := range in {
		out = append(out, apidto.DiscoverItem{
			ID:          it.ID,
			Title:       it.Title,
			PosterPath:  it.PosterPath,
			Overview:    it.Overview,
			ReleaseDate: it.ReleaseDate,
			VoteAverage: it.VoteAverage,
			MediaType:   string(it.MediaType),
		})
	}
	return out
}

// discoverCalendarHandler backs GET /api/modes/{mode}/discover/calendar?from=
// YYYY-MM-DD&to=YYYY-MM-DD — the Calendar view's month-range fetch (Movies/
// Series only; Adult is TPDB-backed with no TMDB release calendar). Movies use
// the primary_release_date range; Series use first_air_date (premieres). v1 is
// title-level (a movie's release, a show's first air date) — a per-episode
// air-date calendar is a documented follow-up (needs heavier per-episode
// queries). Deliberately does NOT route through filterReleasedMovies/
// HasUSRelease (that's a trending/popular-only "hide unreleased" pass — a
// calendar's whole point is to show upcoming/unreleased titles) and leaves
// SortBy unset so the "newest" sort's own dateField.lte=today cap can't
// collide with the `to` bound.
func discoverCalendarHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		if m != mode.Movies && m != mode.Series {
			http.Error(w, "calendar is only supported for movies/series", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		if from == "" || to == "" {
			http.Error(w, "from and to query parameters are required (YYYY-MM-DD)", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		opts := tmdb.FilterOptions{DateFrom: from, DateTo: to}
		var items []tmdb.Item
		if m == mode.Series {
			items, err = sess.TMDB.DiscoverTVFiltered(ctx, opts, 1)
		} else {
			items, err = sess.TMDB.DiscoverMoviesFiltered(ctx, opts, 1)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		writeJSON(w, mapDiscoverItems(items))
	}
}

// lookupOMDbTitle fetches IMDb/RT scores when an OMDb key is configured.
// Soft-fails to a zero Title on any miss — same contract as Cast/providers.
func lookupOMDbTitle(ctx context.Context, httpClient *http.Client, connStore *connections.Store, tmdbClient *tmdb.Client, isTV bool, tmdbID int, movieIMDBID string) omdb.Title {
	conn, err := connStore.Get(ctx, "omdb")
	if err != nil {
		if !errors.Is(err, connections.ErrNotFound) {
			log.Printf("discover detail: omdb connection failed for tmdbId=%d, omitting imdb/rt: %v", tmdbID, err)
		}
		return omdb.Title{}
	}
	if conn.APIKey == "" {
		return omdb.Title{}
	}
	imdbID := movieIMDBID
	if isTV {
		var e error
		imdbID, e = tmdbClient.TVIMDBID(ctx, tmdbID)
		if e != nil {
			log.Printf("discover detail: tv imdb id failed for tmdbId=%d, omitting imdb/rt: %v", tmdbID, e)
			return omdb.Title{}
		}
	}
	if imdbID == "" {
		return omdb.Title{}
	}
	c := omdb.New(omdb.Config{BaseURL: omdb.DefaultBaseURL, APIKey: conn.APIKey}, httpClient)
	got, e := c.ByIMDBID(ctx, imdbID)
	if e != nil {
		log.Printf("discover detail: omdb lookup failed for tmdbId=%d imdb=%s, omitting imdb/rt: %v", tmdbID, imdbID, e)
		return omdb.Title{}
	}
	return got
}

const (
	scoreKindTen     = "ten"
	scoreKindPercent = "percent"
	rtFreshThreshold = 60
)

func assembleOfficialRatings(tmdbAvg float64, tmdbVotes int, tr trakt.Ratings, om omdb.Title) []apidto.OfficialRating {
	out := make([]apidto.OfficialRating, 0, 5)
	if om.IMDbRating > 0 {
		out = append(out, apidto.OfficialRating{
			Source: "imdb", Label: "IMDb", Score: om.IMDbRating,
			ScoreKind: scoreKindTen, Votes: om.IMDbVotes,
		})
	}
	if om.TomatoMeter > 0 {
		out = append(out, apidto.OfficialRating{
			Source: "rtCritics", Label: "RT Critics", Score: float64(om.TomatoMeter),
			ScoreKind: scoreKindPercent, Badge: rtCriticsBadge(om.TomatoMeter),
		})
	}
	if om.TomatoUserMeter > 0 {
		out = append(out, apidto.OfficialRating{
			Source: "rtAudience", Label: "RT Audience", Score: float64(om.TomatoUserMeter),
			ScoreKind: scoreKindPercent, Badge: rtAudienceBadge(om.TomatoUserMeter),
		})
	}
	if tmdbAvg > 0 {
		out = append(out, apidto.OfficialRating{
			Source: "tmdb", Label: "TMDB", Score: tmdbAvg,
			ScoreKind: scoreKindTen, Votes: tmdbVotes,
		})
	}
	if tr.Rating > 0 {
		out = append(out, apidto.OfficialRating{
			Source: "trakt", Label: "Trakt", Score: tr.Rating * 10,
			ScoreKind: scoreKindPercent, Votes: tr.Votes,
		})
	}
	return out
}

func rtCriticsBadge(meter int) string {
	if meter >= rtFreshThreshold {
		return "Fresh"
	}
	return "Rotten"
}

func rtAudienceBadge(meter int) string {
	if meter >= rtFreshThreshold {
		return "Hot"
	}
	return "Spilled"
}
