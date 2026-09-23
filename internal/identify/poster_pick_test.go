package identify

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/websearch"
)

type imageSearch struct {
	res []websearch.Result
}

func (s imageSearch) Search(context.Context, string, int) ([]websearch.Result, error) {
	return nil, nil
}
func (s imageSearch) Ping(context.Context) error { return nil }
func (s imageSearch) SearchImages(context.Context, string, int) ([]websearch.Result, error) {
	return s.res, nil
}

type pickAI struct {
	url any
}

func (p pickAI) ChatJSON(context.Context, string) (map[string]any, error) {
	return map[string]any{"url": p.url}, nil
}

func TestPickPosterURL_UsesChosenImage(t *testing.T) {
	search := imageSearch{res: []websearch.Result{
		{Title: "other", URL: "https://img.example/a.jpg"},
		{Title: "poster", URL: "https://img.example/poster.jpg"},
	}}
	got, err := PickPosterURL(context.Background(), search, pickAI{url: "https://img.example/poster.jpg"}, "Funny Faces", 1980, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://img.example/poster.jpg" {
		t.Fatalf("got %q", got)
	}
}

func TestPickPosterURL_RejectsInventedURL(t *testing.T) {
	search := imageSearch{res: []websearch.Result{
		{Title: "poster", URL: "https://img.example/real.jpg"},
	}}
	got, err := PickPosterURL(context.Background(), search, pickAI{url: "https://img.example/invented.jpg"}, "Funny Faces", 1980, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://img.example/real.jpg" {
		t.Fatalf("got %q", got)
	}
}

func TestPickPosterURL_DeclineReturnsEmpty(t *testing.T) {
	search := imageSearch{res: []websearch.Result{
		{Title: "poster", URL: "https://img.example/real.jpg"},
	}}
	got, err := PickPosterURL(context.Background(), search, pickAI{url: nil}, "Funny Faces", 1980, "movie")
	if err != nil || got != "" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestPickPosterURL_NoImageSearch(t *testing.T) {
	got, err := PickPosterURL(context.Background(), textOnly{}, pickAI{url: "https://img.example/a.jpg"}, "Funny Faces", 1980, "movie")
	if err != nil || got != "" {
		t.Fatalf("got %q err %v", got, err)
	}
}

type textOnly struct{}

func (textOnly) Search(context.Context, string, int) ([]websearch.Result, error) {
	return []websearch.Result{{URL: "https://example.test/page"}}, nil
}
func (textOnly) Ping(context.Context) error { return nil }
