package identify

import (
	"context"
	"strings"
	"testing"
)

func TestExtractTitleFromSearch_JoJoDancerStyle(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		if !strings.Contains(prompt, "Web search results") {
			t.Errorf("expected search results in prompt")
		}
		return `{"title":"Jo Jo Dancer, Your Life Is Calling","year":1986}`
	})
	defer closeSrv()

	got, err := ExtractTitleFromSearch(context.Background(), client, "JoJo Dancer Pryor autobiography 2007", []SearchSnippet{
		{Title: "Jo Jo Dancer, Your Life Is Calling - Wikipedia", Description: "1986 American semi-autobiographical film starring Richard Pryor", URL: "https://en.wikipedia.org/wiki/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Jo Jo Dancer, Your Life Is Calling" || got.Year != 1986 {
		t.Fatalf("got %+v", got)
	}
}

func TestExtractTitleFromSearch_Decline(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"title":null,"year":null}`
	})
	defer closeSrv()
	got, err := ExtractTitleFromSearch(context.Background(), client, "xyz", []SearchSnippet{
		{Title: "unrelated", Description: "nope", URL: "https://x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "" {
		t.Fatalf("expected empty on decline, got %+v", got)
	}
}
