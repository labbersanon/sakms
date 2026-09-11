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

// Store persists optional mirrors of download engine durable state.
type Store struct {
	db *sql.DB
}

// New returns a Store over db (SQLite).
func New(db *sql.DB) *Store { return &Store{db: db} }

// SaveResume mirrors a usenet staging resume snapshot (UI/debug). Sidecar remains SoT.
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

// ClearResume drops the DB mirror for gid (after import / force-full).
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

// GetResumeJSON returns the mirrored resume JSON for gid, or "" when absent.
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

// SeedState is the durable seed-window baseline for a torrent gid (infohash).
type SeedState struct {
	GID           string
	SeedStartedAt time.Time
	BaselineUp    int64
	TotalBytes    int64
}

// SaveSeed persists seed-window baselines so ratio/duration limits survive restart.
func (s *Store) saveSeedRow(st SeedState) error {
	if s == nil || s.db == nil || st.GID == "" {
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
	`, st.GID, st.SeedStartedAt.UTC().Format(time.RFC3339Nano), st.BaselineUp, st.TotalBytes, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("downloadstate: save torrent seed %s: %w", st.GID, err)
	}
	return nil
}

// GetSeed loads durable seed baselines for gid.
func (s *Store) GetSeed(gid string) (SeedState, bool, error) {
	var st SeedState
	if s == nil || s.db == nil || gid == "" {
		return st, false, nil
	}
	var started string
	err := s.db.QueryRow(`
		SELECT download_gid, seed_started_at, seed_baseline_up, seed_total_bytes
		FROM torrent_seed_state WHERE download_gid = ?
	`, gid).Scan(&st.GID, &started, &st.BaselineUp, &st.TotalBytes)
	if err == sql.ErrNoRows {
		return st, false, nil
	}
	if err != nil {
		return st, false, fmt.Errorf("downloadstate: get torrent seed %s: %w", gid, err)
	}
	st.SeedStartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if st.SeedStartedAt.IsZero() {
		st.SeedStartedAt, _ = time.Parse(time.RFC3339, started)
	}
	return st, true, nil
}

// ClearSeed drops seed baselines after the seed window ends or the torrent is removed.
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

// SaveSeed implements downloader.SeedStore.
func (s *Store) SaveSeed(gid string, startedAt time.Time, baselineUp, totalBytes int64) error {
	return s.saveSeedRow(SeedState{GID: gid, SeedStartedAt: startedAt, BaselineUp: baselineUp, TotalBytes: totalBytes})
}

// LoadSeed implements downloader.SeedStore.
func (s *Store) LoadSeed(gid string) (time.Time, int64, int64, bool, error) {
	st, ok, err := s.GetSeed(gid)
	if err != nil || !ok {
		return time.Time{}, 0, 0, ok, err
	}
	return st.SeedStartedAt, st.BaselineUp, st.TotalBytes, true, nil
}
