package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/adultmerge"
	"github.com/labbersanon/sakms/internal/adultmergecache"
	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// mergedBrowsePerPage is the fallback page size the merged Performers/Studios
// handlers clamp a missing/zero perPage to BEFORE fetching either source. It is
// load-bearing for the Q1 exhaustion proof's coupling condition (C): both the
// TPDB and StashDB legs MUST be fetched at the SAME perPage, or the "merged
// batch drops below perPage only when both sources are drained" invariant the
// frontend's `batch.length < perPage` exhaustion check relies on breaks. If the
// caller omits perPage, adultPagination returns 0 and each client would
// otherwise clamp to its OWN internal defaultBrowsePerPage (today both 20, but
// that identity is not a contract) — clamping here to one value first makes the
// G6b "perPage flows identically to both source fetches" guarantee literally
// true in code rather than an assumption about two packages' private defaults.
const mergedBrowsePerPage = 20

// hashSetHasAny reports whether any hash in candidates is present in set — the
// phash-intersection test the merged scene drill-down uses to drop a StashDB
// scene TPDB already carries. Reconstructed here (with dedupeStashScenesByTPDBHashes
// below) from the phash-merge pattern the removed adultDiscoverMergedRecentHandler
// used before commit 08a15f1 re-pointed it onto the identified-available pool.
func hashSetHasAny(set map[string]bool, candidates []string) bool {
	for _, h := range candidates {
		if set[h] {
			return true
		}
	}
	return false
}

// dedupeStashScenesByTPDBHashes builds the merged, phash-deduped scene list a
// merged performer/studio drill-down returns: every TPDB scene is emitted
// (Source:"tpdb"), then every StashDB scene whose PHashes do NOT intersect the
// TPDB scenes' Hashes (Source:"stashdb") — a StashDB scene sharing a phash TPDB
// already carries is a duplicate and dropped. Source is stamped on EVERY emitted
// scene so the frontend's existing per-scene source badge renders for free (no
// component change — AdultCard/EntityCard/DetailPopup already read Source).
// Reconstructed from the removed adultDiscoverMergedRecentHandler's phash-merge
// pattern (git show 08a15f1^:internal/api/adultdiscover_stashbox.go).
//
// A TPDB scene with no Hashes contributes nothing to the dedup set, so it can
// never mask a StashDB scene — the documented property of the old handler.
func dedupeStashScenesByTPDBHashes(tpdb []tpdbrest.Scene, stash []stashbox.Scene) []adultScene {
	out := make([]adultScene, 0, len(tpdb)+len(stash))
	tpdbHashes := map[string]bool{}
	for _, s := range tpdb {
		for _, h := range s.Hashes {
			tpdbHashes[h] = true
		}
		out = append(out, tpdbSceneToAdultScene(s)) // stamps Source:"tpdb"
	}
	for _, s := range stash {
		if hashSetHasAny(tpdbHashes, s.PHashes) {
			continue // a phash TPDB already carries → duplicate, drop it.
		}
		out = append(out, adultScene{
			ID:              s.ID,
			Title:           s.Title,
			Studio:          s.StudioName,
			Date:            s.ReleaseDate,
			Image:           s.ImageURL,
			DurationSeconds: s.Duration,
			Source:          "stashdb",
			// Rating stays 0 — stash-box has no numeric rating field.
		})
	}
	return out
}

// availableNameSets rolls up the adultnewest pool's Scene/Movie rows into two
// normalized "currently grabbable" name sets (performers, studios), gated by
// FeedHealth.Available — the grabbable-availability signal behind
// filterAvailablePerformers/filterAvailableStudios below. See
// .omc/plans/ralplan-adult-discover-performers-availability-gate.md's
// Decision DB for the full rationale and the operator's explicit
// hard-filter decision this implements.
//
// Excludes Performer/Studio pool rows deliberately (ScenePoolCredits already
// only reads Scene/Movie rows) — those are written BrowseConfirmed=true
// unconditionally by the scan job, so including them would bypass the
// FeedHealth gate and turn this into a catalog-presence signal, not a
// grabbable one.
//
// Empty/whitespace-only names are excluded at build time — EntityStudio is
// frequently "" (scenes often lack clean studio attribution) and a blank
// entry would otherwise pollute the set with a value that normalizes to "",
// spuriously matching any card whose own name also normalizes to empty.
//
// Fails OPEN on a pool-read error (logs and returns two empty sets) rather
// than erroring the whole request — a pool-read hiccup is treated the same
// as an empty/not-yet-scanned pool, which the caller's own empty-pool
// fail-open floor already handles correctly (skip filtering, never empty
// the tab over an infrastructure gap).
func availableNameSets(ctx context.Context, releaseStore *adultnewest.ReleaseStore, feedHealth *adultnewest.FeedHealth) (performerNames []string, studioNames []string) {
	credits, err := releaseStore.ScenePoolCredits(ctx)
	if err != nil {
		log.Printf("adult discover: availability pool read failed, availability filtering disabled for this request: %v", err)
		return nil, nil
	}
	now := time.Now()
	for _, c := range credits {
		if !feedHealth.Available(c.BrowseConfirmed, c.FeedID, time.Unix(c.LastConfirmedSeen, 0), now) {
			continue
		}
		for _, p := range c.Performers {
			if strings.TrimSpace(p) != "" {
				performerNames = append(performerNames, p)
			}
		}
		if strings.TrimSpace(c.Studio) != "" {
			studioNames = append(studioNames, c.Studio)
		}
	}
	return performerNames, studioNames
}

