package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dedupscan"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// TestDedupScan_ConcurrentSameMode_Returns409 proves the concurrent-same-mode
// guard: with a movies scan already marked in-flight on the shared Hub, a second
// POST for the same mode is rejected 409 (the handler's hub.TryStart returns
// false). Using the Hub directly makes this deterministic — a real fast scan
// might already be finished by the time the second POST lands.
func TestDedupScan_ConcurrentSameMode_Returns409(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := settingsStore.Set(context.Background(), moviesLibraryRootFolderKey, t.TempDir()); err != nil {
		t.Fatalf("seeding root folder: %v", err)
	}

	hub := dedupscan.New()
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, hub, nil, nil, nil, nil))
	defer srv.Close()

	// Simulate a movies scan already running.
	if !hub.TryStart("movies") {
		t.Fatalf("expected TryStart to succeed on a fresh hub")
	}
	defer hub.Finish("movies")

	resp, err := http.Post(srv.URL+"/api/modes/movies/dedup/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for a concurrent same-mode scan, got %d", resp.StatusCode)
	}
}

// TestDedupScan_EmptyRootFolder_Returns400 proves the flagged behavior
// improvement (plan §2/§6): an unconfigured library root folder is a fast
// synchronous 400, not a late 502 from inside the scan. It asserts the body
// text, since a mode.Build failure would also return 400 — a status-only check
// could pass for the wrong reason.
func TestDedupScan_EmptyRootFolder_Returns400(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)

	hub := dedupscan.New()
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, hub, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/modes/movies/dedup/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unconfigured root folder, got %d", resp.StatusCode)
	}
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "root folder") {
		t.Fatalf("expected the 400 to name the missing root folder, got: %s", body[:n])
	}
	// A scan that failed validation must never have marked the mode in-flight.
	if hub.Inflight("movies") {
		t.Fatalf("expected movies NOT in-flight after a 400 validation failure")
	}
}

// TestDedupScan_StatusEndpoint reports the mode's live in-flight state,
// disambiguating "scan still running" from "scan finished (possibly with zero
// groups)".
func TestDedupScan_StatusEndpoint(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)

	hub := dedupscan.New()
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, hub, nil, nil, nil, nil))
	defer srv.Close()

	getInflight := func() bool {
		resp, err := http.Get(srv.URL + "/api/modes/movies/dedup/scan/status")
		if err != nil {
			t.Fatalf("status GET failed: %v", err)
		}
		defer resp.Body.Close()
		var st struct {
			Inflight bool `json:"inflight"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			t.Fatalf("decoding status: %v", err)
		}
		return st.Inflight
	}

	if getInflight() {
		t.Fatalf("expected inflight=false before any scan")
	}
	hub.TryStart("movies")
	if !getInflight() {
		t.Fatalf("expected inflight=true while a movies scan is running")
	}
	hub.Finish("movies")
	if getInflight() {
		t.Fatalf("expected inflight=false after the scan finished")
	}
}

// TestDedupScan_DoneEventArrivesOnStream is the end-to-end proof that a
// successful background scan publishes a terminal done event over the SSE
// stream. Subscribing (and reading the ": connected" prime) BEFORE issuing the
// POST guarantees the subscription is live before any event is published, so no
// frame can be missed.
func TestDedupScan_DoneEventArrivesOnStream(t *testing.T) {
	dir := t.TempDir()
	trackedDir := filepath.Join(dir, "Some Movie (2020)")
	orphanDir := filepath.Join(dir, "Some.Movie.2020.1080p.BluRay.x264-GROUP")
	trackedFile := writeTestVideoFile(t, trackedDir, "movie.mkv", 10)
	orphanFile := writeTestVideoFile(t, orphanDir, "movie.mkv", 10)

	fakeTMDB := httptest.NewServer(fakeTMDBSearchHandler(t, 42, "Some Movie"))
	defer fakeTMDB.Close()

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", fakeTMDB.URL)
	if err := connStore.Upsert(ctx, "tmdb", fakeTMDB.URL, "test-key"); err != nil {
		t.Fatalf("seeding tmdb: %v", err)
	}
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, dir); err != nil {
		t.Fatalf("seeding root folder: %v", err)
	}
	if _, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("seeding tracked item: %v", err)
	}

	prober := &fakeDedupProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	hub := dedupscan.New()
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, prober, testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, hub, nil, nil, nil, nil))
	defer srv.Close()

	// Subscribe first: a Timeout on this client also bounds the whole stream read
	// so a wedged scan fails the test instead of hanging until the go-test kill.
	streamClient := &http.Client{Timeout: 15 * time.Second}
	streamResp, err := streamClient.Get(srv.URL + "/api/modes/movies/dedup/scan/stream")
	if err != nil {
		t.Fatalf("stream GET failed: %v", err)
	}
	defer streamResp.Body.Close()
	reader := bufio.NewReader(streamResp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before the connected prime: %v", err)
		}
		if strings.HasPrefix(line, ": connected") {
			break
		}
	}

	// Subscription is live — now start the scan.
	scanResp, err := http.Post(srv.URL+"/api/modes/movies/dedup/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 from scan, got %d", scanResp.StatusCode)
	}

	var sawProgress bool
	var done dedupscan.Event
	for done.Type == "" {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before a done event (sawProgress=%v): %v", sawProgress, err)
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev dedupscan.Event
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev); err != nil {
			t.Fatalf("bad SSE frame %q: %v", line, err)
		}
		switch ev.Type {
		case "progress":
			sawProgress = true
		case "error":
			t.Fatalf("scan reported an error over the stream: %s", ev.Error)
		case "done":
			done = ev
		}
	}

	if !sawProgress {
		t.Errorf("expected at least one progress event before done")
	}
	if done.Count != 1 {
		t.Errorf("expected the done event to report 1 duplicate group, got %+v", done)
	}
	// Total on a done event is the authoritative final processed count: 1 tracked
	// item + 1 orphan file = 2 files analyzed.
	if done.Total != 2 {
		t.Errorf("expected the done event's authoritative Total=2 files processed, got %+v", done)
	}

	// The staged list is fetchable via the existing GET once done fired.
	listResp, err := http.Get(srv.URL + "/api/modes/movies/dedup/proposals")
	if err != nil {
		t.Fatalf("list proposals failed: %v", err)
	}
	defer listResp.Body.Close()
	var listedPage struct {
		Items []proposals.Proposal `json:"items"`
	}
	json.NewDecoder(listResp.Body).Decode(&listedPage)
	listed := listedPage.Items
	if len(listed) != 1 {
		t.Fatalf("expected 1 staged proposal after done, got %+v", listed)
	}
}
