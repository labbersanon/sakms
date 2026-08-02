package api

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// Calendar's Upcoming routes. This file holds the Movies handler and the Series
// one — two structurally different queries against different backends,
// deliberately NOT one mode-parameterised route: a /api/modes/{mode}/... shape
// would need an `adult` member that returns 400 forever, which is the apology
// discoverCalendarHandler already has to make. Non-mode-scoped paths have
// precedent here (/api/requests, /api/autograb-batch).
//
// Both routes require from/to and return 400 without them (calendarWindow),
// matching discoverCalendarHandler's existing contract and preventing an
// unbounded query.
//
// The two handlers differ on ONE thing that looks like an inconsistency and is
// not: Movies 400s when TMDB is unconfigured, Series does not. See
// upcomingSeriesHandler's own note before "fixing" it.

// calendarWindow reads and validates both Upcoming routes' required from/to
// window, writing the 400 itself and reporting whether the caller may proceed.
//
// Extracted only once a SECOND caller existed (the Series handler), per this
// codebase's no-premature-abstraction rule — two handlers with the same
// required-window contract are exactly the case where one copy is worth less
// than one function. The dates are not parsed: every date this feature compares
// is TMDB's fixed YYYY-MM-DD, for which lexicographic order IS chronological
// order, and parsing would only add a policy for malformed input that neither
// handler has any better answer for than the empty result it already produces.
func calendarWindow(w http.ResponseWriter, r *http.Request) (from, to string, ok bool) {
	from = r.URL.Query().Get("from")
	to = r.URL.Query().Get("to")
	if from == "" || to == "" {
		http.Error(w, "from and to query parameters are required (YYYY-MM-DD)", http.StatusBadRequest)
		return "", "", false
	}
	return from, to, true
}

// upcomingMoviesHandler backs GET /api/calendar/upcoming/movies?from=&to= —
// the Upcoming calendar's Movies data, from TMDB's two-query merge
// (tmdb.DiscoverMoviesUpcoming: a region-scoped digital/physical release query
// plus the plain primary-release-date query, merged by id with the typed query
// winning) plus a server-computed already-requested flag per title.
//
// UNVERIFIED ASSUMPTION, and it is the reason this handler does per-item work
// at all (see tmdb.DiscoverMoviesUpcoming's own note): it is NOT confirmed
// against a live TMDB call whether a /discover/movie result's release_date
// carries the TYPED date the with_release_type filter matched or the PRIMARY
// release date. This handler assumes the expensive reading — PRIMARY — and
// resolves each digital-tagged item's real typed date via MovieDetails. If a
// live check ever settles it the other way, the fix is deleting
// resolveTypedReleaseDates; building the cheap way and being wrong would mean
// bucketing every digitally-released film into the month it opened in cinemas,
// leaving the month the operator is actually looking at empty.
//
// Cost: 2 discover calls + at most one MovieDetails call per DIGITAL item.
// Only the digital items are resolved, because only their date is ambiguous —
// the planned items came from a primary_release_date query and therefore carry
// a primary date under either reading, so there is nothing to correct. Page 1
// only, so that is ≤20 resolutions, ≤22 TMDB calls for a month view.
// SEQUENTIAL, never an errgroup fan-out: this codebase's firm rule against
// concurrent TMDB/indexer fan-outs on a read path (the same rule that got the
// per-card availability badge permanently banned) applies here too.
func upcomingMoviesHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, grabsStore *grabs.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		from, to, ok := calendarWindow(w, r)
		if !ok {
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Movies)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		movies, err := sess.TMDB.DiscoverMoviesUpcoming(ctx, from, to)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		requested, err := alreadyRequestedMovieIDs(ctx, grabsStore, libStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		entries := make([]apidto.UpcomingMovieEntry, 0, len(movies))
		for _, m := range movies {
			entries = append(entries, apidto.UpcomingMovieEntry{
				TMDBID:           m.ID,
				Title:            m.Title,
				PosterPath:       m.PosterPath,
				ReleaseDate:      resolveTypedReleaseDate(ctx, sess.TMDB, m, from, to),
				ReleaseKind:      m.ReleaseKind,
				AlreadyRequested: requested[m.ID],
			})
		}
		writeJSON(w, entries)
	}
}

