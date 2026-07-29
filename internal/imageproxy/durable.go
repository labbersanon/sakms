package imageproxy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DurableStore persists validated image bytes across process restarts. bytes on
// disk under baseDir; a TEXT-only index row in sakms.db (migration 0046). Keyed
// by the SHA-256 of the POST-VALIDATION url string (imageproxy.validate's u.String()),
// identical on the precompute-write and request-read sides.
type DurableStore struct {
	db      *sql.DB
	baseDir string // <DataDir>/imagecache
}

// tempSuffix marks in-progress writes. The deterministic "<sha>.tmp-*" prefix
// (os.CreateTemp appends a random suffix) makes stray temps — interrupted-write
// orphans — trivially identifiable by Reconcile's single tree walk.
const tempSuffix = ".tmp-"

// NewDurableStore returns a DurableStore backed by db (the same *sql.DB every
// other store wraps) with its byte payloads rooted at baseDir. baseDir is
// created if missing so the first Put never fails on a nonexistent root.
func NewDurableStore(db *sql.DB, baseDir string) (*DurableStore, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating image cache dir %s: %w", baseDir, err)
	}
	return &DurableStore{db: db, baseDir: baseDir}, nil
}

// shaOf is the single keying function: the SHA-256 hex of the normalized url
// string. Callers pass the post-validation url string; the store never re-parses
// or re-validates it (that is imageproxy.validate's job), so the write and read
// sides can never key differently.
func shaOf(normalizedURL string) string {
	sum := sha256.Sum256([]byte(normalizedURL))
	return hex.EncodeToString(sum[:])
}

// pathFor returns the sharded on-disk path for a key: baseDir/<sha[:2]>/<sha>.
// Sharding by the first two hex chars keeps any single directory to a few
// hundred entries even at the 512 MB cap's file count.
func (s *DurableStore) pathFor(sha string) (shard, file string) {
	shard = filepath.Join(s.baseDir, sha[:2])
	return shard, filepath.Join(shard, sha)
}

// Get returns bytes+contentType for the normalized url key, or found=false.
// SELF-HEALING (read side): an index hit whose backing file is missing/unreadable
// is reported as a miss AND its stale index row is deleted (returns found=false).
func (s *DurableStore) Get(ctx context.Context, normalizedURL string) (body []byte, contentType string, found bool, err error) {
	sha := shaOf(normalizedURL)
	var ct string
	row := s.db.QueryRowContext(ctx,
		`SELECT content_type FROM adult_image_cache WHERE url_sha256 = ?`, sha)
	switch err := row.Scan(&ct); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, "", false, nil
	case err != nil:
		return nil, "", false, err
	}

	_, file := s.pathFor(sha)
	data, readErr := os.ReadFile(file)
	if readErr != nil {
		// Self-heal: the index claims this key but the backing file is gone or
		// unreadable. Report a clean miss (nil error — the caller falls through
		// to the live path) and drop the stale row so Has stops lying too.
		if _, delErr := s.db.ExecContext(ctx,
			`DELETE FROM adult_image_cache WHERE url_sha256 = ?`, sha); delErr != nil {
			return nil, "", false, delErr
		}
		return nil, "", false, nil
	}
	return data, ct, true, nil
}

