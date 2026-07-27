package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

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

// firstNonEmpty returns the first non-empty string in vals, or "" if all are
// empty — the StashDB-first name/image preference the merge cards apply
// (firstNonEmpty(stash.X, tpdb.X)). tpdbrest has an identical unexported
// helper; this is package api's own copy rather than an exported dependency on
// another client package for a two-line utility.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

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

// mergePerformers fuzzy-pairs a TPDB performer page and a StashDB performer page
// into a single merged-row card list (see the ralplan-merge-tpdb-stashbox plan's
// Q1/Q3/Q5). It calls identify.PairPerformersByName (asymmetry-safe mutual-best
// pairing at the tuned 0.6 threshold, single-sourced in internal/identify), then
// for each pair decides collapse-vs-surface-both via identify.NearExactName:
//   - near-exact names → one displayed name (firstNonEmpty(stash.Name, tpdb.Name),
//     StashDB-canonical-first), NamesDiverged=false.
//   - divergent names → surface BOTH (Name=stash.Name, AltName=tpdb.Name,
//     NamesDiverged=true) so a possible false-positive merge is visible to the
//     operator instead of being hidden behind one name.
//
// Paired image is StashDB-first (firstNonEmpty(stash.ImageURL, tpdb.Image)).
// Cards are emitted TPDB-order-first (the spine), with unpaired StashDB
// exclusives appended in StashDB order. An empty (nil) leg degrades to a pure
// passthrough of the other leg's items as unpaired cards — never a panic, never
// treating empty-as-error. This is exactly the steady state once TPDB's
// out-of-range clamp-detection kicks in (BrowsePerformers returns nil past its
// final page): the row keeps scrolling as pure StashDB.
func mergePerformers(tpdb []tpdbrest.Performer, stash []stashbox.Performer) []apidto.MergedPerformerCard {
	tNames := make([]string, len(tpdb))
	for i, p := range tpdb {
		tNames[i] = p.Name
	}
	sNames := make([]string, len(stash))
	for i, p := range stash {
		sNames[i] = p.Name
	}
	// A = TPDB (the spine), B = StashDB.
	pairs := identify.PairPerformersByName(tNames, sNames)
	stashForT := make(map[int]int, len(pairs))
	pairedS := make([]bool, len(stash))
	for _, p := range pairs {
		stashForT[p.AIndex] = p.BIndex
		pairedS[p.BIndex] = true
	}

	out := make([]apidto.MergedPerformerCard, 0, len(tpdb)+len(stash))
	for i, t := range tpdb {
		j, paired := stashForT[i]
		if !paired {
			out = append(out, apidto.MergedPerformerCard{
				Name: t.Name, Image: t.Image, Source: "tpdb", TPDBID: t.ID,
			})
			continue
		}
		s := stash[j]
		card := apidto.MergedPerformerCard{
			Image: firstNonEmpty(s.ImageURL, t.Image), Source: "merged",
			TPDBID: t.ID, StashDBID: s.ID,
		}
		if identify.NearExactName(t.Name, s.Name) {
			card.Name = firstNonEmpty(s.Name, t.Name)
		} else {
			card.Name = s.Name
			card.AltName = t.Name
			card.NamesDiverged = true
		}
		out = append(out, card)
	}
	for j, s := range stash {
		if pairedS[j] {
			continue
		}
		out = append(out, apidto.MergedPerformerCard{
			Name: s.Name, Image: s.ImageURL, Source: "stashdb", StashDBID: s.ID,
		})
	}
	return out
}

