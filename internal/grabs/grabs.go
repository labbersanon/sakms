// Package grabs persists the record of a release the user chose to
// download — created the moment a search result is grabbed, updated as its
// download-client status changes, and eventually marked imported once
// internal/rename's file-relocation logic has moved it into the library.
//
// This is deliberately NOT internal/proposals: a Proposal models "Scan
// discovered something already on disk, staged asynchronously for later
// review" (Pending/Unmatched/Applied/Dismissed). A grab's lifecycle
// (queued -> downloading -> completed -> imported/failed) is a different
// shape entirely — synchronous, user-initiated (search now, pick now, grab
// now), and it needs to track a real download client's progress over time
// rather than a human's review decision. See the plan this was built from
// for the full reasoning.
package grabs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/curtiswtaylorjr/sakms/internal/mode"
)

// ErrNotFound is returned by Get when no grab exists with the given ID.
var ErrNotFound = errors.New("grabs: no grab with that id")

// Status is a grab's lifecycle stage.
type Status string

const (
	Queued      Status = "queued"
	Downloading Status = "downloading"
	Completed   Status = "completed"
	Imported    Status = "imported"
	Failed      Status = "failed"
)

// Grab is one release the user chose to download.
type Grab struct {
	ID               int64     `json:"id"`
	Mode             mode.Mode `json:"mode"`
	Title            string    `json:"title"`
	TMDBID           int       `json:"tmdbId,omitempty"`
	TVDBID           int       `json:"tvdbId,omitempty"`
	QualityProfileID int       `json:"qualityProfileId,omitempty"`
	Indexer          string    `json:"indexer"`
	Protocol         string    `json:"protocol"`
	DownloadClient   string    `json:"downloadClient"`
	// ClientRef is the download client's own identifier for this
	// download — qBittorrent's torrent hash, or NZBGet's NZBID (as a
	// string) — used to poll that client for status.
	ClientRef      string `json:"clientRef,omitempty"`
	Status         Status `json:"status"`
	RootFolderPath string `json:"rootFolderPath"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Create persists a new grab, starting at Queued regardless of what g.Status
// was set to by the caller. Returns g with its ID and timestamps populated.
func (s *Store) Create(ctx context.Context, g Grab) (Grab, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO grabs (
			mode, title, tmdb_id, tvdb_id, quality_profile_id, indexer, protocol,
			download_client, client_ref, status, root_folder_path
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at
	`, string(g.Mode), g.Title, g.TMDBID, g.TVDBID, g.QualityProfileID, g.Indexer, g.Protocol,
		g.DownloadClient, g.ClientRef, string(Queued), g.RootFolderPath)

	g.Status = Queued
	if err := row.Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return Grab{}, fmt.Errorf("inserting grab for %q: %w", g.Title, err)
	}
	return g, nil
}

// List returns every grab for m, most recently created first.
func (s *Store) List(ctx context.Context, m mode.Mode) ([]Grab, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, quality_profile_id, indexer, protocol,
		       download_client, client_ref, status, root_folder_path, created_at, updated_at
		FROM grabs WHERE mode = ? ORDER BY created_at DESC
	`, string(m))
	if err != nil {
		return nil, fmt.Errorf("listing grabs: %w", err)
	}
	defer rows.Close()

	// []Grab{}, not var out []Grab — matches connections.Store.List and
	// proposals.Store.List's convention of never serializing a blank
	// install's empty list as JSON null.
	out := []Grab{}
	for rows.Next() {
		g, err := scanGrab(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning grab: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Get returns a single grab by ID.
func (s *Store) Get(ctx context.Context, id int64) (*Grab, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, quality_profile_id, indexer, protocol,
		       download_client, client_ref, status, root_folder_path, created_at, updated_at
		FROM grabs WHERE id = ?
	`, id)
	g, err := scanGrab(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading grab %d: %w", id, err)
	}
	return &g, nil
}

// UpdateStatus records a grab's current lifecycle stage, as last observed
// from its download client (or, for Imported, from internal/rename having
// completed the import).
func (s *Store) UpdateStatus(ctx context.Context, id int64, status Status) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET status = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?
	`, string(status), id)
	if err != nil {
		return fmt.Errorf("updating grab %d status: %w", id, err)
	}
	return checkAffected(res, id)
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, so scanGrab works
// for List and Get alike.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanGrab(row rowScanner) (Grab, error) {
	var g Grab
	var m string
	err := row.Scan(&g.ID, &m, &g.Title, &g.TMDBID, &g.TVDBID, &g.QualityProfileID, &g.Indexer, &g.Protocol,
		&g.DownloadClient, &g.ClientRef, &g.Status, &g.RootFolderPath, &g.CreatedAt, &g.UpdatedAt)
	g.Mode = mode.Mode(m)
	return g, err
}

func checkAffected(res sql.Result, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking update result for grab %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
