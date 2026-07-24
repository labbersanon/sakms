package api

import (
	"log"
	"net/http"
	"strconv"

	"golang.org/x/sync/errgroup"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// discoverDetailHandler backs GET /api/modes/{mode}/discover/detail?tmdbId=N —
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
// Soft-fail mechanics (mirrors filterByUSRelease in discover.go): each
// goroutine captures its own result + error into its own local variable, logs
// on error, and ALWAYS returns nil — so a plain errgroup.Group (not
// errgroup.WithContext, which would cancel the group's shared context on the
// first non-nil return) is deliberately used: with every goroutine returning
// nil, there is no cancellation to reason about, and the sibling sub-calls
// all complete even when one fails.
func discoverDetailHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		isTV := m == mode.Series

		// Each sub-call writes only its own captured var below — disjoint, so no
		// data race across the parallel goroutines. Assembled into the DTO after
		// g.Wait().
		var (
			ext       titleExtended
			credits   tmdb.Credits
			keywords  []string
			providers []tmdb.WatchProvider
			recs      []tmdb.Item
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
		_ = g.Wait() // every goroutine returns nil — see the soft-fail note above.

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
	}
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
func discoverCalendarHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, nil, m)
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
