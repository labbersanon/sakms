package db_test

import (
	"context"
	"database/sql"
	"embed"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"

	"github.com/pressly/goose/v3"
)

// Claude 2026-08-08: live up/down round-trip for migration 0005.
// Reason: every sibling migration_*_test.go in this package is a retired SQLite stub, so
// there was no live pattern to follow — this is the first one. It is package db_test (not
// db) because internal/dbtest imports internal/db; an in-package test cannot reach it, and
// internal/db's own openTestDB hits the SHARED test DSN, where goose.DownTo would destroy
// sibling packages' schema mid-run.
// Troubleshooting: skips when SAKMS_TEST_DATABASE_URL is unset (dbtest.New's own gate).
// Review if: internal/dbtest stops cloning a fully-migrated template database.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// TestMigration0005LibraryEpisodeFiles asserts the constraints that distinguish
// library_episode_files from a copy of library_item_files (0003), then rolls 0005 back and
// forward again, proving the Down reverses the Up and that the Up's backfill runs against
// pre-existing episode rows.
func TestMigration0005LibraryEpisodeFiles(t *testing.T) {
	// No t.Parallel: goose's dialect and base FS are package globals.
	sqlDB := dbtest.New(t)
	ctx := context.Background()

	const rangePath = "/tv/Show/S01E01-E02.mkv"

	seriesID := insertSeries(t, sqlDB, 900501)
	// epA and epB are the two halves of one logical-episode-split (range) file: two episode
	// rows at ONE path. epC is a separate slot, used for the cascade check so the round-trip
	// below still has both halves of the range alive.
	epA := insertEpisode(t, sqlDB, seriesID, 1, 1, rangePath)
	epB := insertEpisode(t, sqlDB, seriesID, 1, 2, rangePath)
	epC := insertEpisode(t, sqlDB, seriesID, 1, 3, "/tv/Show/S01E03.mkv")

	// A range file legitimately backs several episode rows at one path: the reason this
	// table has UNIQUE(episode_id, file_path) and not 0003's bare UNIQUE(file_path).
	insertFile(t, sqlDB, epA, "/tv/Show/alt.mkv", false)
	insertFile(t, sqlDB, epB, "/tv/Show/alt.mkv", false)

	// The composite key itself still rejects a true duplicate.
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO library_episode_files (episode_id, file_path) VALUES (?, ?)`,
		epA, "/tv/Show/alt.mkv",
	); err == nil {
		t.Fatal("duplicate (episode_id, file_path) was accepted; UNIQUE constraint missing")
	}

	// One primary per episode, enforced by the partial unique index.
	insertFile(t, sqlDB, epA, "/tv/Show/primary.mkv", true)
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO library_episode_files (episode_id, file_path, is_primary) VALUES (?, ?, true)`,
		epA, "/tv/Show/second-primary.mkv",
	); err == nil {
		t.Fatal("second is_primary row for one episode was accepted; partial index missing")
	}

	// ON DELETE CASCADE from library_episodes.
	insertFile(t, sqlDB, epC, "/tv/Show/S01E03.mkv", true)
	if _, err := sqlDB.ExecContext(ctx, `DELETE FROM library_episodes WHERE id = ?`, epC); err != nil {
		t.Fatalf("delete episode: %v", err)
	}
	if got := countFiles(t, sqlDB, epC); got != 0 {
		t.Fatalf("after parent delete: %d file rows remain, want 0 (cascade missing)", got)
	}

	// --- Down: 0005 -> 0004 -------------------------------------------------
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.DownTo(sqlDB, "migrations", 4); err != nil {
		t.Fatalf("goose down to 4: %v", err)
	}
	if version := currentVersion(t, sqlDB); version != 4 {
		t.Fatalf("after Down: goose version %d, want 4", version)
	}
	if tableExists(t, sqlDB) {
		t.Fatal("after Down: library_episode_files still exists")
	}

	// --- Up: 0004 -> 0005, re-running the backfill --------------------------
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		t.Fatalf("goose up: %v", err)
	}
	if version := currentVersion(t, sqlDB); version != 5 {
		t.Fatalf("after Up: goose version %d, want 5", version)
	}
	if !tableExists(t, sqlDB) {
		t.Fatal("after Up: library_episode_files missing")
	}

	// The backfill's defining behaviour: the range file's TWO episode rows each get their own
	// primary row at the SAME path. This is the assertion that fails loudly if a future edit
	// "simplifies" the constraint back to 0003's bare UNIQUE(file_path).
	var primaries, distinctEpisodes int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT count(*), count(DISTINCT episode_id) FROM library_episode_files
		 WHERE file_path = ? AND is_primary`, rangePath,
	).Scan(&primaries, &distinctEpisodes); err != nil {
		t.Fatalf("query backfilled primaries: %v", err)
	}
	if primaries != 2 || distinctEpisodes != 2 {
		t.Fatalf("backfill produced %d primary rows across %d episodes at %q, want 2 across 2",
			primaries, distinctEpisodes, rangePath)
	}

	// Alternates are NOT restored — the Down dropped them, which is exactly the operational
	// irreversibility the migration file documents. Each episode is left with its primary only.
	for _, id := range []int64{epA, epB} {
		if got := countFiles(t, sqlDB, id); got != 1 {
			t.Fatalf("after round-trip: %d file rows for episode %d, want 1 (backfill only)", got, id)
		}
	}
}

func insertSeries(t *testing.T, sqlDB *sql.DB, tmdbID int) int64 {
	t.Helper()
	var id int64
	err := sqlDB.QueryRowContext(context.Background(),
		`INSERT INTO library_series (tmdb_id, title, root_folder_path) VALUES (?, ?, ?) RETURNING id`,
		tmdbID, "Round Trip Show", "/tv",
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}
	return id
}

func insertEpisode(t *testing.T, sqlDB *sql.DB, seriesID int64, season, episode int, path string) int64 {
	t.Helper()
	var id int64
	err := sqlDB.QueryRowContext(context.Background(),
		`INSERT INTO library_episodes (series_id, season_number, episode_number, file_path)
		 VALUES (?, ?, ?, ?) RETURNING id`,
		seriesID, season, episode, path,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert episode: %v", err)
	}
	return id
}

func insertFile(t *testing.T, sqlDB *sql.DB, episodeID int64, path string, primary bool) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO library_episode_files (episode_id, file_path, is_primary) VALUES (?, ?, ?)`,
		episodeID, path, primary,
	); err != nil {
		t.Fatalf("insert file row %q (primary=%v): %v", path, primary, err)
	}
}

func countFiles(t *testing.T, sqlDB *sql.DB, episodeID int64) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT count(*) FROM library_episode_files WHERE episode_id = ?`, episodeID,
	).Scan(&n); err != nil {
		t.Fatalf("count file rows: %v", err)
	}
	return n
}

func tableExists(t *testing.T, sqlDB *sql.DB) bool {
	t.Helper()
	var exists bool
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT to_regclass('public.library_episode_files') IS NOT NULL`,
	).Scan(&exists); err != nil {
		t.Fatalf("check table existence: %v", err)
	}
	return exists
}

func currentVersion(t *testing.T, sqlDB *sql.DB) int64 {
	t.Helper()
	var version int64
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT max(version_id) FROM goose_db_version`,
	).Scan(&version); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	return version
}
