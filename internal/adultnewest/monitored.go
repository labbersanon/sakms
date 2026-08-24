// This file is the Adult monitored-entities store — the persistence layer for
// which Adult performers/studios the operator wants monitored (auto-grabbed when
// new matching releases surface via Prowlarr). It is deliberately separate from
// releases.go / adultnewest.go, which own the scan-cache table, so the two
// concerns are independently readable and the store is independently deletable
// alongside adultmonitor.go.
//
// monitor_entity_key format (recorded here rather than in grabs.go, which only
// stores the value): kind + \x1f + entity_source + \x1f + entity_id, where
// \x1f is ASCII unit separator (Unicode U+001F). The separator cannot appear in
// kind ("performer"/"studio"), entity_source ("tpdb"/"stashdb"/…), or entity_id
// (catalog opaque ids, which are alphanumeric or UUID-shaped). This key is the
// ORIGIN MARKER on grabs — non-empty only for monitor-originated rows, never
// cleared after dispatch.
package adultnewest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrMonitoredNotFound is returned when a looked-up monitored entity row does
// not exist.
var ErrMonitoredNotFound = errors.New("adultnewest: no monitored entity with those identifiers")

// monitorEntityKeySep is the separator used in monitor_entity_key on the grabs
// table. Declared here (the authoritative site) and used by FormatMonitorKey
// and SplitMonitorKey below.
const monitorEntityKeySep = "\x1f"

// FormatMonitorKey builds the monitor_entity_key value for a grab row.
// kind + sep + source + sep + id.
func FormatMonitorKey(kind, source, id string) string {
	return strings.Join([]string{kind, source, id}, monitorEntityKeySep)
}

// SplitMonitorKey splits a monitor_entity_key back into (kind, source, id).
// Returns three empty strings when the key is empty or malformed.
func SplitMonitorKey(key string) (kind, source, id string) {
	if key == "" {
		return "", "", ""
	}
	parts := strings.SplitN(key, monitorEntityKeySep, 3)
	if len(parts) != 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

// MonitoredEntity is one row in adult_monitored_entities.
type MonitoredEntity struct {
	ID             int64  `json:"id"`
	Kind           string `json:"kind"`
	EntitySource   string `json:"entitySource"`
	EntityID       string `json:"entityId"`
	EntityName     string `json:"entityName"`
	EntityImage    string `json:"entityImage"`
	Monitored      bool   `json:"monitored"`
	MonitoredSince string `json:"monitoredSince"`
	NextPollAt     string `json:"nextPollAt"`
	EmptyPolls     int    `json:"emptyPolls"`
	LastPolledAt   string `json:"lastPolledAt"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// MonitoredStore is the persistence layer for adult_monitored_entities.
type MonitoredStore struct {
	db *sql.DB
}

// NewMonitoredStore builds a MonitoredStore.
func NewMonitoredStore(db *sql.DB) *MonitoredStore {
	return &MonitoredStore{db: db}
}

// monitoredColumns is the column list used in every SELECT — kept central so
// scanMonitored and the query sites stay in sync.
const monitoredColumns = `id, kind, entity_source, entity_id, entity_name, entity_image, monitored, monitored_since, next_poll_at, empty_polls, last_polled_at, created_at, updated_at`

func scanMonitored(row interface{ Scan(...any) error }) (MonitoredEntity, error) {
	var e MonitoredEntity
	var monitoredInt int
	err := row.Scan(&e.ID, &e.Kind, &e.EntitySource, &e.EntityID, &e.EntityName, &e.EntityImage,
		&monitoredInt, &e.MonitoredSince, &e.NextPollAt, &e.EmptyPolls, &e.LastPolledAt,
		&e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return MonitoredEntity{}, err
	}
	e.Monitored = monitoredInt != 0
	return e, nil
}

// ListMonitored returns every row with monitored=1, unordered.
func (s *MonitoredStore) ListMonitored(ctx context.Context) ([]MonitoredEntity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+monitoredColumns+`
		FROM adult_monitored_entities WHERE monitored = 1`)
	if err != nil {
		return nil, fmt.Errorf("listing monitored entities: %w", err)
	}
	defer rows.Close()
	out := []MonitoredEntity{}
	for rows.Next() {
		e, err := scanMonitored(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning monitored entity: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListDue returns every monitored row whose next_poll_at <= now (or is empty),
// up to limit rows, ordered by next_poll_at ASC so the oldest overdue entities
// are processed first.
func (s *MonitoredStore) ListDue(ctx context.Context, now time.Time, limit int) ([]MonitoredEntity, error) {
	// Claude 2026-08-24: empty next_poll_at sorts before every real timestamp
	// (same convention as retry_after on grabs), so newly-added monitored
	// entities with no poll yet are always due.
	ts := now.UTC().Format(sakmsTimestampFormat)
	rows, err := s.db.QueryContext(ctx, `SELECT `+monitoredColumns+`
		FROM adult_monitored_entities
		WHERE monitored = 1 AND (next_poll_at = '' OR next_poll_at <= $1)
		ORDER BY next_poll_at ASC
		LIMIT $2`, ts, limit)
	if err != nil {
		return nil, fmt.Errorf("listing due monitored entities: %w", err)
	}
	defer rows.Close()
	out := []MonitoredEntity{}
	for rows.Next() {
		e, err := scanMonitored(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning due monitored entity: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetByKindSourceID fetches a single monitored entity by its unique key. Returns
// ErrMonitoredNotFound when no such row exists.
func (s *MonitoredStore) GetByKindSourceID(ctx context.Context, kind, source, entityID string) (MonitoredEntity, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+monitoredColumns+`
		FROM adult_monitored_entities WHERE kind = $1 AND entity_source = $2 AND entity_id = $3`,
		kind, source, entityID)
	e, err := scanMonitored(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MonitoredEntity{}, ErrMonitoredNotFound
	}
	if err != nil {
		return MonitoredEntity{}, fmt.Errorf("getting monitored entity: %w", err)
	}
	return e, nil
}

// GetByKindName finds the monitored entity for kind+name, using entity_name
// as the lookup key (case-insensitive). Returns ErrMonitoredNotFound when absent.
// Used by the API GET handler (which only knows kind+name at that point).
func (s *MonitoredStore) GetByKindName(ctx context.Context, kind, name string) (MonitoredEntity, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+monitoredColumns+`
		FROM adult_monitored_entities
		WHERE kind = $1 AND TRIM(LOWER(entity_name)) = TRIM(LOWER($2))
		LIMIT 1`,
		kind, name)
	e, err := scanMonitored(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MonitoredEntity{}, ErrMonitoredNotFound
	}
	if err != nil {
		return MonitoredEntity{}, fmt.Errorf("getting monitored entity by name: %w", err)
	}
	return e, nil
}

// UpsertOnMonitor creates or updates a monitored entity row, setting monitored=1
// and recording monitored_since. On conflict (kind, entity_source, entity_id)
// the name/image/monitored/monitored_since/updated_at are updated; next_poll_at
// is NOT reset on re-enable so it is not immediately re-polled if already
// scheduled. Returns the resulting row.
func (s *MonitoredStore) UpsertOnMonitor(ctx context.Context, kind, source, entityID, name, image, monitoredSince string) (MonitoredEntity, error) {
	// Claude 2026-08-24: next_poll_at is NOT touched on re-enable — if a row
	// already exists and is re-enabled, it keeps its existing schedule. New rows
	// get next_poll_at='' which ListDue treats as immediately due.
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO adult_monitored_entities
			(kind, entity_source, entity_id, entity_name, entity_image, monitored, monitored_since)
		VALUES ($1, $2, $3, $4, $5, 1, $6)
		ON CONFLICT (kind, entity_source, entity_id) DO UPDATE SET
			entity_name     = EXCLUDED.entity_name,
			entity_image    = EXCLUDED.entity_image,
			monitored       = 1,
			monitored_since = EXCLUDED.monitored_since,
			updated_at      = sakms_now()
		RETURNING `+monitoredColumns,
		kind, source, entityID, name, image, monitoredSince)
	e, err := scanMonitored(row)
	if err != nil {
		return MonitoredEntity{}, fmt.Errorf("upserting monitored entity: %w", err)
	}
	return e, nil
}

