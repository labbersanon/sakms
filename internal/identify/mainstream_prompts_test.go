package identify

import (
	"context"
	"strings"
	"testing"
)

func TestGuessTitle_ParsesConfidentGuess(t *testing.T) {
	var seenPrompt string
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		seenPrompt = prompt
		return `{"title":"Breaking Bad","year":2008}`
	})
	defer closeSrv()

	got, err := GuessTitle(context.Background(), client, "brba.s01e01.720p-GROUP")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "Breaking Bad" || got.Year != 2008 {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(seenPrompt, "brba.s01e01.720p-GROUP") {
		t.Error("expected the original name to be embedded in the prompt")
	}
	if !strings.Contains(seenPrompt, "null") {
		t.Error("expected decline-heavy prompt to mention null")
	}
}

func TestGuessTitle_StripsYearFromTitleField(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"title":"The Matrix (1999)","year":null}`
	})
	defer closeSrv()
	got, err := GuessTitle(context.Background(), client, "matrix.1999.1080p")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "The Matrix" || got.Year != 1999 {
		t.Fatalf("got %+v", got)
	}
}

// The model has an explicit "I don't know" escape valve for opaque names —
// a null title must be treated as a real failure, not an empty-string guess
// that could go on to match an unrelated Lookup result.
func TestGuessTitle_DeclinesOnOpaqueName(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"title":null,"year":null}`
	})
	defer closeSrv()

	_, err := GuessTitle(context.Background(), client, "xyz123")
	if err == nil {
		t.Fatal("expected an error when the model declines to guess")
	}
}

func TestGuessTitle_LiteralNullStringTreatedAsDecline(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"title":"null"}`
	})
	defer closeSrv()

	_, err := GuessTitle(context.Background(), client, "xyz123")
	if err == nil {
		t.Fatal("expected the literal string \"null\" to normalize to a decline")
	}
}

func TestGuessTitle_MalformedResponseErrors(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `not json`
	})
	defer closeSrv()

	_, err := GuessTitle(context.Background(), client, "xyz123")
	if err == nil {
		t.Fatal("expected an error for a malformed Ollama response")
	}
}
