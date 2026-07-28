// Package adultmerge holds the shared TPDB+StashDB fetch-and-merge core for
// Adult Discover's merged Performers/Studios rows. It was extracted out of
// package api so that BOTH the live HTTP handlers (internal/api's
// adultPerformersMergedHandler/adultStudiosMergedHandler) AND the background
// precompute job (internal/adultmergecache) can call the exact same
// fetch+merge+HasMore logic without it drifting between the two paths, and
// without an import cycle: package api imports internal/adultmergecache (for
// its Store type in NewMux), so internal/adultmergecache cannot import package
// api back — the merge core therefore lives here, in a lower-level package both
// can import.
//
// Scope is deliberately narrow: this package owns ONLY the concurrent source
// fetch, the fuzzy-name merge, and the pre-filter HasMore derivation. It has no
// HTTP concern, no cache/Store concern, and — critically — no availability
// filter: the availability hard filter (availableNameSets + filterAvailable*)
// stays in package api and is applied LIVE at read time against whatever this
// package produced, because feed-health changes independently of the merge
// data. What this package returns is the PRE-filter card list; the caller
// filters.
//
// It follows the repo's "no premature abstraction / sibling functions over a
// forced-shared path" convention: Performers and Studios stay as parallel
// sibling functions (MergePerformers/MergeStudios, FetchAndMergePerformers/
// FetchAndMergeStudios), never unified into one cross-row-type helper — the two
// pipelines genuinely differ (Studios has FilterZeroSceneSites and sources
// HasMore from BrowseSites' explicit flag; Performers derives HasMore from page
// length). Only what is byte-for-byte identical is shared. This package touches
// TPDB/StashDB only — it imports no prowlarr symbol, honoring Discover's
// "never queries Prowlarr" rule.
package adultmerge

import (
	"context"
	"log"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// firstNonEmpty returns the first non-empty string in vals, or "" if all are
// empty — the StashDB-first name/image preference the merge cards apply
// (firstNonEmpty(stash.X, tpdb.X)). tpdbrest has an identical unexported
// helper; this is this package's own copy rather than an exported dependency on
// another client package for a two-line utility.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// normalizeGender maps a raw upstream gender token (TPDB extras.gender free
// string, or StashDB GenderEnum name) to the only two values the gender-split
// Performers row surfaces: "female" or "male". Everything else — unset, empty,
// "transgender_female", "transgender_male", "intersex", "non_binary",
// "non-binary", "trans female", "unknown", or any unrecognized token —
// normalizes to "" and is EXCLUDED from both sections by design (documented
// simplification, not a silent guess).
//
// EXACT-MATCH after lower+trim ONLY. Never substring/Contains: "TRANSGENDER_FEMALE"
// CONTAINS "female", so a Contains match would wrongly bucket a trans-female
// performer as female. Exact match is the whole point — fail-safe default:
// any non-exact value is excluded (hidden), never mis-bucketed. The upstream
// value space is documentation-modeled, not live-verified; excluding by default
// makes new/odd values fail safe.
func normalizeGender(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "female":
		return "female"
	case "male":
		return "male"
	default:
		return ""
	}
}

// MergePerformers fuzzy-pairs a TPDB performer page and a StashDB performer page
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
//
// Every emitted card also carries a normalized Gender ("female"/"male"/"" via
// normalizeGender). For a paired card, StashDB's raw gender wins over TPDB's
// when picking which raw value to normalize (firstNonEmpty(s.Gender, t.Gender)
// then normalizeGender) — e.g. StashDB=NON_BINARY + TPDB=Female normalizes
// the StashDB value first, yielding "" (excluded), honoring StashDB's call
// even though TPDB alone would have normalized to "female". Non-binary,
// transgender, unknown, and any other non-exact-match token are excluded from
// both the female and male sections by design (see normalizeGender) — this is
// a documented simplification of a richer upstream value space, not a bug.
func MergePerformers(tpdb []tpdbrest.Performer, stash []stashbox.Performer) []apidto.MergedPerformerCard {
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
				Gender: normalizeGender(t.Gender),
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
		card.Gender = normalizeGender(firstNonEmpty(s.Gender, t.Gender))
		out = append(out, card)
	}
	for j, s := range stash {
		if pairedS[j] {
			continue
		}
		out = append(out, apidto.MergedPerformerCard{
			Name: s.Name, Image: s.ImageURL, Source: "stashdb", StashDBID: s.ID,
			Gender: normalizeGender(s.Gender),
		})
	}
	return out
}