// SetMonitored updates the monitored flag. When monitored=false, monitored_since
// is cleared (the next enable records a fresh since timestamp). Returns
// ErrMonitoredNotFound when no row exists for the given key.
func (s *MonitoredStore) SetMonitored(ctx context.Context, kind, source, entityID string, monitored bool) error {
	monitoredInt := 0
	since := ""
	if monitored {
		monitoredInt = 1
		since = time.Now().UTC().Format(sakmsTimestampFormat)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE adult_monitored_entities SET
			monitored       = $1,
			monitored_since = $2,
			updated_at      = sakms_now()
		WHERE kind = $3 AND entity_source = $4 AND entity_id = $5`,
		monitoredInt, since, kind, source, entityID)
	if err != nil {
		return fmt.Errorf("setting monitored entity monitored=%v: %w", monitored, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return ErrMonitoredNotFound
	}
	return nil
}

// RecordPoll updates last_polled_at, next_poll_at (via backoff), and
// increments empty_polls when no new releases were found. See
// monitorBackoff for the backoff schedule.
func (s *MonitoredStore) RecordPoll(ctx context.Context, id int64, found int, now time.Time) error {
	var emptyPolls int
	if err := s.db.QueryRowContext(ctx, `SELECT empty_polls FROM adult_monitored_entities WHERE id = $1`, id).Scan(&emptyPolls); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMonitoredNotFound
		}
		return fmt.Errorf("reading empty_polls for poll record: %w", err)
	}

	if found == 0 {
		emptyPolls++
	} else {
		emptyPolls = 0
	}

	next := now.Add(monitorBackoff(emptyPolls))

	res, err := s.db.ExecContext(ctx, `
		UPDATE adult_monitored_entities SET
			last_polled_at = $1,
			next_poll_at   = $2,
			empty_polls    = $3,
			updated_at     = sakms_now()
		WHERE id = $4`,
		now.UTC().Format(sakmsTimestampFormat), next.UTC().Format(sakmsTimestampFormat), emptyPolls, id)
	if err != nil {
		return fmt.Errorf("recording poll for entity %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrMonitoredNotFound
	}
	return nil
}

// monitorBackoff returns the next poll interval based on how many consecutive
// empty polls have occurred. The ladder starts at 24h (the default interval)
// and widens gradually, mirroring airDateRetryBackoff's shape but anchored to
// the 24h baseline since that is the default monitor interval.
//
//   - emptyPolls <= 2  → 24h
//   - emptyPolls <= 4  → 72h  (3 days)
//   - emptyPolls <= 6  → 168h (7 days)
//   - emptyPolls <= 8  → 336h (14 days)
//   - else             → 720h (30 days)
func monitorBackoff(emptyPolls int) time.Duration {
	switch {
	case emptyPolls <= 2:
		return 24 * time.Hour
	case emptyPolls <= 4:
		return 72 * time.Hour
	case emptyPolls <= 6:
		return 168 * time.Hour
	case emptyPolls <= 8:
		return 336 * time.Hour
	default:
		return 720 * time.Hour
	}
}
