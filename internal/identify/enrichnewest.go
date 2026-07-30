package identify

import (
	"context"
	"fmt"
	"log"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// Tunables for the Adult "newest" drill-down catalog enrichment. All three are
// package-level vars (not consts) matching this package's convention for
// tuned-but-test-overridable knobs (see entityverify.go's threshold block and
// newestScenesOutboundTimeout in internal/api) — nothing mutates them in
// production.
var (
	// catalogPages is how many pages of an entity's NEWEST scene catalog to
	// fetch per box. 1 keeps enrichment O(boxes): one resolve + one catalog
	// call per configured box. Bump to 2 (a +1 throttled call per box, still
	// O(boxes)) if the per-drill observability line shows frequent
	// catalog-window misses.
	catalogPages = 1

	// enrichTitleThreshold is the local scene-title match bar — the SAME 0.4
	// title bar SearchStashBox/SearchTPDB use (boxlookup.go), looser than the
	// 0.6 name bar because scene titles are long/multi-token and matched via
	// containment-aware maxSimilarity.
	enrichTitleThreshold = 0.4

	// nameTieDelta is the near-tie ambiguity margin for entity resolution: if a
	// second candidate also clears the 0.6 name threshold AND is within this
	// margin of the best candidate's score, resolution declines (returns no id)
	// rather than attributing scenes to whichever near-tied entity sorted
	// first. Raising the flat 0.6 threshold cannot fix genuine same-name
	// collisions (both clear any single bar) — this delta is the cheap,
	// correct guard. See Principle 4 / §5.2 of the drill-down plan.
	nameTieDelta = 0.05
)

// namedCandidate is one resolve candidate carrying both the name (for scoring)
// and the id (for the catalog fetch) — the object-keyed distinction from
// bestMatch, which only ever returns a name and so can't hand back the id of
// the candidate that produced the winning name.
type namedCandidate struct {
	name string
	id   string
}

// boxEnrichResult is one box's contribution to a drill: its resolve decision
// (for the unconditional per-drill log) plus the matches it produced, keyed by
// the caller's releaseTitles index.
type boxEnrichResult struct {
	box       string
	outcome   string // accepted | declined-ambiguous | declined-low-score | skipped
	candidate string // the winning candidate's name, "" when none accepted
	score     float64
	matches   map[int]*MatchResult
}

// EnrichNewestScenes resolves an entity (kind "studio"|"performer") by name to
// its StashDB/FansDB/TPDB catalog id(s), fetches that entity's NEWEST scene
// catalog page once per configured box, and locally matches each raw release
// title against the fetched catalog using this package's own similarity
// primitives. Returns, keyed by the caller's releaseTitles slice index, a
// *MatchResult for each confident match. O(boxes) upstream calls, every one
// routed through id.Throttle. Best-effort: an unresolvable or ambiguous entity,
// a box error, a throttle-abort, or no confident match yields no entry for that
// release (never an error that should fail the caller). No boxes configured →
// empty map, no error. Local matching is zero-cost (CPU only).
//
// TPDB catalog ordering caveat (unverified, honest): the TPDB sub-resource
// endpoints (ScenesBySite/ScenesByPerformer) are called with their default
// server-side order — this package does NOT send orderBy=recently_released, to
// keep the change surface within the tpdbrest client's existing signatures
// (the drill-down plan's US-3 file-scope gate). TPDB's default sub-resource
// order is not verified in-code, so a TPDB-only match may occasionally fall
// outside the newest window; the StashDB-preferred cross-box merge (below,
// StashDB catalog IS confirmed date-desc) and the catalogPages knob mitigate
// this. See the drill-down plan §5.2/§7 for the full reasoning.
func (id *Identifier) EnrichNewestScenes(ctx context.Context, kind, name string, releaseTitles []string) (map[int]*MatchResult, error) {
	result := make(map[int]*MatchResult)
	if id.Boxes == nil || len(releaseTitles) == 0 {
		return result, nil
	}
	boxes := id.enrichBoxes(name)
	if len(boxes) == 0 {
		return result, nil
	}
	// Mirror verifyStudio/verifyOnePerformer: normalize once and use the same
	// cleaned string as both the search term and the scoring guess.
	cleaned := normalizeForSearch(name)

	// Each goroutine writes its own disjoint results[i] slot — no shared
	// mutable state, so no mutex needed; g.Wait() provides the happens-before
	// for the merge below. Plain errgroup (not WithContext) so one box's
	// failure never cancels its siblings (the fetchAdultRSSItems soft-fail
	// pattern). SetLimit(3) = one slot per box family (independent host queues).
	results := make([]boxEnrichResult, len(boxes))
	var g errgroup.Group
	g.SetLimit(3)
	for i, box := range boxes {
		i, box := i, box
		g.Go(func() error {
			results[i] = id.enrichOneBox(ctx, box, kind, cleaned, releaseTitles)
			return nil
		})
	}
	_ = g.Wait()

	// Cross-box merge: boxes is already in StashDB > FansDB > TPDB preference
	// order, so first-writer-wins per index is exactly that preference.
	for _, br := range results {
		for idx, mr := range br.matches {
			if _, ok := result[idx]; !ok {
				result[idx] = mr
			}
		}
	}

	logResolveDecisions(kind, name, results, len(result), len(releaseTitles))
	return result, nil
}

// enrichBoxes returns the configured boxes to consult, in StashDB > FansDB >
// TPDB preference order. FansDB is consulted only when the entity name is
// fansite-hinted (the same gate searchInternalDBs/verifyStudio use).
func (id *Identifier) enrichBoxes(name string) []string {
	var boxes []string
	if id.Boxes.stashBoxes["stashdb"] != nil {
		boxes = append(boxes, "stashdb")
	}
	if IsFansiteHinted(name) && id.Boxes.stashBoxes["fansdb"] != nil {
		boxes = append(boxes, "fansdb")
	}
	if id.Boxes.tpdb != nil {
		boxes = append(boxes, "tpdb")
	}
	return boxes
}

// enrichOneBox resolves the entity in one box, fetches its newest catalog page,
// and locally matches the release titles against it. Best-effort throughout:
// any failure yields a boxEnrichResult with no matches and a decline outcome.
func (id *Identifier) enrichOneBox(ctx context.Context, box, kind, cleaned string, releaseTitles []string) boxEnrichResult {
	entityID, outcome, candidate, score := id.resolveEntity(ctx, box, kind, cleaned)
	br := boxEnrichResult{box: box, outcome: outcome, candidate: candidate, score: score}
	if entityID == "" {
		return br
	}
	catalog := id.fetchCatalog(ctx, box, kind, entityID)
	br.matches = matchReleaseTitles(releaseTitles, catalog)
	return br
}

// resolveEntity resolves the drill entity to a box-specific catalog id. Studio
// on stashdb/fansdb uses the exact FindStudio (no threshold, no near-tie —
// it's an exact-name query). Every fuzzy path (studio-tpdb, performer on all
// three boxes) goes through the object-keyed resolveCandidateID (>= 0.6 +
// near-tie decline). Every upstream call is throttled first.
func (id *Identifier) resolveEntity(ctx context.Context, box, kind, cleaned string) (entityID, outcome, candidate string, score float64) {
	switch {
	case kind == "studio" && (box == "stashdb" || box == "fansdb"):
		client := id.Boxes.stashBoxes[box]
		if client == nil {
			return "", "skipped", "", 0
		}
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return "", "skipped", "", 0
		}
		studio, err := client.FindStudio(ctx, cleaned)
		if err != nil || studio == nil || studio.ID == "" || studio.Name == "" {
			return "", "declined-low-score", "", 0
		}
		return studio.ID, "accepted", studio.Name, 1.0

	case kind == "studio" && box == "tpdb":
		if id.Boxes.tpdb == nil {
			return "", "skipped", "", 0
		}
		if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
			return "", "skipped", "", 0
		}
		sites, err := id.Boxes.tpdb.SearchSites(ctx, cleaned)
		if err != nil {
			return "", "declined-low-score", "", 0
		}
		cands := make([]namedCandidate, len(sites))
		for i, s := range sites {
			cands[i] = namedCandidate{name: s.Name, id: s.ID}
		}
		eid, name, sc, oc := resolveCandidateID(cleaned, cands, studioMatchThreshold)
		return eid, oc, name, sc

	case kind == "performer" && (box == "stashdb" || box == "fansdb"):
		client := id.Boxes.stashBoxes[box]
		if client == nil {
			return "", "skipped", "", 0
		}
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return "", "skipped", "", 0
		}
		performers, err := client.SearchPerformer(ctx, cleaned, 5)
		if err != nil {
			return "", "declined-low-score", "", 0
		}
		cands := make([]namedCandidate, len(performers))
		for i, p := range performers {
			cands[i] = namedCandidate{name: p.Name, id: p.ID}
		}
		eid, name, sc, oc := resolveCandidateID(cleaned, cands, performerMatchThreshold)
		return eid, oc, name, sc

	case kind == "performer" && box == "tpdb":
		if id.Boxes.tpdb == nil {
			return "", "skipped", "", 0
		}
		if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
			return "", "skipped", "", 0
		}
		performers, err := id.Boxes.tpdb.SearchPerformers(ctx, cleaned)
		if err != nil {
			return "", "declined-low-score", "", 0
		}
		cands := make([]namedCandidate, len(performers))
		for i, p := range performers {
			cands[i] = namedCandidate{name: p.Name, id: p.ID}
		}
		eid, name, sc, oc := resolveCandidateID(cleaned, cands, performerMatchThreshold)
		return eid, oc, name, sc
	}
	return "", "skipped", "", 0
}

