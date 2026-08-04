package rssfeeds

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/secrets"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file and
// a real secrets.Store — exercising the actual schema/migration and actual
// encryption, not mocks, the same convention as trakt/connections tests.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return newTestStoreOnDB(t, newTestDB(t))
}

// newTestDB opens a fresh migrated SQLite file, returning its handle so a test
// can seed a pre-migration-shaped row (e.g. a plaintext feed_url) before
// building the Store over it.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB := dbtest.New(t)
	return sqlDB
}

func newTestStoreOnDB(t *testing.T, sqlDB *sql.DB) *Store {
	t.Helper()
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return NewStore(sqlDB, secretStore)
}

// ptr is a tiny helper for the three-state *string feedURL on Update.
func ptr(s string) *string { return &s }

func TestCreateAndList_RoundTripsFeed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	f, err := s.Create(ctx, "NZBGeek Saved Search", "https://nzbgeek.info/rss?t=1", TargetMovie, Usenet, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.ID == 0 {
		t.Fatal("expected a non-zero id")
	}
	if f.SortOrder != 0 {
		t.Errorf("expected first feed to get sort_order 0, got %d", f.SortOrder)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].Title != "NZBGeek Saved Search" || list[0].FeedURL != "https://nzbgeek.info/rss?t=1" {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestCreate_AppendsSortOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.Create(ctx, "First", "https://example.com/rss1", TargetMovie, Usenet, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := s.Create(ctx, "Second", "https://example.com/rss2", TargetTV, Torrent, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.SortOrder != 0 || second.SortOrder != 1 {
		t.Errorf("expected sort_order 0 then 1, got %d then %d", first.SortOrder, second.SortOrder)
	}
}

func TestCreate_EmptyList_ReturnsEmptySliceNotNil(t *testing.T) {
	s := newTestStore(t)
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if list == nil {
		t.Fatal("expected an empty slice, got nil")
	}
	if len(list) != 0 {
		t.Errorf("expected no feeds, got %+v", list)
	}
}

func TestCreate_RejectsBlankTitle(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "", "https://example.com/rss", TargetMovie, Usenet, true)
	if !errors.Is(err, ErrTitleRequired) {
		t.Fatalf("expected ErrTitleRequired, got %v", err)
	}
}

func TestCreate_RejectsBlankFeedURL(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "Title", "", TargetMovie, Usenet, true)
	if !errors.Is(err, ErrFeedURLRequired) {
		t.Fatalf("expected ErrFeedURLRequired, got %v", err)
	}
}

func TestCreate_RejectsUnknownTarget(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "Bogus", "https://example.com/rss", Target("bogus"), Usenet, true)
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("expected ErrInvalidTarget, got %v", err)
	}
}

func TestCreate_RejectsUnknownProtocol(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "Bogus", "https://example.com/rss", TargetMovie, Protocol("bogus"), true)
	if !errors.Is(err, ErrInvalidProtocol) {
		t.Fatalf("expected ErrInvalidProtocol, got %v", err)
	}
}