// nameAvailable reports whether name near-exact-matches any entry in the
// available-name set — the O(cards×poolNames) pairwise comparison the plan
// authorizes at this scale (dozens-hundreds of pool names, ~20 cards/page).
// An empty name (e.g. a non-diverged card's AltName) never matches.
func nameAvailable(name string, set []string) bool {
	if name == "" {
		return false
	}
	for _, avail := range set {
		if identify.NearExactName(name, avail) {
			return true
		}
	}
	return false
}

// cardAvailable tests BOTH name and altName against set — required for
// diverged merged cards. A merged card's Name is StashDB-preferred and
// AltName (when NamesDiverged) holds the TPDB spelling
// (mergePerformers/mergeStudios above), but the pool stores a scene's credit
// under whichever box's identify pass actually matched it — which for a
// diverged card can be the TPDB spelling living in AltName, not the StashDB
// spelling in Name. Testing Name alone would systematically false-negative
// on diverged cards and silently hide them.
func cardAvailable(name, altName string, set []string) bool {
	return nameAvailable(name, set) || nameAvailable(altName, set)
}

// filterAvailablePerformers hard-filters merged performer cards to those
// present in the grabbable-availability name set — per the operator's
// explicit decision ("performers in studios should be filters for content
// available in feeds. I'll accept the potential bad matches for now" — see
// the plan's "Operator decision & accepted risk" section). Fails OPEN
// (available is empty here, so every card is kept) in three cases: the pool
// was never scanned or adultnewest is disabled; a pool-read error
// (availableNameSets logs and returns nil); or a populated pool whose every
// scene row is feed-only with every feed currently stale/unhealthy (no row
// is ever BrowseConfirmed, and FeedHealth.Available rejects all of them, so
// availableNameSets' rollup comes back empty even though scans did run). An
// infrastructure/feed-health gap must never silently empty the row; only a
// populated pool's genuine sampling gaps are allowed to hide cards, which is
// exactly the risk the operator accepted. Emits a server-log-only
// kept/dropped diagnostic line — never surfaced to the DTO/UI — the only
// signal that can distinguish "filter engaging against a genuinely sparse
// pool" from "filter bug" once every user-facing signal (badge, count) was
// deliberately dropped from this design.
func filterAvailablePerformers(cards []apidto.MergedPerformerCard, available []string) []apidto.MergedPerformerCard {
	if len(available) == 0 {
		return cards
	}
	out := make([]apidto.MergedPerformerCard, 0, len(cards))
	for _, c := range cards {
		if cardAvailable(c.Name, c.AltName, available) {
			out = append(out, c)
		}
	}
	log.Printf("adult discover performers-merged: availability filter kept %d, dropped %d of %d", len(out), len(cards)-len(out), len(cards))
	return out
}

// filterAvailableStudios is filterAvailablePerformers' studio sibling — same
// signal, same empty-pool fail-open floor, same diagnostic logging.
func filterAvailableStudios(cards []apidto.MergedStudioCard, available []string) []apidto.MergedStudioCard {
	if len(available) == 0 {
		return cards
	}
	out := make([]apidto.MergedStudioCard, 0, len(cards))
	for _, c := range cards {
		if cardAvailable(c.Name, c.AltName, available) {
			out = append(out, c)
		}
	}
	log.Printf("adult discover studios-merged: availability filter kept %d, dropped %d of %d", len(out), len(cards)-len(out), len(cards))
	return out
}

