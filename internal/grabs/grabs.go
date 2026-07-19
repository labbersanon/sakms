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

	"github.com/curtiswtaylorjr/sakms/internal/dbutil"
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
//
// SeasonNumber/EpisodeNumber are Series-only: season>0,episode=0 means a
// season-pack grab; both >0 means a single-episode grab. TMDB uses season 0
// for "Specials," which collides with SeasonNumber's Go zero-value for "no
// season was picked at all" (a plain series-wide grab) — SeasonSpecified is
// what disambiguates the two: true means SeasonNumber/EpisodeNumber were
// deliberately supplied (including a genuine Season 0), false means they
// weren't and must not be trusted as real season/episode data.
type Grab struct {
	ID               int64     `json:"id"`
	Mode             mode.Mode `json:"mode"`
	Title            string    `json:"title"`
	TMDBID           int       `json:"tmdbId,omitempty"`
	TVDBID           int       `json:"tvdbId,omitempty"`
	SeasonNumber     int       `json:"seasonNumber,omitempty"`
	EpisodeNumber    int       `json:"episodeNumber,omitempty"`
	SeasonSpecified  bool      `json:"seasonSpecified,omitempty"`
	QualityProfileID int       `json:"qualityProfileId,omitempty"`
	Indexer          string    `json:"indexer"`
	Protocol         string    `json:"protocol"`
	DownloadClient   string    `json:"downloadClient"`
	// ClientRef is the download client's own identifier for this
	// download — qBittorrent's torrent hash, or NZBGet's NZBID (as a
	// string) — used to poll that client for status.
	ClientRef string `json:"clientRef,omitempty"`
	// DownloadGID is the aria2 GID the unified downloader assigned this
	// grab (empty for grabs not routed through aria2). It's how the
	// downloader Manager's onComplete callback finds a grab by the GID that
	// just finished, to run the auto-import. Distinct from ClientRef, which
	// held a qBittorrent hash / NZBGet id under the legacy per-client path.
	DownloadGID string `json:"downloadGid,omitempty"`
	// DownloadStatus is the last-observed aria2 status for this grab
	// ("active"/"waiting"/"paused"/"complete"/"error"/"removed"), recorded
	// so the Grabs list can show download progress state without a live RPC
	// call. Advisory mirror of aria2's own state, not a lifecycle Status.
	DownloadStatus string `json:"downloadStatus,omitempty"`
	// DownloadStagingPath is where aria2 staged this grab's files, captured
	// at import time for reference.
	DownloadStagingPath string `json:"downloadStagingPath,omitempty"`
	Status              Status `json:"status"`
	RootFolderPath      string `json:"rootFolderPath"`
	// FlaggedForReview is set by auto-grab's post-grab mislabel check (see
	// internal/autograb.RuntimeMismatch) when an imported file's actual
	// duration is wildly inconsistent with the known TMDB/TPDB runtime. The
	// import still succeeded — this is an advisory signal for the operator to
	// review, and for the Discover UI to badge — not a lifecycle status.
	// FlagReason is a short human-readable explanation ("" when not flagged).
	FlaggedForReview bool   `json:"flaggedForReview,omitempty"`
	FlagReason       string `json:"flagReason,omitempty"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
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
			mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
			download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at
	`, string(g.Mode), g.Title, g.TMDBID, g.TVDBID, g.SeasonNumber, g.EpisodeNumber, g.SeasonSpecified, g.QualityProfileID, g.Indexer, g.Protocol,
		g.DownloadClient, g.ClientRef, g.DownloadGID, g.DownloadStatus, g.DownloadStagingPath, string(Queued), g.RootFolderPath)

	g.Status = Queued
	if err := row.Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return Grab{}, fmt.Errorf("inserting grab for %q: %w", g.Title, err)
	}
	return g, nil
}

// List returns every grab for m, most recently created first.
func (s *Store) List(ctx context.Context, m mode.Mode) ([]Grab, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason, created_at, updated_at
		FROM grabs WHERE mode = ? ORDER BY created_at DESC, id DESC
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
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason, created_at, updated_at
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
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// Flag marks a grab for operator review — used by auto-grab's post-grab
// mislabel check when the imported file's actual duration is wildly
// inconsistent with the known TMDB/TPDB runtime. It does not touch the grab's
// lifecycle status (the import still succeeded); it only sets the advisory
// flag and its reason. Idempotent — re-flagging with the same reason is a
// harmless no-op beyond the updated_at bump.
func (s *Store) Flag(ctx context.Context, id int64, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET flagged_for_review = 1, flag_reason = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?
	`, reason, id)
	if err != nil {
		return fmt.Errorf("flagging grab %d: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// SetDownloadGID records the aria2 GID the unified downloader assigned this
// grab, so a later completion (looked up by GID via GetByDownloadGID) can be
// tied back to it for auto-import. Set once, right after the grab is handed
// to aria2.
func (s *Store) SetDownloadGID(ctx context.Context, id int64, gid string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET download_gid = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?
	`, gid, id)
	if err != nil {
		return fmt.Errorf("setting grab %d download gid: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// SetDownloadStatus records the last-observed aria2 status (and, when a
// download completes and imports, its staging path) for the grab's Grabs-list
// display. Advisory — it mirrors aria2's own state, distinct from the grab's
// lifecycle Status.
func (s *Store) SetDownloadStatus(ctx context.Context, id int64, downloadStatus, stagingPath string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET download_status = ?, download_staging_path = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?
	`, downloadStatus, stagingPath, id)
	if err != nil {
		return fmt.Errorf("setting grab %d download status: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// GetByDownloadGID returns the grab the unified downloader assigned gid to.
// The downloader Manager's onComplete callback uses this to find the grab a
// finished aria2 download belongs to. Returns ErrNotFound when no grab holds
// that GID (e.g. a download aria2 knows about that SAK didn't initiate).
func (s *Store) GetByDownloadGID(ctx context.Context, gid string) (*Grab, error) {
	// download_gid defaults to '' for every grab not routed through aria2;
	// an empty GID would match the first such row arbitrarily.
	if gid == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason, created_at, updated_at
		FROM grabs WHERE download_gid = ?
	`, gid)
	g, err := scanGrab(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading grab for download gid %q: %w", gid, err)
	}
	return &g, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, so scanGrab works
// for List and Get alike.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanGrab(row rowScanner) (Grab, error) {
	var g Grab
	var m string
	err := row.Scan(&g.ID, &m, &g.Title, &g.TMDBID, &g.TVDBID, &g.SeasonNumber, &g.EpisodeNumber, &g.SeasonSpecified, &g.QualityProfileID, &g.Indexer, &g.Protocol,
		&g.DownloadClient, &g.ClientRef, &g.DownloadGID, &g.DownloadStatus, &g.DownloadStagingPath, &g.Status, &g.RootFolderPath, &g.FlaggedForReview, &g.FlagReason, &g.CreatedAt, &g.UpdatedAt)
	g.Mode = mode.Mode(m)
	return g, err
}
