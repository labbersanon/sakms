package disc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLookupTOC_ExactCountFromWikipedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "query":
			w.Write([]byte(`{"query":{"search":[{"title":"Looney Tunes Golden Collection: Volume 5"}]}}`))
		case "parse":
			w.Write(wikiJSON(goldenWiki))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	orig := wikiAPIURL
	t.Cleanup(func() { wikiAPIURL = orig })
	wikiAPIURL = srv.URL

	got := LookupTOC(context.Background(), srv.Client(), "LOONEY_TUNES_GOLDEN_V5_D1", "x.iso", 4, nil)
	if len(got) != 4 || got[0] != "14 Carrot Rabbit" {
		t.Fatalf("got %v", got)
	}
}

func TestLookupTOC_SearXNGURLWhenSearchEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "query" {
			w.Write([]byte(`{"query":{"search":[]}}`))
			return
		}
		if r.URL.Query().Get("page") != "Looney Tunes Golden Collection: Volume 5" {
			http.NotFound(w, r)
			return
		}
		w.Write(wikiJSON(goldenWiki))
	}))
	t.Cleanup(srv.Close)
	orig := wikiAPIURL
	t.Cleanup(func() { wikiAPIURL = orig })
	wikiAPIURL = srv.URL

	extra := []string{"https://en.wikipedia.org/wiki/Looney_Tunes_Golden_Collection:_Volume_5"}
	got := LookupTOC(context.Background(), srv.Client(), "LOONEY_TUNES_GOLDEN_V5_D1", "x.iso", 4, extra)
	if len(got) != 4 || got[2] != "Transylvania 6-5000" {
		t.Fatalf("got %v", got)
	}
}

func TestLookupTOC_WrongCountDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(wikiJSON(goldenWiki))
	}))
	t.Cleanup(srv.Close)
	orig := wikiAPIURL
	t.Cleanup(func() { wikiAPIURL = orig })
	wikiAPIURL = srv.URL

	got := LookupTOC(context.Background(), srv.Client(), "V5_D1", "x.iso", 15, []string{
		"https://en.wikipedia.org/wiki/Looney_Tunes_Golden_Collection:_Volume_5",
	})
	if got != nil {
		t.Fatalf("count mismatch must drop, got %v", got)
	}
}

func TestProbeNamed_RejectsVolumeLabel(t *testing.T) {
	titles := []Title{
		{N: 1, DurationS: 5400, Name: "SHOW_DVD"},
	}
	if ProbeNamed(titles, "SHOW_DVD") {
		t.Fatal("volume label is not a title name")
	}
}

func TestAttachTOCNames_WritesSidecar(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "show.iso")
	if err := os.WriteFile(src, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Map{
		Volume: "SHOW",
		Source: src,
		Titles: []Title{
			{N: 1, DurationS: 6386, Chapters: 15},
			{N: 2, DurationS: 433},
			{N: 3, DurationS: 400},
		},
	}
	names := []string{"14 Carrot Rabbit", "Ali Baba Bunny"}
	if !AttachTOCNames(m, names) {
		t.Fatal("attach failed")
	}
	got := TOCForWorks(m)
	if len(got) != 2 || got[0] != names[0] {
		t.Fatalf("TOC = %v", got)
	}
	disk, err := readSidecar(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.TOC) != 2 {
		t.Fatalf("sidecar TOC = %v", disk.TOC)
	}
}

func TestWikiPageFromURL(t *testing.T) {
	got := wikiPageFromURL("https://en.wikipedia.org/wiki/Looney_Tunes_Golden_Collection:_Volume_5#Disc_1")
	if got != "Looney Tunes Golden Collection: Volume 5" {
		t.Fatalf("page = %q", got)
	}
	if wikiPageFromURL("https://en.wikipedia.org/wiki/File:Cover.jpg") != "" {
		t.Fatal("file page must be rejected")
	}
}

func wikiJSON(wikitext string) []byte {
	b, err := json.Marshal(map[string]any{"parse": map[string]any{"wikitext": wikitext}})
	if err != nil {
		panic(err)
	}
	return b
}
