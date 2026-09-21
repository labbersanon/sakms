package downloadstate

import (
	"database/sql"
	"fmt"
	"time"
)

// Claude 2026-09-11: torrent seed baselines that survive process restart
// Reason: seed-ratio windows must accumulate across boots (ARR-parity Phase 2)
// Troubleshooting: rows in torrent_seed_state; cleared on seed-stop/cancel
// Review if: seed state gains per-file paths
// Related: downloader.beginSeeding
//
// Claude 2026-09-21: Usenet DB resume mirror removed (sidecar-only).
// Reason: usenet_resume_state TOAST filled the 8G sakms_db LUN; .sakms-resume.json
//   is SoT (like NZBGet/SABnzbd). This store keeps torrent seed methods only.
// Troubleshooting: migration 0028 DROP TABLE usenet_resume_state
// Review if: a UI/debug resume view is required (read sidecar, do not re-mirror).

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) SaveSeed(gid string, startedAt time.Time, baselineUp, totalBytes int64) error {
	if s == nil || s.db == nil || gid == "" {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO torrent_seed_state (download_gid, seed_started_at, seed_baseline_up, seed_total_bytes, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(download_gid) DO UPDATE SET
			seed_started_at = excluded.seed_started_at,
			seed_baseline_up = excluded.seed_baseline_up,
			seed_total_bytes = excluded.seed_total_bytes,
			updated_at = excluded.updated_at
	`, gid, startedAt.UTC().Format(time.RFC3339Nano), baselineUp, totalBytes, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("downloadstate: save torrent seed %s: %w", gid, err)
	}
	return nil
}

func (s *Store) LoadSeed(gid string) (time.Time, int64, int64, bool, error) {
	if s == nil || s.db == nil || gid == "" {
		return time.Time{}, 0, 0, false, nil
	}
	var started string
	var baseline, total int64
	err := s.db.QueryRow(`
		SELECT seed_started_at, seed_baseline_up, seed_total_bytes
		FROM torrent_seed_state WHERE download_gid = ?
	`, gid).Scan(&started, &baseline, &total)
	if err == sql.ErrNoRows {
		return time.Time{}, 0, 0, false, nil
	}
	if err != nil {
		return time.Time{}, 0, 0, false, fmt.Errorf("downloadstate: get torrent seed %s: %w", gid, err)
	}
	startedAt, _ := time.Parse(time.RFC3339Nano, started)
	if startedAt.IsZero() {
		startedAt, _ = time.Parse(time.RFC3339, started)
	}
	return startedAt, baseline, total, true, nil
}

func (s *Store) ClearSeed(gid string) error {
	if s == nil || s.db == nil || gid == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM torrent_seed_state WHERE download_gid = ?`, gid)
	if err != nil {
		return fmt.Errorf("downloadstate: clear torrent seed %s: %w", gid, err)
	}
	return nil
}
