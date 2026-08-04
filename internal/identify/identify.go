// Package identify implements the AI-assisted content identification
// pipeline: direct UUID lookup -> Qwen filename parse -> internal DB text
// search -> Brave web search + grounded re-extraction -> re-search -> bare
// web-identified fallback.
package identify

import (
	"context"
	"strings"

	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/parseentity"
	"github.com/labbersanon/sakms/internal/throttle"
)

type Identifier struct {
	Boxes *BoxSearcher
	// AI is whichever provider is configured (Ollama, OpenAI, Gemini, or
	// Anthropic — see mode.buildAIClient) behind the shared AIClient
	// interface. Every prompt in this package is written to be
	// provider-agnostic (see AIClient's doc comment). Nil when no external
	// AI provider is configured — the DB-first path (EntityStore) runs
	// without it; AI is only the BYOAI fallback when fields are still empty.
	AI AIClient
	// EntityStore is the entity cache for DB-first filename parsing. When
	// non-nil, ParseFilenameDB runs before the AI path. When nil, the AI
	// path runs unconditionally (legacy behaviour). At least one of AI or
	// EntityStore must be non-nil for IdentifyDetailed to do useful work.
	EntityStore parseentity.EntityStore
	Brave       *bravesearch.Client // nil if no Brave key is available — web search step is skipped
	Throttle    *throttle.Throttle
	// GiveBack submits identification results back to the community databases
	// (fingerprints, or scene drafts for web-identified-only matches). Nil if
	// neither TPDB nor StashDB/FansDB is configured — callers must nil-check.
	GiveBack *GiveBack
	// StashBoxes is the ordered snapshot of configured stash-box databases
	// (enabled only, cascade order, TPDB NOT included — it is appended
	// separately as a literal). It drives ITERATION; Boxes.stashBoxes and
	// GiveBack.Boxes stay name-keyed maps and drive CLIENT LOOKUP.
	//
	// EMPTY MEANS LEGACY, NOT "none configured": an Identifier built without
	// this field behaves exactly as the pre-Stage-5 code did, against
	// "stashdb" then "fansdb" (see cascade.go's legacyCascade). That is what
	// keeps every hand-built Identifier in tests, and any caller that has not
	// been taught about the registry, working unchanged.
	//
	// mode.Build snapshots this per Scan, so it is immutable for the life of
	// one Scan — an operator reordering databases mid-Scan does not reorder
	// the cascade underneath a run in progress.
	StashBoxes []DatabaseRef
}

var skipParentNames = map[string]bool{
	"adult-nas": true, "_unmatched": true, "scenes": true, "movies": true,
	".trash-1001": true, "unmatched": true,
}

func cleanParentName(name string) string {
	if skipParentNames[strings.ToLower(name)] {
		return ""
	}
	return name
}

// DetailedMatch is IdentifyDetailed's richer result: the scene match Identify
// would return, plus the corrected Studio and Performer identities the pipeline
// derives internally (via verifyStudio/verifyPerformers) — Identify() computes
// these too but discards them after using them to refine the scene search term.
// Callers that need Studio/Performer as first-class results (not just a scene
// search refinement) should use IdentifyDetailed instead of re-deriving them
// separately.
type DetailedMatch struct {
	Scene      *MatchResult // nil if no scene matched (same "no match" semantics as Identify)
	StudioName string       // the corrected studio name from verifyStudio, "" if none
	Performers []string     // the corrected performer names from verifyPerformers, empty if none
}

// Identify runs the full AI-assisted identification pipeline for a file whose
// filename stem is `stem`, found in a folder named `parentName`. Returns nil
// (no error) if nothing identifies the file with enough confidence to place
// it — the caller should route the file to unmatched in that case.
func (id *Identifier) Identify(ctx context.Context, stem, parentName string) (*MatchResult, error) {
	detail, err := id.IdentifyDetailed(ctx, stem, parentName)
	if err != nil {
		return nil, err
	}
	return detail.Scene, nil
}

