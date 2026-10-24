package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/newznabcatalog"
	"github.com/labbersanon/sakms/internal/newznabondemand"
	"github.com/labbersanon/sakms/internal/playback"
	"github.com/labbersanon/sakms/internal/usenet"
)

const playTestNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p@example.com" date="0" subject="release.mkv (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
      <segment bytes="512" number="1">seg1@test</segment>
    </segments>
  </file>
</nzb>`

func ondemandPlayMux(t *testing.T, nzb *usenet.Manager, dataDir string) *httptest.Server {
	t.Helper()
	resetOndemandPlaysForTest()
	sqlDB := dbtest.New(t)
	st := newznabondemand.NewStore(sqlDB)
	AttachNewznabOndemand(st)
	AttachNewznabOndemandDir(dataDir)
	t.Cleanup(func() {
		AttachNewznabOndemand(nil)
		AttachNewznabOndemandDir("")
		resetOndemandPlaysForTest()
	})
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	return httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nzb, nil, nil, nil, nil, nil))
}

func savePlayNZB(t *testing.T, dataDir, indexer string) string {
	t.Helper()
	rel := filepath.Join("ondemand", "nzb", indexer, "play.nzb")
	abs := filepath.Join(dataDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(playTestNZB), 0o600); err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestOndemandPlayHandler_StartsFromCacheWithoutURLs(t *testing.T) {
	dataDir := t.TempDir()
	rel := savePlayNZB(t, dataDir, "newznab")
	nzb := usenet.New(usenet.Config{StagingDir: filepath.Join(dataDir, "downloads")})
	srv := ondemandPlayMux(t, nzb, dataDir)
	defer srv.Close()

	st := ondemandStore
	guid := "https://nzbgeek.info/geekseek.php?guid=hash1"
	if err := st.Save(context.Background(), newznabondemand.Row{
		Indexer: "newznab", GUID: guid, Kind: newznabcatalog.KindMovies,
		Title: "Shelf.Movie.2021", Status: newznabondemand.StatusVerified, Streamable: true,
		NZBRelPath: rel,
	}); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(ondemandPlayRequest{Indexer: "newznab", NzbID: "hash1"})
	resp, err := http.Post(srv.URL+"/api/modes/movies/ondemand/play", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
	lower := strings.ToLower(string(raw))
	for _, banned := range []string{"enclosure", "apikey", "downloadurl", "http://", "https://", "geekseek"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("payload contains %q: %s", banned, raw)
		}
	}
	var got ondemandPlayStatus
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !usenet.IsOwnedStagingName(got.GID) || got.Status == "" {
		t.Fatalf("status=%+v", got)
	}

	stResp, err := http.Get(srv.URL + "/api/modes/movies/ondemand/play/" + got.GID)
	if err != nil {
		t.Fatal(err)
	}
	defer stResp.Body.Close()
	if stResp.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d", stResp.StatusCode)
	}
}

func TestOndemandPlayHandler_RejectsUnknownAndURLs(t *testing.T) {
	nzb := usenet.New(usenet.Config{StagingDir: t.TempDir()})
	srv := ondemandPlayMux(t, nzb, t.TempDir())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/modes/bogus/ondemand/play", "application/json", strings.NewReader(`{"indexer":"newznab","nzbId":"h"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bogus mode status=%d", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/api/modes/movies/ondemand/play", "application/json", strings.NewReader(`{"indexer":"newznab","nzbId":"https://x/?apikey=secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("url nzbId status=%d", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/api/modes/movies/ondemand/play", "application/json", strings.NewReader(`{"indexer":"newznab","nzbId":"missing"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status=%d", resp.StatusCode)
	}
}

func TestOndemandPlayVideoHandler_ServesCompleteStaging(t *testing.T) {
	dataDir := t.TempDir()
	stage := filepath.Join(dataDir, "downloads")
	nzb := usenet.New(usenet.Config{StagingDir: stage})
	srv := ondemandPlayMux(t, nzb, dataDir)
	defer srv.Close()

	gid := "nzb-0123456789abcdef"
	vid := filepath.Join(stage, gid, "movie.mp4")
	if err := os.MkdirAll(filepath.Dir(vid), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vid, []byte("mp4-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	rememberOndemandPlay(ondemandPlay{GID: gid, Mode: "movies", Indexer: "newznab", NzbID: "hash1"})
	nzb.InjectDownloadForTest(gid)
	if !nzb.MarkCompleteForTest(gid, []string{vid}) {
		t.Fatal("mark complete")
	}

	stResp, err := http.Get(srv.URL + "/api/modes/movies/ondemand/play/" + gid)
	if err != nil {
		t.Fatal(err)
	}
	defer stResp.Body.Close()
	var ready ondemandPlayStatus
	if err := json.NewDecoder(stResp.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if ready.Status != "complete" || ready.PlaySrc == "" {
		t.Fatalf("ready=%+v", ready)
	}
	if strings.Contains(strings.ToLower(ready.PlaySrc), "http") {
		t.Fatalf("playSrc must be a path: %q", ready.PlaySrc)
	}

	vidResp, err := http.Get(srv.URL + ready.PlaySrc)
	if err != nil {
		t.Fatal(err)
	}
	defer vidResp.Body.Close()
	vidBody, _ := io.ReadAll(vidResp.Body)
	if vidResp.StatusCode != http.StatusOK || string(vidBody) != "mp4-bytes" {
		t.Fatalf("video status=%d body=%q", vidResp.StatusCode, vidBody)
	}

	early, err := http.Get(srv.URL + "/api/modes/movies/ondemand/play/nzb-0000000000000001/video")
	if err != nil {
		t.Fatal(err)
	}
	early.Body.Close()
	if early.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown gid status=%d", early.StatusCode)
	}
}

func TestOndemandPlayVideoHandler_ServesWhileActive(t *testing.T) {
	dataDir := t.TempDir()
	stage := filepath.Join(dataDir, "downloads")
	nzb := usenet.New(usenet.Config{StagingDir: stage})
	srv := ondemandPlayMux(t, nzb, dataDir)
	defer srv.Close()

	gid := "nzb-0123456789abcdef"
	vid := filepath.Join(stage, gid, "movie.mkv")
	if err := os.MkdirAll(filepath.Dir(vid), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("m"), 512*1024)
	if err := os.WriteFile(vid, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	rememberOndemandPlay(ondemandPlay{GID: gid, Mode: "movies", Indexer: "newznab", NzbID: "hash1"})
	nzb.InjectDownloadForTest(gid)

	stResp, err := http.Get(srv.URL + "/api/modes/movies/ondemand/play/" + gid)
	if err != nil {
		t.Fatal(err)
	}
	defer stResp.Body.Close()
	var ready ondemandPlayStatus
	if err := json.NewDecoder(stResp.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if ready.Status != "active" || ready.PlaySrc == "" {
		t.Fatalf("ready=%+v", ready)
	}
	if !strings.Contains(ready.PlaySrc, "/hls/index.m3u8") {
		t.Fatalf("mkv playSrc should be HLS: %q", ready.PlaySrc)
	}

	restore := playback.ReplaceHub(playback.NewFakeHub(
		func(context.Context, string) (string, string, error) { return "h264", "aac", nil },
		func(ctx context.Context, in, outDir string, d playback.Decision) error {
			return os.WriteFile(filepath.Join(outDir, "index.m3u8"), []byte("#EXTM3U\n"), 0o644)
		},
	))
	defer restore()

	vidResp, err := http.Get(srv.URL + ready.PlaySrc)
	if err != nil {
		t.Fatal(err)
	}
	defer vidResp.Body.Close()
	if vidResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(vidResp.Body)
		t.Fatalf("hls status=%d body=%s", vidResp.StatusCode, body)
	}
}
