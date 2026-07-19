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
	TargetMovie Target = "movie"
	TargetTV    Target = "tv"
	TargetAdult Target = "adult"
)

var validTargets = map[Target]bool{
	TargetMovie: true,
	TargetTV:    true,
	TargetAdult: true,
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

// Feed is one admin-defined raw RSS feed row.
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

// Store persists RSS feed row configs against a database.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
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

// Create validates and inserts a new feed, appended after every existing one
// (sort_order = current max + 1, or 0 for the first feed), and returns the
// stored row with its assigned id and timestamps.
func (s *Store) Create(ctx context.Context, title, feedURL string, target Target, protocol Protocol, enabled bool) (*Feed, error) {
	if err := validate(title, feedURL, target, protocol); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO rss_feeds (title, feed_url, target, protocol, sort_order, enabled, updated_at)
		VALUES (?, ?, ?, ?, (SELECT COALESCE(MAX(sort_order), -1) + 1 FROM rss_feeds), ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		RETURNING id, sort_order, created_at, updated_at
	`, title, feedURL, string(target), string(protocol), enabled)

	f := &Feed{Title: title, FeedURL: feedURL, Target: target, Protocol: protocol, Enabled: enabled}
	if err := row.Scan(&f.ID, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, fmt.Errorf("creating rss feed %q: %w", title, err)
	}
	return f, nil
}

// Update validates and overwrites every editable field of the feed with the
// given id. sort_order is untouched — reordering is Reorder's job, not
// Update's, matching discoversliders.Store.Update's convention.
func (s *Store) Update(ctx context.Context, id int, title, feedURL string, target Target, protocol Protocol, enabled bool) (*Feed, error) {
	if err := validate(title, feedURL, target, protocol); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		UPDATE rss_feeds SET
			title = ?, feed_url = ?, target = ?, protocol = ?, enabled = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?
		RETURNING id, sort_order, created_at, updated_at
	`, title, feedURL, string(target), string(protocol), enabled, id)

	f := &Feed{ID: id, Title: title, FeedURL: feedURL, Target: target, Protocol: protocol, Enabled: enabled}
	if err := row.Scan(&f.ID, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("updating rss feed %d: %w", id, err)
	}
	return f, nil
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
		SELECT id, title, feed_url, target, protocol, sort_order, enabled, created_at, updated_at
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
		var target, protocol string
		if err := rows.Scan(&f.ID, &f.Title, &f.FeedURL, &target, &protocol, &f.SortOrder, &f.Enabled, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning rss feed: %w", err)
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
			UPDATE rss_feeds SET sort_order = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			WHERE id = ?
		`, i, id); err != nil {
			return fmt.Errorf("reordering rss feed %d: %w", id, err)
		}
	}
	return tx.Commit()
}
