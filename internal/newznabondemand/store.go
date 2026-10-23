// Package newznabondemand caches Newznab t=get NZBs and STATs them so a
// later Discover On Demand filter can show only verified streamable releases.
// Enclosure URLs are never stored.
package newznabondemand

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/usenet"
)

const (
	StatusPending     = "pending"
	StatusXMLRejected = "xml_rejected"
	StatusFetched     = "fetched"
	StatusVerified    = "verified"
	StatusDead        = "dead"
	StatusError       = "error"
)

// Row is one catalog NZB's On Demand cache + STAT result.
type Row struct {
	Indexer         string
	GUID            string
	Kind            string
	Title           string
	Status          string
	Streamable      bool
	RejectReason    string
	NZBRelPath      string
	PayloadFiles    int
	PayloadSegments int
	StatChecked     int
	StatMissing     int
	Plan            usenet.SequencePlan
	UpdatedAt       int64
	VerifiedAt      int64
}

// CatalogRef is a newznab_catalog key waiting for t=get.
type CatalogRef struct {
	Indexer string
	GUID    string
	Kind    string
	Title   string
}

// Store persists On Demand rows.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db}
}

func (s *Store) NextFromCatalog(ctx context.Context) (CatalogRef, bool, error) {
	var ref CatalogRef
	if s == nil || s.db == nil {
		return ref, false, nil
	}
	err := s.db.QueryRowContext(ctx, `
		SELECT c.indexer, c.guid, c.kind, c.title
		FROM newznab_catalog c
		LEFT JOIN newznab_ondemand o ON o.indexer = c.indexer AND o.guid = c.guid
		WHERE o.guid IS NULL OR o.status = $1
		ORDER BY CASE WHEN o.guid IS NULL THEN 0 ELSE 1 END, c.ingested_at ASC, c.guid ASC
		LIMIT 1`, StatusError).Scan(&ref.Indexer, &ref.GUID, &ref.Kind, &ref.Title)
	if err == sql.ErrNoRows {
		return ref, false, nil
	}
	if err != nil {
		return ref, false, err
	}
	return ref, true, nil
}

func (s *Store) NextFetched(ctx context.Context) (Row, bool, error) {
	var row Row
	if s == nil || s.db == nil {
		return row, false, nil
	}
	var plan string
	err := s.db.QueryRowContext(ctx, `
		SELECT indexer, guid, kind, title, status, streamable, reject_reason, nzb_relpath,
			payload_files, payload_segments, stat_checked, stat_missing, plan_json, updated_at, verified_at
		FROM newznab_ondemand
		WHERE status = $1 AND streamable = TRUE
		ORDER BY updated_at ASC
		LIMIT 1`, StatusFetched).Scan(
		&row.Indexer, &row.GUID, &row.Kind, &row.Title, &row.Status, &row.Streamable,
		&row.RejectReason, &row.NZBRelPath, &row.PayloadFiles, &row.PayloadSegments,
		&row.StatChecked, &row.StatMissing, &plan, &row.UpdatedAt, &row.VerifiedAt)
	if err == sql.ErrNoRows {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	_ = json.Unmarshal([]byte(plan), &row.Plan)
	return row, true, nil
}

func (s *Store) Save(ctx context.Context, row Row) error {
	if s == nil || s.db == nil {
		return nil
	}
	row.Indexer = strings.TrimSpace(row.Indexer)
	row.GUID = strings.TrimSpace(row.GUID)
	if row.Indexer == "" || row.GUID == "" {
		return nil
	}
	if row.UpdatedAt == 0 {
		row.UpdatedAt = time.Now().Unix()
	}
	plan, err := json.Marshal(row.Plan)
	if err != nil {
		return err
	}
	if looksLikeURL(string(plan)) || looksLikeURL(row.NZBRelPath) || looksLikeURL(row.RejectReason) {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO newznab_ondemand (
			indexer, guid, kind, title, status, streamable, reject_reason, nzb_relpath,
			payload_files, payload_segments, stat_checked, stat_missing, plan_json, updated_at, verified_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (indexer, guid) DO UPDATE SET
			kind = EXCLUDED.kind,
			title = EXCLUDED.title,
			status = EXCLUDED.status,
			streamable = EXCLUDED.streamable,
			reject_reason = EXCLUDED.reject_reason,
			nzb_relpath = EXCLUDED.nzb_relpath,
			payload_files = EXCLUDED.payload_files,
			payload_segments = EXCLUDED.payload_segments,
			stat_checked = EXCLUDED.stat_checked,
			stat_missing = EXCLUDED.stat_missing,
			plan_json = EXCLUDED.plan_json,
			updated_at = EXCLUDED.updated_at,
			verified_at = EXCLUDED.verified_at`,
		row.Indexer, row.GUID, row.Kind, row.Title, row.Status, row.Streamable, row.RejectReason,
		row.NZBRelPath, row.PayloadFiles, row.PayloadSegments, row.StatChecked, row.StatMissing,
		string(plan), row.UpdatedAt, row.VerifiedAt)
	return err
}

func looksLikeURL(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "apikey=") ||
		strings.Contains(l, "enclosure") ||
		strings.Contains(l, "downloadurl")
}
