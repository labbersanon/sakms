package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/disc"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/websearch"
)

func TestOrganizeDiscIdentify_RejectsNonDisc(t *testing.T) {
	tmp := t.TempDir()
	mkv := filepath.Join(tmp, "a.mkv")
	if err := os.WriteFile(mkv, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)
	body, _ := json.Marshal(apidto.OrganizeDiscIdentifyRequest{Path: mkv})
	rr := httptest.NewRecorder()
	organizeDiscIdentifyHandler(nil, nil, nil, nil, nil)(rr, httptest.NewRequest(http.MethodPost, "/api/organize/discs/identify", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
}

func TestOrganizeDiscIdentify_ReturnsWorks(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "show.iso")
	if err := os.WriteFile(src, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)
	orig := inspectDiscFn
	t.Cleanup(func() { inspectDiscFn = orig })
	inspectDiscFn = func(ctx context.Context, path string) (*disc.Map, error) {
		return &disc.Map{
			Volume: "LOONEY_TUNES_GOLDEN_V5_D1",
			Source: path,
			Titles: []disc.Title{
				{N: 1, DurationS: 6386, Chapters: 15},
				{N: 2, DurationS: 433},
				{N: 3, DurationS: 400},
			},
		}, nil
	}
	body, _ := json.Marshal(apidto.OrganizeDiscIdentifyRequest{Path: src})
	rr := httptest.NewRecorder()
	organizeDiscIdentifyHandler(nil, nil, nil, nil, nil)(rr, httptest.NewRequest(http.MethodPost, "/api/organize/discs/identify", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
	var resp apidto.OrganizeDiscIdentifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Volume != "LOONEY_TUNES_GOLDEN_V5_D1" {
		t.Fatalf("volume = %q", resp.Volume)
	}
	if len(resp.Works) != 2 {
		t.Fatalf("works = %+v, want 2 shorts", resp.Works)
	}
	if len(resp.Queries) == 0 {
		t.Fatal("expected search queries")
	}
}

func TestOrganizeDiscIdentify_FillsWikipediaNames(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "show.iso")
	if err := os.WriteFile(src, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)
	origInspect := inspectDiscFn
	origLookup := lookupDiscTOCFn
	t.Cleanup(func() {
		inspectDiscFn = origInspect
		lookupDiscTOCFn = origLookup
	})
	inspectDiscFn = func(ctx context.Context, path string) (*disc.Map, error) {
		return &disc.Map{
			Volume: "LOONEY_TUNES_GOLDEN_V5_D1",
			Source: path,
			Titles: []disc.Title{
				{N: 1, DurationS: 6386, Chapters: 15},
				{N: 2, DurationS: 433},
				{N: 3, DurationS: 400},
			},
		}, nil
	}
	lookupDiscTOCFn = func(ctx context.Context, hc *http.Client, volume, iso string, want int, extra []string) []string {
		if want != 2 {
			t.Fatalf("want = %d", want)
		}
		return []string{"14 Carrot Rabbit", "Ali Baba Bunny"}
	}
	body, _ := json.Marshal(apidto.OrganizeDiscIdentifyRequest{Path: src})
	rr := httptest.NewRecorder()
	organizeDiscIdentifyHandler(nil, nil, nil, nil, nil)(rr, httptest.NewRequest(http.MethodPost, "/api/organize/discs/identify", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
	var resp apidto.OrganizeDiscIdentifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Works) != 2 {
		t.Fatalf("works = %+v", resp.Works)
	}
	if resp.Works[0].EpisodeTitle != "14 Carrot Rabbit" || resp.Works[1].EpisodeTitle != "Ali Baba Bunny" {
		t.Fatalf("names = %+v", resp.Works)
	}
}

func TestWikiURLsFromSearch_KeepsWikipediaArticle(t *testing.T) {
	got := wikiURLsFromSearch([]websearch.Result{
		{URL: "https://example.com/looney"},
		{URL: "https://en.wikipedia.org/wiki/Looney_Tunes_Golden_Collection:_Volume_5"},
		{URL: "https://en.wikipedia.org/wiki/File:Cover.jpg"},
	})
	if len(got) != 1 || got[0] != "https://en.wikipedia.org/wiki/Looney_Tunes_Golden_Collection:_Volume_5" {
		t.Fatalf("got %v", got)
	}
}

func TestDiscOnlyNames(t *testing.T) {
	got := discOnlyNames([]apidto.OrganizeDiscUnpackItem{
		{Name: "t02", Conflict: "replace"},
		{Name: ""},
		{Name: "t03"},
	})
	if len(got) != 2 || got[0] != "t02" || got[1] != "t03" {
		t.Fatalf("names = %v", got)
	}
}

func TestOrganizeDiscUnpack_PassesOnlyNames(t *testing.T) {
	resetDiscJobForTest()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "show.iso")
	if err := os.WriteFile(src, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "show - t02.mkv")
	withBrowsableRoot(t, tmp)

	orig := unpackDiscFn
	t.Cleanup(func() { unpackDiscFn = orig })
	var gotNames []string
	unpackDiscFn = func(ctx context.Context, path string, opts disc.Options) (*disc.Result, error) {
		gotNames = append([]string(nil), opts.OnlyNames...)
		if opts.OnProgress != nil {
			opts.OnProgress(1, 1)
		}
		if err := os.WriteFile(out, []byte("mkv"), 0o644); err != nil {
			return nil, err
		}
		return &disc.Result{
			Map:     &disc.Map{Volume: "SHOW", Source: path},
			Outputs: []string{out},
		}, nil
	}

	body, _ := json.Marshal(apidto.OrganizeDiscUnpackRequest{
		Path: src,
		Items: []apidto.OrganizeDiscUnpackItem{
			{Name: "t02", Conflict: "keep_both"},
		},
	})
	rr := httptest.NewRecorder()
	organizeDiscUnpackStartHandler(discUnpackDeps{})(rr, httptest.NewRequest(http.MethodPost, "/api/organize/discs/extract", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	var st apidto.OrganizeDiscUnpackStatus
	for time.Now().Before(deadline) {
		gr := httptest.NewRecorder()
		organizeDiscUnpackStatusHandler()(gr, httptest.NewRequest(http.MethodGet, "/api/organize/discs/extract?path="+src, nil))
		if err := json.Unmarshal(gr.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		if st.Status == "done" || st.Status == "error" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.Status != "done" {
		t.Fatalf("status = %+v", st)
	}
	if len(gotNames) != 1 || gotNames[0] != "t02" {
		t.Fatalf("OnlyNames = %v", gotNames)
	}
}

func TestDiscWorkNameFromOutput(t *testing.T) {
	if got := discWorkNameFromOutput("/media/LOONEY_TUNES_GOLDEN - t02.mkv"); got != "t02" {
		t.Fatalf("got %q", got)
	}
	if got := discWorkNameFromOutput("/media/SHOW - t01c07.mkv"); got != "t01c07" {
		t.Fatalf("got %q", got)
	}
}

func TestOrganizeDiscUnpack_ImportsAssignedThenDeletesISO(t *testing.T) {
	resetDiscJobForTest()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "show.iso")
	if err := os.WriteFile(src, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "SHOW - t02.mkv")
	dest := filepath.Join(tmp, "library", "Show S01E04.mkv")
	withBrowsableRoot(t, tmp)

	origUnpack := unpackDiscFn
	origImport := importDiscOutputsFn
	t.Cleanup(func() {
		unpackDiscFn = origUnpack
		importDiscOutputsFn = origImport
	})
	unpackDiscFn = func(ctx context.Context, path string, opts disc.Options) (*disc.Result, error) {
		if !opts.SkipDelete {
			t.Fatal("expected SkipDelete when importing")
		}
		if err := os.WriteFile(out, []byte("mkv"), 0o644); err != nil {
			return nil, err
		}
		return &disc.Result{
			Map:     &disc.Map{Volume: "SHOW", Source: path},
			Outputs: []string{out},
		}, nil
	}
	importDiscOutputsFn = func(ctx context.Context, deps discUnpackDeps, iso string, req apidto.OrganizeDiscUnpackRequest, outputs []string) ([]string, error) {
		if req.TMDBID != 99 || req.Items[0].EpisodeNumber != 4 {
			t.Fatalf("req = %+v", req)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, err
		}
		if err := os.Rename(outputs[0], dest); err != nil {
			return nil, err
		}
		return []string{dest}, nil
	}

	body, _ := json.Marshal(apidto.OrganizeDiscUnpackRequest{
		Path: src, Mode: "series", TMDBID: 99, Title: "Show", Year: 1990,
		Items: []apidto.OrganizeDiscUnpackItem{
			{Name: "t02", SeasonNumber: 1, EpisodeNumber: 4, EpisodeTitle: "Short"},
		},
	})
	rr := httptest.NewRecorder()
	organizeDiscUnpackStartHandler(discUnpackDeps{settingsStore: &settings.Store{}})(rr, httptest.NewRequest(http.MethodPost, "/api/organize/discs/extract", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	var st apidto.OrganizeDiscUnpackStatus
	for time.Now().Before(deadline) {
		gr := httptest.NewRecorder()
		organizeDiscUnpackStatusHandler()(gr, httptest.NewRequest(http.MethodGet, "/api/organize/discs/extract?path="+src, nil))
		if err := json.Unmarshal(gr.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		if st.Status == "done" || st.Status == "error" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.Status != "done" {
		t.Fatalf("status = %+v", st)
	}
	if !st.DeletedSource {
		t.Fatal("expected ISO deleted after import")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("ISO still on disk: %v", err)
	}
	if len(st.Outputs) != 1 || st.Outputs[0] != dest {
		t.Fatalf("outputs = %v", st.Outputs)
	}
}

func TestParseDiscMode(t *testing.T) {
	if got := parseDiscMode(""); got != "movies" {
		t.Fatalf("empty = %q", got)
	}
	if got := parseDiscMode("series"); got != "series" {
		t.Fatalf("series = %q", got)
	}
	if got := parseDiscMode("adult"); got != "adult" {
		t.Fatalf("adult = %q", got)
	}
	if got := parseDiscMode("nope"); got != "movies" {
		t.Fatalf("unknown = %q", got)
	}
}

func TestDiscHasCatalog(t *testing.T) {
	if discHasCatalog(apidto.OrganizeDiscUnpackRequest{Mode: "movies"}) {
		t.Fatal("movies without tmdb should not import")
	}
	if !discHasCatalog(apidto.OrganizeDiscUnpackRequest{Mode: "movies", TMDBID: 1}) {
		t.Fatal("movies with tmdb should import")
	}
	if !discHasCatalog(apidto.OrganizeDiscUnpackRequest{
		Mode:  "movies",
		Items: []apidto.OrganizeDiscUnpackItem{{Name: "t02", TMDBID: 101}},
	}) {
		t.Fatal("movies with per-item tmdb should import")
	}
	if discHasCatalog(apidto.OrganizeDiscUnpackRequest{Mode: "adult", TMDBID: 1}) {
		t.Fatal("adult must not import on tmdb id alone")
	}
	if !discHasCatalog(apidto.OrganizeDiscUnpackRequest{Mode: "adult", Box: "tpdb", SceneID: "9"}) {
		t.Fatal("adult with box+scene should import")
	}
}

func TestUniqueMovieByTitle(t *testing.T) {
	got, ok := uniqueMovieByTitle("The Daffy Doc", []tmdb.Item{
		{ID: 419819, Title: "Looney Tunes Golden Collection, Vol. 5"},
		{ID: 101, Title: "The Daffy Doc", ReleaseDate: "1938-11-26"},
	})
	if !ok || got.ID != 101 {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	_, ok = uniqueMovieByTitle("Duck Soup", []tmdb.Item{
		{ID: 1, Title: "Duck Soup"},
		{ID: 2, Title: "Duck Soup"},
	})
	if ok {
		t.Fatal("remakes must not pick a year")
	}
}

func TestShouldSkipSharedMovieImport(t *testing.T) {
	req := apidto.OrganizeDiscUnpackRequest{
		Mode: "movies", TMDBID: 419819, Title: "Vol. 5",
		Items: []apidto.OrganizeDiscUnpackItem{
			{Name: "t02"},
			{Name: "t03"},
		},
	}
	if !shouldSkipSharedMovieImport(req, req.Items[0]) {
		t.Fatal("two untitled items must not share the volume movie id")
	}
	req.Items[0].TMDBID = 101
	req.Items[0].Title = "The Daffy Doc"
	if shouldSkipSharedMovieImport(req, req.Items[0]) {
		t.Fatal("per-title id must import")
	}
	single := apidto.OrganizeDiscUnpackRequest{
		Mode: "movies", TMDBID: 603, Title: "The Matrix",
		Items: []apidto.OrganizeDiscUnpackItem{{Name: "t01"}},
	}
	if shouldSkipSharedMovieImport(single, single.Items[0]) {
		t.Fatal("single-feature job id must import")
	}
}

func TestDiscMovieIdentity_PrefersItem(t *testing.T) {
	req := apidto.OrganizeDiscUnpackRequest{TMDBID: 419819, Title: "Vol. 5", Year: 2007}
	it := apidto.OrganizeDiscUnpackItem{TMDBID: 101, Title: "The Daffy Doc", Year: 1938}
	id, title, year := discMovieIdentity(req, it)
	if id != 101 || title != "The Daffy Doc" || year != 1938 {
		t.Fatalf("id=%d title=%q year=%d", id, title, year)
	}
}

func TestAttachDiscWorkMovies_NamesEachShort(t *testing.T) {
	searcher := movieSearchMap{
		"The Daffy Doc":  {{ID: 101, Title: "The Daffy Doc", ReleaseDate: "1938-11-26"}},
		"Bacall to Arms": {{ID: 102, Title: "Bacall to Arms", ReleaseDate: "1946-08-03"}},
	}
	works := []apidto.OrganizeDiscWork{
		{Name: "t02", EpisodeTitle: "The Daffy Doc"},
		{Name: "t03", EpisodeTitle: "Bacall to Arms"},
	}
	attachDiscWorkMovies(context.Background(), searcher, nil, works)
	if works[0].TMDBID != 101 || works[0].CatalogTitle != "The Daffy Doc" || works[0].Year != 1938 {
		t.Fatalf("t02 = %+v", works[0])
	}
	if works[1].TMDBID != 102 || works[1].CatalogTitle != "Bacall to Arms" {
		t.Fatalf("t03 = %+v", works[1])
	}
}

func TestAttachDiscWorkMovies_SingleFeatureKeepsVolume(t *testing.T) {
	searcher := movieSearchMap{
		"The Matrix": {{ID: 603, Title: "The Matrix", ReleaseDate: "1999-03-31"}},
	}
	works := []apidto.OrganizeDiscWork{
		{Name: "t01", EpisodeTitle: "The Matrix"},
	}
	attachDiscWorkMovies(context.Background(), searcher, nil, works)
	if works[0].TMDBID != 0 {
		t.Fatalf("single feature must keep the volume hit, got %+v", works[0])
	}
}

type movieSearchMap map[string][]tmdb.Item

func (m movieSearchMap) SearchMovies(ctx context.Context, query string) ([]tmdb.Item, error) {
	return m[query], nil
}