// resolveCandidateID picks the id of the single best-scoring candidate OBJECT,
// but only if its name similarity clears threshold AND is not a near-tie with a
// second candidate. Distinct from bestMatch (entityverify.go), which keys on
// extracted name strings and returns a name, not an id. Scoring uses the SAME
// fixed-argument-order TitleSimilarity bestMatch uses. Returns the winning
// id/name/score and one of the three canonical outcomes for the drill log.
func resolveCandidateID(drillName string, candidates []namedCandidate, threshold float64) (id, name string, score float64, outcome string) {
	bestIdx, bestScore, secondScore := -1, 0.0, 0.0
	for i, c := range candidates {
		s := TitleSimilarity(drillName, c.name)
		if s > bestScore {
			secondScore = bestScore
			bestScore = s
			bestIdx = i
		} else if s > secondScore {
			secondScore = s
		}
	}
	if bestIdx < 0 || bestScore < threshold {
		return "", "", bestScore, "declined-low-score"
	}
	// Near-tie: a second candidate also clears threshold and is within
	// nameTieDelta of the best → ambiguous, decline rather than guess.
	if secondScore >= threshold && bestScore-secondScore <= nameTieDelta {
		return "", "", bestScore, "declined-ambiguous"
	}
	return candidates[bestIdx].id, candidates[bestIdx].name, bestScore, "accepted"
}

