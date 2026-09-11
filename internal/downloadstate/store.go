package downloadstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-09-11: DB mirror for usenet resume + torrent seed baselines
// Reason: staging sidecar is SoT for segments; DB mirror feeds UI/debug and
//         seed-ratio windows must survive process restart (ARR-parity Phase 2)
// Troubleshooting: rows in usenet_resume_state / torrent_seed_state; cleared on import/stop
// Review if: resume state moves entirely into grabs table
// Related: usenet.resumeTracker, downloader.beginSeeding, RemoveOwnedStagingDir

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) SaveResume(gid string, snap usenet.ResumeSnapshot) error {
	if s == nil || s.db == nil || gid == "" {
		return nil
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO usenet_resume_state (download_gid, state_json, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(download_gid) DO UPDATE SET
			state_json = excluded.state_json,
			updated_at = excluded.updated_at
	`, gid, string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("downloadstate: save usenet resume %s: %w", gid, err)
	}
	return nil
}

func (s *Store) ClearResume(gid string) error {
	if s == nil || s.db == nil || gid == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM usenet_resume_state WHERE download_gid = ?`, gid)
	if err != nil {
		return fmt.Errorf("downloadstate: clear usenet resume %s: %w", gid, err)
	}
	return nil
}

func (s *Store) GetResumeJSON(ctx context.Context, gid string) (string, error) {
	if s == nil || s.db == nil || gid == "" {
		return "", nil
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT state_json FROM usenet_resume_state WHERE download_gid = ?`, gid).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("downloadstate: get usenet resume %s: %w", gid, err)
	}
	return raw, nil
}

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
