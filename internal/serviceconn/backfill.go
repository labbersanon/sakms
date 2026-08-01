package serviceconn

import (
	"context"
	"fmt"
	"log"

	"github.com/labbersanon/sakms/internal/usenet"
)

// BackfillUsenetURL normalizes any usenet row still carrying a legacy
// `nntp://host:port` URL into the host/port/tls columns and blanks url.
//
// Migration 0053 copies the old connections.nntp row's url VERBATIM because
// SQLite cannot parse a URL in SQL, so the moved row lands with url set and
// host/port/tls zeroed. This is the Go half of that move — the real production
// migration, distinct from the fresh-install schema change, mirroring
// rssfeeds.Store.BackfillEncryption exactly.
//
// Idempotent: it reads only rows that still hold a url, so re-runs are no-ops
// and it is safe to call unconditionally at every boot. A row whose URL cannot
// be parsed is logged and SKIPPED rather than failing the whole backfill —
// leaving it untouched keeps the (also lossy-URL-reconstructing) 0053 Down path
// working, and the operator can correct it in Settings.
//
// Writes raw SQL rather than routing through Update on purpose: Update enforces
// the "a usenet row needs host and port" invariant, which is exactly the state
// these rows have not reached yet.
func (s *Store) BackfillUsenetURL(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url FROM service_connections WHERE kind = 'usenet' AND url != ''
	`)
	if err != nil {
		return fmt.Errorf("reading legacy usenet urls for backfill: %w", err)
	}
	type pending struct {
		id  int64
		url string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.url); err != nil {
			rows.Close()
			return fmt.Errorf("scanning usenet connection for backfill: %w", err)
		}
		todo = append(todo, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	// Close before issuing UPDATEs — SQLite doesn't allow writing through the
	// same connection while a read cursor is still open.
	rows.Close()

	for _, p := range todo {
		cfg, err := usenet.ParseURL(p.url)
		if err != nil {
			log.Printf("serviceconn: usenet connection %d has an unparseable url (%v) — left as-is for the operator to correct in Settings", p.id, err)
			continue
		}
		if _, err := s.db.ExecContext(ctx, `
			UPDATE service_connections SET host = ?, port = ?, tls = ?, url = '',
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			WHERE id = ?
		`, cfg.Host, cfg.Port, cfg.TLS, p.id); err != nil {
			return fmt.Errorf("backfilling host/port for usenet connection %d: %w", p.id, err)
		}
	}
	return nil
}