func TestUpdate_OverwritesFieldsAndPreservesSortOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.Create(ctx, "First", "https://example.com/rss1", TargetMovie, Usenet, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Create(ctx, "Second", "https://example.com/rss2", TargetTV, Torrent, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := s.Update(ctx, a.ID, "First Renamed", ptr("https://example.com/rss1-new"), TargetAdult, Torrent, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Title != "First Renamed" || updated.FeedURL != "https://example.com/rss1-new" || updated.Target != TargetAdult || updated.Protocol != Torrent || updated.Enabled {
		t.Errorf("unexpected updated feed: %+v", updated)
	}
	if updated.SortOrder != a.SortOrder {
		t.Errorf("expected Update to leave sort_order at %d, got %d", a.SortOrder, updated.SortOrder)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Update(context.Background(), 999, "X", ptr("https://example.com/rss"), TargetMovie, Usenet, true)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdate_ValidatesLikeCreate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, err := s.Create(ctx, "First", "https://example.com/rss1", TargetMovie, Usenet, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Update(ctx, a.ID, "First", ptr(""), TargetMovie, Usenet, true); !errors.Is(err, ErrFeedURLRequired) {
		t.Fatalf("expected ErrFeedURLRequired, got %v", err)
	}
}

// TestCreate_EncryptsURLAtRest asserts the stored feed_url column is blanked
// and the ciphertext lives in feed_url_encrypted, while List round-trips the
// decrypted plaintext URL back.
func TestCreate_EncryptsURLAtRest(t *testing.T) {
	sqlDB := newTestDB(t)
	s := newTestStoreOnDB(t, sqlDB)
	ctx := context.Background()

	const url = "https://indexer.example/rss?apikey=SECRET123"
	f, err := s.Create(ctx, "Feed", url, TargetAdult, Torrent, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var plain, encrypted string
	if err := sqlDB.QueryRowContext(ctx, `SELECT feed_url, feed_url_encrypted FROM rss_feeds WHERE id = ?`, f.ID).Scan(&plain, &encrypted); err != nil {
		t.Fatalf("reading stored columns: %v", err)
	}
	if plain != "" {
		t.Errorf("expected feed_url blanked at rest, got %q", plain)
	}
	if encrypted == "" || encrypted == url {
		t.Errorf("expected feed_url_encrypted to be non-empty ciphertext, got %q", encrypted)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].FeedURL != url {
		t.Errorf("expected List to decrypt url back to %q, got %+v", url, list)
	}
}

// TestUpdate_NilURLPreservesStoredCiphertext asserts the three-state rule: a
// nil feedURL (an untouched save from a form that never received the real URL)
// keeps the stored encrypted URL intact rather than wiping it.
func TestUpdate_NilURLPreservesStoredCiphertext(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const url = "https://indexer.example/rss?apikey=KEEPME"
	f, err := s.Create(ctx, "Feed", url, TargetAdult, Torrent, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Toggle enabled off without touching the URL (feedURL == nil).
	if _, err := s.Update(ctx, f.ID, "Feed", nil, TargetAdult, Torrent, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].FeedURL != url || list[0].Enabled {
		t.Errorf("expected preserved url %q and enabled=false, got %+v", url, list)
	}
}

// TestUpdate_ValueReplacesURL asserts a non-nil, non-empty feedURL replaces the
// stored ciphertext.
func TestUpdate_ValueReplacesURL(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	f, err := s.Create(ctx, "Feed", "https://old.example/rss", TargetAdult, Torrent, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	const newURL = "https://new.example/rss?apikey=NEW"
	if _, err := s.Update(ctx, f.ID, "Feed", ptr(newURL), TargetAdult, Torrent, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].FeedURL != newURL {
		t.Errorf("expected replaced url %q, got %+v", newURL, list)
	}
}

// TestBackfillEncryption_MigratesPlaintextAndIsIdempotent seeds a pre-migration
// plaintext row directly, then asserts BackfillEncryption encrypts it, blanks
// the plaintext column, is a no-op on re-run, and leaves an already-encrypted
// row untouched. It also asserts the decrypt-on-read plaintext FALLBACK: before
// backfill runs, List still resolves the plaintext row's URL.
func TestBackfillEncryption_MigratesPlaintextAndIsIdempotent(t *testing.T) {
	sqlDB := newTestDB(t)
	s := newTestStoreOnDB(t, sqlDB)
	ctx := context.Background()

	// A row shaped like a pre-0043 install: plaintext feed_url, no ciphertext.
	const plainURL = "https://legacy.example/rss?apikey=LEGACY"
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO rss_feeds (title, feed_url, feed_url_encrypted, target, protocol, sort_order, enabled, updated_at)
		VALUES ('Legacy', ?, '', 'adult', 'torrent', 0, true, sakms_now())
	`, plainURL); err != nil {
		t.Fatalf("seeding plaintext row: %v", err)
	}

	// Read-fallback: List resolves the plaintext URL even before backfill.
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].FeedURL != plainURL {
		t.Fatalf("expected plaintext read-fallback to return %q, got %+v", plainURL, list)
	}

	if err := s.BackfillEncryption(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var plain, encrypted string
	if err := sqlDB.QueryRowContext(ctx, `SELECT feed_url, feed_url_encrypted FROM rss_feeds WHERE title = 'Legacy'`).Scan(&plain, &encrypted); err != nil {
		t.Fatalf("reading columns after backfill: %v", err)
	}
	if plain != "" || encrypted == "" || encrypted == plainURL {
		t.Errorf("expected plaintext blanked and ciphertext set, got feed_url=%q feed_url_encrypted=%q", plain, encrypted)
	}

	// Idempotent: a second run changes nothing (ciphertext unchanged).
	if err := s.BackfillEncryption(ctx); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	var encrypted2 string
	if err := sqlDB.QueryRowContext(ctx, `SELECT feed_url_encrypted FROM rss_feeds WHERE title = 'Legacy'`).Scan(&encrypted2); err != nil {
		t.Fatalf("reading ciphertext after second backfill: %v", err)
	}
	if encrypted2 != encrypted {
		t.Errorf("expected idempotent backfill to leave ciphertext unchanged, got %q then %q", encrypted, encrypted2)
	}

	// Still resolves to the original plaintext after backfill.
	list, err = s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].FeedURL != plainURL {
		t.Errorf("expected url %q after backfill, got %+v", plainURL, list)
	}
}

func TestDelete_RemovesFeed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.Create(ctx, "First", "https://example.com/rss1", TargetMovie, Usenet, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no feeds after delete, got %+v", list)
	}
}

func TestDelete_NonExistentIDReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound deleting a feed that never existed, got %v", err)
	}
}

func TestReorder_AppliesNewOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", "https://example.com/a", TargetMovie, Usenet, true)
	b, _ := s.Create(ctx, "B", "https://example.com/b", TargetTV, Usenet, true)
	c, _ := s.Create(ctx, "C", "https://example.com/c", TargetAdult, Usenet, true)

	if err := s.Reorder(ctx, []int{c.ID, a.ID, b.ID}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 3 || list[0].ID != c.ID || list[1].ID != a.ID || list[2].ID != b.ID {
		t.Errorf("unexpected order after reorder: %+v", list)
	}
	if list[0].SortOrder != 0 || list[1].SortOrder != 1 || list[2].SortOrder != 2 {
		t.Errorf("unexpected sort_order values after reorder: %+v", list)
	}
}

func TestReorder_RejectsPartialIDList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", "https://example.com/a", TargetMovie, Usenet, true)
	_, _ = s.Create(ctx, "B", "https://example.com/b", TargetTV, Usenet, true)

	if err := s.Reorder(ctx, []int{a.ID}); !errors.Is(err, ErrReorderMismatch) {
		t.Fatalf("expected ErrReorderMismatch, got %v", err)
	}
}

func TestReorder_RejectsUnknownID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", "https://example.com/a", TargetMovie, Usenet, true)

	if err := s.Reorder(ctx, []int{a.ID, 999}); !errors.Is(err, ErrReorderMismatch) {
		t.Fatalf("expected ErrReorderMismatch, got %v", err)
	}
}

func TestReorder_RejectsDuplicateID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", "https://example.com/a", TargetMovie, Usenet, true)
	b, _ := s.Create(ctx, "B", "https://example.com/b", TargetTV, Usenet, true)

	if err := s.Reorder(ctx, []int{a.ID, a.ID}); !errors.Is(err, ErrReorderMismatch) {
		t.Fatalf("expected ErrReorderMismatch for duplicate id, got %v", err)
	}
	_ = b
}