// IdentifyDetailed runs the exact same pipeline as Identify but also returns
// the corrected Studio and Performer identities the pipeline derives internally
// (post-verifyStudio/verifyPerformers correction, not the raw AI guess). Identify
// is a thin wrapper over this method that keeps only the scene match, so the two
// can never diverge. The StudioName/Performers rejection and canonical-name
// correction come entirely from verifyStudio/verifyPerformers (including the
// release-group/content-rating tag guard in entityverify.go) — this method adds
// no second guard of its own.
//
// In the direct-UUID branch, verifyStudio/verifyPerformers never run, so
// StudioName is "" and Performers is empty even though Scene is populated — this
// is faithful to "exactly what the pipeline derived," not a value backfilled
// from the scene.
func (id *Identifier) IdentifyDetailed(ctx context.Context, stem, parentName string) (*DetailedMatch, error) {
	parentName = cleanParentName(parentName)

	if result, err := id.tryUUIDLookup(ctx, stem, parentName); err != nil {
		return nil, err
	} else if result != nil {
		return &DetailedMatch{Scene: result}, nil
	}

	// DB-first parse: deterministic, zero-latency entity lookup.
	// BYOAI fallback: only when AI is configured AND key fields are still empty.
	var parsed ParsedFilename
	var parseErr error
	if id.EntityStore != nil {
		parsed, parseErr = ParseFilenameDB(ctx, stem, parentName, id.EntityStore)
		if parseErr != nil {
			return &DetailedMatch{}, nil //nolint:nilerr
		}
	}
	if id.AI != nil && parsed.Studio == "" && parsed.Title == "" {
		parsed, parseErr = ParseFilename(ctx, id.AI, stem, parentName)
		if parseErr != nil {
			return &DetailedMatch{}, nil //nolint:nilerr
		}
	}
	if id.EntityStore == nil && id.AI == nil {
		return &DetailedMatch{}, nil
	}
	if parsed.Title == "" && parsed.Studio == "" {
		return &DetailedMatch{}, nil
	}

	// Correct the AI's raw studio/performer guess against real StashDB/
	// FansDB/TPDB data before using it any further (as a scene-search term,
	// a web-search query, or the final fallback value) — see
	// entityverify.go's doc comments for why this replaces relying on the
	// AI's own text formatting.
	studioGuess := parsed.Studio
	parsed.Studio = id.verifyStudio(ctx, studioGuess, stem)
	parsed.Performers = id.verifyPerformers(ctx, parsed.Performers, stem, studioGuess)

	detail := &DetailedMatch{StudioName: parsed.Studio, Performers: parsed.Performers}

	if result, err := id.searchInternalDBs(ctx, parsed.Title, parsed.Studio, stem); err != nil {
		return nil, err
	} else if result != nil {
		if result.Date == "" {
			result.Date = parsed.Year
		}
		detail.Scene = result
		return detail, nil
	}

	if id.Brave == nil || id.AI == nil {
		// webSearchAndGround's whole job is handing Brave results to the AI
		// client for interpretation — with no AI client configured, it has
		// nothing to do (and ExtractFromSearch would call a nil AIClient and
		// panic the whole process, which happened in production on 2026-07-25
		// once the browse pass started actually running and reached this path
		// with Brave configured but no AI connection set up).
		return detail, nil
	}

	grounded, err := id.webSearchAndGround(ctx, stem, parentName, parsed)
	if err != nil {
		return nil, err
	}
	if grounded.Title == "" || grounded.Studio == "" {
		return detail, nil
	}

	scene, err := id.reSearchAfterGrounding(ctx, grounded)
	if err != nil {
		return nil, err
	}
	detail.Scene = scene
	return detail, nil
}

func (id *Identifier) tryUUIDLookup(ctx context.Context, stem, parentName string) (*MatchResult, error) {
	uuid, ok := ExtractUUID(stem)
	if !ok {
		uuid, ok = ExtractUUID(parentName)
	}
	if !ok {
		return nil, nil
	}

	// Claude 2026-08-04: was []string{"stashdb","fansdb"} with a literal
	// "fansdb" filename flip (Stage 5 Wave 3). uuidBoxes generalises both:
	// cascade order, ungated (a UUID lookup is exact, so a fansite catalog
	// carries no false-match risk), with a name-mentioned database promoted.
	// for _, box := range boxes {   // ← was: the two hardcoded names above
	for _, box := range id.uuidBoxes(stem, parentName) {
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return nil, err
		}
		result, err := id.Boxes.SceneByID(ctx, box, uuid)
		if err != nil {
			continue // best-effort: try the next box rather than aborting the whole pipeline
		}
		if result != nil {
			return result, nil
		}
	}
	return nil, nil
}

