// Package rssfeeds persists admin-defined raw RSS 2.0 feed rows — a
// per-row feed URL (NZBGeek saved-search style: RSS 2.0 with an
// <enclosure> per item) fetched and parsed server-side (internal/rssfeed),
// rendered as one more optional Discover row alongside the built-in ones.
// This package is persistence + validation only — no HTTP handlers and no
// internal/apidto types; the API layer maps its own DTOs onto Feed. Same
// config-store/stateless-fetcher split as internal/discoversliders vs.
// internal/tmdb, and the same CRUD+Reorder shape as
// internal/discoversliders.Store — mirrored here almost exactly, not
// reinvented.
package rssfeeds

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned by Update/Delete when id has no stored feed.
var ErrNotFound = errors.New("rssfeeds: no feed with that id")

// ErrInvalidTarget is returned when Target isn't one of the fixed enum values.
var ErrInvalidTarget = errors.New("rssfeeds: invalid target")

// ErrInvalidProtocol is returned when Protocol isn't one of the fixed enum values.
var ErrInvalidProtocol = errors.New("rssfeeds: invalid protocol")

// ErrTitleRequired is returned when Title is blank.
var ErrTitleRequired = errors.New("rssfeeds: title is required")

// ErrFeedURLRequired is returned when FeedURL is blank.
var ErrFeedURLRequired = errors.New("rssfeeds: feed url is required")

// ErrReorderMismatch is returned by Reorder when the given ids don't cover
// exactly the same set of existing feeds — a partial or stale id list would
// otherwise silently strand the omitted feeds at their old sort_order
// instead of a well-defined position.
var ErrReorderMismatch = errors.New("rssfeeds: reorder ids must match the full set of existing feeds exactly")

// Target restricts a feed to exactly one mode — no multi-mode/"mixed" feeds.
type Target string

const (
	TargetMovie      Target = "movie"
	TargetTV         Target = "tv"
	TargetAdult      Target = "adult"
	TargetAdultMovie Target = "adult-movie"
)

// IsAdultTarget reports whether t is an Adult-section feed (scene or movie).
// Section-lock, Discover feed scanning, and Settings adult-feed lists must
// use this rather than comparing to TargetAdult alone — adult-movie feeds
// are Adult-locked too.
func IsAdultTarget(t Target) bool {
	return t == TargetAdult || t == TargetAdultMovie
}

var validTargets = map[Target]bool{
	TargetMovie:      true,
	TargetTV:         true,
	TargetAdult:      true,
	TargetAdultMovie: true,
}

// Protocol is admin-set per feed at creation, not sniffed from the feed's
// XML — enclosure MIME types are inconsistent across indexers.
type Protocol string

const (
	Torrent Protocol = "torrent"
	Usenet  Protocol = "usenet"
)

var validProtocols = map[Protocol]bool{
	Torrent: true,
	Usenet:  true,
}

// Feed is one admin-defined raw RSS feed row. FeedURL is always the decrypted,
// plaintext URL for in-process callers — the Store encrypts it into
// feed_url_encrypted before it reaches SQLite and decrypts it on read.
type Feed struct {
	ID        int
	Title     string
	FeedURL   string
	Target    Target
	Protocol  Protocol
	SortOrder int
	Enabled   bool
	CreatedAt string
	UpdatedAt string
}

// encryptor is the subset of *secrets.Store this package needs — mirrors
// internal/trakt's own encryptor interface so both packages can be satisfied
// by the same *secrets.Store without either importing the other. A feed URL
// commonly embeds an indexer API key, so it's encrypted at rest like every
// other secret in this codebase.
type encryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
}

// Store persists RSS feed row configs against a database, encrypting the feed
// URL at rest via secretStore (the trakt.Store pattern exactly).
type Store struct {
	db      *sql.DB
	secrets encryptor
}

// NewStore builds a Store. secretStore encrypts feed_url into
// feed_url_encrypted before it reaches SQLite and decrypts it on read — same
// key, same convention as internal/connections.Store / internal/trakt.Store.
func NewStore(db *sql.DB, secretStore encryptor) *Store {
	return &Store{db: db, secrets: secretStore}
}

// validate checks title/feedURL/target/protocol against the fixed enums,
// shared by Create and Update.
func validate(title, feedURL string, target Target, protocol Protocol) error {
	if title == "" {
		return ErrTitleRequired
	}
	if feedURL == "" {
		return ErrFeedURLRequired
	}
	if !validTargets[target] {
		return fmt.Errorf("%w: %q", ErrInvalidTarget, target)
	}
	if !validProtocols[protocol] {
		return fmt.Errorf("%w: %q", ErrInvalidProtocol, protocol)
	}
	return nil
}

