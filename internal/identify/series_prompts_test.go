package identify

import (
	"context"
	"strings"
	"testing"
)

func TestParseSeriesEpisode_ParsesFullGuess(t *testing.T) {
	var seenPrompt string
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		seenPrompt = prompt
		return `{"showTitle":"Laurel & Hardy","episodeTitle":"Chickens Come Home","seasonNumber":null,"episodeNumber":null,"year":1931}`
	})
	defer closeSrv()

	guess, err := ParseSeriesEpisode(context.Background(), client, "Politiquerias.mkv", "Laurel and Hardy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guess.ShowTitle != "Laurel & Hardy" {
		t.Errorf("ShowTitle = %q", guess.ShowTitle)
	}
	if guess.EpisodeTitle != "Chickens Come Home" {
		t.Errorf("EpisodeTitle = %q", guess.EpisodeTitle)
	}
	if guess.Year != 1931 {
		t.Errorf("Year = %d, want 1931", guess.Year)
	}
	if guess.SeasonNumber != 0 || guess.EpisodeNumber != 0 {
		t.Errorf("explicit nulls should decode to 0, got S%d E%d", guess.SeasonNumber, guess.EpisodeNumber)
	}
	if seenPrompt == "" {
		t.Fatal("expected a prompt to have been captured")
	}
}

// The sink exists precisely to ABSORB a number the model thinks it sees, so the
// number must actually land in the struct rather than being dropped — a dropped
// number is one the model would have smuggled into a title field instead.
// This asserts DECODING only. That no caller ever READS these two fields is
// asserted on the caller side (internal/rename).
func TestParseSeriesEpisode_NumbersDecodeIntoTheSink(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"showTitle":"The Red Skelton Show","episodeTitle":"x","seasonNumber":15,"episodeNumber":24}`
	})
	defer closeSrv()

	guess, err := ParseSeriesEpisode(context.Background(), client, "e1524.mp4", "The Red Skelton Show")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guess.SeasonNumber != 15 || guess.EpisodeNumber != 24 {
		t.Fatalf("got S%d E%d, want S15 E24", guess.SeasonNumber, guess.EpisodeNumber)
	}
}

func TestParseSeriesEpisode_AcceptsStringNumbers(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"showTitle":"The Red Skelton Show","episodeTitle":"x","seasonNumber":"15","episodeNumber":"24","year":"1951"}`
	})
	defer closeSrv()

	guess, err := ParseSeriesEpisode(context.Background(), client, "e1524.mp4", "The Red Skelton Show")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guess.SeasonNumber != 15 || guess.EpisodeNumber != 24 {
		t.Errorf("got S%d E%d, want S15 E24", guess.SeasonNumber, guess.EpisodeNumber)
	}
	if guess.Year != 1951 {
		t.Errorf("Year = %d, want 1951", guess.Year)
	}
}

// Soft-fail contract: a decline is an ordinary "fall through to the next path"
// event, so it is a zero struct and a NIL error — never an error.
func TestParseSeriesEpisode_DeclinesOnAllNull(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"showTitle":null,"episodeTitle":null,"seasonNumber":null,"episodeNumber":null,"year":null}`
	})
	defer closeSrv()

	guess, err := ParseSeriesEpisode(context.Background(), client, "xyz123.mkv", "Whatever")
	if err != nil {
		t.Fatalf("a decline must not be an error, got %v", err)
	}
	if guess != (SeriesEpisodeGuess{}) {
		t.Fatalf("expected a zero guess, got %+v", guess)
	}
}

// An empty JSON object is the same decline, reached a different way.
func TestParseSeriesEpisode_DeclinesOnEmptyObject(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{}`
	})
	defer closeSrv()

	guess, err := ParseSeriesEpisode(context.Background(), client, "xyz123.mkv", "Whatever")
	if err != nil {
		t.Fatalf("an empty object must not be an error, got %v", err)
	}
	if guess != (SeriesEpisodeGuess{}) {
		t.Fatalf("expected a zero guess, got %+v", guess)
	}
}

