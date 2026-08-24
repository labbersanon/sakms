package adultnewest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/rssfeeds"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
)

// newTestFeedStores builds connections/settings/release/rssfeeds stores over one
// shared, freshly-migrated SQLite file — the feed pass reads feeds and writes
// releases against the same DB, so they must share it.
func newTestFeedStores(t *testing.T) (*connections.Store, *settings.Store, *ReleaseStore, *rssfeeds.Store) {
	t.Helper()
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return connections.New(sqlDB, secretStore), settings.New(sqlDB), NewReleaseStore(sqlDB), rssfeeds.NewStore(sqlDB, secretStore)
}

// fakeRSSFeed serves an RSS 2.0 body, optionally signalling each hit on hit.
func fakeRSSFeed(t *testing.T, body string, hit chan<- struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hit != nil {
			select {
			case hit <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const oneItemFeed = `<?xml version="1.0"?>
<rss version="2.0"><channel>
<item>
  <title>Some.Studio.Some.Scene.XXX.1080p</title>
  <link>https://example.com/details/1</link>
  <enclosure url="https://example.com/fetch/1.torrent" length="2048"/>
</item>
</channel></rss>`

func TestRunFeedCycle_FetchErrorMarksUnhealthy(t *testing.T) {
	connStore, settingsStore, releaseStore, rssFeedsStore := newTestFeedStores(t)
	ctx := context.Background()
	ollama := fakeOllama(t, `{"studio":null,"title":null,"performers":null}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	// A feed whose server always 500s → fetch fails → MarkUnhealthy, no rows.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	f, err := rssFeedsStore.Create(ctx, "Bad Feed", bad.URL, rssfeeds.TargetAdult, rssfeeds.Torrent, true)
	if err != nil {
		t.Fatalf("creating feed: %v", err)
	}

	fh := NewFeedHealth()
	runFeedCycle(ctx, &http.Client{Timeout: 5 * time.Second}, connStore, nil, settingsStore, releaseStore, nil, rssFeedsStore, fh)

	if fh.Healthy(int64(f.ID)) {
		t.Error("a feed whose fetch errored must be marked unhealthy")
	}
	list, err := releaseStore.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no rows from a failed feed fetch, got %+v", list)
	}
}

func TestRunFeedCycle_PruneToEnabledDropsRemovedFeed(t *testing.T) {
	connStore, settingsStore, releaseStore, rssFeedsStore := newTestFeedStores(t)
	ctx := context.Background()
	ollama := fakeOllama(t, `{"studio":null,"title":null,"performers":null}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	feedSrv := fakeRSSFeed(t, oneItemFeed, nil)
	f, err := rssFeedsStore.Create(ctx, "Good Feed", feedSrv.URL, rssfeeds.TargetAdult, rssfeeds.Torrent, true)
	if err != nil {
		t.Fatalf("creating feed: %v", err)
	}

	fh := NewFeedHealth()
	// A stale health entry for a feed id that is NOT in the enabled set.
	fh.SetHealthy(999, time.Now())

	runFeedCycle(ctx, &http.Client{Timeout: 5 * time.Second}, connStore, nil, settingsStore, releaseStore, nil, rssFeedsStore, fh)

	if fh.Healthy(999) {
		t.Error("a feed no longer enabled must be pruned from the health map")
	}
	if !fh.Healthy(int64(f.ID)) {
		t.Error("the enabled, reachable feed must be marked healthy")
	}
}

// TestRunFeedCycle_RefreshesFullWindowLastSeen proves writer (ii): a healthy
// poll advances last_confirmed_seen for an already-cached row still present in
// the window even when that item is already "seen" (so the identify budget skips
// it and the upsert never runs) — the durable TTL-clock refresh that keeps a
// continuously-listed item from ever TTL-expiring.
func TestRunFeedCycle_RefreshesFullWindowLastSeen(t *testing.T) {
	connStore, settingsStore, releaseStore, rssFeedsStore := newTestFeedStores(t)
	ctx := context.Background()
	ollama := fakeOllama(t, `{"studio":null,"title":null,"performers":null}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	feedSrv := fakeRSSFeed(t, oneItemFeed, nil)
	f, err := rssFeedsStore.Create(ctx, "Good Feed", feedSrv.URL, rssfeeds.TargetAdult, rssfeeds.Torrent, true)
	if err != nil {
		t.Fatalf("creating feed: %v", err)
	}

	const key = "https://example.com/fetch/1.torrent"
	// Pre-cache a feed-sourced row keyed on the item's enclosure with a STALE
	// last_confirmed_seen, and mark the item already seen so identify skips it —
	// only the full-window refresh can advance its clock.
	if err := releaseStore.Insert(ctx, MatchedRelease{
		RowType: RowScene, EntityID: "cached", EntitySource: "tpdb", EntityTitle: "Cached",
		DownloadURL: key, DownloadProtocol: "torrent", FeedID: int64(f.ID), FeedItemKey: key, LastConfirmedSeen: 100,
	}); err != nil {
		t.Fatalf("pre-inserting cached row: %v", err)
	}
	if err := releaseStore.MarkSeen(ctx, key); err != nil {
		t.Fatalf("pre-marking seen: %v", err)
	}

	before := time.Now().Unix()
	runFeedCycle(ctx, &http.Client{Timeout: 5 * time.Second}, connStore, nil, settingsStore, releaseStore, nil, rssFeedsStore, NewFeedHealth())

	got := findByID(t, releaseStore, RowScene, "cached")
	if got.LastConfirmedSeen < before {
		t.Errorf("expected last_confirmed_seen advanced to ~now (>= %d), got %d", before, got.LastConfirmedSeen)
	}
}

// adultFeedBody builds a one-item RSS 2.0 feed whose item carries the given
// title and a torrent enclosure — lets the US-1 feed tests control the title the
// identify pipeline parses.
func adultFeedBody(title string) string {
	return `<?xml version="1.0"?><rss version="2.0"><channel>` +
		`<item><title>` + title + `</title>` +
		`<link>https://example.com/details/1</link>` +
		`<enclosure url="https://example.com/fetch/1.torrent" length="2048"/></item>` +
		`</channel></rss>`
}

// TestProcessFeedItem_NoSceneRow_SkipsStudioPerformer is US-1's orphan-prevention
// proof for the FEED pass: when a feed item resolves to a confirmable
// studio+performer but NO scene/movie row is persisted (/scenes and /movies both
// empty), zero performer/studio rows are created — the same gate as the browse
// pass, at processFeedItem's own call site.
func TestProcessFeedItem_NoSceneRow_SkipsStudioPerformer(t *testing.T) {
	connStore, settingsStore, releaseStore, rssFeedsStore := newTestFeedStores(t)
	ctx := context.Background()

	// /scenes + /movies empty → no scene/movie row; /sites + /performers confirm
	// the names, so identifyStudioPerformers would (pre-fix) orphan them.
	tpdb := fakeTPDB(t, map[string]bool{"Real Studio": true}, map[string]bool{"Real Performer": true})
	overrideTPDBBaseURL(t, tpdb.URL)
	if err := connStore.Upsert(ctx, "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("configuring tpdb: %v", err)
	}
	ollama := fakeOllama(t, `{"studio":"Real Studio","title":"Some Scene Title","performers":["Real Performer"]}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	feedSrv := fakeRSSFeed(t, adultFeedBody("Real.Studio.Some.Scene.Title.Real.Performer.XXX.1080p"), nil)
	if _, err := rssFeedsStore.Create(ctx, "Adult Feed", feedSrv.URL, rssfeeds.TargetAdult, rssfeeds.Torrent, true); err != nil {
		t.Fatalf("creating feed: %v", err)
	}

	runFeedCycle(ctx, &http.Client{Timeout: 5 * time.Second}, connStore, nil, settingsStore, releaseStore, nil, rssFeedsStore, NewFeedHealth())

	if got := rawEntityTitles(t, releaseStore, RowScene); len(got) != 0 {
		t.Errorf("expected no scene row, got %v", got)
	}
	if got := rawEntityTitles(t, releaseStore, RowStudio); len(got) != 0 {
		t.Errorf("expected NO studio row when no scene/movie persisted (US-1, feed pass), got %v", got)
	}
	if got := rawEntityTitles(t, releaseStore, RowPerformer); len(got) != 0 {
		t.Errorf("expected NO performer row when no scene/movie persisted (US-1, feed pass), got %v", got)
	}
}

// TestProcessFeedItem_SceneRow_CreatesStudioPerformer is the positive counterpart:
// when the feed pass DOES persist a scene row, the confirmed studio/performer
// rows are still created alongside it — no regression to the working case.
func TestProcessFeedItem_SceneRow_CreatesStudioPerformer(t *testing.T) {
	connStore, settingsStore, releaseStore, rssFeedsStore := newTestFeedStores(t)
	ctx := context.Background()

	tpdb := tpdbSceneStudioPerformerFake(t, "Some Scene Title",
		map[string]bool{"Real Studio": true}, map[string]bool{"Real Performer": true})
	overrideTPDBBaseURL(t, tpdb.URL)
	if err := connStore.Upsert(ctx, "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("configuring tpdb: %v", err)
	}
	ollama := fakeOllama(t, `{"studio":"Real Studio","title":"Some Scene Title","performers":["Real Performer","Garbage Fragment"]}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	feedSrv := fakeRSSFeed(t, adultFeedBody("Real.Studio.Some.Scene.Title.Real.Performer.XXX.1080p"), nil)
	if _, err := rssFeedsStore.Create(ctx, "Adult Feed", feedSrv.URL, rssfeeds.TargetAdult, rssfeeds.Torrent, true); err != nil {
		t.Fatalf("creating feed: %v", err)
	}

	runFeedCycle(ctx, &http.Client{Timeout: 5 * time.Second}, connStore, nil, settingsStore, releaseStore, nil, rssFeedsStore, NewFeedHealth())

	if got := rawEntityTitles(t, releaseStore, RowScene); len(got) != 1 {
		t.Fatalf("expected the scene row to be persisted, got %v", got)
	}
	if got := rawEntityTitles(t, releaseStore, RowStudio); len(got) != 1 || got[0] != "Real Studio" {
		t.Errorf("expected the confirmed studio cached alongside the scene, got %v", got)
	}
	if got := rawEntityTitles(t, releaseStore, RowPerformer); len(got) != 1 || got[0] != "Real Performer" {
		t.Errorf("expected only the confirmed performer (Garbage Fragment skipped), got %v", got)
	}
}

// TestRun_FeedBootPollFiresBeforeInterval proves D7's immediate boot poll: with
// a long feed interval, the feed is still polled at t≈0 (before the first tick),
// so feed content isn't invisible for a whole interval after a restart.
func TestRun_FeedBootPollFiresBeforeInterval(t *testing.T) {
	connStore, settingsStore, releaseStore, rssFeedsStore := newTestFeedStores(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ollama := fakeOllama(t, `{"studio":null,"title":null,"performers":null}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	hit := make(chan struct{}, 1)
	feedSrv := fakeRSSFeed(t, oneItemFeed, hit)
	if _, err := rssFeedsStore.Create(ctx, "Good Feed", feedSrv.URL, rssfeeds.TargetAdult, rssfeeds.Torrent, true); err != nil {
		t.Fatalf("creating feed: %v", err)
	}

	// A one-hour feed interval; browse pass off. If the boot poll didn't fire, the
	// feed would not be hit for an hour — the test would time out at 2s instead.
	if err := settingsStore.Set(ctx, FeedIntervalSettingKey, "3600"); err != nil {
		t.Fatalf("setting feed interval: %v", err)
	}
	go Run(ctx, 0, connStore, nil, settingsStore, releaseStore, nil, rssFeedsStore, NewFeedHealth(), nil)

	select {
	case <-hit:
		// Boot poll fired — success.
	case <-time.After(2 * time.Second):
		t.Fatal("feed was not polled within 2s — the immediate boot poll did not fire before the interval")
	}
}