// Put writes bytes atomically then upserts the index row. A torn write is never
// indexed. Idempotent overwrite (same key re-Put replaces file + row).
//
// EXDEV-SAFE INVARIANT (MUST — see pre-mortem S1/S3): the temp file MUST be
// created INSIDE the final shard directory, never in the system temp dir, so the
// rename is always same-filesystem. os.CreateTemp("", ...) (system /tmp,
// frequently a different fs/tmpfs than the iSCSI-mounted DataDir) would EXDEV in
// production while passing a single-t.TempDir unit test — the exact silent-no-op
// trap AC #12 guards against.
func (s *DurableStore) Put(ctx context.Context, normalizedURL, contentType string, body []byte) error {
	sha := shaOf(normalizedURL)
	shard, file := s.pathFor(sha)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		return fmt.Errorf("creating shard dir %s: %w", shard, err)
	}

	// Temp file created INSIDE the shard dir so the rename below is same-fs.
	tmp, err := os.CreateTemp(shard, sha+tempSuffix+"*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", shard, err)
	}
	// Self-clean: a no-op after a successful rename (the temp no longer exists),
	// but reclaims the temp on ANY error path (disk full, ctx cancel between
	// CreateTemp and Rename) so a mid-uptime errored Put never leaks until the
	// next cycle's Reconcile.
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("writing image bytes: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsyncing image bytes: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	// Same-filesystem atomic rename: the final file is all-or-nothing.
	if err := os.Rename(tmp.Name(), file); err != nil {
		return fmt.Errorf("renaming temp into place: %w", err)
	}

	// Index row is written ONLY after a successful rename — a torn/failed write
	// is never indexed. byte_size is the real on-disk size (len(body)) so the
	// walk-based cap and the index agree once the tree is reconciled.
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO adult_image_cache (url_sha256, source_url, content_type, byte_size, fetched_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(url_sha256) DO UPDATE SET
		   source_url   = excluded.source_url,
		   content_type = excluded.content_type,
		   byte_size    = excluded.byte_size,
		   fetched_at   = excluded.fetched_at`,
		sha, normalizedURL, contentType, len(body), time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upserting image cache index row: %w", err)
	}
	return nil
}

// Has is an index-only presence check for the incremental-skip path (does not
// open the file). It is honest because Reconcile (below) runs at the START OF
// EVERY runCycle (not just boot — §2.5), so at each cycle start the index matches
// disk; an out-of-band file loss between cycles is caught by the next cycle's
// Reconcile and, on the read side, by Get's self-heal. Callers must not treat Has
// as a disk-truth oracle mid-cycle, but every precompute cycle begins reconciled.
func (s *DurableStore) Has(ctx context.Context, normalizedURL string) (bool, error) {
	sha := shaOf(normalizedURL)
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM adult_image_cache WHERE url_sha256 = ?`, sha).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Reconcile is the WRITE-SIDE self-heal + accounting-truth sweep (MUST — see
// pre-mortem S4). Runs at the START OF EVERY runCycle (not just boot — §2.5),
// folded into the same tree walk TotalBytes/PruneToCap already do, before warming:
//
//	(a) delete stray *.tmp-* files (interrupted-write orphans);
//	(b) delete index rows whose backing file is gone (write-side analogue of
//	    Get's read-side self-heal, so Has stops lying after a file loss);
//	(c) delete orphan data files with no index row (invisible-to-cap leaks).
//
// Cheap: one walk over a directory whose size the 512 MB cap already bounds
// (a few thousand small files worst case). Returns counts for the boot log.
func (s *DurableStore) Reconcile(ctx context.Context) (strayTemps, orphanRows, orphanFiles int, err error) {
	// Parallel index scan: read the full set of indexed shas up front (cursor
	// closed before any mutation, mandatory under SetMaxOpenConns(1)). A live
	// data file is removed from the set during the walk; whatever remains after
	// the walk is a row with no backing file (orphan row).
	indexed := make(map[string]bool)
	rows, qErr := s.db.QueryContext(ctx, `SELECT url_sha256 FROM adult_image_cache`)
	if qErr != nil {
		return 0, 0, 0, qErr
	}
	for rows.Next() {
		var sha string
		if scanErr := rows.Scan(&sha); scanErr != nil {
			rows.Close()
			return 0, 0, 0, scanErr
		}
		indexed[sha] = true
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		rows.Close()
		return 0, 0, 0, rowsErr
	}
	rows.Close()

	// Single tree walk classifying each file entry as stray-temp / live / orphan.
	walkErr := filepath.WalkDir(s.baseDir, func(path string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		// Check temp FIRST so a stray temp is never double-counted as an orphan
		// data file.
		if strings.Contains(name, tempSuffix) {
			if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				return rmErr
			}
			strayTemps++
			return nil
		}
		// A non-temp file's basename is its sha key. Live if indexed (consume the
		// entry), orphan otherwise.
		if indexed[name] {
			delete(indexed, name)
			return nil
		}
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return rmErr
		}
		orphanFiles++
		return nil
	})
	if walkErr != nil {
		return strayTemps, 0, orphanFiles, walkErr
	}

	// Whatever remains in the index set has no backing file — delete those rows
	// (write-side self-heal, so Has stops lying after an out-of-band file loss).
	for sha := range indexed {
		if _, delErr := s.db.ExecContext(ctx,
			`DELETE FROM adult_image_cache WHERE url_sha256 = ?`, sha); delErr != nil {
			return strayTemps, orphanRows, orphanFiles, delErr
		}
		orphanRows++
	}
	return strayTemps, orphanRows, orphanFiles, nil
}

// TotalBytes computes usage by a DIRECTORY WALK (summing real on-disk file
// sizes), NOT SUM(byte_size) over the index — so orphans and any index/disk drift
// can never fool the cap into believing it's under budget (pre-mortem S4). Called
// after Reconcile at cycle start, so the walk sees a reconciled tree.
func (s *DurableStore) TotalBytes(ctx context.Context) (int64, error) {
	var total int64
	err := filepath.WalkDir(s.baseDir, func(path string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if d.IsDir() {
			return nil
		}
		info, iErr := d.Info()
		if iErr != nil {
			// A file that vanished mid-walk contributes nothing; ignore it
			// rather than failing the whole accounting pass.
			if errors.Is(iErr, fs.ErrNotExist) {
				return nil
			}
			return iErr
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// PruneToCap enforces the hard disk cap (§4 circuit breaker): when TotalBytes is
// over cap, delete oldest-fetched rows+files (LRU by fetched_at) down to a
// low-water mark, deleting the file and its index row together. TERMINATION
// GUARD: the loop stops once the index is empty even if walk-based usage is still
// nominally over cap — any remaining bytes at that point can only be unindexed
// orphans, which only Reconcile clears, so continuing would spin forever.
func (s *DurableStore) PruneToCap(ctx context.Context, capBytes int64) error {
	total, err := s.TotalBytes(ctx)
	if err != nil {
		return err
	}
	if total <= capBytes {
		return nil
	}

	// Read the eviction candidates oldest-first in one query (cursor closed
	// before any DELETE, mandatory under SetMaxOpenConns(1)). Iterating this
	// slice — rather than re-walking the tree per eviction — is the accounting
	// the running subtraction relies on; safe because byte_size == real on-disk
	// size after Reconcile.
	type candidate struct {
		sha  string
		size int64
	}
	var candidates []candidate
	rows, qErr := s.db.QueryContext(ctx,
		`SELECT url_sha256, byte_size FROM adult_image_cache ORDER BY fetched_at ASC`)
	if qErr != nil {
		return qErr
	}
	for rows.Next() {
		var c candidate
		if scanErr := rows.Scan(&c.sha, &c.size); scanErr != nil {
			rows.Close()
			return scanErr
		}
		candidates = append(candidates, c)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		rows.Close()
		return rowsErr
	}
	rows.Close()

	for _, c := range candidates {
		if total <= capBytes {
			break
		}
		_, file := s.pathFor(c.sha)
		if rmErr := os.Remove(file); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return rmErr
		}
		if _, delErr := s.db.ExecContext(ctx,
			`DELETE FROM adult_image_cache WHERE url_sha256 = ?`, c.sha); delErr != nil {
			return delErr
		}
		total -= c.size
	}
	return nil
}