// fetchCatalog fetches up to catalogPages of the entity's NEWEST scenes for one
// box and converts each to a *MatchResult using the same field construction the
// box-lookup paths use (boxlookup.go). Every page fetch is throttled. No
// resolveTPDBDuration re-fetch — the catalog scene's Duration is accepted as-is
// (0 = unknown).
func (id *Identifier) fetchCatalog(ctx context.Context, box, kind, entityID string) []*MatchResult {
	var out []*MatchResult
	for page := 1; page <= catalogPages; page++ {
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return out
		}
		switch box {
		case "stashdb", "fansdb":
			client := id.Boxes.stashBoxes[box]
			if client == nil {
				return out
			}
			var scenes []stashbox.Scene
			var err error
			if kind == "studio" {
				scenes, err = client.QueryScenesByStudio(ctx, entityID, page, 0)
			} else {
				scenes, err = client.QueryScenesByPerformer(ctx, entityID, page, 0)
			}
			if err != nil {
				return out
			}
			for _, s := range scenes {
				out = append(out, stashboxSceneToMatch(box, s))
			}
		case "tpdb":
			if id.Boxes.tpdb == nil {
				return out
			}
			var scenes []tpdbrest.Scene
			var err error
			if kind == "studio" {
				scenes, err = id.Boxes.tpdb.ScenesBySite(ctx, entityID, page, 0)
			} else {
				scenes, err = id.Boxes.tpdb.ScenesByPerformer(ctx, entityID, page, 0)
			}
			if err != nil {
				return out
			}
			for _, s := range scenes {
				out = append(out, tpdbSceneToMatch(s))
			}
		}
	}
	return out
}

