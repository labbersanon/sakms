package api

import (
	"context"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/webhooks"
)

// adultSearchPoolPageSize is a local copy of adultnewest's unexported pool page
// size (defaultResolvePerPage = 20): a page whose pool result reaches this count
// means another pool page may exist, so HasMore is offered. Kept as a package
// constant here since the adultnewest const isn't exported (the same local-copy
// pattern newestScenesOutboundTimeout uses for main's outboundTimeout).
const adultSearchPoolPageSize = 20

// adultSearchMaxBoxIdentify caps how many still-unmatched Prowlarr raw releases
// the Adult catalog Search sends to a box text-search on submit — the bound that
// keeps external identification fan-out O(cap), never O(result-set), honoring the
// "one bounded action" invariant (no unbounded per-release box/AI lookup for a
// whole Prowlarr result set). Pool-local title matches run first and cost ZERO
// external calls; only raws matching no already-identified pool scene consume
// this budget, and any beyond it — or that still don't identify — are DROPPED,
// never shown raw (catalog-first has no unmatched-card fallback). A var (not
// const) purely so tests can tighten it, same rationale as
// newestScenesOutboundTimeout; nothing mutates it in production.
var adultSearchMaxBoxIdentify = 12

// adultSearchHandler backs the Adult catalog-first manual Search
// (GET /api/modes/adult/search?q=&page=N) — the Adult-only, one-shot sibling of
// the Movies/Series two-step catalog→click flow. It is mounted on a CONCRETE
// path ("adult", not "{mode}"), so Go's ServeMux prefers it over the generic
// GET /api/modes/{mode}/search wildcard for Adult specifically (the same
// concrete-shadows-wildcard pattern GET /api/modes/adult/discover already uses
// against GET /api/modes/{mode}/discover). Movies/Series keep using the generic
// searchHandler unchanged.
//
// On submit (page<=1) it does, together in one request:
//   - (a) an RSS-pool query (releaseStore.SearchScenes) — zero external calls,
//     the existing D4b Adult search-bar pool search, reused as-is; and
//   - (b) exactly ONE bounded Prowlarr search for the query (mirroring the Show
//     More page>1 mechanics: a dedicated *http.Client, its own context deadline,
//     the adultAutoGrabCategory), then
//   - groups the combined pool+Prowlarr results into one card per identified
//     scene, each card carrying its release variants INLINE so the click needs
//     no second network call.
//
// Pagination (page>1) pages the RSS POOL only and fires ZERO further Prowlarr —
// one Prowlarr call per one explicit operator action, the action being the
// submit. StashDB/TPDB/FansDB are used ONLY to identify/group the RSS+Prowlarr
// results (via sess.Identify, the same handle Show More uses), NEVER as the
// primary title lookup — the settled D4b decision is not reversed.
//
// When usenet_autograb_enabled is on, the deduped Prowlarr releases go to the
// SHARED runToggleGatedSearch instead of to groupAdultSearchScenes, and the
// response is an outcome (apidto.SearchAutoGrabOutcome) — the same shape and
// the same function Movies/Series use, never a second copy of that branch.
// Three Adult-specific constraints hold on that branch:
//   - Still exactly ONE Prowlarr call per submit. RunAutoGrab is handed the
//     releases already fetched here; it must not search again.
//   - Only the Prowlarr releases are scored. The RSS pool's direct-grab feed
//     enclosures are NOT candidates — they already have their own operator
//     route through autoGrabHandler's direct-grab branch, and excluding them is
//     what keeps the shared function mode-agnostic.
//   - HasMore is meaningless on that branch (an outcome has no page 2), so no
//     pagination path hangs off it. The page>1 pool-only branch is untouched
//     under both toggle states: it fires zero Prowlarr calls, so it is never a
//     candidate source.
func adultSearchHandler(connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, releaseStore *adultnewest.ReleaseStore, feedHealth *adultnewest.FeedHealth, dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store, whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		query := r.URL.Query().Get("q")
		if query == "" {
			http.Error(w, "q query parameter is required", http.StatusBadRequest)
			return
		}
		page := parseNewestPage(r.URL.Query().Get("page"))

		// (a) RSS pool — zero external calls, on every page. A lookup error
		// degrades to an empty pool (best-effort) rather than failing search,
		// mirroring the entity-scenes page-1 contract.
		poolMatches, err := releaseStore.SearchScenes(ctx, query, page)
		if err != nil {
			log.Printf("adult search: pool query for q=%q page=%d (returning empty pool): %v", query, page, err)
			poolMatches = nil
		}
		now := time.Now()
		poolScenes := poolMatchesToSearchScenes(poolMatches, feedHealth, now)
		hasMore := len(poolMatches) >= adultSearchPoolPageSize

		if page > 1 {
			// Pagination pages the POOL only — ZERO further Prowlarr.
			writeJSON(w, apidto.AdultSearchScenesPage{Items: poolScenes, HasMore: hasMore})
			return
		}

		// (b) Submit (page 1): exactly ONE bounded Prowlarr search. Its own
		// *http.Client (not the shared one) whose Timeout bounds the whole round
		// trip, plus a matching context deadline — see the identical reasoning in
		// adultdiscover_newest_scenes.go's Show More path.
		searchClient := &http.Client{Timeout: newestScenesOutboundTimeout}
		// dl, not nil: the toggle-ON branch dispatches, and
		// dispatchToDownloadClient rejects a torrent dispatch on a nil
		// Session.Downloader. searchClient stays the outbound client — its
		// Timeout is what bounds this route's one Prowlarr round trip.
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, searchClient, dl, mode.Adult)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.Prowlarr == nil {
			http.Error(w, "prowlarr isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		tctx, cancel := context.WithTimeout(ctx, newestScenesOutboundTimeout)
		defer cancel()

		releases, err := sess.Prowlarr.Search(tctx, normalizeAdultQuery(query), []int{adultAutoGrabCategory})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		// Collapse true cross-posts (one release on several indexers) up front,
		// keyed on DownloadURL OR normalized RAW title — the SAME dedupeReleases
		// the Movies/Series picker and Show More use, so distinct quality variants
		// stay individually selectable within a scene (searchdedup.go unchanged).
		releases = dedupeReleases(releases, func(rel prowlarr.Release) releaseDedupKey {
			return releaseDedupKey{
				downloadURL:     rel.DownloadURL,
				normalizedTitle: normalizeAdultQuery(rel.Title),
				seeders:         rel.Seeders,
			}
		})

		// Toggle ON: score the deduped Prowlarr releases and answer with the
		// outcome. Read here — after the pool query, the one Prowlarr search and
		// dedupeReleases, but BEFORE groupAdultSearchScenes — the point at which
		// the candidate list exists and the expensive identification/grouping
		// work has not yet been paid for. That work is skipped entirely: no
		// grouping, no boxIdentifyRelease fan-out, no scene cards, because there
		// is nothing left to render once the toggle has picked.
		autoGrab, err := settingsStore.GetBool(tctx, usenetAutoGrabEnabledKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if autoGrab {
			outcome, status, err := runToggleGatedSearch(tctx,
				AutoGrabDeps{SettingsStore: settingsStore, NZB: nzb, GrabsStore: grabsStore, Webhooks: whStore},
				sess, searchGrabIdentity{Mode: mode.Adult, Title: query}, releases)
			if err != nil {
				http.Error(w, err.Error(), status)
				return
			}
			writeJSON(w, outcome)
			return
		}

		prefs, err := searchQualityProfile(tctx, settingsStore, mode.Adult)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		items := groupAdultSearchScenes(tctx, sess.Identify, poolScenes, releases, prefs, now)
		writeJSON(w, apidto.AdultSearchScenesPage{Items: items, HasMore: hasMore})
	}
}

// poolMatchesToSearchScenes gates each pooled scene through the D5 visibility
// rule (browse-confirmed OR feed-fresh, exactly as writePooledScenes does) and
// maps the survivors onto AdultSearchScene cards. A pool scene carries its own
// direct-grab feed enclosure as its one inline release when the feed is
// currently fresh (poolReleaseToDiscoverItem sets DownloadURL only then, via
// FeedHealth.DirectGrabURL); otherwise its Releases start empty and get any
// Prowlarr variants that match its title attached later. The grab-bearing fields
// come straight off the pool row — never mutated (grab-safety).
func poolMatchesToSearchScenes(matches []adultnewest.MatchedRelease, feedHealth *adultnewest.FeedHealth, now time.Time) []apidto.AdultSearchScene {
	out := make([]apidto.AdultSearchScene, 0, len(matches))
	for _, m := range matches {
		if !feedHealth.Available(m.BrowseConfirmed, m.DownloadProtocol, m.FeedID, time.Unix(m.LastConfirmedSeen, 0), now) {
			continue
		}
		scene := poolReleaseToDiscoverItem(m, feedHealth, now)
		releases := []apidto.SearchReleaseResult{}
		if scene.DownloadURL != "" {
			releases = append(releases, apidto.SearchReleaseResult{
				Title:       scene.ReleaseTitle,
				Protocol:    scene.Protocol,
				Size:        scene.SizeBytes,
				DownloadURL: scene.DownloadURL,
			})
		}
		out = append(out, apidto.AdultSearchScene{Scene: scene, Releases: releases})
	}
	return out
}

// groupAdultSearchScenes folds the one Prowlarr search's raw releases into the
// pool scenes, producing one card per identified scene with its release variants
// inline. Identification is bounded and reuses this codebase's own primitives —
// no forked matcher:
//
//  1. LOCAL, zero-external: every raw title is matched against the already-
//     identified pool scenes' titles via identify.MatchReleaseTitlesToCatalog
//     (the same normalizeForSearch + maxSimilarity + 0.4-threshold primitive
//     EnrichNewestScenes uses); a match attaches the raw as a release variant of
//     that pool scene.
//  2. BOUNDED box lookup: each still-unmatched raw, up to adultSearchMaxBoxIdentify
//     of them, is identified via sess.Identify's box text-search
//     (SearchTPDB/SearchStashBox); a confident match either merges into an
//     existing scene sharing its identified title or opens a new scene card.
//     Raws beyond the budget, or that still don't identify, are DROPPED — never
//     shown raw (catalog-first has no unmatched-card fallback).
//
// Quality variants of one scene therefore collapse to a single card (they attach
// to the same scene identity), while each variant stays individually selectable
// in that card's Releases — the two deliberately-opposite dedup layers. Distinct
// pool entities that merely share a title are NEVER merged (each stays its own
// card, same protection dedupeAdultPoolItems gives page-1). Releases within a
// card are score-sorted, highest first.
func groupAdultSearchScenes(ctx context.Context, id *identify.Identifier, poolScenes []apidto.AdultSearchScene, releases []prowlarr.Release, prefs release.Profile, now time.Time) []apidto.AdultSearchScene {
	scenes := make([]apidto.AdultSearchScene, len(poolScenes))
	copy(scenes, poolScenes)

	// keyToIdx routes a box-identified raw to a scene sharing its normalized
	// title (first-seen wins, so two distinct pool entities with the same title
	// are never merged into each other). catalog + mrToScene back the local pool
	// title match: each pool scene contributes one *MatchResult whose pointer
	// maps back to its scene index.
	keyToIdx := make(map[string]int, len(scenes))
	catalog := make([]*identify.MatchResult, 0, len(scenes))
	mrToScene := make(map[*identify.MatchResult]int, len(scenes))
	for i := range scenes {
		key := normalizeAdultQuery(scenes[i].Scene.Title)
		if key == "" {
			continue
		}
		if _, ok := keyToIdx[key]; !ok {
			keyToIdx[key] = i
		}
		mr := &identify.MatchResult{Title: scenes[i].Scene.Title}
		catalog = append(catalog, mr)
		mrToScene[mr] = i
	}

	rawTitles := make([]string, len(releases))
	for i, rel := range releases {
		rawTitles[i] = rel.Title
	}

	handled := make([]bool, len(releases))

	// 1. Local pool-title match — zero external calls.
	if len(catalog) > 0 {
		for ri, mr := range identify.MatchReleaseTitlesToCatalog(rawTitles, catalog) {
			if si, ok := mrToScene[mr]; ok {
				scenes[si].Releases = append(scenes[si].Releases, prowlarrToSearchRelease(releases[ri], prefs, now))
				handled[ri] = true
			}
		}
	}

	// 2. Bounded box lookup for the rest.
	budget := adultSearchMaxBoxIdentify
	for ri := range releases {
		if handled[ri] {
			continue
		}
		if budget <= 0 {
			break // bounded external fan-out: drop the remainder
		}
		budget--
		mr := boxIdentifyRelease(ctx, id, releases[ri].Title)
		if mr == nil || mr.Title == "" {
			continue // dropped — no unmatched-card fallback
		}
		key := normalizeAdultQuery(mr.Title)
		if key == "" {
			continue
		}
		si, ok := keyToIdx[key]
		if !ok {
			scenes = append(scenes, apidto.AdultSearchScene{
				Scene:    matchResultToDiscoverItem(mr),
				Releases: []apidto.SearchReleaseResult{},
			})
			si = len(scenes) - 1
			keyToIdx[key] = si
		}
		scenes[si].Releases = append(scenes[si].Releases, prowlarrToSearchRelease(releases[ri], prefs, now))
		handled[ri] = true
	}

	// Score-sort each card's release list, highest first (mirrors searchHandler).
	for i := range scenes {
		rs := scenes[i].Releases
		sort.SliceStable(rs, func(a, b int) bool { return rs[a].Score > rs[b].Score })
	}
	return scenes
}

// boxIdentifyRelease identifies one raw Prowlarr release title against the
// configured stash-boxes via sess.Identify's box text-search primitives,
// returning the first confident scene match (TPDB, then StashDB, then FansDB) or
// nil. Each box's client is nil-tolerant (an unconfigured box returns nil,nil),
// so this naturally probes only what's configured. Errors are swallowed
// (best-effort identification — a box hiccup drops the raw, never fails search).
func boxIdentifyRelease(ctx context.Context, id *identify.Identifier, rawTitle string) *identify.MatchResult {
	if id == nil || id.Boxes == nil {
		return nil
	}
	if mr, err := id.Boxes.SearchTPDB(ctx, rawTitle, ""); err == nil && mr != nil {
		return mr
	}
	for _, box := range []string{"stashdb", "fansdb"} {
		if mr, err := id.Boxes.SearchStashBox(ctx, box, rawTitle, ""); err == nil && mr != nil {
			return mr
		}
	}
	return nil
}

// prowlarrToSearchRelease maps one raw Prowlarr release to the release-picker
// wire item, scored against the mode's quality prefs exactly as searchHandler
// scores. The four grab-bearing fields (Title/Protocol/Size/DownloadURL) are
// copied verbatim — never mutated post-fetch (grab-safety).
func prowlarrToSearchRelease(rel prowlarr.Release, prefs release.Profile, now time.Time) apidto.SearchReleaseResult {
	info := release.Parse(rel.Title)
	return apidto.SearchReleaseResult{
		GUID: rel.GUID, Title: rel.Title, Indexer: rel.Indexer,
		Protocol: string(rel.Protocol), Size: rel.Size, Seeders: rel.Seeders,
		DownloadURL: rel.DownloadURL, PublishDate: rel.PublishDate,
		Score: release.ScoreCandidate(release.Candidate{
			Info: info, Protocol: string(rel.Protocol), Seeders: rel.Seeders,
			PublishDate: rel.PublishDate, IndexerFlags: rel.IndexerFlags,
		}, prefs, now),
	}
}

// matchResultToDiscoverItem maps a box identification result onto the Adult
// scene card shape for a Prowlarr-only scene (one with no pool row). Genres/
// Performers are split from the MatchResult's comma-joined strings (same
// splitCommaTrim the Show More enrichment uses); the grab-bearing fields stay
// on the carried release, not the scene.
func matchResultToDiscoverItem(mr *identify.MatchResult) apidto.AdultDiscoverItem {
	return apidto.AdultDiscoverItem{
		Title:           mr.Title,
		Studio:          mr.Studio,
		Date:            mr.Date,
		Image:           mr.Image,
		DurationSeconds: mr.RuntimeSeconds,
		Source:          mr.Box,
		Genres:          splitCommaTrim(mr.Tags),
		Performers:      splitCommaTrim(mr.Performers),
	}
}
