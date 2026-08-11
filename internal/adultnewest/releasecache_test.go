package adultnewest

import (
	"context"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/prowlarr"
)

// seedRelease is a test helper that inserts one release into adult_release_cache
// with the given download_url, protocol, and persisted_at, bypassing PersistReleases'
// sakms_now() default so tests can control the TTL clock.
func seedRelease(t *testing.T, s *ReleaseStore, url, protocol, persistedAt string) {
	t.Helper()
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO adult_release_cache
			(download_url, protocol, persisted_at)
		VALUES (?, ?, ?)
		ON CONFLICT(download_url) DO UPDATE SET
			protocol     = excluded.protocol,
			persisted_at = excluded.persisted_at`,
		url, protocol, persistedAt)
	if err != nil {
		t.Fatalf("seedRelease(%q, %q, %q): %v", url, protocol, persistedAt, err)
	}
}

// seedLink links a download_url to a scene_key for testing FreshReleasesForScene.
func seedLink(t *testing.T, s *ReleaseStore, sceneKey, url string) {
	t.Helper()
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO adult_release_scene_links (scene_key, download_url) VALUES (?, ?)
		 ON CONFLICT DO NOTHING`, sceneKey, url)
	if err != nil {
		t.Fatalf("seedLink(%q, %q): %v", sceneKey, url, err)
	}
}

func makeRelease(url, protocol string) prowlarr.Release {
	return prowlarr.Release{
		DownloadURL:  url,
		GUID:         url + "-guid",
		Title:        "Test Release " + url,
		Protocol:     prowlarr.Protocol(protocol),
		Indexer:      "test-indexer",
		Categories:   []int{6000},
		IndexerFlags: []string{},
	}
}

