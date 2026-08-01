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

	"github.com/labbersanon/sakms/internal/dbutil"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/mode"
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
	// PendingRetry is an auto-grab that found nothing worth grabbing (no
	// candidate cleared the quality floor) or whose usenet retrieval failed
	// transiently — it holds no download and will be re-searched from scratch
	// on the next retry cycle. Deliberately NOT Failed: the Requests
	// aggregation would misreport it, and a genuinely failed grab would be
	// retried forever.
	PendingRetry Status = "pending_retry"
)

// maxRetryAttempts caps how many times a pending_retry grab is re-searched
// before it gives up and becomes Failed with an explicit reason. Some items are
// PERMANENTLY ungradeable — a Series season pack always has runtime 0 by design
// (seriesEpisodeRuntimeSeconds returns 0 for episode <= 0), as does an Adult
// item whose identification carried no duration — so without a cap their
// retries could never succeed and would run forever.
const maxRetryAttempts = 5

// sqliteTimeLayout is the shape strftime('%Y-%m-%dT%H:%M:%fZ', 'now') writes
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
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
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
	}
	encrypted, err := s.encrypt(g.DownloadURL)
	if err != nil {
		return Grab{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO grabs (
			mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
			download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path,
			download_url_encrypted, retry_after, retry_count, retry_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at
	`, string(g.Mode), g.Title, g.TMDBID, g.TVDBID, g.SeasonNumber, g.EpisodeNumber, g.SeasonSpecified, g.QualityProfileID, g.Indexer, g.Protocol,
		g.DownloadClient, g.ClientRef, g.DownloadGID, g.DownloadStatus, g.DownloadStagingPath, string(g.Status), g.RootFolderPath,
		encrypted, g.RetryAfter, g.RetryCount, g.RetryReason)

	if err := row.Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return Grab{}, fmt.Errorf("inserting grab for %q: %w", g.Title, err)
	}
	return g, nil
}

// List returns every grab for m, most recently created first.
func (s *Store) List(ctx context.Context, m mode.Mode) ([]Grab, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, created_at, updated_at
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
		       download_url_encrypted, retry_after, retry_count, retry_reason, created_at, updated_at
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
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, created_at, updated_at
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
		       download_url_encrypted, retry_after, retry_count, retry_reason, created_at, updated_at
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
		&encryptedURL, &g.RetryAfter, &g.RetryCount, &g.RetryReason, &g.CreatedAt, &g.UpdatedAt)
	g.Mode = mode.Mode(m)
	if err != nil {
		return g, err
	}
	g.DownloadURL, err = s.decrypt(encryptedURL)
	return g, err
}

// SetPendingRetry parks a grab for a later re-search, or gives up on it.
//
// The retry_count increment and the give-up decision are ONE statement so they
// cannot interleave: once the incremented count exceeds maxRetryAttempts the
// row becomes Failed with an explaining reason and no retry_after, instead of
// cycling back to PendingRetry forever. A freshly created pending_retry row is
// NOT routed through here — Create writes it at count 0 (see Create).
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
	gaveUp := fmt.Sprintf("gave up after %d retry attempts: %s", maxRetryAttempts, reason)
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET
			retry_count  = retry_count + 1,
			status       = CASE WHEN retry_count + 1 > ? THEN 'failed' ELSE 'pending_retry' END,
			retry_after  = CASE WHEN retry_count + 1 > ? THEN ''       ELSE ? END,
			retry_reason = CASE WHEN retry_count + 1 > ? THEN ?        ELSE ? END,
			download_gid = '',
			updated_at   = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?
	`, maxRetryAttempts, maxRetryAttempts, FormatTime(after), maxRetryAttempts, gaveUp, reason, id)
	if err != nil {
		return fmt.Errorf("setting grab %d pending retry: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
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
// deliberately KEPT — it is this request's attempt history, so a later failure
// resumes the maxRetryAttempts cap where it left off instead of restarting it.
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
			updated_at            = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
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
func (s *Store) DueForRetry(ctx context.Context, now time.Time) ([]Grab, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, title, tmdb_id, tvdb_id, season_number, episode_number, season_specified, quality_profile_id, indexer, protocol,
		       download_client, client_ref, download_gid, download_status, download_staging_path, status, root_folder_path, flagged_for_review, flag_reason,
		       download_url_encrypted, retry_after, retry_count, retry_reason, created_at, updated_at
		FROM grabs
		WHERE status = ? AND download_gid = '' AND retry_after != '' AND retry_after <= ?
		ORDER BY retry_after ASC, id ASC
	`, string(PendingRetry), FormatTime(now))
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
		       download_url_encrypted, retry_after, retry_count, retry_reason, created_at, updated_at
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
