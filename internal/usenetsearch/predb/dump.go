package predb

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DumpStats is one nZEDb TSV import.
type DumpStats struct {
	Files    int
	Read     int
	Upserted int
	Skipped  int
}

const dumpBatch = 500

// Claude 2026-10-18: nZEDb PreDB dump importer (operator-supplied file).
// Reason: IRC is forward-only (~200 titles) so hash-obfuscated history never
//   matches unless the local table has the pre's md5/md5md5/sha1 and filename.
//   nZEDb dumps are TSV with `\t\t` fields; we do not scrape api.predb.net
//   to build this table (provider ToS).
// Troubleshooting: upserted=0 on a real dump → separator is single-tab or
//   the first column is not title; o2cli grep "predb dump".
// Review if: a newer dump uses CSV/header-only or size-in-MB.
// Related files: store.go, namefix.go, docs/usenet-nntp-native.md

// nZEDb dump_predb.php column order (title..groupname).
var nzedbDumpCols = []string{
	"title", "nfo", "size", "files", "filename", "nuked", "nukereason",
	"category", "created", "source", "requestid", "groupname",
}

// ImportPath loads one nZEDb dump file or every dump-shaped file in a directory.
func (s *Store) ImportPath(ctx context.Context, path string) (DumpStats, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.Contains(path, "://") {
		return DumpStats{}, nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return DumpStats{}, err
	}
	if !st.IsDir() {
		n, err := s.ImportFile(ctx, path)
		n.Files = 1
		return n, err
	}
	var total DumpStats
	ents, err := os.ReadDir(path)
	if err != nil {
		return DumpStats{}, err
	}
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !IsDumpFilename(e.Name()) {
			continue
		}
		n, err := s.ImportFile(ctx, filepath.Join(path, e.Name()))
		total.Files++
		total.Read += n.Read
		total.Upserted += n.Upserted
		total.Skipped += n.Skipped
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// IsDumpFilename reports whether name looks like an nZEDb PreDB dump file.
func IsDumpFilename(name string) bool {
	low := strings.ToLower(name)
	if strings.HasSuffix(low, ".gz") {
		low = strings.TrimSuffix(low, ".gz")
	}
	for _, ext := range []string{".csv", ".tsv", ".txt"} {
		if strings.HasSuffix(low, ext) {
			return true
		}
	}
	return strings.Contains(low, "predb")
}

// ImportFile streams one dump into the local table.
func (s *Store) ImportFile(ctx context.Context, path string) (DumpStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return DumpStats{}, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return DumpStats{}, err
		}
		defer gz.Close()
		r = gz
	}
	return s.ImportDump(ctx, r)
}

// ImportDump reads an nZEDb PreDB TSV (double-tab, optional header) from r.
func (s *Store) ImportDump(ctx context.Context, r io.Reader) (DumpStats, error) {
	if s == nil || s.db == nil {
		return DumpStats{}, errors.New("predb: store not configured")
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var stats DumpStats
	var cols []string
	var batch []Entry
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := s.upsertAll(ctx, batch, SourceDump)
		stats.Upserted += n
		batch = batch[:0]
		return err
	}
	first := true
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var fields []string
		if first {
			first = false
			line = strings.TrimPrefix(line, "\ufeff")
			fields = splitDumpLine(line)
			if len(fields) > 0 && strings.EqualFold(strings.TrimSpace(fields[0]), "title") {
				cols = headerCols(fields)
				continue
			}
			cols = nzedbDumpCols
		} else {
			fields = splitDumpLine(line)
		}
		e, ok := entryFromDump(fields, cols)
		if !ok {
			stats.Skipped++
			continue
		}
		stats.Read++
		batch = append(batch, e)
		if len(batch) >= dumpBatch {
			if err := flush(); err != nil {
				return stats, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return stats, err
	}
	if err := flush(); err != nil {
		return stats, err
	}
	return stats, nil
}

func splitDumpLine(line string) []string {
	if strings.Contains(line, "\t\t") {
		return strings.Split(line, "\t\t")
	}
	return strings.Split(line, "\t")
}

func headerCols(fields []string) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = strings.ToLower(strings.TrimSpace(f))
	}
	return out
}

func entryFromDump(fields, cols []string) (Entry, bool) {
	get := func(name string) string {
		for i, c := range cols {
			if c == name && i < len(fields) {
				return strings.TrimSpace(fields[i])
			}
		}
		return ""
	}
	title := get("title")
	if title == "" {
		return Entry{}, false
	}
	e := Entry{Title: title, Filename: get("filename"), Category: get("category")}
	if v := get("size"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			e.SizeBytes = n
		}
	}
	if v := get("files"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			e.Files = n
		}
	}
	if v := get("created"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			e.PreAt = n
		}
	}
	switch strings.ToLower(get("nuked")) {
	case "1", "true", "t", "yes":
		e.Nuked = true
	}
	return e, true
}

func (s *Store) upsertAll(ctx context.Context, batch []Entry, source string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, upsertSQL)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	now := time.Now().Unix()
	n := 0
	for _, e := range batch {
		title := strings.TrimSpace(e.Title)
		if title == "" {
			continue
		}
		m, mm, sh := hashTriple(title)
		if _, err := stmt.ExecContext(ctx, title, m, mm, sh, e.SizeBytes, e.Files, e.Filename, e.Category, e.PreAt, e.Nuked, source, now); err != nil {
			return n, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	return n, nil
}
