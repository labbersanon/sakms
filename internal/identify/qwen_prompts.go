package identify

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/ollama"
)

// ParsedFilename is the result of extracting metadata from a bare filename
// stem (+ optional parent-folder context): studio/title/performers come from
// the local LLM, Year is resolved deterministically (see
// ExtractYearFromToken) since a qwen2.5:1.5b-scale model was found to
// frequently mis-bind which segment of a YY.MM.DD token is the year.
type ParsedFilename struct {
	Studio     string
	Title      string
	Year       string
	Performers []string
}

// dateTokenPattern matches a YY.MM.DD or YYYY.MM.DD-style date token: a
// 2-or-4-digit year, a valid month (01-12), and a valid day (01-31),
// dot-separated. This is a fully mechanical pattern, not something that
// benefits from an LLM's judgment.
var dateTokenPattern = regexp.MustCompile(`\b(\d{2}|\d{4})\.(0[1-9]|1[0-2])\.(0[1-9]|[12]\d|3[01])\b`)

// ExtractYearFromToken finds a YY.MM.DD or YYYY.MM.DD-style date token in s
// and returns its normalized 4-digit year, or "" if no such token is present.
// A 2-digit year is expanded as 20XX.
func ExtractYearFromToken(s string) string {
	m := dateTokenPattern.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	year := m[1]
	if len(year) == 2 {
		return "20" + year
	}
	return year
}

// ParseFilename asks Ollama to extract studio/title/performers from a
// filename stem, and resolves the year deterministically from a date token in
// the stem (falling back to parentName) rather than asking the LLM — see
// ExtractYearFromToken. parentName (if non-empty) is also passed to the LLM as
// extra context — a parent folder often names the real studio/performer more
// reliably than the filename alone.
func ParseFilename(ctx context.Context, client AIClient, stem, parentName string) (ParsedFilename, error) {
	year := ExtractYearFromToken(stem)
	if year == "" {
		year = ExtractYearFromToken(parentName)
	}

	contextStr := ""
	if parentName != "" {
		contextStr = fmt.Sprintf(
			"Context: The file was found in a parent folder named: '%s'.\n"+
				"This folder name often contains the real studio name or performer name. Use it to guide your extraction.\n\n",
			parentName)
	}

	prompt := "You are parsing adult content filenames to extract metadata.\n" +
		contextStr +
		"Analyze the filename stem and extract:\n" +
		"1. studio: The production company/site/label (usually at the very beginning of the filename or matching the parent folder name, e.g., 'Tushy', 'Wow Girls', 'Brazzers', 'Candygirl Video').\n" +
		"2. title: The main descriptive name/title of the scene.\n" +
		"3. performers: A JSON array of performer/actor names mentioned in the filename or parent folder name, or null/empty array if none.\n\n" +
		"Guidelines:\n" +
		"- Clean up the title, studio, and performer names (remove extra tags like resolution, video quality, site domains, release dates, and release-group suffixes).\n" +
		"- Dots, dashes, and underscores in the filename are typically word separators, not literal characters — when extracting studio, title, or performer names, replace them with spaces and capitalize each word normally (e.g., 'riley.reid' becomes 'Riley Reid', 'deep-desires' becomes 'Deep Desires'). Only keep punctuation that's a genuine part of a name (e.g., a hyphenated surname).\n" +
		"- For studio, do NOT return names of aggregator/tube sites or host sites if there is a real studio name.\n" +
		"Return ONLY valid JSON with exactly these keys: studio, title, performers.\n" +
		"Use null for any field you cannot determine.\n\n" +
		fmt.Sprintf("Filename: %s\n\nJSON:", stem)

	result, err := client.ChatJSON(ctx, prompt)
	if err != nil {
		return ParsedFilename{}, err
	}

	performers := normalizePerformers(result["performers"])

	return ParsedFilename{
		Studio:     ollama.NormalizeField(result["studio"]),
		Title:      ollama.NormalizeField(result["title"]),
		Year:       year,
		Performers: performers,
	}, nil
}