// mergeStudios is mergePerformers' studio sibling — same pairing, collapse-vs-
// surface-both, StashDB-first image (firstNonEmpty(stash.ImageURL, tpdb.Image),
// note tpdbrest.Site's field is Image), TPDB-spine ordering, and empty-leg
// passthrough contract. Kept as a parallel sibling function rather than a shared
// generic over the two source types, per the repo's "no premature abstraction /
// sibling functions over a forced-shared path" convention.
func mergeStudios(tpdb []tpdbrest.Site, stash []stashbox.Studio) []apidto.MergedStudioCard {
	tNames := make([]string, len(tpdb))
	for i, s := range tpdb {
		tNames[i] = s.Name
	}
	sNames := make([]string, len(stash))
	for i, s := range stash {
		sNames[i] = s.Name
	}
	pairs := identify.PairStudiosByName(tNames, sNames)
	stashForT := make(map[int]int, len(pairs))
	pairedS := make([]bool, len(stash))
	for _, p := range pairs {
		stashForT[p.AIndex] = p.BIndex
		pairedS[p.BIndex] = true
	}

	out := make([]apidto.MergedStudioCard, 0, len(tpdb)+len(stash))
	for i, t := range tpdb {
		j, paired := stashForT[i]
		if !paired {
			out = append(out, apidto.MergedStudioCard{
				Name: t.Name, Image: t.Image, Source: "tpdb", TPDBID: t.ID,
			})
			continue
		}
		s := stash[j]
		card := apidto.MergedStudioCard{
			Image: firstNonEmpty(s.ImageURL, t.Image), Source: "merged",
			TPDBID: t.ID, StashDBID: s.ID,
		}
		if identify.NearExactName(t.Name, s.Name) {
			card.Name = firstNonEmpty(s.Name, t.Name)
		} else {
			card.Name = s.Name
			card.AltName = t.Name
			card.NamesDiverged = true
		}
		out = append(out, card)
	}
	for j, s := range stash {
		if pairedS[j] {
			continue
		}
		out = append(out, apidto.MergedStudioCard{
			Name: s.Name, Image: s.ImageURL, Source: "stashdb", StashDBID: s.ID,
		})
	}
	return out
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
// REQUIRED (400 when unconfigured, matching every other adult TPDB handler);
// StashDB is optional and degrades to TPDB-only (logged, never a request error)
// when unconfigured or erroring.
//
// CONCURRENCY (Q4 footgun): the TPDB and StashDB legs MUST run concurrently.
// BrowsePerformers runs its own bounded-concurrency image backfill internally,
// so the TPDB leg is already latency-heavy — running the legs sequentially would
// make total latency sum(legs) instead of max(legs). Clients are built BEFORE
// the goroutines (store access isn't concurrency-safe to assume here) and only
// the two upstream fetches run in parallel.
//
// EXHAUSTION: the response is a MergedPerformerPage{Items,HasMore} envelope
// (2026-07-27, not a bare array — see that DTO's doc comment), because this
// handler now runs a post-merge grabbable-availability hard filter
// (filterAvailablePerformers) that can drop items from an already-fetched
// page, breaking the old `batch.length < perPage` bare-array inference the
// original G6/G6b proof relied on. HasMore is computed from PRE-filter
// source lengths (`len(tpdbItems) >= perPage || len(stashItems) >= perPage`,
// captured before the availability filter runs) — see that computation's own
// comment for why it must never be derived from the post-filter merged
// count. Both legs are still fetched at the SAME perPage (clamped up front,
// see mergedBrowsePerPage), which is what makes "either source returned a
// full page" a correct pre-filter "more may exist" signal. (A TPDB page past
// its final one still comes back as an empty slice via BrowsePerformers'
// clamp detection, so it behaves exactly like a genuine empty-200 here.)
func adultPerformersMergedHandler(httpClient *http.Client, connStore *connections.Store, releaseStore *adultnewest.ReleaseStore, feedHealth *adultnewest.FeedHealth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tpdb, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return // adultTPDBClient already wrote the 400/500.
		}
		stash, hasStash, err := adultStashBoxClient(ctx, connStore, httpClient, "stashdb")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		page, perPage := adultPagination(r)
		if perPage <= 0 {
			perPage = mergedBrowsePerPage
		}

		var (
			tpdbItems  []tpdbrest.Performer
			tpdbErr    error
			stashItems []stashbox.Performer
			stashErr   error
			wg         sync.WaitGroup
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tpdbItems, tpdbErr = tpdb.BrowsePerformers(ctx, page, perPage)
		}()
		if hasStash {
			wg.Add(1)
			go func() {
				defer wg.Done()
				stashItems, stashErr = stash.QueryPerformers(ctx, page, perPage)
			}()
		}
		wg.Wait()

		if tpdbErr != nil {
			http.Error(w, tpdbErr.Error(), http.StatusBadGateway)
			return
		}
		if hasStash && stashErr != nil {
			log.Printf("adult discover performers-merged: stashdb query failed, degrading to tpdb-only: %v", stashErr)
			stashItems = nil
		}
		// hasMore is derived from PRE-filter source lengths, captured here before
		// the availability hard filter runs below — BrowsePerformers returns no
		// hasMore flag (unlike BrowseSites, which walks internally and needs one;
		// BrowsePerformers is a single call and clamp-detects past its final
		// page), so a full page from either leg is this row's "more may exist"
		// signal. Deliberately NOT derived from the post-filter merged count —
		// that would reintroduce the exact premature-termination bug the
		// Studios hasMore fix (see filterZeroSceneSites) exists to prevent: a
		// page whose real items all happened to fail the availability filter
		// would look like "the row is exhausted" when it isn't.
		hasMore := len(tpdbItems) >= perPage || len(stashItems) >= perPage

		merged := mergePerformers(tpdbItems, stashItems)
		performerNames, _ := availableNameSets(ctx, releaseStore, feedHealth)
		merged = filterAvailablePerformers(merged, performerNames)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apidto.MergedPerformerPage{
			Items:   merged,
			HasMore: hasMore,
		})
	}
}