// adultPerformersMergedHandler backs Adult Discover's merged Performers row —
// TPDB + StashDB performer pages fuzzy-deduped into one card list. TPDB is
// REQUIRED on the live path (400 when unconfigured, matching every other adult
// TPDB handler); StashDB is optional and degrades to TPDB-only (logged, never a
// request error) when unconfigured or erroring. The concurrent fetch, fuzzy
// merge, and PRE-filter HasMore derivation are single-sourced in
// adultmerge.FetchAndMergePerformers, shared with the background precompute job.
//
// READ CACHE: for the leading pages (page <= adultmergecache.PrecomputePages) at
// the precomputed page size (perPage == mergedBrowsePerPage), the merged card
// list is served from the background precompute cache instead of being computed
// live. The cache stores the PRE-availability-filter list, so the availability
// hard filter (availableNameSets + filterAvailablePerformers, unchanged) is
// applied LIVE at read time against the decoded cards — feed-health flips are
// reflected on the next request with no precompute run. HasMore comes straight
// from the stored pre-filter value, never re-derived from a post-filter count.
// A cache miss, a store/decode error, or an ineligible request (page beyond K,
// or a non-default perPage) all fall through to today's exact live path below —
// never an error, never an empty row.
func adultPerformersMergedHandler(httpClient *http.Client, connStore *connections.Store, releaseStore *adultnewest.ReleaseStore, feedHealth *adultnewest.FeedHealth, cacheStore *adultmergecache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		page, perPage := adultPagination(r)
		if perPage <= 0 {
			perPage = mergedBrowsePerPage
		}

		// A nil cacheStore means no cache is wired (production always wires one;
		// some tests don't) — treat it like a miss and serve live, same fail-open
		// contract as a store error below.
		if cacheStore != nil && page <= adultmergecache.PrecomputePages && perPage == mergedBrowsePerPage {
			if cached, found, err := cacheStore.Get(ctx, "performers", page, perPage); err != nil {
				log.Printf("adult discover performers-merged: cache read failed, falling back to live merge: %v", err)
			} else if found {
				var cards []apidto.MergedPerformerCard
				if err := json.Unmarshal(cached.Items, &cards); err != nil {
					log.Printf("adult discover performers-merged: cached payload decode failed, falling back to live merge: %v", err)
				} else {
					performerNames, _ := availableNameSets(ctx, releaseStore, feedHealth)
					cards = filterAvailablePerformers(cards, performerNames)
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(apidto.MergedPerformerPage{
						Items:   cards,
						HasMore: cached.HasMore,
					})
					return
				}
			}
		}

		// Live fallback — today's exact fetch-and-merge behavior.
		tpdb, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return // adultTPDBClient already wrote the 400/500.
		}
		stash, hasStash, err := adultStashBoxClient(ctx, connStore, httpClient, "stashdb")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		merged, hasMore, err := adultmerge.FetchAndMergePerformers(ctx, tpdb, stash, hasStash, page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		performerNames, _ := availableNameSets(ctx, releaseStore, feedHealth)
		merged = filterAvailablePerformers(merged, performerNames)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apidto.MergedPerformerPage{
			Items:   merged,
			HasMore: hasMore,
		})
	}
}

// adultStudiosMergedHandler is adultPerformersMergedHandler's studios sibling —
// same required-TPDB/optional-StashDB contract, same cache-read/live-fallback
// shape (see that handler's doc comment for the full rationale). Its fetch+merge
// core is single-sourced in adultmerge.FetchAndMergeStudios, which additionally
// runs FilterZeroSceneSites over the TPDB leg and sources HasMore from
// BrowseSites' explicit tpdbHasMore. Layer 3 (the grabbable-availability hard
// filter, filterAvailableStudios) is applied LIVE at read time on both the cache
// and live paths — source-agnostic, and it can never perturb the stored
// pre-filter HasMore.
func adultStudiosMergedHandler(httpClient *http.Client, connStore *connections.Store, releaseStore *adultnewest.ReleaseStore, feedHealth *adultnewest.FeedHealth, cacheStore *adultmergecache.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		page, perPage := adultPagination(r)
		if perPage <= 0 {
			perPage = mergedBrowsePerPage
		}

		if cacheStore != nil && page <= adultmergecache.PrecomputePages && perPage == mergedBrowsePerPage {
			if cached, found, err := cacheStore.Get(ctx, "studios", page, perPage); err != nil {
				log.Printf("adult discover studios-merged: cache read failed, falling back to live merge: %v", err)
			} else if found {
				var cards []apidto.MergedStudioCard
				if err := json.Unmarshal(cached.Items, &cards); err != nil {
					log.Printf("adult discover studios-merged: cached payload decode failed, falling back to live merge: %v", err)
				} else {
					_, studioNames := availableNameSets(ctx, releaseStore, feedHealth)
					cards = filterAvailableStudios(cards, studioNames)
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(apidto.MergedStudioPage{
						Items:   cards,
						HasMore: cached.HasMore,
					})
					return
				}
			}
		}

		// Live fallback — today's exact fetch-and-merge behavior.
		tpdb, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return
		}
		stash, hasStash, err := adultStashBoxClient(ctx, connStore, httpClient, "stashdb")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		merged, hasMore, err := adultmerge.FetchAndMergeStudios(ctx, tpdb, stash, hasStash, page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_, studioNames := availableNameSets(ctx, releaseStore, feedHealth)
		merged = filterAvailableStudios(merged, studioNames)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apidto.MergedStudioPage{
			Items:   merged,
			HasMore: hasMore,
		})
	}
}

