package adultnewest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/labbersanon/sakms/internal/prowlarr"
)

// Claude 2026-08-11: raw Prowlarr release cache store methods for Adult.
// Reason: "search once, persist, reuse" — DetailPopup and unattended grabs read from
// this cache instead of re-querying Prowlarr on every open/trigger.
// Troubleshooting: zero fresh rows for a scene key means either no write site fired
// (check HIT/MISS log lines in o2cli) or every row is past its protocol window.
// Review if: a second mode ever needs release persistence (currently Adult-only).

// sakmsTimestampFormat matches the output of sakms_now() — used for persisted_at
// cutoffs passed to SQL so the text comparison against the stored value is correct.
// A7: format must be 2006-01-02T15:04:05.000Z (millisecond precision, UTC Z suffix).
const sakmsTimestampFormat = "2006-01-02T15:04:05.000Z"

// protocolCutoffs renders the usenet and torrent freshness cutoffs as sakms_now()
// formatted strings, so SQL's text comparison against persisted_at is correct.
func protocolCutoffs(now time.Time) (usenet, torrent string) {
	return now.UTC().Add(-NZBReleaseTTL).Format(sakmsTimestampFormat),
		now.UTC().Add(-TorrentReleaseTTL).Format(sakmsTimestampFormat)
}