// upcomingSeriesHandler backs GET /api/calendar/upcoming/series?from=&to= —
// the Upcoming calendar's Series data: every not-yet-aired episode of an
// already-tracked series that is not on disk. Bounded by the operator's own
// library rather than by a catalog query, so unlike the Movies half it makes no
// TMDB discover call at all; the only TMDB work is the catalog sync below.
//
// IT DOES NOT 400 WHEN TMDB IS UNCONFIGURED, and that asymmetry with
// upcomingMoviesHandler two functions above is deliberate — do not "fix" it.
// Movies has literally nothing to serve without TMDB (its entire result set IS
// a discover query). Series' result set comes from library_episodes, which the
// sync merely REFRESHES; on a TMDB-less install the honest answer is whatever
// rows are already there, which is a populated view for anyone who has run the
// air-date pass before. Returning 400 here would make the view unreachable for
// a reason that does not apply to it.
//
// THE SYNC IS UNCONDITIONAL — it runs whatever usenet_autograb_enabled says.
// That is the "always available" half of the metadata/dispatch split
// seriescatalog.go's file doc records: nothing else in this codebase ever
// writes an episode's air_date, and the only other caller runs inside the retry
// cycle, which does not start at all on a default install. Gating this on the
// toggle would render the view permanently empty for most operators, which is
// the exact bug the extraction exists to fix.
//
// PROWLARR IS NOT REQUIRED, also deliberately. monitorAirDates returns early on
// `sess.TMDB == nil || sess.Prowlarr == nil`, but that guard lives in
// monitorAirDates because that pass exists to DISPATCH; a direct caller of
// syncSeriesCatalogForRead inherits nothing from it, and a browsing view that
// cannot dispatch must not acquire an indexer dependency it has no use for.
// Copying the Prowlarr conjunct here would empty the view on a Prowlarr-less
// install for no reason.
//
// Cost: one ListSeries plus one MissingEpisodes per tracked series — N SQLite
// queries, no external calls, the same N-query shape requestsHandler already
// runs on every /api/requests load. The TMDB side is bounded by
// syncSeriesCatalogForRead's own freshness cache and per-request cap, not here.
func upcomingSeriesHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		from, to, ok := calendarWindow(w, r)
		if !ok {
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Series)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		seriesList, err := libStore.ListSeries(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Soft by construction: syncSeriesCatalogForRead returns having never
		// failed and no-ops entirely when sess.TMDB is nil, so a TMDB outage or
		// an unconfigured TMDB degrades to "serve whatever the library already
		// has" rather than to an error page or a blank one.
		syncSeriesCatalogForRead(ctx, sess, libStore, seriesList, time.Now())

		entries := make([]apidto.UpcomingSeriesEntry, 0, len(seriesList))
		for _, series := range seriesList {
			missing, err := libStore.MissingEpisodes(ctx, series.ID)
			if err != nil {
				// Logged and skipped, never fatal to the request — one series'
				// failure must not blank a whole month, the same fault
				// isolation dispatchAirDateGrabs uses over the same call.
				log.Printf("calendar upcoming: listing missing episodes for series %d: %v", series.ID, err)
				continue
			}
			for _, ep := range upcomingEpisodes(missing, from, to) {
				entries = append(entries, apidto.UpcomingSeriesEntry{
					SeriesTitle:   series.Title,
					SeriesID:      series.ID,
					TMDBID:        series.TMDBID,
					SeasonNumber:  ep.SeasonNumber,
					EpisodeNumber: ep.EpisodeNumber,
					EpisodeTitle:  ep.Title,
					AirDate:       ep.AirDate,
				})
			}
		}
		writeJSON(w, entries)
	}
}

// upcomingEpisodes filters MissingEpisodes' output to the episodes that belong
// in the requested calendar window. Two predicates, and the absence of a third
// is as load-bearing as either of them:
//
//  1. The air date is KNOWN. TMDB leaves air_date empty for unannounced
//     episodes — UpsertEpisodeCatalog's COALESCE(NULLIF(...)) exists because
//     that case is real and common — and an empty string sorts before every
//     real date. Without this guard every unannounced episode would bucket at
//     the epoch instead of being dropped. Mandatory, not defensive.
//  2. The air date falls inside [from, to]. Plain string comparison: TMDB's
//     air_date is a fixed YYYY-MM-DD, for which lexicographic order IS
//     chronological order.
//
// "In the future" is expressed as "at or after the window's start", NOT as a
// comparison against time.Now(), and that is the deliberate reading of the
// window. This backs a month grid, which must render every episode of the month
// it is showing — a now-check would punch holes in the current month's earlier
// cells and make the function's output depend on the wall clock. A caller that
// wants strictly-future episodes passes today as `from`.
//
// EVERY RETURNED EPISODE'S AirDate IS INSIDE [from, to], and is therefore always
// bucketable into a cell of the requested month. This is the OPPOSITE guarantee
// to the movies half's — resolveTypedReleaseDate deliberately may return a date
// outside the window, so a grid rendering both must drop out-of-window movies
// but never needs to for series.
//
// SIBLING OF airdatemonitor.go's eligibleEpisodes, NEVER A REUSE OF IT, and the
// difference is not a refactoring opportunity. That one selects episodes that
// have ALREADY AIRED (air_date <= today) whose season is MONITORED, because it
// decides what may be auto-searched. This one takes no monitored map at all:
// the monitored flag gates SEARCHING, and Upcoming is a browsing view where an
// operator must be able to see that a season they have not monitored has
// episodes coming — otherwise there is nothing to look at that would make them
// want to monitor it. Two filters that share a parameter type but not a
// predicate are not one filter.
func upcomingEpisodes(missing []library.Episode, from, to string) []library.Episode {
	out := []library.Episode{}
	for _, ep := range missing {
		if ep.AirDate == "" {
			continue
		}
		if ep.AirDate < from || ep.AirDate > to {
			continue
		}
		out = append(out, ep)
	}
	return out
}

