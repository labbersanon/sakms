// Package identify implements the AI-assisted content identification
// pipeline: direct UUID lookup -> Qwen filename parse -> internal DB text
// search -> Brave web search + grounded re-extraction -> re-search -> bare
// web-identified fallback.
package identify

import (
	"context"
	"strings"

	"github.com/curtiswtaylorjr/tidyarr/internal/bravesearch"
	"github.com/curtiswtaylorjr/tidyarr/internal/throttle"
)

type Identifier struct {
	Boxes *BoxSearcher
	// AI is whichever provider is configured (Ollama, OpenAI, Gemini, or
	// Anthropic — see mode.buildAIClient) behind the shared AIClient
	// interface. Every prompt in this package is written to be
	// provider-agnostic (see AIClient's doc comment).
	AI       AIClient
	Brave    *bravesearch.Client // nil if no Brave key is available — web search step is skipped
	Throttle *throttle.Throttle
	// GiveBack submits identification results back to the community databases
	// (fingerprints, or scene drafts for web-identified-only matches). Nil if
	// neither TPDB nor StashDB/FansDB is configured — callers must nil-check.
	GiveBack *GiveBack
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

// Identify runs the full AI-assisted identification pipeline for a file whose
// filename stem is `stem`, found in a folder named `parentName`. Returns nil
// (no error) if nothing identifies the file with enough confidence to place
// it — the caller should route the file to unmatched in that case.
func (id *Identifier) Identify(ctx context.Context, stem, parentName string) (*MatchResult, error) {
	parentName = cleanParentName(parentName)

	if result, err := id.tryUUIDLookup(ctx, stem, parentName); err != nil {
		return nil, err
	} else if result != nil {
		return result, nil
	}

	parsed, err := ParseFilename(ctx, id.AI, stem, parentName)
	if err != nil {
		return nil, nil //nolint:nilerr // a parse failure is a soft "no match", not a hard error
	}
	if parsed.Title == "" {
		return nil, nil
	}

	if result, err := id.searchInternalDBs(ctx, parsed.Title, parsed.Studio, stem); err != nil {
		return nil, err
	} else if result != nil {
		if result.Date == "" {
			result.Date = parsed.Year
		}
		return result, nil
	}

	if id.Brave == nil {
		return nil, nil
	}

	grounded, err := id.webSearchAndGround(ctx, stem, parentName, parsed)
	if err != nil {
		return nil, err
	}
	if grounded.Title == "" || grounded.Studio == "" {
		return nil, nil
	}

	return id.reSearchAfterGrounding(ctx, grounded)
}

func (id *Identifier) tryUUIDLookup(ctx context.Context, stem, parentName string) (*MatchResult, error) {
	uuid, ok := ExtractUUID(stem)
	if !ok {
		uuid, ok = ExtractUUID(parentName)
	}
	if !ok {
		return nil, nil
	}

	boxes := []string{"stashdb", "fansdb"}
	if strings.Contains(strings.ToLower(stem), "fansdb") || strings.Contains(strings.ToLower(parentName), "fansdb") {
		boxes = []string{"fansdb", "stashdb"}
	}
	for _, box := range boxes {
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

// searchInternalDBs: StashDB always, FansDB only if fansite-hinted, then TPDB.
func (id *Identifier) searchInternalDBs(ctx context.Context, title, studio, stem string) (*MatchResult, error) {
	boxes := []string{"stashdb"}
	if IsFansiteHinted(stem, studio) {
		boxes = append(boxes, "fansdb")
	}
	for _, box := range boxes {
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
// Source-tagging is intentionally asymmetric: a StashDB/FansDB match gets its
// Source prefixed "web+" (e.g. "web+stashdb_text"), while a TPDB match does
// not (stays plain "tpdb_text").
func (id *Identifier) reSearchAfterGrounding(ctx context.Context, grounded GroundedExtraction) (*MatchResult, error) {
	for _, box := range []string{"stashdb", "fansdb"} {
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