// stashboxSceneToMatch mirrors SearchStashBox's Scene → MatchResult
// construction (boxlookup.go:74-78). Tags is empty for the catalog path: the
// browse query (queryScenesQuery) doesn't request tags, so s.Tags is nil — the
// same empty-Genres outcome as today's raw fallback, by design.
func stashboxSceneToMatch(box string, s stashbox.Scene) *MatchResult {
	return &MatchResult{
		Title: s.Title, Studio: s.StudioName, Date: s.ReleaseDate,
		Type: "scene", Source: box + "_catalog", SceneID: s.ID, Box: box,
		Image: s.ImageURL, Tags: strings.Join(s.Tags, ","), RuntimeSeconds: s.Duration,
	}
}

// tpdbSceneToMatch mirrors SearchTPDB's Scene → MatchResult construction
// (boxlookup.go:127-133) — Tags/Performers comma-joined with no space — but
// with RuntimeSeconds taken directly from the catalog scene (no
// resolveTPDBDuration re-fetch, which would be an O(N) unthrottled call).
func tpdbSceneToMatch(s tpdbrest.Scene) *MatchResult {
	tagNames := make([]string, len(s.Tags))
	for i, t := range s.Tags {
		tagNames[i] = t.Name
	}
	return &MatchResult{
		Title: s.Title, Studio: s.Site, Date: s.Date,
		Type: "scene", Source: "tpdb_catalog", SceneID: s.ID, Box: "tpdb",
		Image: s.Image, Tags: strings.Join(tagNames, ","),
		Performers:     strings.Join(s.Performers, ","),
		RuntimeSeconds: s.Duration,
	}
}

// matchReleaseTitles matches each release title against the box's catalog using
// the asymmetry-safe maxSimilarity (so release cruft matches a clean catalog
// title via containment), keeping the best catalog scene per release index if
// it clears enrichTitleThreshold. Zero upstream cost — CPU only.
func matchReleaseTitles(releaseTitles []string, catalog []*MatchResult) map[int]*MatchResult {
	out := make(map[int]*MatchResult)
	for i, rt := range releaseTitles {
		norm := normalizeForSearch(rt)
		var best *MatchResult
		bestScore := 0.0
		for _, mr := range catalog {
			if s := maxSimilarity(norm, mr.Title); s > bestScore {
				bestScore = s
				best = mr
			}
		}
		if best != nil && bestScore >= enrichTitleThreshold {
			out[i] = best
		}
	}
	return out
}

// logResolveDecisions emits the unconditional (never debug-gated) per-drill
// summary: how many items enriched, and — because this path attributes
// adult-content scenes to a real performer/studio — each box's resolve decision
// (candidate name, score, outcome). This is the detection mechanism the
// mis-resolution pre-mortem relies on, so it is a firm requirement, not an
// optional aside.
func logResolveDecisions(kind, name string, results []boxEnrichResult, enriched, total int) {
	parts := make([]string, 0, len(results))
	for _, br := range results {
		parts = append(parts, fmt.Sprintf("%s:%s(cand=%q score=%.2f)", br.box, br.outcome, br.candidate, br.score))
	}
	log.Printf("adult newest enrichment: kind=%q name=%q enriched %d/%d items; resolve=[%s]",
		kind, name, enriched, total, strings.Join(parts, " "))
}
