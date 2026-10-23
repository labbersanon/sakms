// One-shot: write On Demand bitrate_mbps from catalog size + TMDB runtime.
// Not part of the container image. Run inside the sakms container so secret.key
// and SAKMS_DATABASE_URL_FILE stay on the host data volume.
//
// Claude 2026-10-23: backfill stored Mbps for verified rows the shelf labels Unknown.
// Reason: qualityChip reads bitrate_mbps; STAT-time applyBitrate left 0 when payload XML bytes were 0 even though catalog.size and tmdb_id exist.
// Troubleshooting: "no runtime" → TMDB MovieDetails.runtime is 0; concerts with tmdb_id 0 are skipped on purpose (no film length).
// Review if: crawler applyBitrate always persists catalog size at STAT.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	_ "github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/tmdb"
)

type row struct {
	indexer      string
	guid         string
	kind         string
	title        string
	payloadBytes int64
	catalogSize  int64
	tmdbID       string
}

func main() {
	log.SetFlags(0)
	ctx := context.Background()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	dataDir := os.Getenv("SAKMS_DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}
	dsn, err := databaseURL()
	if err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return fmt.Errorf("secret.key: %w", err)
	}
	sec, err := secrets.New(key)
	if err != nil {
		return err
	}
	sqlDB, err := sql.Open("pgx-qmark", dsn)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(2)
	conns := connections.New(sqlDB, sec)
	tmdbConn, err := conns.Get(ctx, "tmdb")
	if err != nil {
		return fmt.Errorf("tmdb connection: %w", err)
	}
	if strings.TrimSpace(tmdbConn.APIKey) == "" {
		return fmt.Errorf("tmdb api key empty")
	}
	client := tmdb.New(tmdb.Config{
		BaseURL:     tmdb.DefaultBaseURL,
		APIKey:      tmdbConn.APIKey,
		BypassCache: true,
	}, &http.Client{Timeout: 30 * time.Second})
	tmdbConn.APIKey = ""

	rows, err := load(ctx, sqlDB)
	if err != nil {
		return err
	}
	wrote, skipped := 0, 0
	now := time.Now().Unix()
	for i, r := range rows {
		payload := r.payloadBytes
		if payload <= 0 {
			payload = r.catalogSize
		}
		id, _ := strconv.Atoi(newznab.DigitsOnly(r.tmdbID))
		mins, err := runtimeMinutes(ctx, client, r.kind, id)
		if err != nil {
			log.Printf("tmdb-err indexer=%s tmdb=%d title=%q err=%v", r.indexer, id, r.title, err)
			skipped++
			continue
		}
		var mbps float64
		if mins > 0 && payload > 0 {
			mbps = float64(payload) * 8 / (float64(mins) * 60) / 1e6
		}
		if _, err := sqlDB.ExecContext(ctx, `
			UPDATE newznab_ondemand
			SET payload_bytes = ?, bitrate_mbps = ?, updated_at = ?
			WHERE indexer = ? AND guid = ? AND status = ?`,
			payload, mbps, now, r.indexer, r.guid, "verified"); err != nil {
			return err
		}
		if mbps > 0 {
			log.Printf("wrote indexer=%s mbps=%.2f mins=%d bytes=%d title=%q", r.indexer, mbps, mins, payload, r.title)
			wrote++
		} else {
			log.Printf("no-runtime indexer=%s tmdb=%d bytes=%d title=%q", r.indexer, id, payload, r.title)
			skipped++
		}
		if i+1 < len(rows) {
			time.Sleep(250 * time.Millisecond)
		}
	}
	log.Printf("done wrote=%d no-runtime-or-err=%d considered=%d", wrote, skipped, len(rows))
	return nil
}

func databaseURL() (string, error) {
	if p := strings.TrimSpace(os.Getenv("SAKMS_DATABASE_URL_FILE")); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("SAKMS_DATABASE_URL_FILE: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	dsn := strings.TrimSpace(os.Getenv("SAKMS_DATABASE_URL"))
	if dsn == "" {
		return "", fmt.Errorf("set SAKMS_DATABASE_URL_FILE or SAKMS_DATABASE_URL")
	}
	return dsn, nil
}

func load(ctx context.Context, sqlDB *sql.DB) ([]row, error) {
	q, err := sqlDB.QueryContext(ctx, `
		SELECT o.indexer, o.guid, o.kind, o.title, o.payload_bytes, c.size, COALESCE(c.tmdb_id, '')
		FROM newznab_ondemand o
		JOIN newznab_catalog c ON c.indexer = o.indexer AND c.guid = o.guid
		WHERE o.status = ?
			AND o.bitrate_mbps <= 0
			AND COALESCE(c.tmdb_id, '') ~ '^[1-9]'
			AND (o.payload_bytes > 0 OR c.size > 0)
		ORDER BY o.indexer, o.guid`, "verified")
	if err != nil {
		return nil, err
	}
	defer q.Close()
	var out []row
	for q.Next() {
		var r row
		if err := q.Scan(&r.indexer, &r.guid, &r.kind, &r.title, &r.payloadBytes, &r.catalogSize, &r.tmdbID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, q.Err()
}

func runtimeMinutes(ctx context.Context, client *tmdb.Client, kind string, tmdbID int) (int, error) {
	if tmdbID <= 0 {
		return 0, nil
	}
	if kind == "series" {
		d, err := client.TVDetails(ctx, tmdbID)
		if err != nil {
			return 0, err
		}
		return d.Runtime, nil
	}
	d, err := client.MovieDetails(ctx, tmdbID)
	if err != nil {
		return 0, err
	}
	return d.Runtime, nil
}
