package serviceconn

import (
	"context"
	"testing"
)

// TestBackfillUsenetURL_NormalizesAndIsIdempotent covers the Go half of
// migration 0053's data move: the migration copies the legacy `nntp://host:port`
// url verbatim (SQLite cannot parse a URL in SQL) with host/port/tls zeroed, and
// this turns it into the columns internal/usenet actually builds a pool from.
func TestBackfillUsenetURL_NormalizesAndIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert the way migration 0053 does — raw, bypassing Create's
	// "a usenet row needs host and port" invariant, which is exactly the state
	// this backfill exists to reach.
	var id int64
	if err := s.db.QueryRowContext(ctx, `
		INSERT INTO service_connections (kind, provider, label, url, username, secret_encrypted, enabled)
		VALUES ('usenet', 'nntp', 'Usenet', 'nntps://news.example.com:563', 'u', '', true)
		RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seeding legacy row: %v", err)
	}

	if err := s.BackfillUsenetURL(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Host != "news.example.com" || got.Port != 563 || !got.TLS {
		t.Errorf("want host/port/tls parsed out of the url, got %q/%d/%v", got.Host, got.Port, got.TLS)
	}
	if got.URL != "" {
		t.Errorf("url should be blanked once normalized, got %q", got.URL)
	}
	updatedOnce := got.UpdatedAt

	// Idempotent: a second run must be a no-op, so it is safe to call at every
	// boot (rssfeeds.Store.BackfillEncryption's contract exactly).
	if err := s.BackfillUsenetURL(ctx); err != nil {
		t.Fatalf("backfill (re-run): %v", err)
	}
	again, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if again.UpdatedAt != updatedOnce || again.Host != "news.example.com" {
		t.Errorf("re-running the backfill must not touch an already-normalized row, got %+v", again)
	}
}

// TestBackfillUsenetURL_SkipsUnparseableRows proves one bad row doesn't fail
// the whole boot step — it is left as-is for the operator to correct, and the
// good rows still normalize.
func TestBackfillUsenetURL_SkipsUnparseableRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	var bad, good int64
	if err := s.db.QueryRowContext(ctx, `
		INSERT INTO service_connections (kind, provider, label, url, enabled)
		VALUES ('usenet', 'nntp', 'Broken', 'http://not-an-nntp-url', true) RETURNING id`).Scan(&bad); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `
		INSERT INTO service_connections (kind, provider, label, url, enabled)
		VALUES ('usenet', 'nntp', 'Fine', 'nntp://news.example.com', true) RETURNING id`).Scan(&good); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if err := s.BackfillUsenetURL(ctx); err != nil {
		t.Fatalf("one unparseable row must not fail the backfill: %v", err)
	}
	if got, _ := s.Get(ctx, bad); got.URL != "http://not-an-nntp-url" || got.Host != "" {
		t.Errorf("an unparseable row should be left untouched, got %+v", got)
	}
	// Missing port defaults to 119 for a plain nntp:// scheme.
	if got, _ := s.Get(ctx, good); got.Host != "news.example.com" || got.Port != 119 || got.TLS {
		t.Errorf("the parseable row should still normalize, got %q/%d/%v", got.Host, got.Port, got.TLS)
	}
}