// validateMeta checks the always-required non-URL fields — used by Update,
// whose URL is three-state (nil = preserve the stored URL) and so can't go
// through validate's mandatory feed-url check.
func validateMeta(title string, target Target, protocol Protocol) error {
	if title == "" {
		return ErrTitleRequired
	}
	if !validTargets[target] {
		return fmt.Errorf("%w: %q", ErrInvalidTarget, target)
	}
	if !validProtocols[protocol] {
		return fmt.Errorf("%w: %q", ErrInvalidProtocol, protocol)
	}
	return nil
}

// resolveURL turns a stored (plaintext, encrypted) column pair into the
// decrypted feed URL for in-process callers. The encrypted column is
// authoritative once populated; when it's empty but the plaintext column isn't
// (a row the boot backfill hasn't reached, or a partially-failed backfill), it
// falls back to the plaintext value so the feed still resolves rather than
// returning "" and 502-ing the resolve handler. This fallback is read-only — it
// never re-writes plaintext; the next BackfillEncryption run finishes the row.
func (s *Store) resolveURL(plaintext, encrypted string) (string, error) {
	if encrypted != "" {
		return s.secrets.Decrypt(encrypted)
	}
	if plaintext != "" {
		return plaintext, nil
	}
	return "", nil
}

// Create validates and inserts a new feed, appended after every existing one
// (sort_order = current max + 1, or 0 for the first feed), and returns the
// stored row with its assigned id and timestamps.
func (s *Store) Create(ctx context.Context, title, feedURL string, target Target, protocol Protocol, enabled bool) (*Feed, error) {
	if err := validate(title, feedURL, target, protocol); err != nil {
		return nil, err
	}
	encrypted, err := s.secrets.Encrypt(feedURL)
	if err != nil {
		return nil, fmt.Errorf("encrypting rss feed url: %w", err)
	}
	// feed_url is written as '' (the ciphertext in feed_url_encrypted is
	// authoritative); it stays NOT NULL with no DEFAULT, so the empty string
	// must be supplied explicitly (see migration 0043's build note).
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO rss_feeds (title, feed_url, feed_url_encrypted, target, protocol, sort_order, enabled, updated_at)
		VALUES (?, '', ?, ?, ?, (SELECT COALESCE(MAX(sort_order), -1) + 1 FROM rss_feeds), ?, sakms_now())
		RETURNING id, sort_order, created_at, updated_at
	`, title, encrypted, string(target), string(protocol), enabled)

	f := &Feed{Title: title, FeedURL: feedURL, Target: target, Protocol: protocol, Enabled: enabled}
	if err := row.Scan(&f.ID, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, fmt.Errorf("creating rss feed %q: %w", title, err)
	}
	return f, nil
}

// Update validates and overwrites every editable field of the feed with the
// given id. sort_order is untouched — reordering is Reorder's job, not
// Update's, matching discoversliders.Store.Update's convention.
//
// feedURL follows this project's three-state secret rule (see
// trakt.Store.SaveCredentials / connections.Store.UpsertPreservingSecret): nil
// preserves the stored (encrypted) URL — the DTO masks it, so the Settings form
// never receives the real value back and an untouched save must not wipe it — a
// non-empty value replaces it, and "" is rejected as ErrFeedURLRequired (a feed
// with no URL is not a valid state, unlike a clearable optional secret).
func (s *Store) Update(ctx context.Context, id int, title string, feedURL *string, target Target, protocol Protocol, enabled bool) (*Feed, error) {
	if err := validateMeta(title, target, protocol); err != nil {
		return nil, err
	}

	var row *sql.Row
	f := &Feed{ID: id, Title: title, Target: target, Protocol: protocol, Enabled: enabled}
	if feedURL != nil {
		if *feedURL == "" {
			return nil, ErrFeedURLRequired
		}
		encrypted, err := s.secrets.Encrypt(*feedURL)
		if err != nil {
			return nil, fmt.Errorf("encrypting rss feed url: %w", err)
		}
		f.FeedURL = *feedURL
		row = s.db.QueryRowContext(ctx, `
			UPDATE rss_feeds SET
				title = ?, feed_url = '', feed_url_encrypted = ?, target = ?, protocol = ?, enabled = ?,
				updated_at = sakms_now()
			WHERE id = ?
			RETURNING id, sort_order, created_at, updated_at
		`, title, encrypted, string(target), string(protocol), enabled, id)
	} else {
		// Preserve the stored feed_url_encrypted by omitting it from SET.
		row = s.db.QueryRowContext(ctx, `
			UPDATE rss_feeds SET
				title = ?, target = ?, protocol = ?, enabled = ?,
				updated_at = sakms_now()
			WHERE id = ?
			RETURNING id, sort_order, created_at, updated_at
		`, title, string(target), string(protocol), enabled, id)
	}

	if err := row.Scan(&f.ID, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("updating rss feed %d: %w", id, err)
	}
	return f, nil
}

// BackfillEncryption migrates any pre-existing plaintext feed_url rows to the
// encrypted column, idempotently: it reads only rows still holding a plaintext
// URL with no ciphertext yet (feed_url_encrypted = '' AND feed_url != ''),
// encrypts each, and writes feed_url_encrypted while blanking feed_url. The
// guard makes re-runs no-ops, so it is safe to call unconditionally at every
// boot — this is the real production migration the encrypt-feed-url work needs,
// distinct from a fresh-install schema change. Called before both the feed
// poller starts and the HTTP server accepts requests (see cmd/sakms/main.go),
// so no request or poll can read/write a half-migrated row.
func (s *Store) BackfillEncryption(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, feed_url FROM rss_feeds WHERE feed_url_encrypted = '' AND feed_url != ''
	`)
	if err != nil {
		return fmt.Errorf("reading plaintext rss feed urls for backfill: %w", err)
	}
	type pending struct {
		id      int
		feedURL string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.feedURL); err != nil {
			rows.Close()
			return fmt.Errorf("scanning rss feed for backfill: %w", err)
		}
		todo = append(todo, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	// Close before issuing UPDATEs — SQLite doesn't allow writing through the
	// same connection while a read cursor is still open.
	rows.Close()

	for _, p := range todo {
		encrypted, err := s.secrets.Encrypt(p.feedURL)
		if err != nil {
			return fmt.Errorf("encrypting rss feed %d for backfill: %w", p.id, err)
		}
		if _, err := s.db.ExecContext(ctx, `
			UPDATE rss_feeds SET feed_url_encrypted = ?, feed_url = '' WHERE id = ?
		`, encrypted, p.id); err != nil {
			return fmt.Errorf("backfilling encrypted url for rss feed %d: %w", p.id, err)
		}
	}
	return nil
}