// digitalReleaseTypes are TMDB's release_dates type ids for Digital (4) and
// Physical (5) — the same two tmdb.DiscoverMoviesUpcoming's query A filters on
// (its own list is unexported) and the same two HasUSRelease treats as
// "actually available". MovieDetails' ReleaseDates list is already US-scoped,
// matching tmdb.UpcomingRegion, so no region filtering is needed here.
var digitalReleaseTypes = map[int]bool{4: true, 5: true}

// resolveTypedReleaseDate returns the date this movie should bucket on.
//
// For a planned-tagged item it is the discover-returned date unchanged: that
// item came from the primary_release_date query, so its date is a primary date
// under either reading of the open question above and needs no correction.
//
// For a digital-tagged item it is the real US digital/physical date from
// MovieDetails (one round trip — release_dates arrive with the details, no
// second call), preferring one inside the requested window and falling back to
// the earliest typed date otherwise. The kind is NOT re-derived here: it is
// decided by which query matched, so an item whose details carry no type-4/5
// entry stays "digital" and merely keeps its discover date.
//
// Soft-fails to the discover date on any TMDB error or missing typed entry —
// one unresolvable title must not 502 a whole month view, the same degradation
// discoverDetailHandler's per-section fan-out already uses.
//
// A RETURNED DATE IS NOT GUARANTEED TO FALL INSIDE [from, to], and a caller
// must not assume it does. A digital item whose only typed dates sit outside
// the window keeps the earliest of them, deliberately: reporting the real
// digital date is honest, and a title that goes digital in November genuinely
// does not belong in a September cell. A month grid should drop such an entry
// rather than treat every returned row as bucketable.
func resolveTypedReleaseDate(ctx context.Context, client *tmdb.Client, m tmdb.UpcomingMovie, from, to string) string {
	if m.ReleaseKind != tmdb.ReleaseKindDigital {
		return m.ReleaseDate
	}
	details, err := client.MovieDetails(ctx, m.ID)
	if err != nil {
		return m.ReleaseDate
	}
	var earliest, inWindow string
	for _, rd := range details.ReleaseDates {
		if !digitalReleaseTypes[rd.Type] {
			continue
		}
		// TMDB's release_dates entries carry a full ISO timestamp
		// ("2023-07-01T00:00:00.000Z"), not the bare YYYY-MM-DD the discover
		// results use. Truncating is required, not cosmetic: the frontend
		// buckets on an exact YYYY-MM-DD day key, so an untruncated value
		// lands in no calendar cell at all.
		day := truncateToDay(rd.Date)
		if day == "" {
			continue
		}
		if earliest == "" || day < earliest {
			earliest = day
		}
		if day >= from && day <= to && (inWindow == "" || day < inWindow) {
			inWindow = day
		}
	}
	switch {
	case inWindow != "":
		return inWindow
	case earliest != "":
		return earliest
	default:
		return m.ReleaseDate
	}
}

// truncateToDay reduces a TMDB date string to its bare YYYY-MM-DD day part,
// tolerating both the timestamp form release_dates uses and the already-bare
// form discover results use. Returns "" for anything shorter than a day.
func truncateToDay(s string) string {
	if len(s) < 10 {
		return ""
	}
	return strings.TrimSpace(s[:10])
}

// alreadyRequestedMovieIDs is the set of Movies TMDB ids the operator has
// already tracked or already requested — what the Upcoming calendar's
// click-to-request affordance is suppressed on. Sourced from the tracked
// library plus the grab log, the same two sources requestsHandler aggregates,
// and NEVER from Prowlarr (CLAUDE.md: a "do I already own this" signal on a
// browse surface comes from the tracked library, never an indexer).
//
// The grabs side MUST be filtered. grabsStore.List returns every status, so a
// naive membership test would mark a movie already-requested on the strength
// of a FAILED grab and permanently suppress the click for a title the operator
// can no longer request by any other means. A grab counts only when it is
// still active (isActiveGrab — which includes PendingRetry, so a held
// pre-release row correctly counts as already-requested) or already Imported.
//
// Ids ≤ 0 are skipped: a tracked row TMDB never matched carries id 0, and
// admitting it would collide with every other unmatched row.
func alreadyRequestedMovieIDs(ctx context.Context, grabsStore *grabs.Store, libStore *library.Store) (map[int]bool, error) {
	out := map[int]bool{}
	tracked, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, err
	}
	for _, it := range tracked {
		if it.TMDBID > 0 {
			out[it.TMDBID] = true
		}
	}
	grabList, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, err
	}
	for _, g := range grabList {
		if g.TMDBID <= 0 {
			continue
		}
		if isActiveGrab(g.Status) || g.Status == grabs.Imported {
			out[g.TMDBID] = true
		}
	}
	return out, nil
}