func normalizePerformers(v any) []string {
	switch val := v.(type) {
	case []any:
		var out []string
		for _, p := range val {
			cleaned := ollama.NormalizeField(p)
			if cleaned != "" {
				out = append(out, cleaned)
			}
		}
		return out
	case string:
		cleaned := ollama.NormalizeField(val)
		if cleaned == "" {
			return nil
		}
		return []string{cleaned}
	default:
		return nil
	}
}

// GroundedExtraction is the result of re-identifying a file using real web
// search results as grounding context.
type GroundedExtraction struct {
	Studio string
	Title  string
	Year   string
}

// SearchSnippet is one web search result fed to the model as grounding
// context.
type SearchSnippet struct {
	Title       string
	Description string
	URL         string
}

// ExtractFromSearch grounds the filename-parse guess in real web search
// results: feeds the model the original messy filename plus search result
// titles/snippets, asking it to extract the real studio/title/date it can now
// cross-reference against actual sources, rather than guessing from filename
// tokens alone. Rejects a result whose title is too dissimilar to the
// original filename (search results were clearly about something else).
func ExtractFromSearch(ctx context.Context, client AIClient, stem string, results []SearchSnippet, parentName string) (GroundedExtraction, error) {
	if len(results) == 0 {
		return GroundedExtraction{}, nil
	}

	contextStr := ""
	if parentName != "" {
		contextStr = fmt.Sprintf("Parent folder name context: '%s' (this is highly relevant context indicating the likely studio, series, or performer name).\n", parentName)
	}

	var snippets strings.Builder
	for i, r := range results {
		if i > 0 {
			snippets.WriteString("\n\n")
		}
		fmt.Fprintf(&snippets, "Result %d:\nTitle: %s\nSnippet: %s\nURL: %s", i+1, r.Title, r.Description, r.URL)
	}

	prompt := "You are identifying adult content from a messy filename using web search results.\n" +
		contextStr +
		fmt.Sprintf("Original filename: %s\n\n", stem) +
		"Web search results for this filename:\n" +
		snippets.String() + "\n\n" +
		"Based on these search results and the provided context, determine the REAL studio name, scene/movie title, " +
		"and release year for this file.\n" +
		"IMPORTANT: the studio is the PRODUCTION COMPANY (e.g. Tushy, Brazzers, Evil Angel, " +
		"Wow Girls) — NEVER the website hosting or aggregating the video (tube sites, " +
		"streaming/rip sites like xxxstreams, porndude, thebrazzershd, bigfuck, " +
		"pornstar-scenes are NOT studios).\n" +
		"GUIDELINES:\n" +
		"- Do NOT bundle the studio name into the title. Separate them strictly: e.g., if you see 'Exposed Latinas - Threesome Scene 1', 'Exposed Latinas' is the studio, and 'Threesome Scene 1' is the title.\n" +
		"- If the parent folder name context or the filename itself names a studio (like 'Exposed Latinas'), prefer that as the studio.\n" +
		"- Only use information the search results and folder context actually support — if they don't clearly identify this specific content, return null for fields you're not confident about. Do not guess.\n" +
		"Return ONLY valid JSON with exactly these keys: studio, title, year.\n" +
		"JSON:"

	result, err := client.ChatJSON(ctx, prompt)
	if err != nil {
		return GroundedExtraction{}, err
	}

	extracted := GroundedExtraction{
		Studio: ollama.NormalizeField(result["studio"]),
		Title:  ollama.NormalizeField(result["title"]),
		Year:   ollama.NormalizeField(result["year"]),
	}

	// Sanity gate: the grounded title must actually resemble the filename —
	// a grounded result sharing almost no tokens with the stem means the
	// search results were about something else entirely.
	if extracted.Title != "" && TitleSimilarity(extracted.Title, stem) < 0.2 {
		return GroundedExtraction{}, nil
	}
	return extracted, nil
}
