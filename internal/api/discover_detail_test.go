package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
)

// fakeTMDBDetail serves the five endpoints discoverDetailHandler fans out to
// for a Movie. keywordsStatus lets a test force ONE sub-call (keywords) to fail
// so the soft-fail contract can be asserted: the whole popup must still 200
// with every OTHER section populated and only the failed one empty.
func fakeTMDBDetail(t *testing.T, keywordsStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/movie/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/credits"):
			w.Write([]byte(`{"cast":[{"name":"Lead","character":"Hero","profile_path":"/a.jpg"}],"crew":[{"name":"Dir","job":"Director","department":"Directing","profile_path":"/d.jpg"}]}`))
		case strings.HasSuffix(r.URL.Path, "/keywords"):
			if keywordsStatus != http.StatusOK {
				http.Error(w, "boom", keywordsStatus)
				return
			}
			w.Write([]byte(`{"keywords":[{"name":"heist"}]}`))
		case strings.HasSuffix(r.URL.Path, "/watch/providers"):
			w.Write([]byte(`{"results":{"US":{"flatrate":[{"provider_name":"Netflix","logo_path":"/nf.jpg"}]}}}`))
		case strings.HasSuffix(r.URL.Path, "/recommendations"):
			w.Write([]byte(`{"results":[{"id":99,"title":"Similar","poster_path":"/x.jpg","release_date":"2021-01-01","vote_average":6.6}]}`))
		default: // /movie/{id} details
			w.Write([]byte(`{"id":42,"title":"A Movie","status":"Released","original_language":"en","runtime":100,"genres":[{"name":"Action"}],"production_companies":[{"name":"Studio One"}],"production_countries":[{"iso_3166_1":"US","name":"United States of America"}],"release_dates":{"results":[{"iso_3166_1":"US","release_dates":[{"type":4,"release_date":"2023-07-01T00:00:00.000Z"}]}]}}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeTMDBDetailTV is fakeTMDBDetail's TV sibling, serving /tv/ instead of
// /movie/ with TV's DIFFERENT response shapes (aggregate_credits' roles[]/
// jobs[] instead of flat character/job, keywords' results[] instead of
// keywords[], and TVDetails' networks/production_countries/episode_run_time
// fields) — exercising the movie-vs-TV divergence that fakeTMDBDetail alone
// never reaches, since every existing test in this file only calls the
// movies mode.
func fakeTMDBDetailTV(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/tv/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/aggregate_credits"):
			w.Write([]byte(`{"cast":[{"name":"Lead","profile_path":"/a.jpg","roles":[{"character":"Hero"}]}],"crew":[{"name":"Dir","department":"Directing","profile_path":"/d.jpg","jobs":[{"job":"Director"}]}]}`))
		case strings.HasSuffix(r.URL.Path, "/keywords"):
			w.Write([]byte(`{"results":[{"name":"heist"}]}`))
		case strings.HasSuffix(r.URL.Path, "/watch/providers"):
			w.Write([]byte(`{"results":{"US":{"flatrate":[{"provider_name":"Netflix","logo_path":"/nf.jpg"}]}}}`))
		case strings.HasSuffix(r.URL.Path, "/recommendations"):
			w.Write([]byte(`{"results":[{"id":99,"name":"Similar Show","poster_path":"/x.jpg","first_air_date":"2021-01-01","vote_average":6.6}]}`))
		default: // /tv/{id} details
			w.Write([]byte(`{"id":42,"name":"A Show","status":"Returning Series","original_language":"en","episode_run_time":[45],"genres":[{"name":"Drama"}],"networks":[{"name":"HBO"}],"production_countries":[{"iso_3166_1":"US","name":"United States of America"}]}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func detailMux(t *testing.T, tmdbURL string) *http.ServeMux {
	t.Helper()
	connStore, _, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbURL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbURL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/modes/{mode}/discover/detail", discoverDetailHandler(testHTTPClient(), connStore, settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/discover/calendar", discoverCalendarHandler(testHTTPClient(), connStore, settingsStore))
	return mux
}

func TestDiscoverDetailHandler_AllSectionsPopulated(t *testing.T) {
	srv := httptest.NewServer(detailMux(t, fakeTMDBDetail(t, http.StatusOK).URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/detail?tmdbId=42")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var d apidto.TitleDetail
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Status != "Released" || d.Runtime != 100 || d.ProductionCountry != "United States of America" {
		t.Errorf("extended details not populated: %+v", d)
	}
	if len(d.Cast) != 1 || len(d.Crew) != 1 || d.Crew[0].Job != "Director" {
		t.Errorf("credits not populated: cast=%+v crew=%+v", d.Cast, d.Crew)
	}
	if len(d.Keywords) != 1 || len(d.WatchProviders) != 1 || len(d.Recommendations) != 1 {
		t.Errorf("keywords/providers/recommendations not populated: %+v", d)
	}
}

// TestDiscoverDetailHandler_SoftFailsOneSection is the F1 acceptance criterion:
// one sub-call failing (keywords 500) degrades to an empty keyword section, and
// the whole handler still returns 200 with every other section intact — never a
// popup-wide 500.
func TestDiscoverDetailHandler_SoftFailsOneSection(t *testing.T) {
	srv := httptest.NewServer(detailMux(t, fakeTMDBDetail(t, http.StatusInternalServerError).URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/detail?tmdbId=42")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a single failing sub-call must not 500 the whole popup; got %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	// A soft-failed section must be an empty JSON array, never null (repo's
	// never-null-array convention; the generated TS type is non-nullable).
	if !strings.Contains(string(raw), `"keywords":[]`) {
		t.Errorf("expected keywords to serialize as [] after soft-fail, got body: %s", raw)
	}
	// A Movie has no networks — that type-absent section must also be [], not null.
	if !strings.Contains(string(raw), `"networks":[]`) {
		t.Errorf("expected networks to serialize as [] for a movie, got body: %s", raw)
	}
	var d apidto.TitleDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(d.Keywords) != 0 {
		t.Errorf("expected empty keyword section after its sub-call failed, got %v", d.Keywords)
	}
	// The other sections must be unaffected by the keyword failure.
	if d.Status != "Released" || len(d.Cast) != 1 || len(d.WatchProviders) != 1 || len(d.Recommendations) != 1 {
		t.Errorf("sibling sections wrongly affected by keyword failure: %+v", d)
	}
}

// TestDiscoverDetailHandler_SeriesUsesTVShapes is the handler-layer sibling of
// TestDiscoverDetailHandler_AllSectionsPopulated for Series mode — every prior
// test in this file exercises Movies only, so this is the first assertion
// that discoverDetailHandler actually dispatches to the TV sub-calls
// (TVAggregateFullCredits/TVKeywords/TVWatchProviders/TVRecommendations/
// TVDetails) rather than the movie ones, and that TV's DIFFERENT response
// shapes (aggregate_credits roles[]/jobs[], keywords results[], and
// Networks — the field TVDetails previously had NO way to populate at all)
// decode correctly end-to-end through the DTO.
func TestDiscoverDetailHandler_SeriesUsesTVShapes(t *testing.T) {
	srv := httptest.NewServer(detailMux(t, fakeTMDBDetailTV(t).URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover/detail?tmdbId=42")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var d apidto.TitleDetail
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Status != "Returning Series" || d.Runtime != 45 {
		t.Errorf("TV extended details not populated: %+v", d)
	}
	if len(d.Networks) != 1 || d.Networks[0] != "HBO" {
		t.Errorf("Networks — the field TVDetails had no way to populate before this feature — not populated: %+v", d.Networks)
	}
	// TV's roles[]/jobs[] shape, not a movie's flat character/job field.
	if len(d.Cast) != 1 || d.Cast[0].Character != "Hero" || len(d.Crew) != 1 || d.Crew[0].Job != "Director" {
		t.Errorf("TV aggregate_credits roles[]/jobs[] shape not decoded correctly: cast=%+v crew=%+v", d.Cast, d.Crew)
	}
	// TV keywords' results[] shape, not a movie's keywords[] shape.
	if len(d.Keywords) != 1 || d.Keywords[0] != "heist" {
		t.Errorf("TV keywords results[] shape not decoded correctly: %+v", d.Keywords)
	}
	if len(d.Recommendations) != 1 || d.Recommendations[0].Title != "Similar Show" {
		t.Errorf("TV recommendations (name field, not title) not decoded correctly: %+v", d.Recommendations)
	}
}

func TestDiscoverDetailHandler_AdultRejected(t *testing.T) {
	srv := httptest.NewServer(detailMux(t, fakeTMDBDetail(t, http.StatusOK).URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/detail?tmdbId=42")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for adult (no TMDB id), got %d", resp.StatusCode)
	}
}

// TestDiscoverCalendarHandler_DateRange asserts the calendar handler threads the
// from/to window into the correct movie date-range query params (and never
// through the unreleased-hiding filter).
func TestDiscoverCalendarHandler_DateRange(t *testing.T) {
	var lastQuery url.Values
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"Upcoming","poster_path":"/p.jpg","release_date":"2026-07-15","vote_average":0}]}`))
	}))
	defer tmdb.Close()

	srv := httptest.NewServer(detailMux(t, tmdb.URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/calendar?from=2026-07-01&to=2026-07-31")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var items []apidto.DiscoverItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Upcoming" {
		t.Errorf("unexpected items: %+v", items)
	}
	if lastQuery.Get("primary_release_date.gte") != "2026-07-01" || lastQuery.Get("primary_release_date.lte") != "2026-07-31" {
		t.Errorf("expected the from/to window as a movie date range, got %v", lastQuery)
	}
}

// TestDiscoverCalendarHandler_SeriesDateRange is TestDiscoverCalendarHandler_
// DateRange's Series sibling — asserts the calendar handler threads the
// from/to window into first_air_date.gte/.lte (TV premieres), not
// primary_release_date (movies) — the two are genuinely different TMDB query
// params, and every other calendar test in this file only exercises movies.
func TestDiscoverCalendarHandler_SeriesDateRange(t *testing.T) {
	var lastQuery url.Values
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"name":"New Show","poster_path":"/p.jpg","first_air_date":"2026-07-15","vote_average":0}]}`))
	}))
	defer tmdb.Close()

	srv := httptest.NewServer(detailMux(t, tmdb.URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover/calendar?from=2026-07-01&to=2026-07-31")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var items []apidto.DiscoverItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 1 || items[0].Title != "New Show" {
		t.Errorf("unexpected items: %+v", items)
	}
	if lastQuery.Get("first_air_date.gte") != "2026-07-01" || lastQuery.Get("first_air_date.lte") != "2026-07-31" {
		t.Errorf("expected the from/to window as a TV premiere date range (first_air_date), got %v", lastQuery)
	}
	if lastQuery.Get("primary_release_date.gte") != "" {
		t.Errorf("Series calendar must not send the movie date-range param, got %v", lastQuery)
	}
}

func TestDiscoverCalendarHandler_MissingParams(t *testing.T) {
	srv := httptest.NewServer(detailMux(t, fakeTMDBDetail(t, http.StatusOK).URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/calendar?from=2026-07-01")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 when 'to' is missing, got %d", resp.StatusCode)
	}
}