func TestPersistReleases_PersistsRawList(t *testing.T) {
	// 5 releases, 2 with titles that wouldn't survive a content filter — but
	// PersistReleases is supposed to persist ALL of them (raw, no filtering).
	s := newTestReleaseStore(t)
	ctx := context.Background()

	releases := []prowlarr.Release{
		makeRelease("https://example.com/r1.nzb", "usenet"),
		makeRelease("https://example.com/r2.nzb", "usenet"),
		makeRelease("https://example.com/r3.torrent", "torrent"),
		{DownloadURL: "https://example.com/r4.torrent", Protocol: "torrent", GUID: "g4", Title: "Totally Unrelated Title"},
		{DownloadURL: "https://example.com/r5.nzb", Protocol: "usenet", GUID: "g5", Title: "Another Mismatch Title"},
	}

	if err := s.PersistReleases(ctx, "test query", releases, []string{"stashdb:scene-abc"}); err != nil {
		t.Fatalf("PersistReleases: %v", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM adult_release_cache`,
	).Scan(&count); err != nil {
		t.Fatalf("counting cache rows: %v", err)
	}
	if count != 5 {
		t.Errorf("adult_release_cache has %d row(s), want 5 (all raw — no content filtering)", count)
	}
}

func TestPersistReleases_LinksAndUnlinked(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	r := makeRelease("https://example.com/linked.nzb", "usenet")
	r2 := makeRelease("https://example.com/unlinked.nzb", "usenet")

	// Persist with no scene keys — both rows land in cache, zero links created.
	if err := s.PersistReleases(ctx, "q", []prowlarr.Release{r, r2}, nil); err != nil {
		t.Fatalf("PersistReleases (nil keys): %v", err)
	}
	var linkCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM adult_release_scene_links`,
	).Scan(&linkCount); err != nil {
		t.Fatalf("counting link rows: %v", err)
	}
	if linkCount != 0 {
		t.Errorf("link rows = %d after nil sceneKeys, want 0", linkCount)
	}

	// LinkReleaseToScene attaches one of them.
	if err := s.LinkReleaseToScene(ctx, "title:some-scene", r.DownloadURL); err != nil {
		t.Fatalf("LinkReleaseToScene: %v", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM adult_release_scene_links WHERE scene_key = ?`,
		"title:some-scene",
	).Scan(&linkCount); err != nil {
		t.Fatalf("counting links for scene: %v", err)
	}
	if linkCount != 1 {
		t.Errorf("link rows for scene = %d, want 1", linkCount)
	}
}

func TestPersistReleases_UpsertRefreshesTTLClock(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	r := makeRelease("https://example.com/upsert.nzb", "usenet")

	// First persist.
	if err := s.PersistReleases(ctx, "q1", []prowlarr.Release{r}, nil); err != nil {
		t.Fatalf("first PersistReleases: %v", err)
	}
	var firstPersistedAt string
	if err := s.db.QueryRowContext(ctx,
		`SELECT persisted_at FROM adult_release_cache WHERE download_url = ?`,
		r.DownloadURL,
	).Scan(&firstPersistedAt); err != nil {
		t.Fatalf("reading first persisted_at: %v", err)
	}

	// Wait a tiny bit so sakms_now() ticks forward, then persist again.
	time.Sleep(10 * time.Millisecond)
	if err := s.PersistReleases(ctx, "q2", []prowlarr.Release{r}, nil); err != nil {
		t.Fatalf("second PersistReleases: %v", err)
	}
	var secondPersistedAt string
	if err := s.db.QueryRowContext(ctx,
		`SELECT persisted_at FROM adult_release_cache WHERE download_url = ?`,
		r.DownloadURL,
	).Scan(&secondPersistedAt); err != nil {
		t.Fatalf("reading second persisted_at: %v", err)
	}

	if secondPersistedAt <= firstPersistedAt {
		t.Errorf("persisted_at did not advance on upsert: first=%q second=%q", firstPersistedAt, secondPersistedAt)
	}

	// Only one row in the table (no duplicate).
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM adult_release_cache`,
	).Scan(&count); err != nil {
		t.Fatalf("counting cache rows: %v", err)
	}
	if count != 1 {
		t.Errorf("adult_release_cache has %d row(s) after upsert, want 1", count)
	}
}

func TestPersistReleases_SkipsEmptyDownloadURL(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	releases := []prowlarr.Release{
		{DownloadURL: "", Protocol: "usenet", GUID: "g1", Title: "No URL"},
		makeRelease("https://example.com/has-url.nzb", "usenet"),
	}

	if err := s.PersistReleases(ctx, "q", releases, nil); err != nil {
		t.Fatalf("PersistReleases: %v", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM adult_release_cache`,
	).Scan(&count); err != nil {
		t.Fatalf("counting cache rows: %v", err)
	}
	if count != 1 {
		t.Errorf("adult_release_cache has %d row(s), want 1 (empty DownloadURL must be skipped)", count)
	}
}

func TestFreshReleasesForScene_MixedProtocolPerReleaseTTL(t *testing.T) {
	// Locked-decision guard #1: one NZB at -30d + one torrent at -30d.
	// The NZB must be returned; the torrent must NOT. A stale torrent sibling
	// must never force a whole-list miss when a fresh NZB sibling exists.
	s := newTestReleaseStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// A7: seed timestamps formatted as sakms_now() output.
	thirtyDaysAgo := now.AddDate(0, 0, -30).Format(sakmsTimestampFormat)
	sceneKey := "stashdb:locked-decision-1"

	seedRelease(t, s, "https://example.com/nzb.nzb", "usenet", thirtyDaysAgo)
	seedRelease(t, s, "https://example.com/torrent.torrent", "torrent", thirtyDaysAgo)
	seedLink(t, s, sceneKey, "https://example.com/nzb.nzb")
	seedLink(t, s, sceneKey, "https://example.com/torrent.torrent")

	fresh, err := s.FreshReleasesForScene(ctx, sceneKey, now)
	if err != nil {
		t.Fatalf("FreshReleasesForScene: %v", err)
	}
	if len(fresh) != 1 {
		t.Fatalf("got %d fresh release(s), want 1 (NZB only)", len(fresh))
	}
	if string(fresh[0].Protocol) != "usenet" {
		t.Errorf("fresh[0].Protocol = %q, want %q", fresh[0].Protocol, "usenet")
	}
	if fresh[0].DownloadURL != "https://example.com/nzb.nzb" {
		t.Errorf("fresh[0].DownloadURL = %q, want the NZB url", fresh[0].DownloadURL)
	}
}

func TestFreshReleasesForScene_AllStaleReturnsEmpty(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Both NZB and torrent seeded past their windows (2y+1d for usenet, 8d for torrent).
	usenetStale := now.Add(-(NZBReleaseTTL + 24*time.Hour)).Format(sakmsTimestampFormat)
	torrentStale := now.Add(-(TorrentReleaseTTL + 24*time.Hour)).Format(sakmsTimestampFormat)
	sceneKey := "stashdb:all-stale"

	seedRelease(t, s, "https://example.com/old.nzb", "usenet", usenetStale)
	seedRelease(t, s, "https://example.com/old.torrent", "torrent", torrentStale)
	seedLink(t, s, sceneKey, "https://example.com/old.nzb")
	seedLink(t, s, sceneKey, "https://example.com/old.torrent")

	fresh, err := s.FreshReleasesForScene(ctx, sceneKey, now)
	if err != nil {
		t.Fatalf("FreshReleasesForScene: %v", err)
	}
	if len(fresh) != 0 {
		t.Errorf("got %d fresh release(s), want 0 (all past their windows)", len(fresh))
	}
}

func TestPurgeExpiredReleases_CascadesLinks(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Seed one expired torrent and one fresh usenet.
	expiredAt := now.Add(-(TorrentReleaseTTL + 24*time.Hour)).Format(sakmsTimestampFormat)
	freshAt := now.Add(-time.Hour).Format(sakmsTimestampFormat)

	seedRelease(t, s, "https://example.com/expired.torrent", "torrent", expiredAt)
	seedRelease(t, s, "https://example.com/fresh.nzb", "usenet", freshAt)
	seedLink(t, s, "stashdb:purge-test", "https://example.com/expired.torrent")
	seedLink(t, s, "stashdb:purge-test", "https://example.com/fresh.nzb")

	n, err := s.PurgeExpiredReleases(ctx, now)
	if err != nil {
		t.Fatalf("PurgeExpiredReleases: %v", err)
	}
	if n != 1 {
		t.Errorf("PurgeExpiredReleases returned %d, want 1 (expired torrent only)", n)
	}

	// Cache: only the fresh NZB survives.
	var cacheCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM adult_release_cache`,
	).Scan(&cacheCount); err != nil {
		t.Fatalf("counting cache rows: %v", err)
	}
	if cacheCount != 1 {
		t.Errorf("adult_release_cache has %d row(s), want 1 after purge", cacheCount)
	}

	// Links: the expired torrent's link was cascade-deleted; the fresh NZB's link survives.
	var linkCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM adult_release_scene_links WHERE scene_key = ?`,
		"stashdb:purge-test",
	).Scan(&linkCount); err != nil {
		t.Fatalf("counting link rows: %v", err)
	}
	if linkCount != 1 {
		t.Errorf("link rows = %d after purge, want 1 (cascade deleted the expired torrent link)", linkCount)
	}
}
