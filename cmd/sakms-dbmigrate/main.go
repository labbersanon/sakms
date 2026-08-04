// Command sakms-dbmigrate copies a read-only SQLite sakms.db into Postgres and
// verifies a full row-level diff. See .omc/plans/autopilot-impl-sakms-postgres-migration.md WS6.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labbersanon/sakms/internal/db"
	_ "modernc.org/sqlite"
)

func main() {
	sqlitePath := flag.String("sqlite", "", "path to sakms.db (read-only)")
	pgDSN := flag.String("pg", "", "Postgres DSN")
	doMigrate := flag.Bool("migrate", false, "copy SQLite → Postgres")
	doVerify := flag.Bool("verify", false, "full row-level diff")
	forceTruncate := flag.Bool("force-truncate", false, "TRUNCATE app tables before migrate")
	batch := flag.Int("batch", 5000, "CopyFrom batch hint (informational)")
	flag.Parse()
	_ = batch

	if *sqlitePath == "" || *pgDSN == "" {
		log.Fatal("usage: sakms-dbmigrate -sqlite <path> -pg <dsn> (-migrate|-verify) [-force-truncate]")
	}
	if !*doMigrate && !*doVerify {
		log.Fatal("specify -migrate and/or -verify")
	}

	ctx := context.Background()
	if *doMigrate {
		if err := migrate(ctx, *sqlitePath, *pgDSN, *forceTruncate); err != nil {
			log.Fatal(err)
		}
	}
	if *doVerify {
		if err := verify(ctx, *sqlitePath, *pgDSN); err != nil {
			log.Fatal(err)
		}
		fmt.Println("verify: OK (all tables match)")
	}
}

func openSQLite(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=query_only(1)", path)
	return sql.Open("sqlite", dsn)
}

func appTables(ctx context.Context, pg *pgxpool.Pool) ([]string, error) {
	rows, err := pg.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename NOT IN ('goose_db_version')
		ORDER BY tablename`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func tableColumns(ctx context.Context, pg *pgxpool.Pool, table string) ([]string, error) {
	rows, err := pg.Query(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func qid(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func migrate(ctx context.Context, sqlitePath, pgDSN string, forceTruncate bool) error {
	start := time.Now()
	fmt.Println("migrate: applying goose baseline (if needed)…")
	sqlDB, err := db.Open(ctx, pgDSN, db.DefaultPoolOptions())
	if err != nil {
		return fmt.Errorf("open/migrate schema: %w", err)
	}
	sqlDB.Close()

	src, err := openSQLite(sqlitePath)
	if err != nil {
		return err
	}
	defer src.Close()

	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	tables, err := appTables(ctx, pool)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return fmt.Errorf("no app tables in Postgres — goose baseline missing?")
	}

	// Target empty check
	for _, t := range tables {
		var n int64
		if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+qid(t)).Scan(&n); err != nil {
			return err
		}
		if n > 0 && !forceTruncate {
			return fmt.Errorf("table %s has %d rows — refuse migrate without -force-truncate", t, n)
		}
	}
	if forceTruncate {
		fmt.Println("migrate: TRUNCATE … CASCADE")
		quoted := make([]string, len(tables))
		for i, t := range tables {
			quoted[i] = qid(t)
		}
		if _, err := pool.Exec(ctx, "TRUNCATE "+strings.Join(quoted, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
			return err
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
		return err
	}

	// Load parents before children roughly: alphabetical is not enough for FKs,
	// but DEFERRED constraints allow any order.
	sort.Strings(tables)
	counts := map[string]int64{}
	for _, table := range tables {
		cols, err := tableColumns(ctx, pool, table)
		if err != nil {
			return err
		}
		boolCols, err := booleanColumns(ctx, pool, table)
		if err != nil {
			return err
		}
		colList := make([]string, len(cols))
		for i, c := range cols {
			colList[i] = qid(c)
		}
		sel := "SELECT " + strings.Join(colList, ", ") + " FROM " + qid(table)
		srows, err := src.QueryContext(ctx, sel)
		if err != nil {
			// Table may exist only on one side during iteration — skip missing SQLite tables
			if strings.Contains(err.Error(), "no such table") {
				fmt.Printf("  skip %s (not in sqlite)\n", table)
				continue
			}
			return fmt.Errorf("sqlite select %s: %w", table, err)
		}

		var batch [][]any
		var n int64
		for srows.Next() {
			raw := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			if err := srows.Scan(ptrs...); err != nil {
				srows.Close()
				return fmt.Errorf("scan %s: %w", table, err)
			}
			row := make([]any, len(cols))
			for i, v := range raw {
				row[i] = coerceForPG(v, boolCols[cols[i]])
			}
			batch = append(batch, row)
			n++
			if len(batch) >= 5000 {
				if _, err := tx.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromRows(batch)); err != nil {
					srows.Close()
					return fmt.Errorf("copy %s: %w", table, err)
				}
				batch = batch[:0]
			}
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return err
		}
		if len(batch) > 0 {
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromRows(batch)); err != nil {
				return fmt.Errorf("copy %s: %w", table, err)
			}
		}
		counts[table] = n
		fmt.Printf("  %s: %d rows\n", table, n)
	}

	// Reset identity sequences
	seqRows, err := tx.Query(ctx, `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND is_identity = 'YES'
		ORDER BY table_name, column_name`)
	if err != nil {
		return err
	}
	type idCol struct{ t, c string }
	var ids []idCol
	for seqRows.Next() {
		var t, c string
		if err := seqRows.Scan(&t, &c); err != nil {
			seqRows.Close()
			return err
		}
		ids = append(ids, idCol{t, c})
	}
	seqRows.Close()
	for _, ic := range ids {
		q := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s','%s'), COALESCE((SELECT MAX(%s) FROM %s), 0) + 1, false)`,
			ic.t, ic.c, qid(ic.c), qid(ic.t),
		)
		if _, err := tx.Exec(ctx, q); err != nil {
			return fmt.Errorf("setval %s.%s: %w", ic.t, ic.c, err)
		}
	}

	if _, err := tx.Exec(ctx, "ANALYZE"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Printf("migrate: done in %s (%d tables)\n", time.Since(start).Round(time.Millisecond), len(counts))
	return nil
}

func normalizeSQLiteValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	default:
		return x
	}
}

// Claude 2026-08-04: SQLite stores flags as INTEGER 0/1; PG columns are boolean.
func coerceForPG(v any, asBool bool) any {
	v = normalizeSQLiteValue(v)
	if !asBool {
		return v
	}
	switch x := v.(type) {
	case nil:
		return nil
	case bool:
		return x
	case int64:
		return x != 0
	case int32:
		return x != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x == "1" || strings.EqualFold(x, "true") || x == "t"
	default:
		return fmt.Sprint(x) == "1"
	}
}

func booleanColumns(ctx context.Context, pool *pgxpool.Pool, table string) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND data_type = 'boolean'`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out[c] = true
	}
	return out, rows.Err()
}

func verify(ctx context.Context, sqlitePath, pgDSN string) error {
	src, err := openSQLite(sqlitePath)
	if err != nil {
		return err
	}
	defer src.Close()

	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	tables, err := appTables(ctx, pool)
	if err != nil {
		return err
	}

	var failed int
	for _, table := range tables {
		cols, err := tableColumns(ctx, pool, table)
		if err != nil {
			return err
		}
		pk, err := primaryKeyCols(ctx, pool, table)
		if err != nil {
			return err
		}
		if len(pk) == 0 {
			return fmt.Errorf("table %s has no primary key — cannot verify", table)
		}
		order := make([]string, len(pk))
		for i, c := range pk {
			order[i] = qid(c)
		}
		colList := make([]string, len(cols))
		for i, c := range cols {
			colList[i] = qid(c)
		}
		sel := "SELECT " + strings.Join(colList, ", ") + " FROM " + qid(table) +
			" ORDER BY " + strings.Join(order, ", ")

		srows, err := src.QueryContext(ctx, sel)
		if err != nil {
			return fmt.Errorf("sqlite %s: %w", table, err)
		}
		prows, err := pool.Query(ctx, sel)
		if err != nil {
			srows.Close()
			return fmt.Errorf("postgres %s: %w", table, err)
		}

		diffs := 0
		var sn, pn int64
		for {
			shas := srows.Next()
			phas := prows.Next()
			if !shas && !phas {
				break
			}
			if shas != phas {
				diffs++
				if diffs <= 5 {
					fmt.Printf("  DIFF %s: row presence mismatch (sqlite_next=%v pg_next=%v)\n", table, shas, phas)
				}
				if shas {
					sn++
					drainScan(srows, len(cols))
				}
				if phas {
					pn++
					drainScanPg(prows, len(cols))
				}
				continue
			}
			sn++
			pn++
			sv := scanRow(srows, len(cols))
			pv := scanRowPg(prows, len(cols))
			for i := range cols {
				if !valuesEqual(sv[i], pv[i]) {
					diffs++
					if diffs <= 5 {
						fmt.Printf("  DIFF %s col=%s sqlite=%v pg=%v\n", table, cols[i], sv[i], pv[i])
					}
				}
			}
		}
		srows.Close()
		prows.Close()
		status := "OK"
		if diffs > 0 || sn != pn {
			status = "FAIL"
			failed++
		}
		fmt.Printf("verify %s: sqlite=%d pg=%d diffs=%d %s\n", table, sn, pn, diffs, status)
	}
	if failed > 0 {
		return fmt.Errorf("%d table(s) failed full diff", failed)
	}
	return nil
}

func primaryKeyCols(ctx context.Context, pg *pgxpool.Pool, table string) ([]string, error) {
	rows, err := pg.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary
		ORDER BY array_position(i.indkey, a.attnum)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanRow(rows *sql.Rows, n int) []any {
	raw := make([]any, n)
	ptrs := make([]any, n)
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	_ = rows.Scan(ptrs...)
	out := make([]any, n)
	for i, v := range raw {
		out[i] = normalizeSQLiteValue(v)
	}
	return out
}

func scanRowPg(rows pgx.Rows, n int) []any {
	raw := make([]any, n)
	ptrs := make([]any, n)
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	_ = rows.Scan(ptrs...)
	out := make([]any, n)
	for i, v := range raw {
		out[i] = normalizeSQLiteValue(v)
	}
	return out
}

func drainScan(rows *sql.Rows, n int) { _ = scanRow(rows, n) }
func drainScanPg(rows pgx.Rows, n int) { _ = scanRowPg(rows, n) }

func valuesEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch av := a.(type) {
	case int64:
		switch bv := b.(type) {
		case int64:
			return av == bv
		case int32:
			return av == int64(bv)
		case int:
			return av == int64(bv)
		case bool:
			return (av != 0) == bv
		}
	case float64:
		if bv, ok := b.(float64); ok {
			return av == bv
		}
	case string:
		if bv, ok := b.(string); ok {
			return av == bv
		}
		if bv, ok := b.([]byte); ok {
			return av == string(bv)
		}
	case bool:
		if bv, ok := b.(bool); ok {
			return av == bv
		}
		switch bv := b.(type) {
		case int64:
			return av == (bv != 0)
		case int:
			return av == (bv != 0)
		}
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func init() {
	log.SetFlags(0)
	log.SetOutput(os.Stderr)
}