// A partial response is usable: the show folder is often the only thing the
// model can name, and EpisodeTitle alone is what recovers a foreign-language or
// typo'd episode name. Neither is a decline on its own.
func TestParseSeriesEpisode_PartialResponseIsNotADecline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		canned  string
		show    string
		episode string
	}{
		{"show only", `{"showTitle":"The Red Skelton Show"}`, "The Red Skelton Show", ""},
		{"episode only", `{"episodeTitle":"Their First Mistake"}`, "", "Their First Mistake"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, closeSrv := fakeOllama(t, func(prompt string) string { return tc.canned })
			defer closeSrv()

			guess, err := ParseSeriesEpisode(context.Background(), client, "f.mkv", "Folder")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if guess.ShowTitle != tc.show || guess.EpisodeTitle != tc.episode {
				t.Fatalf("got %q/%q, want %q/%q", guess.ShowTitle, guess.EpisodeTitle, tc.show, tc.episode)
			}
			if guess.SeasonNumber != 0 || guess.EpisodeNumber != 0 || guess.Year != 0 {
				t.Errorf("absent numeric fields should be 0, got %+v", guess)
			}
		})
	}
}

// A malformed response is a REAL error — but still alongside a zero struct,
// because the caller discards the error and uses the struct.
func TestParseSeriesEpisode_MalformedResponseErrorsWithZeroGuess(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `not json`
	})
	defer closeSrv()

	guess, err := ParseSeriesEpisode(context.Background(), client, "f.mkv", "Folder")
	if err == nil {
		t.Fatal("expected an error for a malformed response")
	}
	if guess != (SeriesEpisodeGuess{}) {
		t.Fatalf("expected a zero guess alongside the error, got %+v", guess)
	}
}

// An entirely empty response body from the model is the same class of failure.
func TestParseSeriesEpisode_EmptyResponseErrorsWithZeroGuess(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return ``
	})
	defer closeSrv()

	guess, err := ParseSeriesEpisode(context.Background(), client, "f.mkv", "Folder")
	if err == nil {
		t.Fatal("expected an error for an empty response")
	}
	if guess != (SeriesEpisodeGuess{}) {
		t.Fatalf("expected a zero guess alongside the error, got %+v", guess)
	}
}

func TestParseSeriesEpisode_NilClientSoftFails(t *testing.T) {
	guess, err := ParseSeriesEpisode(context.Background(), nil, "f.mkv", "Folder")
	if err != nil {
		t.Fatalf("a nil client must soft-fail, got %v", err)
	}
	if guess != (SeriesEpisodeGuess{}) {
		t.Fatalf("expected a zero guess, got %+v", guess)
	}
}

func TestParseSeriesEpisode_EmbedsFilenameAndFolder(t *testing.T) {
	var withFolder string
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		withFolder = prompt
		return `{"showTitle":"X"}`
	})
	defer closeSrv()

	if _, err := ParseSeriesEpisode(context.Background(), client, "Politiquerias.mkv", "The Red Skelton Show"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(withFolder, "Politiquerias.mkv") {
		t.Error("expected the filename to be embedded in the prompt")
	}
	if !strings.Contains(withFolder, "The Red Skelton Show") {
		t.Error("expected the folder name to be embedded in the prompt")
	}

	var noFolder string
	client2, closeSrv2 := fakeOllama(t, func(prompt string) string {
		noFolder = prompt
		return `{"showTitle":"X"}`
	})
	defer closeSrv2()

	if _, err := ParseSeriesEpisode(context.Background(), client2, "Politiquerias.mkv", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(noFolder, "Politiquerias.mkv") {
		t.Error("expected the filename to be embedded in the folderless prompt")
	}
	if strings.Contains(noFolder, "Containing show folder") {
		t.Errorf("the folder line must be absent entirely when folderName is empty:\n%s", noFolder)
	}
}

// The load-bearing test of this file. Callers assert "this path made no AI
// call" by counting captured prompts that contain the marker — sess.MainstreamAI
// has many other consumers, so a bare call counter discriminates nothing. If any
// prompt variant can ship without the marker, that whole strategy is unsound.
func TestParseSeriesEpisode_EveryPromptCarriesTheMarker(t *testing.T) {
	for _, tc := range []struct {
		name             string
		filename, folder string
	}{
		{"with folder", "Politiquerias.mkv", "The Red Skelton Show"},
		{"without folder", "Politiquerias.mkv", ""},
		{"empty filename and folder", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			client, closeSrv := fakeOllama(t, func(prompt string) string {
				seen = append(seen, prompt)
				return `{"showTitle":"X"}`
			})
			defer closeSrv()

			if _, err := ParseSeriesEpisode(context.Background(), client, tc.filename, tc.folder); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(seen) != 1 {
				t.Fatalf("expected exactly 1 prompt, captured %d", len(seen))
			}
			for i, p := range seen {
				if !strings.Contains(p, SeriesEpisodePromptMarker) {
					t.Errorf("prompt %d is missing %q:\n%s", i, SeriesEpisodePromptMarker, p)
				}
			}
		})
	}
}
