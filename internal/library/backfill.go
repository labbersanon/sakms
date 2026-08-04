package library

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/release"
)

// TierUnknown is the literal written into quality_tier when the inference
// chain runs and concludes nothing. It is deliberately NOT "" — the two
// sentinels mean different things and conflating them breaks this backfill:
//
//	quality_tier = ''        -> never processed (a row predating migration
//	                            0055, or one the backfill has not reached).
//	quality_tier = 'unknown' -> processed, chain concluded Unknown. An
//	                            accepted permanent state, not a retry queue.
//
// The empty-string sentinel is what makes BackfillSizeAndTier idempotent and
// safe to call on every boot forever. Display layers fold it into the same
// Unknown bucket, but they must never write it back.
const TierUnknown = "unknown"

// BackfillSummary is what one BackfillSizeAndTier run did, logged at boot so
// the operator sees the real tier distribution instead of discovering it
// from a mostly-Unknown table. On a library that has been fully Renamed this
// will be mostly Unknown by construction — SAK's own naming schemes strip
// every quality token from the filename — and that is the accepted outcome,
// not a bug.
type BackfillSummary struct {
	Scanned    int
	SizedOK    int
	SizeFailed int
	// ByTier counts what was written, keyed by "low"/"medium"/"high"/
	// "lossless"/"unknown". Always non-nil.
	ByTier map[string]int
}

// pendingRow is one uncaptured row across any of the three tracked tables.
// table is a package-internal constant, never caller input — it is
// interpolated into the UPDATE below, which is only safe because of that.
type pendingRow struct {
	table          string
	id             int64
	filePath       string
	rootFolderPath string
}

// BackfillSizeAndTier populates the size and quality_tier columns added in
// migration 0055 for every tracked row that predates them, across
// library_items, library_episodes and library_scenes.
//
// Idempotent: it reads only rows still in the uncaptured state, so re-running
// it on every boot is a cheap no-op once the library is fully captured.
// Per-row tolerant, matching serviceconn.BackfillUsenetURL: a file that
// cannot be stat'd is left at size 0 with its tier still recorded, and a row
// whose UPDATE fails is logged and skipped rather than aborting the batch.
//
// Three constraints are load-bearing and must survive any future edit:
//
//   - No wrapping transaction. internal/db opens SQLite with
//     SetMaxOpenConns(1); a transaction spanning the whole run would hold the
//     app's ONLY database connection for its entire duration — including
//     every os.Stat over a possibly-dead CIFS/NFS mount — and stall every
//     HTTP request in the app. One ExecContext per row, and every os.Stat
//     happens outside any DB call.
//   - The read cursors are fully drained and closed before any UPDATE is
//     issued. Writing through an open read cursor on the single connection
//     deadlocks (serviceconn/backfill.go documents the same trap).
//   - The per-row UPDATE is guarded on the row still being uncaptured, so a
//     live Apply/Dedup capture-at-write landing between this function's read
//     and its write is never clobbered with a stale inferred value.
//
// Callers should run it in a goroutine after the HTTP server is listening,
// never synchronously at boot: a stale mount makes os.Stat block on the
// syscall, which would otherwise hang startup instead of degrading one
// Dashboard section.
//
// Cancelling ctx stops the sweep between rows and returns the partial summary
// with a NIL error — the rows already written stay written and the next boot's
// idempotent read picks up the rest, so there is nothing for a caller to
// handle. (It cannot interrupt an os.Stat already blocked in the syscall; the
// check lands before the next row's.)
//
// DEVIATION from the plan's doc-comment aside, recorded deliberately. The
// read predicate ANDs the two uncaptured conditions where the plan's prose
// ORed them:
//
//	read + guard:  WHERE ... size = 0 AND quality_tier = ''
//	plan's aside:  WHERE ... size = 0 OR  quality_tier = ''
//
// The read predicate must match the UPDATE guard. With OR, a row left at
// (size 0, tier "unknown") by a genuinely missing file would be re-read and
// re-stat'd on every boot forever while the guard made the write impossible
// to ever commit: pure repeated I/O against exactly the dead-mount case this
// design exists to contain. The cost of the stricter predicate is that a row
// whose file was merely unreachable at one boot does not self-heal at the
// next; the supported recovery is the same DB edit the plan already names as
// the only refresh mechanism (reset both columns to their uncaptured
// values). No re-run trigger is built.
func (s *Store) BackfillSizeAndTier(ctx context.Context) (BackfillSummary, error) {
	summary := BackfillSummary{ByTier: make(map[string]int)}

	pending, err := s.pendingSizeTierRows(ctx)
	if err != nil {
		return summary, err
	}

	for _, row := range pending {
		// Cancellation is checked HERE, not left to the DB calls: the
		// expensive part of a row is the os.Stat below, and only
		// ExecContext/QueryContext see ctx at all — so without this a sweep
		// over a dead mount would run to completion after SIGTERM, which is
		// exactly the case main.go passes the signal-driven ctx for. Partial
		// work is durable (every row is written under its own guarded
		// UPDATE), and the read predicate makes the next boot resume from
		// whatever is left, so a cancelled run returns its partial summary
		// with a nil error rather than an error the caller cannot act on.
		if err := ctx.Err(); err != nil {
			log.Printf("library: backfill cancelled after %d of %d rows (%d sized, %d unstattable) — the next boot resumes from the rest", summary.Scanned, len(pending), summary.SizedOK, summary.SizeFailed)
			return summary, nil
		}
		summary.Scanned++

		// os.Stat rather than the FileSize helper only so the errno survives
		// into the log line — a stale mount and a genuinely deleted file are
		// the two common causes here and they need telling apart. The
		// directory exclusion mirrors FileSize exactly.
		var size int64
		if info, err := os.Stat(row.filePath); err != nil {
			summary.SizeFailed++
			log.Printf("library: backfill could not stat %s row %d (%s): %v — leaving size 0, tier still recorded", row.table, row.id, row.filePath, err)
		} else if info.IsDir() {
			summary.SizeFailed++
			log.Printf("library: backfill found a directory, not a video file, at %s row %d (%s) — leaving size 0, tier still recorded", row.table, row.id, row.filePath)
		} else {
			size = info.Size()
			summary.SizedOK++
		}

		tier := inferTierFromPath(row.filePath, row.rootFolderPath)
		summary.ByTier[tier]++

		if err := s.writeBackfilledSizeAndTier(ctx, row.table, row.id, size, tier); err != nil {
			log.Printf("library: backfill could not update %s row %d: %v — skipped, will be retried on the next boot", row.table, row.id, err)
			continue
		}
	}

	return summary, nil
}