// Delete removes the feed with the given id. Returns ErrNotFound when id has
// no stored feed — matches webhooks.Store.Delete's convention, which lets the
// HTTP handler return a 404 for a missing resource rather than a silent 204.
func (s *Store) Delete(ctx context.Context, id int) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM rss_feeds WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting rss feed %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns every feed ordered by sort_order, ascending.
func (s *Store) List(ctx context.Context) ([]Feed, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, feed_url, feed_url_encrypted, target, protocol, sort_order, enabled, created_at, updated_at
		FROM rss_feeds
		ORDER BY sort_order ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing rss feeds: %w", err)
	}
	defer rows.Close()

	// []Feed{}, not var out []Feed — a blank install's "no feeds yet" should
	// serialize as [] over the API, not null (see discoversliders.Store.List's
	// identical convention).
	out := []Feed{}
	for rows.Next() {
		var f Feed
		var target, protocol, feedURLPlain, feedURLEncrypted string
		if err := rows.Scan(&f.ID, &f.Title, &feedURLPlain, &feedURLEncrypted, &target, &protocol, &f.SortOrder, &f.Enabled, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning rss feed: %w", err)
		}
		f.FeedURL, err = s.resolveURL(feedURLPlain, feedURLEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypting rss feed %d url: %w", f.ID, err)
		}
		f.Target = Target(target)
		f.Protocol = Protocol(protocol)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Reorder assigns sort_order 0..len(ids)-1 to the feeds named by ids, in the
// given order. ids must contain exactly the ids of every existing feed, each
// exactly once — matches discoversliders.Store.Reorder's convention exactly
// (one explicit "here is the new order" action on the full resource, not a
// per-item bulk mutation).
func (s *Store) Reorder(ctx context.Context, ids []int) error {
	existing, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("reordering rss feeds: %w", err)
	}
	existingIDs := make(map[int]bool, len(existing))
	for _, f := range existing {
		existingIDs[f.ID] = true
	}
	if len(ids) != len(existingIDs) {
		return ErrReorderMismatch
	}
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if seen[id] || !existingIDs[id] {
			return ErrReorderMismatch
		}
		seen[id] = true
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reordering rss feeds: %w", err)
	}
	defer tx.Rollback()

	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			UPDATE rss_feeds SET sort_order = ?, updated_at = sakms_now()
			WHERE id = ?
		`, i, id); err != nil {
			return fmt.Errorf("reordering rss feed %d: %w", id, err)
		}
	}
	return tx.Commit()
}