// searchInternalDBs: every configured database in cascade order (fansite-only
// ones skipped unless the file is fansite-hinted), then TPDB.
//
// Claude 2026-08-04: was hardcoded "stashdb" always + "fansdb" if hinted
// (Stage 5 Wave 3). textBoxes reproduces exactly that on a default install —
// the gate is now the row's FansiteOnly flag rather than its name.
func (id *Identifier) searchInternalDBs(ctx context.Context, title, studio, stem string) (*MatchResult, error) {
	for _, box := range id.textBoxes(stem, studio) {
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return nil, err
		}
		result, err := id.Boxes.SearchStashBox(ctx, box, title, studio)
		if err != nil {
			continue
		}
		if result != nil {
			return result, nil
		}
	}

	if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
		return nil, err
	}
	result, err := id.Boxes.SearchTPDB(ctx, title, studio)
	if err != nil {
		return nil, nil //nolint:nilerr // best-effort: fall through to web search
	}
	return result, nil
}

func (id *Identifier) webSearchAndGround(ctx context.Context, stem, parentName string, parsed ParsedFilename) (GroundedExtraction, error) {
	var queryParts []string
	if parsed.Studio != "" {
		queryParts = append(queryParts, parsed.Studio)
	}
	if parsed.Title != "" {
		queryParts = append(queryParts, parsed.Title)
	}
	for i, p := range parsed.Performers {
		if i >= 2 {
			break
		}
		queryParts = append(queryParts, p)
	}
	query := strings.TrimSpace(strings.Join(queryParts, " "))

	if err := id.Throttle.Wait(ctx, "brave"); err != nil {
		return GroundedExtraction{}, err
	}
	searchResults, err := id.Brave.Search(ctx, query, 5)
	if err != nil {
		searchResults = nil // best-effort — Brave failures degrade to "no results", not a hard error
	}

	if len(searchResults) == 0 {
		fallbackQuery := CleanStemForSearch(stem)
		if fallbackQuery != "" && fallbackQuery != query {
			if err := id.Throttle.Wait(ctx, "brave"); err != nil {
				return GroundedExtraction{}, err
			}
			searchResults, _ = id.Brave.Search(ctx, fallbackQuery, 5)
		}
	}

	if len(searchResults) == 0 {
		return GroundedExtraction{}, nil
	}

	snippets := make([]SearchSnippet, len(searchResults))
	for i, r := range searchResults {
		snippets[i] = SearchSnippet{Title: r.Title, Description: r.Description, URL: r.URL}
	}
	return ExtractFromSearch(ctx, id.AI, stem, snippets, parentName)
}

// reSearchAfterGrounding re-checks the internal DBs with the web-grounded
// (more accurate) title/studio — a real DB match is still strictly better
// than a bare web result if one now exists.
//
// Source-tagging is intentionally asymmetric: a stash-box match gets its
// Source prefixed "web+" (e.g. "web+stashdb_text"), while a TPDB match does
// not (stays plain "tpdb_text").
//
// Claude 2026-08-04: was []string{"stashdb","fansdb"} (Stage 5 Wave 3).
// Reason: uses exactBoxes (UNGATED), deliberately NOT textBoxes, even though
// this is a text search. ralplan §2.3 asks for the fansite gate here and
// claims it "reproduces today's behaviour exactly" — it does not: unlike
// searchInternalDBs, this site never had a fansite gate and always consulted
// FansDB. Applying one would change the SET of databases queried on a default
// install, which AC15 (byte-identical default-install cascade) forbids, so the
// existing ungated behaviour is preserved and the ralplan line is the one
// that's wrong.
// Review if: AC15 is retired, at which point gating this is probably the
// better behaviour — a web-grounded title is exactly the kind of generic text
// that spuriously matches FansDB's clip catalog.
func (id *Identifier) reSearchAfterGrounding(ctx context.Context, grounded GroundedExtraction) (*MatchResult, error) {
	for _, box := range id.exactBoxes() {
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return nil, err
		}
		result, err := id.Boxes.SearchStashBox(ctx, box, grounded.Title, grounded.Studio)
		if err != nil {
			continue
		}
		if result != nil {
			if result.Date == "" {
				result.Date = grounded.Year
			}
			result.Source = "web+" + result.Source
			return result, nil
		}
	}

	if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
		return nil, err
	}
	if result, err := id.Boxes.SearchTPDB(ctx, grounded.Title, grounded.Studio); err == nil && result != nil {
		if result.Date == "" {
			result.Date = grounded.Year
		}
		return result, nil
	}

	// Nothing in any database, but the web search confidently identified real
	// studio+title — use it directly rather than leaving genuinely-identified
	// content stuck in _unmatched/.
	return &MatchResult{
		Title: grounded.Title, Studio: grounded.Studio, Date: grounded.Year,
		Type: "scene", Source: "web_search",
	}, nil
}
