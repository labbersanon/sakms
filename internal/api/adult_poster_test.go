package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

func TestResolveAdultScenePoster_TPDBThenStashBox(t *testing.T) {
	tpdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/scenes/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"_id":"tsc1","title":"T","image":"https://1.1.1.1/tpdb.jpg"}}`))
	}))
	t.Cleanup(tpdb.Close)

	stash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScene":{"id":"sc1","title":"T","images":[{"url":"https://1.1.1.1/stash.jpg"}]}}}`))
	}))
	t.Cleanup(stash.Close)

	httpClient := &http.Client{Timeout: 5 * time.Second}
	boxes := identify.NewBoxSearcher(map[string]*stashbox.Client{
		"stashdb": stashbox.New(stashbox.Config{Endpoint: stash.URL, APIKey: "k"}, httpClient),
	}, tpdbrest.New(tpdb.URL, "k", httpClient))

	ctx := context.Background()
	if got := resolveAdultScenePoster(ctx, boxes, "tpdb", "tsc1"); got != "https://1.1.1.1/tpdb.jpg" {
		t.Fatalf("tpdb image = %q", got)
	}
	if got := resolveAdultScenePoster(ctx, boxes, "stashdb", "sc1"); got != "https://1.1.1.1/stash.jpg" {
		t.Fatalf("stash image = %q", got)
	}
	if got := resolveAdultScenePoster(ctx, boxes, library.LocalSceneBox, "phash:x"); got != "" {
		t.Fatalf("local must skip, got %q", got)
	}
}