// MergeStudios is MergePerformers' studio sibling — same pairing, collapse-vs-
// surface-both, StashDB-first image (firstNonEmpty(stash.ImageURL, tpdb.Image),
// note tpdbrest.Site's field is Image), TPDB-spine ordering, and empty-leg
// passthrough contract. Kept as a parallel sibling function rather than a shared
// generic over the two source types, per the repo's "no premature abstraction /
// sibling functions over a forced-shared path" convention.
func MergeStudios(tpdb []tpdbrest.Site, stash []stashbox.Studio) []apidto.MergedStudioCard {
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

// FilterZeroSceneSites checks every TPDB site's scene count concurrently
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
// The ≤5-in-flight bound is load-bearing for the precompute job too: pages are
// precomputed sequentially (never concurrently), so the peak concurrent
// ScenesBySite fan-out across a whole K-page cycle stays ≤5, not K×5.
//
// Fails OPEN on a per-item ScenesBySite error (logs and keeps the item,
// rather than assuming zero scenes) — one transient TPDB hiccup among up to
// perPage per-item calls must not hide a real studio.
func FilterZeroSceneSites(ctx context.Context, tpdb *tpdbrest.Client, sites []tpdbrest.Site) []tpdbrest.Site {
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

// FetchAndMergePerformers runs the Performers row's concurrent TPDB+StashDB
// fetch and fuzzy merge, returning the PRE-availability-filter merged card list
// and the pre-filter HasMore flag. It is the single source of this pipeline for
// BOTH the live handler and the precompute job (R-c: no drift).
//
// The TPDB and StashDB legs run concurrently (BrowsePerformers does its own
// bounded image backfill internally, so sequential legs would sum latency
// instead of max). A TPDB error is fatal and returned to the caller (the
// handler maps it to 502); a StashDB error is logged and degrades to TPDB-only,
// never failing the request. HasMore is derived from PRE-filter source lengths
// (a full page from either leg means "more may exist"), captured before any
// availability filter runs downstream — deliberately never from a post-filter
// count.
func FetchAndMergePerformers(ctx context.Context, tpdb *tpdbrest.Client, stash *stashbox.Client, hasStash bool, page, perPage int) ([]apidto.MergedPerformerCard, bool, error) {
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
		return nil, false, tpdbErr
	}
	if hasStash && stashErr != nil {
		log.Printf("adult discover performers-merged: stashdb query failed, degrading to tpdb-only: %v", stashErr)
		stashItems = nil
	}
	hasMore := len(tpdbItems) >= perPage || len(stashItems) >= perPage
	return MergePerformers(tpdbItems, stashItems), hasMore, nil
}

// FetchAndMergeStudios is FetchAndMergePerformers' studio sibling — same
// concurrent-fetch, required-TPDB/optional-StashDB, no-drift contract, plus the
// two things Studios has that Performers doesn't: the FilterZeroSceneSites pass
// over the TPDB leg, and a HasMore sourced from BrowseSites' explicit
// tpdbHasMore (computed BEFORE the zero-scene filter runs) OR'd with StashDB's
// own full-page signal. Deliberately NOT unified with the Performers sibling —
// the pipelines genuinely differ (see the package doc).
func FetchAndMergeStudios(ctx context.Context, tpdb *tpdbrest.Client, stash *stashbox.Client, hasStash bool, page, perPage int) ([]apidto.MergedStudioCard, bool, error) {
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
		return nil, false, tpdbErr
	}
	if hasStash && stashErr != nil {
		log.Printf("adult discover studios-merged: stashdb query failed, degrading to tpdb-only: %v", stashErr)
		stashItems = nil
	}
	tpdbItems = FilterZeroSceneSites(ctx, tpdb, tpdbItems)
	hasMore := tpdbHasMore || len(stashItems) >= perPage
	return MergeStudios(tpdbItems, stashItems), hasMore, nil
}
