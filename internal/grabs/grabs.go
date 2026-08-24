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
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labbersanon/sakms/internal/dbutil"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/mode"
)

// ErrNotFound is returned by Get when no grab exists with the given ID.
var ErrNotFound = errors.New("grabs: no grab with that id")

// ErrHeldRequestExists is returned by Create when a held Calendar pre-release
// request already exists for the same (mode, tmdb_id) — the partial unique index
// idx_grabs_held_request (migration 0057) refusing a second one.
//
// It exists so the route can degrade a LOST RACE into its ordinary
// already-requested answer instead of a 500. Two concurrent clicks both miss
// FindHeldRequest and both reach Create; the loser gets this, re-reads the row
// the winner minted, and reports the same alreadyRequested the sequential path
// reports. The index is what makes the outcome correct; this error is only what
// makes it presentable.
var ErrHeldRequestExists = errors.New("grabs: a held pre-release request already exists for that mode and tmdb id")

// Status is a grab's lifecycle stage.
type Status string

const (
	Queued      Status = "queued"
	Downloading Status = "downloading"
	Completed   Status = "completed"
	Imported    Status = "imported"
	// Failed is a genuinely terminal, never-retried state with exactly TWO
	// producers, neither of which is retry exhaustion (SetPendingRetry no
	// longer caps retries; see its doc):
	//
	//  1. A permanent classification — a 451/DMCA-removed article
	//     (classifyDownloadState, internal/api/search.go).
	//  2. An operator un-monitoring a Series season (added 2026-08-01 with
	//     air-date monitoring). This is an explicit human STOP action that
	//     cancels that season's already-queued air-date retries outright, not
	//     a retry that ran out of attempts.
	//
	// CORRECTED 2026-08-01 — superseded claim, quoted: "reached ONLY via a
	// permanent classification (e.g. a 451/DMCA-removed article), never via
	// retry exhaustion." The "only" is what changed; the "never via retry
	// exhaustion" clause survives verbatim and applies to both producers.
	//
	// The two are told apart by retry_reason, NEVER by status alone: an
	// un-monitor writes airDateUnmonitoredReason before flipping the status
	// (internal/api/airdatemonitor.go) precisely so a cancelled retry is not
	// misread by an operator as a takedown.
	Failed Status = "failed"
	// PendingRetry is an auto-grab that found nothing worth grabbing (no
	// candidate cleared the quality floor) or whose usenet retrieval failed
	// transiently — it holds no download and will be re-searched from scratch
	// on the next retry cycle, indefinitely (no attempt cap as of 2026-08-01).
	// Deliberately NOT Failed: the Requests aggregation would misreport it as
	// terminal, and this status is by design never terminal on its own.
	PendingRetry Status = "pending_retry"
)

// sqliteTimeLayout is the shape sakms_now() writes
// throughout this schema. retry_after is compared lexicographically in SQL, so
// a Go-written timestamp MUST be UTC and in exactly this layout or the
// due-for-retry comparison silently misbehaves.
const sqliteTimeLayout = "2006-01-02T15:04:05.000Z"