// adultPerformerMergedScenesHandler is the merged performer drill-down — a
// phash-deduped TPDB+StashDB scene list for one merged card, routed by the pair
// of ids the card carries (tpdbId / stashdbId query params, both individually
// optional but at least one REQUIRED — 400 if both absent). Browse-time pairing
// is authoritative: this never re-runs a fuzzy match, it fetches directly by the
// ids. Each id's leg is fetched only when that id is present, concurrently, and
// merged via dedupeStashScenesByTPDBHashes (which stamps per-scene Source).
//
// The TPDB leg (when tpdbId is present) uses adultTPDBClient, which 400s if TPDB
// is unconfigured — a deliberate choice, not an oversight: a card carrying a
// tpdbId implies TPDB was configured at browse time, so its absence at drill
// time is a real misconfiguration worth surfacing. A TPDB fetch error is fatal
// (502, spine-source contract); a StashDB error degrades to the TPDB-only
// result (logged), never failing the request.
func adultPerformerMergedScenesHandler(httpClient *http.Client, connStore *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mergedScenes(w, r, httpClient, connStore,
			func(c *tpdbrest.Client, id string, page, perPage int) ([]tpdbrest.Scene, error) {
				return c.ScenesByPerformer(r.Context(), id, page, perPage)
			},
			func(c *stashbox.Client, id string, page, perPage int) ([]stashbox.Scene, error) {
				return c.QueryScenesByPerformer(r.Context(), id, page, perPage)
			},
		)
	}
}

// adultStudioMergedScenesHandler is adultPerformerMergedScenesHandler's studio
// sibling — same pair-of-ids routing, concurrency, and phash-dedup contract,
// over ScenesBySite + QueryScenesByStudio.
func adultStudioMergedScenesHandler(httpClient *http.Client, connStore *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mergedScenes(w, r, httpClient, connStore,
			func(c *tpdbrest.Client, id string, page, perPage int) ([]tpdbrest.Scene, error) {
				return c.ScenesBySite(r.Context(), id, page, perPage)
			},
			func(c *stashbox.Client, id string, page, perPage int) ([]stashbox.Scene, error) {
				return c.QueryScenesByStudio(r.Context(), id, page, perPage)
			},
		)
	}
}

// mergedScenes is the shared body of the two merged drill-down handlers — the
// only difference between the performer and studio drill is which client method
// fetches each source's scenes, injected as the two fetch closures. Kept here so
// the id-parsing, both-absent 400, concurrent fetch, TPDB-fatal/StashDB-degrade
// error handling, and phash-dedup encode are single-sourced across both drills.
func mergedScenes(
	w http.ResponseWriter, r *http.Request, httpClient *http.Client, connStore *connections.Store,
	fetchTPDB func(*tpdbrest.Client, string, int, int) ([]tpdbrest.Scene, error),
	fetchStash func(*stashbox.Client, string, int, int) ([]stashbox.Scene, error),
) {
	ctx := r.Context()
	tpdbID := r.URL.Query().Get("tpdbId")
	stashID := r.URL.Query().Get("stashdbId")
	if tpdbID == "" && stashID == "" {
		http.Error(w, "at least one of tpdbId or stashdbId is required", http.StatusBadRequest)
		return
	}

	var tpdb *tpdbrest.Client
	if tpdbID != "" {
		c, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return // adultTPDBClient already wrote the 400/500.
		}
		tpdb = c
	}
	var stash *stashbox.Client
	if stashID != "" {
		c, ok, err := adultStashBoxClient(ctx, connStore, httpClient, "stashdb")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ok {
			stash = c
		}
	}

	page, perPage := adultPagination(r)
	var (
		tScenes []tpdbrest.Scene
		tErr    error
		sScenes []stashbox.Scene
		sErr    error
		wg      sync.WaitGroup
	)
	if tpdb != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tScenes, tErr = fetchTPDB(tpdb, tpdbID, page, perPage)
		}()
	}
	if stash != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sScenes, sErr = fetchStash(stash, stashID, page, perPage)
		}()
	}
	wg.Wait()

	if tErr != nil {
		http.Error(w, tErr.Error(), http.StatusBadGateway)
		return
	}
	if sErr != nil {
		log.Printf("adult discover merged-scenes: stashdb query failed, degrading to tpdb-only: %v", sErr)
		sScenes = nil
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dedupeStashScenesByTPDBHashes(tScenes, sScenes))
}