// filterZeroSceneSites checks every TPDB site's scene count concurrently
// (bounded to 5 in flight, same rationale as filterByUSRelease's identical
// pattern in discover.go) and drops entries with zero scenes attached.
//
// Why this exists (live-verified 2026-07-26): a user-reported check found two
// /sites entries ("007xvision", "100% Brazil Productions") that TPDB's own
// REST search finds, but that don't appear on TPDB's public website — neither
// belongs to a known junk-storefront network (junkNetworkIDs in
// internal/tpdbrest), so BrowseSites' existing filter doesn't catch them.
// Sampling 24 non-storefront entries found the real signal: every one of 12
// no-image entries had ZERO scenes (100%), vs 8 of 12 has-image entries with
// real scene counts (2 to 2712) and only 4 with zero. A "studio" with no
// scenes has nothing to show and isn't a useful Discover card, so this is a
// direct, principled correctness signal — not a name or image heuristic.
//
// Scoped to ONLY the page-sized slice BrowseSites already returned for this
// request, deliberately NOT the larger internal buffer BrowseSites may walk
// through (see that method's doc) — an explicit, requested cost/precision
// tradeoff: checking every walked candidate would mean up to
// maxSitePagesPerRequest*perPage extra TPDB API calls per page load, the
// same class of per-card fanout this app's CLAUDE.md permanently bans for
// Discover (the "hundreds of concurrent indexer queries" incident). Checking
// only the ~perPage items about to be displayed keeps this bounded to at
// most perPage calls (5 at a time), at the cost of a page occasionally
// returning fewer than perPage items even when the catalog isn't exhausted —
// accepted, the same tradeoff class as BrowseSites' own walk cap.
//
// Fails OPEN on a per-item ScenesBySite error (logs and keeps the item,
// rather than assuming zero scenes) — one transient TPDB hiccup among up to
// perPage per-item calls must not hide a real studio.
func filterZeroSceneSites(ctx context.Context, tpdb *tpdbrest.Client, sites []tpdbrest.Site) []tpdbrest.Site {
	keep := make([]bool, len(sites))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(5)
	for i, s := range sites {
		i, s := i, s
		g.Go(func() error {
			scenes, err := tpdb.ScenesBySite(gctx, s.ID, 1, 1)
			if err != nil {
				log.Printf("adult discover studios-merged: scene-count check failed for site %q, keeping it rather than assuming zero scenes: %v", s.ID, err)
				keep[i] = true
				return nil
			}
			keep[i] = len(scenes) > 0
			return nil
		})
	}
	_ = g.Wait() // every goroutine above always returns nil — see the fail-open note.
	out := make([]tpdbrest.Site, 0, len(sites))
	for i, s := range sites {
		if keep[i] {
			out = append(out, s)
		}
	}
	return out
}

// adultStudiosMergedHandler is adultPerformersMergedHandler's studios sibling —
// identical concurrency, required-TPDB/optional-StashDB, no-truncation, and
// exhaustion-coupling contract (see that handler's doc comment for the full
// rationale), over BrowseSites + QueryStudios merged by mergeStudios.
func adultStudiosMergedHandler(httpClient *http.Client, connStore *connections.Store, releaseStore *adultnewest.ReleaseStore, feedHealth *adultnewest.FeedHealth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tpdb, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return
		}
		stash, hasStash, err := adultStashBoxClient(ctx, connStore, httpClient, "stashdb")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		page, perPage := adultPagination(r)
		if perPage <= 0 {
			perPage = mergedBrowsePerPage
		}

		var (
			tpdbItems   []tpdbrest.Site
			tpdbHasMore bool
			tpdbErr     error
			stashItems  []stashbox.Studio
			stashErr    error
			wg          sync.WaitGroup
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tpdbItems, tpdbHasMore, tpdbErr = tpdb.BrowseSites(ctx, page, perPage)
		}()
		if hasStash {
			wg.Add(1)
			go func() {
				defer wg.Done()
				stashItems, stashErr = stash.QueryStudios(ctx, page, perPage)
			}()
		}
		wg.Wait()

		if tpdbErr != nil {
			http.Error(w, tpdbErr.Error(), http.StatusBadGateway)
			return
		}
		if hasStash && stashErr != nil {
			log.Printf("adult discover studios-merged: stashdb query failed, degrading to tpdb-only: %v", stashErr)
			stashItems = nil
		}
		tpdbItems = filterZeroSceneSites(ctx, tpdb, tpdbItems)
		// hasMore is sourced from tpdbHasMore (computed by BrowseSites BEFORE
		// filterZeroSceneSites ran — see that method's doc) OR'd with StashDB's
		// own length-based signal (StashDB has no junk filter, so "a full page
		// came back" is already a valid "more may exist" signal for that leg —
		// both legs are fetched at the SAME clamped perPage above, which this
		// comparison depends on). Deliberately NOT inferred from
		// len(mergeStudios(...)) or len(tpdbItems) post-filter: either would
		// reintroduce the exact premature-termination bug filterZeroSceneSites'
		// own doc comment warns about — a page whose real items all happened to
		// be zero-scene would look like "the row is exhausted" when it isn't.
		hasMore := tpdbHasMore || len(stashItems) >= perPage

		merged := mergeStudios(tpdbItems, stashItems)
		// Layer 3: the grabbable-availability hard filter, applied POST-merge
		// (after layers 1-2 above, which already ran pre-merge on the TPDB leg
		// only). This layer is source-agnostic — it also catches
		// StashDB-exclusive studio cards layers 1-2 never see. See
		// filterAvailableStudios' doc for the empty-pool fail-open floor and
		// why this can never perturb hasMore (already computed above, from
		// pre-any-filter source signals).
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