// FormatTime renders t the way this schema's TEXT timestamps are stored.
func FormatTime(t time.Time) string { return t.UTC().Format(sqliteTimeLayout) }

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
	// DownloadURL is the release's download/NZB URL, encrypted at rest — a
	// retry has nothing else to re-derive a grab from (an RSS-sourced item's
	// entire provenance is its enclosure URL plus title). json:"-" is
	// deliberate and load-bearing: an indexer/NZB URL commonly embeds an API
	// key, so it must never reach a wire DTO, the same reasoning that put RSS
	// feed URLs behind encryption.
	DownloadURL string `json:"-"`
	// RetryAfter/RetryCount/RetryReason are the PendingRetry state. RetryAfter
	// is a sqliteTimeLayout timestamp ("" when the grab is not awaiting one).
	RetryAfter  string `json:"retryAfter,omitempty"`
	RetryCount  int    `json:"retryCount,omitempty"`
	RetryReason string `json:"retryReason,omitempty"`
	// HoldUntil is a Calendar pre-release request's hold: a sqliteTimeLayout
	// timestamp ("" for every grab that did not originate as one). It is this
	// feature's ORIGIN MARKER, not merely a schedule — nothing but a Calendar
	// pre-release request can produce a non-empty value, so origin is a
	// queryable fact rather than an inference from status + RetryReason +
	// RetryCount (the pattern that caused a HIGH-severity misclassification bug
	// in series air-date monitoring).
	//
	// It is NEVER cleared, including after the row is promoted and dispatched:
	// provenance outlives the hold. DueForRelease's retry_after = '' guard, not
	// a clear, is what makes promotion fire exactly once.
	HoldUntil string `json:"holdUntil,omitempty"`
	// MonitorEntityKey is the Adult monitored-entity origin marker. Non-empty
	// ONLY for grabs dispatched by the Adult monitor cycle
	// (internal/api/adultmonitor.go). Format: kind + \x1f + entity_source +
	// \x1f + entity_id (see adultnewest.FormatMonitorKey). Never cleared after
	// dispatch — provenance outlives the grab, same as HoldUntil.
	MonitorEntityKey string `json:"monitorEntityKey,omitempty"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

// encryptor is the subset of *secrets.Store this package needs — the same
// local 2-method interface internal/rssfeeds and internal/serviceconn declare,
// so all three are satisfied by one *secrets.Store without importing each
// other.
type encryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
}

type Store struct {
	db      *sql.DB
	secrets encryptor
}

// New builds a Store. secretStore encrypts a grab's download URL into
// download_url_encrypted before it reaches SQLite and decrypts it on read —
// same key, same convention as connections.Store / rssfeeds.Store.
func New(db *sql.DB, secretStore encryptor) *Store {
	return &Store{db: db, secrets: secretStore}
}

// encrypt/decrypt tolerate an empty value AND a nil secret store: a grab
// created without a download URL (every pre-retry path, and every
// pending_retry row, which by contract holds none) must not require one.
func (s *Store) encrypt(url string) (string, error) {
	if url == "" || s.secrets == nil {
		return "", nil
	}
	encrypted, err := s.secrets.Encrypt(url)
	if err != nil {
		return "", fmt.Errorf("encrypting grab download url: %w", err)
	}
	return encrypted, nil
}

func (s *Store) decrypt(encrypted string) (string, error) {
	if encrypted == "" || s.secrets == nil {
		return "", nil
	}
	plaintext, err := s.secrets.Decrypt(encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypting grab download url: %w", err)
	}
	return plaintext, nil
}

// Create persists a new grab, starting at Queued for every status EXCEPT
// PendingRetry, which is stored as given along with its RetryAfter/RetryCount/
// RetryReason.
//
// The PendingRetry exception exists because such a row is created by an
// auto-grab that never dispatched anything: it has no GID and no download, so
// "queued" would be a lie, and creating it as Queued and then transitioning it
// would both be non-atomic and land it at retry_count 1 instead of the 0 a
// freshly-created retry row must have (SetPendingRetry increments, and is for
// SUBSEQUENT cycles only). Every other status is still forced to Queued —
// a caller cannot fabricate a completed/imported grab.
func (s *Store) Create(ctx context.Context, g Grab) (Grab, error) {
	if g.Status != PendingRetry {
		g.Status = Queued
		g.RetryAfter, g.RetryCount, g.RetryReason = "", 0, ""
		// HoldUntil is zeroed by the same arm and for the same reason: a hold
		// only ever belongs to a pending_retry row a Calendar pre-release
		// request minted. Without this a caller could fabricate a queued row
		// carrying a hold, which is exactly the fabrication this arm exists to
		// prevent.
		g.HoldUntil = ""
	}
	encrypted, err := s.encrypt(g.DownloadURL)
	if err != nil {
		return Grab{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO grabs (
			mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
			download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path,
			download_url_encrypted, retry_after, retry_count, retry_reason, hold_until, monitor_entity_key
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at
	`, string(g.Mode), g.Title, g.TMDBID, g.TVDBID, g.SeasonNumber, g.EpisodeNumber, g.SeasonSpecified, g.QualityProfileID, g.Indexer, g.Protocol,
		g.DownloadClient, g.ClientRef, g.DownloadGID, g.DownloadStatus, g.DownloadStagingPath, string(g.Status), g.RootFolderPath,
		encrypted, g.RetryAfter, g.RetryCount, g.RetryReason, g.HoldUntil, g.MonitorEntityKey)

	if err := row.Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt); err != nil {
		// Claude 2026-08-04: Postgres UNIQUE is SQLSTATE 23505 (was SQLite message match).
		var pgErr *pgconn.PgError
		if g.HoldUntil != "" && errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return Grab{}, fmt.Errorf("inserting held request for %q: %w", g.Title, ErrHeldRequestExists)
		}
		return Grab{}, fmt.Errorf("inserting grab for %q: %w", g.Title, err)
	}
	return g, nil
}

