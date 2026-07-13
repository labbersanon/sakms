package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/apidto"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/quality"
)

// fakeTMDBMovieRuntime serves /movie/{id} with a real runtime — the autograb
// Movies path needs it as the bitrate scorer's denominator (fakeTMDBServer in
// availability_test.go omits runtime, which would force every candidate to
// unknown-bitrate).
func fakeTMDBMovieRuntime(t *testing.T, runtimeMinutes int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/movie/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": 42, "title": "Some Movie", "imdb_id": "tt1234567", "runtime": runtimeMinutes,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestAutoGrabHandler_Movies_QualifiedGrabsExactlyOne is the qualified path:
// a healthy, high-bitrate release clears the floor, so auto-grab sends it to
// qBittorrent (exactly once — the backend no-bulk proof) and records exactly
// one grab. No manual release-pick happens; that is the whole point.
func TestAutoGrabHandler_Movies_QualifiedGrabsExactlyOne(t *testing.T) {
	var qbAdds int32
	fakeQB := fakeQBittorrent(t, func(r *http.Request) { atomic.AddInt32(&qbAdds, 1) })
	tmdbSrv := fakeTMDBMovieRuntime(t, 100) // 100 min = 6000 s
	// 8 GB / 6000 s ≈ 10.7 Mbps implied; x265 → ~21 Mbps x264-equiv; clears
	// every 1080p tier floor even after the 25% non-AV1 padding.
	prowlarr := fakeProwlarr(t, `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Movies), string(quality.Low)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/movies"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	body, _ := json.Marshal(apidto.AutoGrabRequest{Title: "Some Movie", TMDBID: 42})
	resp, err := http.Post(srv.URL+"/api/modes/movies/autograb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out apidto.AutoGrabResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !out.Grabbed || out.Fallback || out.Grab == nil {
		t.Fatalf("expected a qualified grab, got %+v", out)
	}
	if out.Grab.DownloadClient != "qbittorrent" || out.Grab.RootFolderPath != "/movies" {
		t.Errorf("unexpected grab: %+v", out.Grab)
	}
	if got := atomic.LoadInt32(&qbAdds); got != 1 {
		t.Errorf("expected exactly ONE download-client add (no bulk), got %d", got)
	}
	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected exactly one recorded grab, got %d", len(list))
	}
}

// TestAutoGrabHandler_Movies_FallbackGrabsNothing is the fallback path: a
// tiny, mislabeled-looking release clears nothing, so auto-grab must NOT touch
// the download client and must return the ranked manual pick list instead of
// grabbing the least-bad option.
func TestAutoGrabHandler_Movies_FallbackGrabsNothing(t *testing.T) {
	var qbAdds int32
	fakeQB := fakeQBittorrent(t, func(r *http.Request) { atomic.AddInt32(&qbAdds, 1) })
	tmdbSrv := fakeTMDBMovieRuntime(t, 100)
	// size:1 → an absurdly low implied bitrate for a "1080p" release → the
	// pre-grab mislabel check excludes it; nothing qualifies.
	prowlarr := fakeProwlarr(t, `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"BadIndexer","protocol":"torrent","size":1,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/movies"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	body, _ := json.Marshal(apidto.AutoGrabRequest{Title: "Some Movie", TMDBID: 42})
	resp, err := http.Post(srv.URL+"/api/modes/movies/autograb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out apidto.AutoGrabResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Grabbed || !out.Fallback || out.Grab != nil {
		t.Fatalf("expected a fallback (no auto-grab), got %+v", out)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].Qualified {
		t.Errorf("expected one non-qualified candidate in the pick list, got %+v", out.Candidates)
	}
	if got := atomic.LoadInt32(&qbAdds); got != 0 {
		t.Errorf("expected ZERO download-client adds on fallback, got %d", got)
	}
	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected zero recorded grabs on fallback, got %d", len(list))
	}
}

// fakeTMDBSeriesRuntime serves the two TMDB endpoints the Series autograb path
// needs: /tv/{id}/external_ids (tvdb_id for the id-scoped Prowlarr search) and
// /tv/{id}/season/{n} (the whole-season episode list carrying per-episode
// runtime). The single episode episodeNumber is given runtimeMinutes; that's
// what the bitrate scorer divides the release size by for a single-episode grab.
func fakeTMDBSeriesRuntime(t *testing.T, episodeNumber, runtimeMinutes int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/tv/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/external_ids") {
			w.Write([]byte(`{"tvdb_id":789}`))
			return
		}
		// /tv/{id}/season/{n} → episodes with per-episode runtime.
		json.NewEncoder(w).Encode(map[string]any{
			"episodes": []map[string]any{
				{"episode_number": episodeNumber, "name": "Ep", "air_date": "2022-01-01", "runtime": runtimeMinutes},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestAutoGrabHandler_Series_SingleEpisodeQualifies is the payoff of wiring
// per-episode TMDB runtime into Series: a single healthy 1080p episode now gets
// a real implied bitrate, clears the floor, and genuinely auto-grabs — no
// longer stuck always falling to the manual list. Critically, the same result
// list also contains a season pack (a whole-season file indexers routinely
// return for an episode query); the pack must NOT be the pick, even though its
// far larger size would score highest if the single-episode runtime were
// (wrongly) applied to it. Proves both the qualification AND the season-pack
// neutralization guard.
func TestAutoGrabHandler_Series_SingleEpisodeQualifies(t *testing.T) {
	var qbAdds int32
	fakeQB := fakeQBittorrent(t, func(r *http.Request) { atomic.AddInt32(&qbAdds, 1) })
	tmdbSrv := fakeTMDBSeriesRuntime(t, 5, 58) // episode 5, 58 min = 3480 s
	// Single episode: 900 MB / 3480 s ≈ 2.07 Mbps implied; x265 → ~4.1 Mbps
	// x264-equiv → ~3.3 after the 25% non-AV1 padding; clears the Low 1080p
	// floor (2). Season pack: 12 GB — if the single-episode runtime were applied
	// to it, ~27 Mbps implied → ~44 x264-equiv, which would out-score the
	// episode and be picked. The neutralization guard forces the pack to
	// unknown-bitrate so the episode wins.
	prowlarr := fakeProwlarr(t, `[
	  {"guid":"1","title":"Some.Show.S03.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":12000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:AAAAAA1234567890abcdef1234567890abcdef12"},
	  {"guid":"2","title":"Some.Show.S03E05.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":900000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:BBBBBB1234567890abcdef1234567890abcdef12"}
	]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Series), string(quality.Low)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, "/series"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	body, _ := json.Marshal(apidto.AutoGrabRequest{Title: "Some Show", TMDBID: 100, SeasonNumber: 3, EpisodeNumber: 5, SeasonSpecified: true})
	resp, err := http.Post(srv.URL+"/api/modes/series/autograb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out apidto.AutoGrabResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !out.Grabbed || out.Fallback || out.Grab == nil {
		t.Fatalf("expected a qualified single-episode grab, got %+v", out)
	}
	if !strings.Contains(out.Message, "S03E05") {
		t.Errorf("expected the single episode (S03E05) to be picked, not the season pack: %q", out.Message)
	}
	if strings.Contains(out.Message, "S03.1080p") {
		t.Errorf("season pack was auto-grabbed under the single-episode runtime — neutralization failed: %q", out.Message)
	}
	if got := atomic.LoadInt32(&qbAdds); got != 1 {
		t.Errorf("expected exactly ONE download-client add (no bulk), got %d", got)
	}
	list, err := grabsStore.List(ctx, mode.Series)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected exactly one recorded grab, got %d", len(list))
	}
}

// TestAutoGrabHandler_Series_SeasonPackGrabFallsBack proves the deliberate
// design decision: a whole-season grab (EpisodeNumber 0) resolves NO runtime
// (a season pack's per-file bitrate is ambiguous), so every candidate is
// unknown-bitrate and the call hands back the manual pick list rather than
// auto-grabbing.
func TestAutoGrabHandler_Series_SeasonPackGrabFallsBack(t *testing.T) {
	var qbAdds int32
	fakeQB := fakeQBittorrent(t, func(r *http.Request) { atomic.AddInt32(&qbAdds, 1) })
	tmdbSrv := fakeTMDBSeriesRuntime(t, 5, 58)
	prowlarr := fakeProwlarr(t, `[{"guid":"1","title":"Some.Show.S03.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":12000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:AAAAAA1234567890abcdef1234567890abcdef12"}]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Series), string(quality.Low)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	// EpisodeNumber omitted → whole-season grab.
	body, _ := json.Marshal(apidto.AutoGrabRequest{Title: "Some Show", TMDBID: 100, SeasonNumber: 3, SeasonSpecified: true})
	resp, err := http.Post(srv.URL+"/api/modes/series/autograb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out apidto.AutoGrabResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Grabbed || !out.Fallback || len(out.Candidates) != 1 {
		t.Fatalf("expected a season-pack grab to fall back to the pick list, got %+v", out)
	}
	if out.Candidates[0].Status != "unknown-bitrate" {
		t.Errorf("expected unknown-bitrate for a season-pack grab, got %q", out.Candidates[0].Status)
	}
	if got := atomic.LoadInt32(&qbAdds); got != 0 {
		t.Errorf("expected zero download-client adds for a season-pack fallback, got %d", got)
	}
}

// TestIsSeasonPackTitle covers the pack/single-episode classifier that guards
// a single-episode grab from over-grabbing a season pack.
func TestIsSeasonPackTitle(t *testing.T) {
	cases := []struct {
		title string
		pack  bool
	}{
		{"Some.Show.S03E05.1080p.WEB-DL.x265-GROUP", false}, // clean single episode
		{"Some.Show.3x05.1080p.WEB-DL-GROUP", false},        // NxNN single episode
		{"Some.Show.S03.1080p.WEB-DL.x265-GROUP", true},     // season-only tag
		{"Some.Show.Season.3.1080p-GROUP", true},            // "Season 3"
		{"Some.Show.S03.Complete.1080p-GROUP", true},        // complete
		{"Some.Show.S03E01E02.1080p-GROUP", true},           // multi-episode list
		{"Some.Show.S03E01-E10.1080p-GROUP", true},          // multi-episode range
		{"Some.Show.2024.1080p.WEB-DL-GROUP", true},         // no episode marker → safe neutral
	}
	for _, tc := range cases {
		if got := isSeasonPackTitle(tc.title); got != tc.pack {
			t.Errorf("isSeasonPackTitle(%q) = %v, want %v", tc.title, got, tc.pack)
		}
	}
}

// TestAutoGrabHandler_Series_PickerGatedFallback proves that when TMDB returns
// no episode runtime (fakeTMDBServer's season-details response carries no
// episodes), a single-episode Series grab degrades gracefully: runtime stays
// unknown → every candidate is unknown-bitrate → the manual pick list, no
// download-client call. The season/episode selection is still carried on the
// request.
func TestAutoGrabHandler_Series_PickerGatedFallback(t *testing.T) {
	var qbAdds int32
	fakeQB := fakeQBittorrent(t, func(r *http.Request) { atomic.AddInt32(&qbAdds, 1) })
	tmdbSrv := fakeTMDBServer(t) // /tv/{id}/external_ids → tvdb_id
	prowlarr := fakeProwlarr(t, `[{"guid":"1","title":"Some.Show.S03E05.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":900000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	body, _ := json.Marshal(apidto.AutoGrabRequest{Title: "Some Show", TMDBID: 100, SeasonNumber: 3, EpisodeNumber: 5, SeasonSpecified: true})
	resp, err := http.Post(srv.URL+"/api/modes/series/autograb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out apidto.AutoGrabResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !out.Fallback || len(out.Candidates) != 1 {
		t.Fatalf("expected Series to fall back to a one-item pick list, got %+v", out)
	}
	if out.Candidates[0].Status != "unknown-bitrate" {
		t.Errorf("expected unknown-bitrate status (no pre-grab runtime), got %q", out.Candidates[0].Status)
	}
	if got := atomic.LoadInt32(&qbAdds); got != 0 {
		t.Errorf("expected zero download-client adds for a Series fallback, got %d", got)
	}
}
