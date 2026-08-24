// Claude 2026-08-24: schema smoke test for migration 0017.
// Reason: migration 0017 creates adult_monitored_entities and alters grabs to
//   add monitor_entity_key. Since there is no data backfill, we verify the
//   schema rather than doing a goose down/up round-trip (which would require
//   seeding pre-17 data, of which there is none to backfill).
// Troubleshooting: skips when SAKMS_TEST_DATABASE_URL is unset.
// Review if: adult_monitored_entities is removed or monitor_entity_key moves.
package db_test

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func TestMigration0017AdultMonitoredEntities_SchemaPresent(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()

	// adult_monitored_entities table must exist with the expected columns.
	expectedColumns := []string{
		"id", "kind", "entity_source", "entity_id", "entity_name", "entity_image",
		"monitored", "monitored_since", "next_poll_at", "empty_polls", "last_polled_at",
		"created_at", "updated_at",
	}
	rows, err := sqlDB.QueryContext(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_name = 'adult_monitored_entities'
		 ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("querying adult_monitored_entities columns: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatalf("scanning column name: %v", err)
		}
		got = append(got, col)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating columns: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("adult_monitored_entities table does not exist — migration 0017 may not have run")
	}
	gotSet := make(map[string]bool, len(got))
	for _, c := range got {
		gotSet[c] = true
	}
	for _, want := range expectedColumns {
		if !gotSet[want] {
			t.Errorf("adult_monitored_entities is missing column %q (got columns: %v)", want, got)
		}
	}

	// Verify UNIQUE constraint fires on (kind, entity_source, entity_id).
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO adult_monitored_entities (kind, entity_source, entity_id, entity_name)
		 VALUES ('performer', 'tpdb', 'test-id', 'Test Name')`)
	if err != nil {
		t.Fatalf("first insert into adult_monitored_entities: %v", err)
	}
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO adult_monitored_entities (kind, entity_source, entity_id, entity_name)
		 VALUES ('performer', 'tpdb', 'test-id', 'Duplicate')`)
	if err == nil {
		t.Error("expected UNIQUE violation on (kind, entity_source, entity_id); got nil")
	}

	// Verify monitor_entity_key column exists on grabs.
	var monitorKeyExists bool
	err = sqlDB.QueryRowContext(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'grabs' AND column_name = 'monitor_entity_key'
		)`).Scan(&monitorKeyExists)
	if err != nil {
		t.Fatalf("checking grabs.monitor_entity_key: %v", err)
	}
	if !monitorKeyExists {
		t.Error("grabs table is missing column monitor_entity_key — migration 0017 ALTER TABLE may not have run")
	}
}