// PersistReleases upserts every release from releases (the raw list — no filtering,
// ever) into adult_release_cache, then links each to every key in sceneKeys via
// adult_release_scene_links. An empty sceneKeys is valid: "persist with no links"
// is the correct state for Show More raw-list members that matched no scene. Runs
// in a single transaction. Rows with an empty DownloadURL are skipped (no identity,
// cannot be grabbed); the count is logged, not returned as an error.
func (s *ReleaseStore) PersistReleases(ctx context.Context, query string, releases []prowlarr.Release, sceneKeys []string) error {
	filtered := releases[:0:0]
	skipped := 0
	for _, r := range releases {
		if r.DownloadURL == "" {
			skipped++
			continue
		}
		filtered = append(filtered, r)
	}
	if skipped > 0 {
		log.Printf("adultnewest: PersistReleases: skipped %d release(s) with empty DownloadURL", skipped)
	}
	if len(filtered) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning persist transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// One multi-row upsert for all filtered releases. Each row refreshes
	// persisted_at on conflict so a re-search resets the TTL.
	args := make([]any, 0, len(filtered)*11)
	valuePlaceholders := make([]byte, 0, len(filtered)*50)
	for i, r := range filtered {
		if i > 0 {
			valuePlaceholders = append(valuePlaceholders, ',')
		}
		valuePlaceholders = append(valuePlaceholders, "(?,?,?,?,?,?,?,?,?,?,sakms_now(),?)"...)

		cats := r.Categories
		if cats == nil {
			cats = []int{}
		}
		catsJSON, err := json.Marshal(cats)
		if err != nil {
			return fmt.Errorf("encoding categories for %q: %w", r.DownloadURL, err)
		}
		flags := r.IndexerFlags
		if flags == nil {
			flags = []string{}
		}
		flagsJSON, err := json.Marshal(flags)
		if err != nil {
			return fmt.Errorf("encoding indexer_flags for %q: %w", r.DownloadURL, err)
		}

		args = append(args,
			r.DownloadURL,
			r.GUID,
			r.Title,
			string(r.Protocol),
			r.Indexer,
			string(catsJSON),
			string(flagsJSON),
			r.Size,
			r.Seeders,
			r.PublishDate,
			query,
		)
	}

	upsertSQL := fmt.Sprintf(`
		INSERT INTO adult_release_cache
			(download_url, guid, title, protocol, indexer, categories, indexer_flags,
			 size_bytes, seeders, publish_date, persisted_at, last_query)
		VALUES %s
		ON CONFLICT(download_url) DO UPDATE SET
			guid          = excluded.guid,
			title         = excluded.title,
			protocol      = excluded.protocol,
			indexer       = excluded.indexer,
			categories    = excluded.categories,
			indexer_flags = excluded.indexer_flags,
			size_bytes    = excluded.size_bytes,
			seeders       = excluded.seeders,
			publish_date  = excluded.publish_date,
			persisted_at  = sakms_now(),
			last_query    = excluded.last_query`,
		valuePlaceholders)

	if _, err := tx.ExecContext(ctx, upsertSQL, args...); err != nil {
		return fmt.Errorf("upserting %d release(s) into adult_release_cache: %w", len(filtered), err)
	}

	if len(sceneKeys) > 0 {
		// One multi-row insert for all (scene_key, download_url) pairs.
		// ON CONFLICT DO NOTHING: duplicate links are idempotent.
		linkArgs := make([]any, 0, len(sceneKeys)*len(filtered)*2)
		linkPlaceholders := make([]byte, 0, len(sceneKeys)*len(filtered)*6)
		first := true
		for _, key := range sceneKeys {
			for _, r := range filtered {
				if !first {
					linkPlaceholders = append(linkPlaceholders, ',')
				}
				first = false
				linkPlaceholders = append(linkPlaceholders, "(?,?)"...)
				linkArgs = append(linkArgs, key, r.DownloadURL)
			}
		}
		linkSQL := fmt.Sprintf(`
			INSERT INTO adult_release_scene_links (scene_key, download_url)
			VALUES %s
			ON CONFLICT (scene_key, download_url) DO NOTHING`, linkPlaceholders)
		if _, err := tx.ExecContext(ctx, linkSQL, linkArgs...); err != nil {
			return fmt.Errorf("linking %d release(s) to %d scene key(s): %w", len(filtered), len(sceneKeys), err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing persist transaction: %w", err)
	}
	return nil
}

// LinkReleaseToScene links one already-persisted release to one scene key. Used by
// Show More's per-item enrichment loop, which learns the scene identity AFTER the
// batch persist. Idempotent: ON CONFLICT DO NOTHING.
func (s *ReleaseStore) LinkReleaseToScene(ctx context.Context, sceneKey, downloadURL string) error {
	if downloadURL == "" || sceneKey == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO adult_release_scene_links (scene_key, download_url)
		VALUES (?, ?)
		ON CONFLICT (scene_key, download_url) DO NOTHING`,
		sceneKey, downloadURL)
	if err != nil {
		return fmt.Errorf("linking release %q to scene %q: %w", downloadURL, sceneKey, err)
	}
	return nil
}

// FreshReleasesForScene returns the linked releases whose own protocol window has
// not expired, newest-persisted first. Per-release TTL (locked decision #1): a stale
// torrent sibling is omitted; it never forces a whole-list miss when a fresh NZB
// sibling exists. Returns an empty slice (not an error) for an unknown scene.
//
// The freshness cut is expressed as two protocol-scoped predicates so the
// (protocol, persisted_at) index is usable. Go-side ReleaseFresh is the authority;
// the SQL predicates encode the same logic so they agree.
func (s *ReleaseStore) FreshReleasesForScene(ctx context.Context, sceneKey string, now time.Time) ([]prowlarr.Release, error) {
	usenetCutoff, torrentCutoff := protocolCutoffs(now)

	rows, err := s.db.QueryContext(ctx, `
		SELECT arc.download_url, arc.guid, arc.title, arc.protocol,
		       arc.indexer, arc.categories, arc.indexer_flags,
		       arc.size_bytes, arc.seeders, arc.publish_date
		FROM adult_release_cache arc
		JOIN adult_release_scene_links arsl ON arsl.download_url = arc.download_url
		WHERE arsl.scene_key = ?
		  AND (
		        (arc.protocol = 'usenet'  AND arc.persisted_at >= ?)
		     OR (arc.protocol <> 'usenet' AND arc.persisted_at >= ?)
		  )
		ORDER BY arc.persisted_at DESC`,
		sceneKey, usenetCutoff, torrentCutoff)
	if err != nil {
		return nil, fmt.Errorf("querying fresh releases for scene %q: %w", sceneKey, err)
	}
	defer rows.Close()

	var out []prowlarr.Release
	for rows.Next() {
		var (
			r                   prowlarr.Release
			protocol            string
			catsJSON, flagsJSON string
		)
		if err := rows.Scan(
			&r.DownloadURL, &r.GUID, &r.Title, &protocol,
			&r.Indexer, &catsJSON, &flagsJSON,
			&r.Size, &r.Seeders, &r.PublishDate,
		); err != nil {
			return nil, fmt.Errorf("scanning fresh release for scene %q: %w", sceneKey, err)
		}
		r.Protocol = prowlarr.Protocol(protocol)
		if err := json.Unmarshal([]byte(catsJSON), &r.Categories); err != nil {
			return nil, fmt.Errorf("decoding categories for release %q: %w", r.DownloadURL, err)
		}
		if err := json.Unmarshal([]byte(flagsJSON), &r.IndexerFlags); err != nil {
			return nil, fmt.Errorf("decoding indexer_flags for release %q: %w", r.DownloadURL, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating fresh releases for scene %q: %w", sceneKey, err)
	}
	return out, nil
}

// PurgeExpiredReleases deletes rows past their own protocol window (cascading the
// links). Bounds this table's otherwise-indefinite growth, exactly as PurgeStale
// bounds adult_newest_releases. Called beside PurgeStale in scan.go's runCycle.
//
// A8 nuance: unlike PurgeStale (which uses adult_newest_releases.first_seen_at and
// the 6-month entity ceiling), this purge is purely TTL-based on persisted_at +
// protocol — NZB rows may survive up to 2 years, torrent rows up to 1 week. The
// 6-month PurgeStale ceiling applies only to matched entity rows, not to this cache.
func (s *ReleaseStore) PurgeExpiredReleases(ctx context.Context, now time.Time) (int64, error) {
	usenetCutoff, torrentCutoff := protocolCutoffs(now)

	res, err := s.db.ExecContext(ctx, `
		DELETE FROM adult_release_cache
		WHERE (protocol = 'usenet'  AND persisted_at < ?)
		   OR (protocol <> 'usenet' AND persisted_at < ?)`,
		usenetCutoff, torrentCutoff)
	if err != nil {
		return 0, fmt.Errorf("purging expired releases from adult_release_cache: %w", err)
	}
	return res.RowsAffected()
}