// List returns every grab for m, most recently created first.
func (s *Store) List(ctx context.Context, m mode.Mode) ([]Grab, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, hold_until, monitor_entity_key, created_at, updated_at
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
		g, err := s.scanGrab(rows)
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
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, hold_until, monitor_entity_key, created_at, updated_at
		FROM grabs WHERE id = ?
	`, id)
	g, err := s.scanGrab(row)
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
		UPDATE grabs SET status = ?, updated_at = sakms_now() WHERE id = ?
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
		UPDATE grabs SET flagged_for_review = true, flag_reason = ?, updated_at = sakms_now() WHERE id = ?
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
		UPDATE grabs SET download_gid = ?, updated_at = sakms_now() WHERE id = ?
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
		UPDATE grabs SET download_status = ?, download_staging_path = ?, updated_at = sakms_now() WHERE id = ?
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
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, hold_until, monitor_entity_key, created_at, updated_at
		FROM grabs WHERE download_gid = ?
	`, gid)
	g, err := s.scanGrab(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading grab for download gid %q: %w", gid, err)
	}
	return &g, nil
}

// ActiveByDownloadGID returns an in-flight grab for m holding gid, or
// ErrNotFound if none exists. It's the idempotency guard the grab handlers
// consult before recording a new grab: a download client dedupes a torrent by
// its infohash, so a repeat grab of the same release comes back with the SAME
// download GID — recording a second grabs row for it would strand a duplicate
// at 'queued' forever (only ONE row can ever win GetByDownloadGID's
// first-row-only lookup when the download completes; the rest never get marked
// imported). "Active" is deliberately status NOT IN ('imported','failed'): a
// genuinely fresh re-grab AFTER a prior attempt reached a terminal state
// (successfully imported, or failed) is legitimate and must NOT be blocked —
// only a still-in-flight duplicate is. Returns the earliest such row (ORDER BY
// id) so the guard is deterministic when duplicates already exist.
func (s *Store) ActiveByDownloadGID(ctx context.Context, m mode.Mode, gid string) (*Grab, error) {
	// An empty GID (grabs not routed through a GID-assigning client) would match
	// every other such row arbitrarily — never treat "" as a dedup key.
	if gid == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, hold_until, monitor_entity_key, created_at, updated_at
		FROM grabs WHERE mode = ? AND download_gid = ? AND status NOT IN ('imported', 'failed')
		ORDER BY id ASC LIMIT 1
	`, string(m), gid)
	g, err := s.scanGrab(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading active grab for download gid %q: %w", gid, err)
	}
	return &g, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, so scanGrab works
// for List and Get alike.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanGrab is a method rather than a free function because it decrypts
// download_url_encrypted, which needs the Store's secret store.
func (s *Store) scanGrab(row rowScanner) (Grab, error) {
	var g Grab
	var m, encryptedURL string
	err := row.Scan(&g.ID, &m, &g.Title, &g.TMDBID, &g.TVDBID, &g.SeasonNumber, &g.EpisodeNumber, &g.SeasonSpecified, &g.QualityProfileID, &g.Indexer, &g.Protocol,
		&g.DownloadClient, &g.ClientRef, &g.DownloadGID, &g.DownloadStatus, &g.DownloadStagingPath, &g.Status, &g.RootFolderPath, &g.FlaggedForReview, &g.FlagReason,
		&encryptedURL, &g.RetryAfter, &g.RetryCount, &g.RetryReason, &g.HoldUntil, &g.MonitorEntityKey, &g.CreatedAt, &g.UpdatedAt)
	g.Mode = mode.Mode(m)
	if err != nil {
		return g, err
	}
	g.DownloadURL, err = s.decrypt(encryptedURL)
	return g, err
}

// SetPendingRetry parks a grab for a later re-search.
//
// CHANGED 2026-08-01 (explicit product decision, not a bug fix): this used to
// cap retries at maxRetryAttempts (5), after which a row became permanently
// Failed. Wade decided auto-grab should keep retrying until a match is found
// rather than give up — the cap is removed. retry_count is still incremented
// on every park (kept for observability/audit — how many times has this been
// tried), but it no longer drives any status transition. A freshly created
// pending_retry row is NOT routed through here — Create writes it at count 0
// (see Create).
//
// Known consequence, accepted deliberately: some items are structurally
// ungradeable forever (e.g. a Series season pack's runtime is always 0 by
// design, as is an Adult item whose identification carried no duration) —
// these now retry on every cycle indefinitely rather than terminating. This
// is bounded by the retry cycle's own interval (not a tight loop), and an
// operator can still manually exclude a request to stop it.
//
// download_gid is CLEARED in the same statement, and that is load-bearing in
// two directions. Parking always means "this download attempt is dead, re-search
// from scratch", and a retry is always a NEW AddNZB + SetDownloadGID (AddNZB
// mints a fresh GID every call; Resume is a no-op stub) — so the old GID is
// stale the moment the row is parked. Leaving it behind breaks both guards that
// read it:
//   - DueForRetry filters on download_gid = ”, so a row parked after an
//     ASYNCHRONOUS retrieval failure (which does hold a GID) would be parked and
//     then never retried — invisible to the very cycle it was parked for.
//   - ActiveByDownloadGID treats pending_retry as "active" (it is only blind to
//     imported/failed), so a torrent re-grab — which the download client dedupes
//     by infohash back to the SAME GID — would be rejected as "already grabbing
//     this release" against a download that is not in flight.
func (s *Store) SetPendingRetry(ctx context.Context, id int64, after time.Time, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET
			retry_count  = retry_count + 1,
			status       = 'pending_retry',
			retry_after  = ?,
			retry_reason = ?,
			download_gid = '',
			updated_at   = sakms_now()
		WHERE id = ?
	`, FormatTime(after), reason, id)
	if err != nil {
		return fmt.Errorf("setting grab %d pending retry: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// SetRetryAfter reschedules an ALREADY-PARKED grab without counting a fresh
// attempt: it writes retry_after and retry_reason and nothing else.
//
// It is the deliberately narrow sibling of SetPendingRetry, and the ONLY
// difference that matters is what it does NOT touch:
//   - retry_count is NOT incremented. SetPendingRetry's increment is
//     unconditional and means "another attempt was just made"; this method
//     means "the same attempt's NEXT try moves to a different time", which is
//     not an attempt at all.
//   - status is NOT written. The caller is adjusting a row that is already
//     pending_retry; this method must never resurrect a terminal row.
//   - download_gid is NOT cleared. SetPendingRetry clears it because parking
//     always means "this download attempt is dead"; rescheduling implies
//     nothing about the download and must not silently invalidate a GID.
//
// It has two callers today, both in internal/api's airdatemonitor.go:
//   - The air-date backoff sweep, which runs AFTER parkPendingRetry has already
//     incremented the count for this cycle's real search — calling
//     SetPendingRetry there would advance retry_count twice per searched cycle
//     and halve the documented backoff schedule.
//   - The un-monitor cleanup, which writes an explanatory retry_reason onto a
//     row it is about to flip to Failed. UpdateStatus takes only (id, status)
//     and cannot carry a reason, so the reason must be a separate, earlier
//     call; SetPendingRetry would additionally re-assert pending_retry and
//     clear download_gid for no reason.
func (s *Store) SetRetryAfter(ctx context.Context, id int64, after time.Time, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET
			retry_after  = ?,
			retry_reason = ?,
			updated_at   = sakms_now()
		WHERE id = ?
	`, FormatTime(after), reason, id)
	if err != nil {
		return fmt.Errorf("setting grab %d retry after: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// SetHoldUntil records (or refreshes) a Calendar pre-release request's hold:
// it writes hold_until and retry_reason and NOTHING else.
//
// Modelled on SetRetryAfter, the narrow-writer sibling above, and narrow for the
// same reasons — what it does NOT touch is the contract:
//   - retry_count is NOT incremented. A re-click is not an attempt. This is why
//     the re-request path must call this and never SetPendingRetry, which would
//     inflate the attempt history of a request that has never been searched.
//   - retry_after is NOT written. An empty retry_after is precisely what keeps a
//     held row invisible to DueForRetry until releaseDueGrabs promotes it; a
//     hold that also parked the row would fire the very search the hold exists
//     to prevent.
//   - status is NOT written. This must never resurrect a terminal row — and
//     since FindHeldRequest is deliberately not status-scoped, the CALLER is what
//     enforces that: it must branch on the found row's status and route a Failed
//     row to RearmHeldRequest below instead of here. Calling this method on a
//     Failed row is the documented misuse, and it is silent: the row keeps
//     status=failed and a non-empty download_gid, fails two of DueForRelease's
//     guards forever, and — because FindHeldRequest's ORDER BY id ASC keeps
//     returning that same row for the id — no later click can route around it.
//     The request vanishes while the API reports success.
//   - download_gid is NOT cleared. A hold says nothing about a download, and
//     hold_until survives promotion and dispatch as inert provenance.
//
// So the precondition is: the row is in a state a bare date refresh is
// meaningful for — still held, in flight, or already delivered. A row whose
// status is Failed needs RearmHeldRequest, not this.
//
// reason is operator-facing copy only ("held until its release date"). NOTHING
// branches on it — hold_until itself is the origin marker, deliberately, so that
// origin is never re-inferred from a reason string.
func (s *Store) SetHoldUntil(ctx context.Context, id int64, until time.Time, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET
			hold_until   = ?,
			retry_reason = ?,
			updated_at   = sakms_now()
		WHERE id = ?
	`, FormatTime(until), reason, id)
	if err != nil {
		return fmt.Errorf("setting grab %d hold until: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// RearmHeldRequest resurrects a TERMINALLY FAILED Calendar pre-release request
// into a fresh, promotable held row — the sanctioned exception to SetHoldUntil's
// "never resurrect a terminal row" contract above, and the only one.
//
// It exists because the re-click path can genuinely land on a Failed row.
// FindHeldRequest is not status-scoped (deliberately — see its doc), so a request
// that promoted, dispatched, and then took a permanent 451/DMCA classification is
// still what a second click finds. A bare SetHoldUntil there writes a future date
// onto a row that DueForRelease can never return, which is a silent dead end, not
// a refresh. Plan §5.3 treats a re-click as a legitimate, successful action; this
// is what makes that true for the one status where it otherwise is not.
//
// It resets ALL THREE of DueForRelease's non-hold guards, which is precisely what
// distinguishes it from the narrow writers above:
//   - status back to pending_retry — a re-armed request has not been attempted
//     since, and pending_retry is by design never terminal on its own.
//   - download_gid cleared — the Failed row's GID is a dead download's, and
//     DueForRelease requires an empty one (a non-empty GID means "already
//     dispatched, do not promote again").
//   - retry_after cleared — the empty retry_after IS the hold, exactly as it is
//     for a freshly parked request (see api.parkPreReleaseRequest). A row that
//     kept one would rejoin the ordinary retry track and search before its date.
//
// retry_count is deliberately KEPT, matching Relaunch's reasoning: it is this
// request's attempt history, observability only since SetPendingRetry no longer
// caps on it, and a re-click is not an attempt to erase.
//
// WHY RESURRECTING IS SAFE, per status — this is not "reset whatever we find":
//   - Failed, and ONLY Failed, is accepted. It is terminal and never retried, so
//     nothing is in flight to be reset out from under, and by the Status doc its
//     two producers are a permanent takedown classification and an operator
//     un-monitor — in both cases the row holds no download and delivered nothing.
//   - Queued/Downloading are ACTIVE. Re-arming one would strand a live download:
//     the GID would be cleared while the downloader still owns it, and the row
//     would promote and dispatch a second copy. The status guard in the WHERE
//     clause refuses this even if a caller asks.
//   - Completed/Imported already delivered the film. Re-arming would re-download
//     something the operator has.
//
// The guard is in the WHERE clause, not only at the call site, so a status change
// racing between the caller's read and this write loses safely: zero rows are
// affected and the caller gets an error rather than a silent stray reset.
//
// One ordering dependency worth stating, because it looks like an unconsidered
// duplicate-download path otherwise: api.terminateSupersededRequest also produces
// Failed held rows (a request the operator satisfied another way). Re-arming one
// is correct ONLY because the route runs nonHeldMovieWork FIRST and short-circuits
// while the superseding library entry or grab still exists — so this is reached
// only once that thing is gone, which is exactly when the operator does want the
// request back.
func (s *Store) RearmHeldRequest(ctx context.Context, id int64, until time.Time, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET
			status       = ?,
			download_gid = '',
			retry_after  = '',
			hold_until   = ?,
			retry_reason = ?,
			updated_at   = sakms_now()
		WHERE id = ? AND status = ? AND hold_until != ''
	`, string(PendingRetry), FormatTime(until), reason, id, string(Failed))
	if err != nil {
		return fmt.Errorf("re-arming held request %d: %w", id, err)
	}
	if err := dbutil.CheckAffected(res, id, ErrNotFound); err != nil {
		return fmt.Errorf("re-arming held request %d: it is no longer a failed held row: %w", id, err)
	}
	return nil
}

// Dispatch is what a successful (re-)dispatch learned about a grab: which
// release was picked, where it came from, and the GID the download engine
// assigned it.
type Dispatch struct {
	Indexer        string
	Protocol       string
	DownloadClient string
	RootFolderPath string
	DownloadURL    string
	GID            string
}

// Relaunch re-arms a pending_retry grab onto the release a retry cycle just
// dispatched, INSTEAD of recording a second row for the same request.
//
// This is the other half of DueForRetry's contract below ("once a retry
// dispatches successfully the row gains a GID and rejoins the normal
// ActiveByDownloadGID-guarded lifecycle"). Without it the retry's new row and
// the original pending row both exist, the pending one still due, and every
// subsequent cycle grabs the same release again — AddNZB mints a fresh GID per
// call, so the GID guard cannot catch that duplicate.
//
// The retry state is cleared in the same statement: a row that is downloading
// must not still carry a retry_after (DueForRetry would hand it straight back)
// nor a retry_reason (the Requests screen renders it, and "no candidate cleared
// the quality floor" on an in-flight download is a visible lie). retry_count is
// deliberately KEPT — it is this request's attempt history (observability only,
// since SetPendingRetry no longer caps on it), so a later failure's count
// continues from where this request's history left off instead of restarting.
func (s *Store) Relaunch(ctx context.Context, id int64, d Dispatch) error {
	encrypted, err := s.encrypt(d.DownloadURL)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET
			status                = 'queued',
			indexer               = ?,
			protocol              = ?,
			download_client       = ?,
			root_folder_path      = ?,
			download_url_encrypted = ?,
			download_gid          = ?,
			retry_after           = '',
			retry_reason          = '',
			updated_at            = sakms_now()
		WHERE id = ?
	`, d.Indexer, d.Protocol, d.DownloadClient, d.RootFolderPath, encrypted, d.GID, id)
	if err != nil {
		return fmt.Errorf("relaunching grab %d: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// DueForRetry returns every pending_retry grab whose retry_after has arrived,
// oldest first.
//
// Two guards, both required. download_gid = ”: once a retry dispatches
// successfully the row gains a GID and rejoins the normal
// ActiveByDownloadGID-guarded lifecycle — returning it again would double-grab.
// retry_after != ”: the column defaults to the empty string, which sorts BELOW
// every real timestamp and would otherwise make every unparked row look due.
//
// A THIRD guard, added 2026-08-02 with the pre-release hold:
// (hold_until = '' OR hold_until <= ?). A held Calendar pre-release request is
// normally invisible here anyway, because it carries no retry_after — but that
// is a property of who writes the row, not a structural guarantee, and it was
// escapable: parkPendingRetry selects its target with FindPendingRetry, which
// is keyed on (mode, tmdb_id, season…) and ignores ExistingGrabID, so an
// unrelated failing retry for the SAME movie could stamp a real retry_after
// onto the held row and cause the unreleased film to be searched — and possibly
// dispatched — before its release date. This conjunct is defence-in-depth
// against ANY writer of retry_after, present or future, not just that one path.
//
// It is deliberately an OR rather than hold_until = '': a PROMOTED row (its
// hold_until now in the past) must rejoin the ordinary retry track, since
// hold_until is never cleared and is inert provenance from that point on.
func (s *Store) DueForRetry(ctx context.Context, now time.Time) ([]Grab, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, hold_until, monitor_entity_key, created_at, updated_at
		FROM grabs
		WHERE status = ? AND download_gid = '' AND retry_after != '' AND retry_after <= ?
		  AND (hold_until = '' OR hold_until <= ?)
		ORDER BY retry_after ASC, id ASC
	`, string(PendingRetry), FormatTime(now), FormatTime(now))
	if err != nil {
		return nil, fmt.Errorf("listing grabs due for retry: %w", err)
	}
	defer rows.Close()

	out := []Grab{}
	for rows.Next() {
		g, err := s.scanGrab(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning grab due for retry: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// FindPendingRetry is the dedup guard for GID-less pending_retry rows.
//
// ActiveByDownloadGID cannot see such a row — it has no GID yet — so without a
// second key a re-submitted request and every retry cycle would insert another
// duplicate. The key is (mode, tmdb_id, season, season_specified, episode) for
// Movies/Series and (mode, lowercased title, season, season_specified, episode)
// for Adult, whose scenes carry no TMDB id.
//
// Both title and seasonSpecified are load-bearing, not optional:
//   - Adult always has tmdbID 0, so without the title discriminator every Adult
//     retry row would collapse onto one key and clobber the others.
//   - TMDB uses season 0 for "Specials", which collides with SeasonNumber's Go
//     zero-value for "no season was picked" — SeasonSpecified is the ONLY thing
//     that separates them (see Grab's doc comment).
//
// The tmdb-or-title choice is delegated to excludes.Key, the same normalization
// api.requestKey uses, and the comparison is done in Go rather than SQL so the
// two can never normalize a title differently. Returns ErrNotFound when no
// matching pending row exists.
func (s *Store) FindPendingRetry(ctx context.Context, m mode.Mode, tmdbID int, title string, season int, seasonSpecified bool, episode int) (*Grab, error) {
	want := excludes.Key(string(m), tmdbID, title)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, hold_until, monitor_entity_key, created_at, updated_at
		FROM grabs
		WHERE mode = ? AND status = ? AND season_number = ? AND season_specified = ? AND episode_number = ?
		ORDER BY id ASC
	`, string(m), string(PendingRetry), season, seasonSpecified, episode)
	if err != nil {
		return nil, fmt.Errorf("finding pending retry grab for %q: %w", title, err)
	}
	defer rows.Close()

	for rows.Next() {
		g, err := s.scanGrab(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning pending retry grab: %w", err)
		}
		if excludes.Key(string(g.Mode), g.TMDBID, g.Title) == want {
			return &g, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, ErrNotFound
}

// FindHeldRequest is the dedup key for Calendar pre-release requests: the
// earliest grab for (mode, tmdb_id) that originated as one. Returns ErrNotFound
// when this title was never click-requested.
//
// It is DELIBERATELY NOT STATUS-SCOPED, and that is the single most important
// property of this query — it is what lets one predicate close two distinct
// duplicate-detection bugs that the status-scoped FindPendingRetry has:
//
//   - A held row can never be confused with an unrelated live retry row.
//     hold_until = '' on every non-held row, so they cannot match at all. There
//     is no branch to get wrong and no discriminator to keep honest — which is
//     the whole point of paying for a column rather than inferring origin from
//     status + retry_reason + retry_count.
//   - A second click finds the first request no matter how far the row has
//     travelled — held, promoted, dispatched, imported or failed. Status-scoping
//     to pending_retry would miss a row that already promoted and flipped to
//     queued, and the click would mint a SECOND row carrying an already-past
//     date, which the very next cycle would dispatch as a duplicate download.
//
// Both of those depend on hold_until never being cleared. Nothing in this
// package clears it; see the Grab field's doc.
//
// ORDER BY id ASC returns the ORIGINAL request if duplicates somehow exist, so
// the guard is deterministic — the same convention ActiveByDownloadGID uses. As
// of migration 0057's partial unique index idx_grabs_held_request, duplicates
// CANNOT exist (Create refuses the second with ErrHeldRequestExists), so this is
// now belt-and-braces rather than the real guarantee; nothing depends on it.
//
// The caller must branch on the returned row's STATUS. This query's whole value
// is that it is not status-scoped, which means it can and does return a terminal
// Failed row — see SetHoldUntil's and RearmHeldRequest's docs for what each
// status needs.
func (s *Store) FindHeldRequest(ctx context.Context, m mode.Mode, tmdbID int) (*Grab, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, hold_until, monitor_entity_key, created_at, updated_at
		FROM grabs
		WHERE mode = ? AND tmdb_id = ? AND hold_until != ''
		ORDER BY id ASC LIMIT 1
	`, string(m), tmdbID)
	g, err := s.scanGrab(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("finding held request for tmdb id %d: %w", tmdbID, err)
	}
	return &g, nil
}

// DueForRelease returns every held pre-release request whose hold_until has
// arrived and which has not yet been promoted — the promotion candidates
// releaseDueGrabs dispatches, soonest-held first.
//
// FOUR guards, every one of them load-bearing:
//
//   - status = pending_retry and download_gid = '' mirror DueForRetry's
//     reasoning exactly: a row that has dispatched holds a GID and has rejoined
//     the normal ActiveByDownloadGID-guarded lifecycle, so returning it again
//     would double-grab.
//   - hold_until != '' excludes every ordinary row. Like retry_after, the column
//     defaults to the empty string, which sorts BELOW every real timestamp and
//     would otherwise make the whole table look due.
//   - retry_after = '' IS WHAT MAKES PROMOTION FIRE EXACTLY ONCE, and it is why
//     hold_until never needs clearing. After a promotion attempt the row carries
//     either a real retry_after (parkPendingRetry on a no-match, reparkFailedRetry
//     on an error) or a real GID (a successful dispatch), so it can never be
//     returned here again. Eligibility ends; provenance survives.
//
// The hold_until <= ? comparison is lexicographic on FormatTime's sortable
// layout, the same string comparison DueForRetry already depends on.
//
// There is deliberately NO LIMIT: the per-cycle dispatch cap belongs to the
// caller's loop (it must not consume budget on rows it skips without searching),
// not to this query.
func (s *Store) DueForRelease(ctx context.Context, now time.Time) ([]Grab, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, hold_until, monitor_entity_key, created_at, updated_at
		FROM grabs
		WHERE status = ? AND download_gid = '' AND retry_after = ''
		  AND hold_until != '' AND hold_until <= ?
		ORDER BY hold_until ASC, id ASC
	`, string(PendingRetry), FormatTime(now))
	if err != nil {
		return nil, fmt.Errorf("listing grabs due for release: %w", err)
	}
	defer rows.Close()

	out := []Grab{}
	for rows.Next() {
		g, err := s.scanGrab(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning grab due for release: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetMonitorEntityKey writes the monitor_entity_key column on an existing grab.
// Called by the Adult monitor dispatch pass after a grab is created or parked,
// to stamp it with the monitor-entity origin marker for later un-monitor cleanup.
// Returns ErrNotFound when id resolves to nothing.
func (s *Store) SetMonitorEntityKey(ctx context.Context, id int64, key string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET
			monitor_entity_key = ?,
			updated_at         = sakms_now()
		WHERE id = ?
	`, key, id)
	if err != nil {
		return fmt.Errorf("setting monitor entity key on grab %d: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}
