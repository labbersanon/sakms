package identify

import (
	"context"
	"fmt"
	"strconv"

	"github.com/labbersanon/sakms/internal/ollama"
)

// SeriesEpisodeGuess is the Series-shaped result of an AI filename parse — the
// episode-level counterpart to ParsedFilename (qwen_prompts.go), which is
// scene-level and deliberately NOT reused (different output shape).
//
// SeasonNumber and EpisodeNumber are a NUMERIC SINK. They are DELIBERATELY
// NEVER READ BY ANY CALLER IN THIS CODEBASE — not by aiEpisodeRecoveryPass, not
// by tryAIEpisodeMatchSeries, not by anything. They exist for exactly one
// reason: to give the model somewhere to PUT a number it believes it sees, so
// that number does not contaminate ShowTitle or EpisodeTitle instead. This is a
// direct mitigation for the qwen-scale numeric mis-binding already recorded in
// qwen_prompts.go:12-22 (a model at this scale "frequently mis-bind[s] which
// segment of a YY.MM.DD token is the year"), and it is why ParseFilename
// resolves Year deterministically rather than asking.
//
// Every acceptance path in internal/rename derives season and episode from a
// CATALOG MATCH — never from these fields. If a future caller reads them, that
// caller is wrong; fix the caller, do not "improve" the model's numbers.
type SeriesEpisodeGuess struct {
	ShowTitle     string
	EpisodeTitle  string
	SeasonNumber  int // numeric sink — never read; see above
	EpisodeNumber int // numeric sink — never read; see above
	Year          int
}

// SeriesEpisodePromptMarker appears verbatim in EVERY prompt ParseSeriesEpisode
// sends. It exists so a test can assert "this specific path made no AI call" by
// inspecting captured PROMPT CONTENT rather than a global call counter —
// sess.MainstreamAI has eleven-plus other consumers reachable from one scan, so
// a bare counter proves nothing.
const SeriesEpisodePromptMarker = "sakms-series-episode-parse-v1"

// ParseSeriesEpisode asks the configured AI client to recover a show title and,
// where the filename genuinely encodes them, a season/episode number from a
// messy Series filename plus its folder context.
//
// folderName is the SHOW FOLDER's own directory name (rename.showFolderName),
// not filepath.Dir's basename — a nested "Uncompressed"/"VIDEO_TS" subdirectory
// carries no title information and actively misleads the model.
//
// Soft-fails: returns a zero SeriesEpisodeGuess and a nil error when client is
// nil, when the model declines (explicit nulls), or when the response has no
// usable title — matching GroundTitleViaSearch's own soft-fail contract rather
// than GuessTitle's error-on-decline contract. A decline here is an ordinary
// "fall through to the next path" event, not an exceptional one. A transport or
// JSON-decode failure still returns a real error, always alongside a ZERO
// struct: the caller discards the error, so a populated-struct-plus-error would
// be acted on as if it were a good guess.
//
// Claude 2026-08-06: new Series AI parse path (C2 of the anthology work)
// Reason: mirrors ParseFilename structurally but never reuses it — different
// output shape, and the numeric fields are a sink rather than an answer
// Troubleshooting: Series rows left Unmatched because the filename carries a
// foreign-language or typo'd EPISODE TITLE, not a number (Politiquerias,
// "Thier First Mistake")
// Review if: any caller starts reading SeasonNumber/EpisodeNumber
func ParseSeriesEpisode(ctx context.Context, client AIClient, filename, folderName string) (SeriesEpisodeGuess, error) {
	if client == nil {
		return SeriesEpisodeGuess{}, nil
	}

	folderLine := ""
	if folderName != "" {
		folderLine = fmt.Sprintf("Containing show folder: %q\n", folderName)
	}

	prompt := fmt.Sprintf(`[%s]
You are parsing a TV episode filename to identify which show and which episode it is.

Filename: %q
%s
The folder name usually names the SHOW. The filename may name the EPISODE, and may
be in a language other than English, may be misspelled, or may carry release-scene
noise (resolution, codec, source, disc numbers, release-group tags).

Extract:
1. showTitle:     the show's real, official English title.
2. episodeTitle:  the episode's real, official English title — corrected for
                  misspellings and translated from any other language.
3. seasonNumber:  the season number, ONLY if the filename or folder actually encodes one.
4. episodeNumber: the episode number, ONLY if the filename or folder actually encodes one.
5. year:          the show's first-air year, if you can tell.

Rules:
- If the filename is an episode TITLE rather than a number (e.g. a short film or
  sketch name), return the cleaned-up, corrected episode title in showTitle's
  companion field episodeTitle, and leave seasonNumber and episodeNumber null.
- If a title is in another language, ALSO return the English title you believe it
  corresponds to, in showTitle/episodeTitle.
- Do NOT invent a season or episode number. If the name does not encode one,
  return null for both. A wrong number is far worse than a null.
- If the name is too opaque to identify at all, return null for every field.

Return ONLY valid JSON with exactly these keys:
{"showTitle": ..., "episodeTitle": ..., "seasonNumber": ..., "episodeNumber": ..., "year": ...}`,
		SeriesEpisodePromptMarker, filename, folderLine)

	resp, err := client.ChatJSON(ctx, prompt)
	if err != nil {
		return SeriesEpisodeGuess{}, fmt.Errorf("AI series episode parse failed: %w", err)
	}

	show := ollama.NormalizeField(resp["showTitle"])
	episode := ollama.NormalizeField(resp["episodeTitle"])
	if show == "" && episode == "" {
		return SeriesEpisodeGuess{}, nil
	}

	return SeriesEpisodeGuess{
		ShowTitle:     show,
		EpisodeTitle:  episode,
		SeasonNumber:  decodeSeriesNumber(resp["seasonNumber"]),
		EpisodeNumber: decodeSeriesNumber(resp["episodeNumber"]),
		Year:          decodeSeriesNumber(resp["year"]),
	}, nil
}

// decodeSeriesNumber mirrors ExtractTitleFromSearch's float64/string switch
// (mainstream_prompts.go): JSON numbers arrive as float64, but a model at this
// scale routinely quotes them instead. Anything absent, null, or unparseable
// decodes to 0.
func decodeSeriesNumber(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		parsed, _ := strconv.Atoi(n)
		return parsed
	default:
		return 0
	}
}
