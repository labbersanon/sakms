package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
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
			w.Write([]byte(`{"id":42,"title":"A Movie","overview":"A movie synopsis.","status":"Released","original_language":"en","runtime":100,"genres":[{"name":"Action"}],"production_companies":[{"name":"Studio One"}],"production_countries":[{"iso_3166_1":"US","name":"United States of America"}],"release_dates":{"results":[{"iso_3166_1":"US","release_dates":[{"type":4,"release_date":"2023-07-01T00:00:00.000Z"}]}]}}`))
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
			w.Write([]byte(`{"id":42,"name":"A Show","overview":"A show synopsis.","status":"Returning Series","original_language":"en","episode_run_time":[45],"genres":[{"name":"Drama"}],"networks":[{"name":"HBO"}],"production_countries":[{"iso_3166_1":"US","name":"United States of America"}]}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func detailMux(t *testing.T, tmdbURL string) *http.ServeMux {
	t.Helper()
	// The TMDB response-cache reset this helper used to do itself now lives in
	// overrideFixedURL's "tmdb" case (called below), so every internal/api test
	// gets it rather than only this file's. Do NOT re-add a local reset — one
	// mechanism, in one place. See that case's comment for the hazard.
	connStore, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbURL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbURL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/modes/{mode}/discover/detail", discoverDetailHandler(testHTTPClient(), connStore, nil, settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/discover/calendar", discoverCalendarHandler(testHTTPClient(), connStore, nil, settingsStore))
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
	if d.Overview != "A movie synopsis." {
		t.Errorf("overview not populated: %q", d.Overview)
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
	if d.Overview != "A show synopsis." {
		t.Errorf("TV overview not populated: %q", d.Overview)
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

// tmdbPathCounter records every path a fake TMDB server is asked for. It is
// mutex-guarded because the handler's season fan-out runs up to 6 goroutines
// concurrently against the same server — a bare map or counter here is a real
// data race, not a theoretical one.
type tmdbPathCounter struct {
	mu    sync.Mutex
	paths []string
}

func (c *tmdbPathCounter) record(p string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, p)
}

// count returns how many recorded paths contain sub.
func (c *tmdbPathCounter) count(sub string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, p := range c.paths {
		if strings.Contains(p, sub) {
			n++
		}
	}
	return n
}

// fakeTMDBSeasons serves a TV show whose /tv/{id} body carries a seasons[]
// array for every number in seasonNumbers (deliberately accepted UNSORTED, so
// a test can prove the handler sorts), plus a /tv/{id}/season/{n} endpoint per
// season and the four bundle endpoints. failSeason, when >= 0, makes that one
// season's endpoint 500 so the per-season soft-fail contract is assertable.
// The returned counter is how the eager-prefetch claim is checked: it is
// exactly the request log, not an inference from the response body.
func fakeTMDBSeasons(t *testing.T, seasonNumbers []int, failSeason int) (*httptest.Server, *tmdbPathCounter) {
	t.Helper()
	counter := &tmdbPathCounter{}
	seasonsJSON := make([]string, 0, len(seasonNumbers))
	for _, n := range seasonNumbers {
		seasonsJSON = append(seasonsJSON, fmt.Sprintf(
			`{"season_number":%d,"episode_count":2,"air_date":"2020-01-01","name":"Season %d","poster_path":"/s%d.jpg"}`, n, n, n))
	}
	detailsBody := fmt.Sprintf(
		`{"id":42,"name":"A Show","status":"Returning Series","original_language":"en","episode_run_time":[45],"genres":[{"name":"Drama"}],"networks":[{"name":"HBO"}],"seasons":[%s]}`,
		strings.Join(seasonsJSON, ","))

	mux := http.NewServeMux()
	mux.HandleFunc("/tv/", func(w http.ResponseWriter, r *http.Request) {
		counter.record(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/aggregate_credits"):
			w.Write([]byte(`{"cast":[{"name":"Lead","profile_path":"/a.jpg","roles":[{"character":"Hero"}]}],"crew":[]}`))
		case strings.HasSuffix(r.URL.Path, "/keywords"):
			w.Write([]byte(`{"results":[{"name":"heist"}]}`))
		case strings.HasSuffix(r.URL.Path, "/watch/providers"):
			w.Write([]byte(`{"results":{"US":{"flatrate":[{"provider_name":"Netflix","logo_path":"/nf.jpg"}]}}}`))
		case strings.HasSuffix(r.URL.Path, "/recommendations"):
			w.Write([]byte(`{"results":[{"id":99,"name":"Similar Show","poster_path":"/x.jpg","first_air_date":"2021-01-01","vote_average":6.6}]}`))
		case strings.Contains(r.URL.Path, "/season/"):
			// Must precede the details default: /tv/{id}/season/{n} would
			// otherwise be served the show body, which unmarshals into the
			// season response as zero episodes WITHOUT an error — a fixture bug
			// that reads exactly like a broken fan-out.
			idx := strings.LastIndex(r.URL.Path, "/season/")
			n, err := strconv.Atoi(r.URL.Path[idx+len("/season/"):])
			if err != nil {
				http.Error(w, "bad season", http.StatusBadRequest)
				return
			}
			if failSeason >= 0 && n == failSeason {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.Write([]byte(fmt.Sprintf(
				`{"episodes":[{"episode_number":1,"name":"S%dE1","air_date":"2020-02-01","runtime":45,"still_path":"/e1.jpg"},{"episode_number":2,"name":"S%dE2","air_date":"2020-02-08","runtime":44,"still_path":""}]}`, n, n)))
		default: // /tv/{id} details
			w.Write([]byte(detailsBody))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, counter
}

// TestDiscoverDetail_PrefetchesAllSeasons is the AC6 proof: "all seasons
// prefetched server-side on popup open" is checked against the fake TMDB
// server's own request log — exactly one /tv/{id}/season/{n} per season TMDB
// reported — not inferred from the response, so a lazy per-season design could
// not pass it. It also asserts the seasons come back sorted ascending
// (the fixture deliberately reports them out of order, Specials included).
//
// "All" means "all, up to maxPrefetchedSeasons" as of Phase-4 review FIX 5 —
// this fixture's 5 seasons are far below the cap, so this test is unaffected.
// TestDiscoverDetail_CapsSeasonPrefetch below covers the over-cap case.
func TestDiscoverDetail_PrefetchesAllSeasons(t *testing.T) {
	tmdbSrv, counter := fakeTMDBSeasons(t, []int{3, 1, 0, 4, 2}, -1)
	srv := httptest.NewServer(detailMux(t, tmdbSrv.URL))
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

	if got := counter.count("/season/"); got != 5 {
		t.Errorf("expected exactly 5 per-season fetches (one per season TMDB reported), got %d", got)
	}
	for _, n := range []int{0, 1, 2, 3, 4} {
		if got := counter.count(fmt.Sprintf("/season/%d", n)); got != 1 {
			t.Errorf("expected exactly 1 fetch for season %d, got %d", n, got)
		}
	}
	if len(d.Seasons) != 5 {
		t.Fatalf("expected 5 seasons in the response, got %d: %+v", len(d.Seasons), d.Seasons)
	}
	for i, s := range d.Seasons {
		if s.SeasonNumber != i {
			t.Errorf("seasons must be sorted by seasonNumber ascending (Specials first); index %d is season %d", i, s.SeasonNumber)
		}
		if len(s.Episodes) != 2 {
			t.Errorf("season %d episodes not prefetched: %+v", s.SeasonNumber, s.Episodes)
			continue
		}
		if s.Episodes[0].Name != fmt.Sprintf("S%dE1", s.SeasonNumber) || s.Episodes[0].Runtime != 45 || s.Episodes[0].StillPath != "/e1.jpg" {
			t.Errorf("season %d episode payload not mapped: %+v", s.SeasonNumber, s.Episodes[0])
		}
		if s.PosterPath != fmt.Sprintf("/s%d.jpg", s.SeasonNumber) || s.EpisodeCount != 2 || s.Name != fmt.Sprintf("Season %d", s.SeasonNumber) {
			t.Errorf("season %d summary fields not mapped: %+v", s.SeasonNumber, s)
		}
	}
}

// TestDiscoverDetail_CapsSeasonPrefetch is the Phase-4 FIX 5 proof: a show
// reporting more seasons than maxPrefetchedSeasons has its eager fan-out
// bounded, and the over-cap seasons are ABSENT from the response rather than
// present-but-empty.
//
// Both halves matter and neither implies the other. The request-count assertion
// is what proves the cost is actually avoided — a handler that fetched all 40
// and then truncated the DTO would satisfy the response-length check while
// still paying every TMDB round trip and still evicting every cache slot, which
// is the entire thing this cap exists to prevent. The fixture reports its
// seasons out of order (40 down to 1) so "the first 30" is proven to mean the
// 30 LOWEST season numbers, not the first 30 TMDB happened to list.
func TestDiscoverDetail_CapsSeasonPrefetch(t *testing.T) {
	seasons := make([]int, 0, 40)
	for n := 40; n >= 1; n-- {
		seasons = append(seasons, n)
	}
	tmdbSrv, counter := fakeTMDBSeasons(t, seasons, -1)
	srv := httptest.NewServer(detailMux(t, tmdbSrv.URL))
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

	// The cost bound: 30 season fetches fired, not 40.
	if got := counter.count("/season/"); got != maxPrefetchedSeasons {
		t.Errorf("expected exactly %d per-season fetches for a 40-season show, got %d — an over-cap season must never be fetched at all", maxPrefetchedSeasons, got)
	}
	if len(d.Seasons) != maxPrefetchedSeasons {
		t.Fatalf("expected %d seasons in the response, got %d", maxPrefetchedSeasons, len(d.Seasons))
	}

	// Seasons 1..30 present and ascending; 31..40 genuinely absent.
	for i, s := range d.Seasons {
		if s.SeasonNumber != i+1 {
			t.Fatalf("expected the %d lowest season numbers ascending; index %d is season %d", maxPrefetchedSeasons, i, s.SeasonNumber)
		}
		if len(s.Episodes) != 2 {
			t.Errorf("season %d episodes not prefetched: %+v", s.SeasonNumber, s.Episodes)
		}
	}
	for n := maxPrefetchedSeasons + 1; n <= 40; n++ {
		if got := counter.count(fmt.Sprintf("/season/%d", n)); got != 0 {
			t.Errorf("season %d is over the cap and must not be fetched, got %d fetches", n, got)
		}
	}
}

// TestDiscoverDetail_SeasonFetchSoftFails asserts the per-season soft-fail
// contract (the codebase's established per-item convention, same posture as
// this handler's existing five sub-calls): one season's endpoint failing
// leaves that season present with an EMPTY episode array — [] in the raw JSON,
// never null — and every sibling season fully populated, with the response
// still 200.
func TestDiscoverDetail_SeasonFetchSoftFails(t *testing.T) {
	tmdbSrv, _ := fakeTMDBSeasons(t, []int{1, 2, 3}, 2)
	srv := httptest.NewServer(detailMux(t, tmdbSrv.URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover/detail?tmdbId=42")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("one failing season must not fail the whole response; got %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(raw), `"episodes":[]`) {
		t.Errorf("a soft-failed season's episodes must serialize as [], never null; body: %s", raw)
	}
	var d apidto.TitleDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(d.Seasons) != 3 {
		t.Fatalf("the failed season must still be present as a card, got %d seasons", len(d.Seasons))
	}
	for _, s := range d.Seasons {
		want := 2
		if s.SeasonNumber == 2 {
			want = 0
		}
		if len(s.Episodes) != want {
			t.Errorf("season %d: expected %d episodes, got %d", s.SeasonNumber, want, len(s.Episodes))
		}
	}
}

// TestDiscoverDetail_SectionsSeasons_SkipsBundle asserts ?sections=seasons
// makes ZERO requests to the four bundle endpoints — the point of the param is
// that the card-level picker's lightweight fetch doesn't pay for credits,
// keywords, providers and recommendations it never renders. The extended
// details call still fires: it is where the season list itself comes from.
//
// Uses a tmdbId distinct from its sibling below: `sections` is a request-level
// detail the upstream TMDB traffic doesn't reflect, so it is not part of the
// package-level response cache's key and the two tests would otherwise be able
// to serve each other's bodies (overrideFixedURL's per-test cache reset is the
// primary guard; the distinct id is belt and braces).
func TestDiscoverDetail_SectionsSeasons_SkipsBundle(t *testing.T) {
	tmdbSrv, counter := fakeTMDBSeasons(t, []int{1, 2}, -1)
	srv := httptest.NewServer(detailMux(t, tmdbSrv.URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover/detail?tmdbId=4343&sections=seasons")
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

	for _, endpoint := range []string{"/aggregate_credits", "/keywords", "/watch/providers", "/recommendations"} {
		if got := counter.count(endpoint); got != 0 {
			t.Errorf("sections=seasons must issue ZERO requests to %s, got %d", endpoint, got)
		}
	}
	if got := counter.count("/season/"); got != 2 {
		t.Errorf("expected both seasons still prefetched under sections=seasons, got %d", got)
	}
	if len(d.Seasons) != 2 || len(d.Seasons[0].Episodes) != 2 {
		t.Errorf("seasons must still be populated under sections=seasons: %+v", d.Seasons)
	}
	if len(d.Cast) != 0 || len(d.Keywords) != 0 || len(d.WatchProviders) != 0 || len(d.Recommendations) != 0 {
		t.Errorf("skipped sections must be empty, not fetched: %+v", d)
	}
}

// TestDiscoverDetail_DefaultIncludesSeasonsAndBundle is the backward-compat
// proof for DetailPopup, which sends no `sections` param and needs everything:
// today's five sub-calls AND the new seasons block both fire on one request.
// See the sibling above for why the tmdbId differs.
func TestDiscoverDetail_DefaultIncludesSeasonsAndBundle(t *testing.T) {
	tmdbSrv, counter := fakeTMDBSeasons(t, []int{1, 2}, -1)
	srv := httptest.NewServer(detailMux(t, tmdbSrv.URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover/detail?tmdbId=4444")
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

	for _, endpoint := range []string{"/aggregate_credits", "/keywords", "/watch/providers", "/recommendations"} {
		if got := counter.count(endpoint); got != 1 {
			t.Errorf("the default (no sections param) must still fetch %s exactly once, got %d", endpoint, got)
		}
	}
	if got := counter.count("/season/"); got != 2 {
		t.Errorf("the default must ALSO prefetch every season, got %d season fetches", got)
	}
	if d.Status != "Returning Series" || len(d.Cast) != 1 || len(d.Keywords) != 1 || len(d.WatchProviders) != 1 || len(d.Recommendations) != 1 {
		t.Errorf("the existing five-way bundle must be unchanged by default: %+v", d)
	}
	if len(d.Seasons) != 2 || len(d.Seasons[1].Episodes) != 2 {
		t.Errorf("the seasons block must also be present by default: %+v", d.Seasons)
	}
}

// TestDiscoverDetail_MoviesSeasonsEmptyArray asserts a Movie serializes
// "seasons":[] rather than null. Asserted against the RAW BYTES on purpose:
// decoding a JSON null into []SeasonSummary yields nil, whose len() is also 0,
// so a struct-level check would pass vacuously against exactly the bug this
// guards (repo's never-null-array convention; the generated TS type is
// non-nullable).
func TestDiscoverDetail_MoviesSeasonsEmptyArray(t *testing.T) {
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
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(raw), `"seasons":[]`) {
		t.Errorf("a Movie must serialize seasons as [], never null; body: %s", raw)
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