// writeBackfilledSizeAndTier is the backfill's one guarded per-row write.
//
// The WHERE clause is the whole point and must not be reduced to a bare
// `WHERE id = ?`: the read-then-write above is not atomic, so a live
// capture-at-write from Apply/Dedup can land on this row between the two.
// Requiring the row to STILL be uncaptured means a stale inferred tier can
// never overwrite a fresher real one — the update simply affects zero rows,
// which is the correct outcome, not an error.
//
// table is one of three package-internal literals set by
// pendingSizeTierRows, never caller input, which is what makes the
// interpolation safe.
func (s *Store) writeBackfilledSizeAndTier(ctx context.Context, table string, id, size int64, tier string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE `+table+`
		   SET size = ?, quality_tier = ?,
		       updated_at = sakms_now()
		 WHERE id = ? AND size = 0 AND quality_tier = ''
	`, size, tier, id)
	if err != nil {
		return fmt.Errorf("backfilling size/tier for %s row %d: %w", table, id, err)
	}
	return nil
}

// pendingSizeTierRows reads every uncaptured row across the three tracked
// tables into memory, closing each cursor before returning — no UPDATE may
// be issued until this has finished (see BackfillSizeAndTier's doc).
//
// Rows with an empty file_path are excluded at the SQL level. That is mandatory
// for library_episodes, which deliberately holds rows for episodes TMDB
// knows about that are not on disk (the state MissingEpisodes queries);
// stat'ing and tiering those would be wasted work and would report hundreds
// of phantom 0-byte Unknown items.
func (s *Store) pendingSizeTierRows(ctx context.Context) ([]pendingRow, error) {
	var pending []pendingRow

	// library_episodes has no root_folder_path column of its own — it lives
	// on the parent library_series row, so the tier inference needs this
	// JOIN to know what prefix to trim.
	queries := []struct {
		table string
		sql   string
	}{
		{"library_items", `
			SELECT id, file_path, root_folder_path
			  FROM library_items
			 WHERE file_path != '' AND size = 0 AND quality_tier = ''
		`},
		{"library_episodes", `
			SELECT e.id, e.file_path, s.root_folder_path
			  FROM library_episodes e
			  JOIN library_series s ON s.id = e.series_id
			 WHERE e.file_path != '' AND e.size = 0 AND e.quality_tier = ''
		`},
		{"library_scenes", `
			SELECT id, file_path, root_folder_path
			  FROM library_scenes
			 WHERE file_path != '' AND size = 0 AND quality_tier = ''
		`},
	}

	for _, q := range queries {
		rows, err := s.db.QueryContext(ctx, q.sql)
		if err != nil {
			return nil, fmt.Errorf("reading uncaptured %s rows for size/tier backfill: %w", q.table, err)
		}
		for rows.Next() {
			p := pendingRow{table: q.table}
			if err := rows.Scan(&p.id, &p.filePath, &p.rootFolderPath); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanning %s row for size/tier backfill: %w", q.table, err)
			}
			pending = append(pending, p)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reading uncaptured %s rows for size/tier backfill: %w", q.table, err)
		}
		// Close before the caller issues any UPDATE — SQLite on a single
		// connection cannot write through an open read cursor.
		rows.Close()
	}

	return pending, nil
}

// inferTierFromPath reconstructs a quality tier from what the file's own path
// still says about it, falling back to TierUnknown.
//
// It parses the path RELATIVE to the root folder, with separators flattened
// to spaces, rather than just the basename. Two reasons, both real
// populations rather than hypotheticals:
//
//   - A season pack carries its quality tokens on the wrapping DIRECTORY
//     ("Show.S01.1080p.WEB-DL-GRP/Show.S01E01.mkv"), so a basename-only parse
//     sees nothing.
//   - Trimming the root folder first stops a token in the operator's own root
//     path (a folder literally named "remux-archive", say) from being read as
//     the release's own attribute.
//
// Everything SAK has already Renamed returns Unknown here by design —
// naming.MovieFileName/EpisodeFileName/AdultFileName strip every quality
// token, which is the app working correctly. The real population for this
// inference is grab-imported files that have not had a Rename Apply run
// against them yet: rename.Relocate preserves the scene-release name verbatim.
func inferTierFromPath(filePath, rootFolderPath string) string {
	rel := strings.TrimPrefix(filePath, rootFolderPath)
	rel = strings.ReplaceAll(rel, string(os.PathSeparator), " ")
	if tier, ok := quality.InferTier(release.Parse(rel)); ok {
		return string(tier)
	}
	return TierUnknown
}
