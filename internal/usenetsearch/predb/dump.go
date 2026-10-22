package predb

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// Claude 2026-10-19: dump path may be a local TSV/gz *or* an http(s) file URL.
// Reason: searchable stays flat without a historical corpus; SSH-copying a TSV
//   onto server1 is the gap. Fetching a dump *file* is allowed; scraping
//   api.predb.net `?type=pre` is still not (provider ToS). nZEDbPre_Dumps is
//   discontinued — the URL is operator-set.
// Troubleshooting: dump imported files=0 with a URL → IsFetchURL rejected it
//   (search API / non-http) or the body was JSON/HTML; o2cli grep "predb dump".
// Review if: a maintained public TSV dump URL is pinned as a default.
// Related files: store.go, namefix.go, usenetnntp_predb.go, docs/usenet-nntp-native.md

// nZEDb dump_predb.php column order (title..groupname).
var nzedbDumpCols = []string{
	"title", "nfo", "size", "files", "filename", "nuked", "nukereason",
	"category", "created", "source", "requestid", "groupname",
}

const (
	maxDumpBytes  = 2 << 30
	dumpUserAgent = "sakms-predb-dump/1"
)

var dumpHTTPClient = &http.Client{
	Timeout:       5 * time.Minute,
	CheckRedirect: dumpRedirect,
}

func dumpRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("predb: too many dump redirects")
	}
	if !IsFetchURL(req.URL.String()) {
		return errors.New("predb: dump redirect is not a dump file URL")
	}
	return nil
}

// IsFetchURL reports whether raw is an http(s) dump *file* URL. Search APIs
// (api.predb.net, ?type=pre) return false and must not be fetched.
func IsFetchURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "api.predb.net" || strings.HasSuffix(host, ".api.predb.net") {
		return false
	}
	if strings.EqualFold(u.Query().Get("type"), "pre") {
		return false
	}
	return true
}

// ImportPath loads one nZEDb dump file, every dump-shaped file in a directory,
// or (when path is an http(s) dump file URL) fetches and imports that file.
func (s *Store) ImportPath(ctx context.Context, path string) (DumpStats, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return DumpStats{}, nil
	}
	// Claude 2026-10-18: reject :// so api.predb.net is never fetched as a dump.
	// if path == "" || strings.Contains(path, "://") {
	// 	return DumpStats{}, nil
	// }
	if IsFetchURL(path) {
		n, err := s.ImportURL(ctx, path)
		n.Files = 1
		return n, err
	}
	if strings.Contains(path, "://") {
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
	// Claude 2026-10-22: VACUUM (PARALLEL 0, ANALYZE) after a dump.
	// Reason: dump upserts ON CONFLICT, so each re-import leaves dead rows.
	//   Autovacuum vacuum threshold is 0.2*reltuples (~1.4M dead); live had
	//   185k dead and last_vacuum empty. ANALYZE does not reclaim them.
	//   Default parallel VACUUM needs ~64MB DSM and fails SQLSTATE 53100 on
	//   sakms-db's 64MiB /dev/shm. Do not VACUUM FULL (exclusive lock).
	// Troubleshooting: n_dead_tup tens of thousands with last_vacuum empty
	//   → this still ANALYZE-only. 53100 on dump import → PARALLEL 0 missing.
	//   57014 statement timeout mid-index → dump ctx must not cap at 15min.
	// Review if: sakms-db compose shm is raised above 64MiB.
	if stats.Upserted > 0 {
		if _, err := s.db.ExecContext(ctx, `VACUUM (PARALLEL 0, ANALYZE) usenet_nntp_predb`); err != nil {
			return stats, err
		}
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
				return unquoteDumpField(fields[i])
			}
		}
		return ""
	}
	title := get("title")
	if title == "" {
		return Entry{}, false
	}
	e := Entry{Title: title, Filename: get("filename"), Category: get("category"), GroupName: get("groupname")}
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
	if v := get("requestid"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			e.RequestID = n
		}
	}
	switch strings.ToLower(get("nuked")) {
	case "1", "true", "t", "yes":
		e.Nuked = true
	}
	return e, true
}

// Claude 2026-10-19: nZEDb daily dumps quote every field and use \N for NULL.
// Reason: github.com/nZEDb/nZEDbPre_Dumps csv.gz is MySQL-style ('Title'\t\t\N).
//   Leaving the quotes in title makes md5(title) miss every obfuscated hash.
// Troubleshooting: dump imported with titles that start with a quote → this
//   helper was skipped; o2cli grep "predb dump imported".
// Review if: dumps are emitted as unquoted TSV (then this is a no-op).

func unquoteDumpField(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == `\N` || strings.EqualFold(s, "null") {
		return ""
	}
	n := len(s)
	if n >= 2 {
		q := s[0]
		if (q == '\'' || q == '"') && s[n-1] == q {
			s = s[1 : n-1]
			s = strings.ReplaceAll(s, `\\`, `\`)
			s = strings.ReplaceAll(s, `\'`, `'`)
			s = strings.ReplaceAll(s, `\"`, `"`)
			s = strings.TrimSpace(s)
		}
	}
	if s == `\N` {
		return ""
	}
	return s
}

// ImportURL GETs an nZEDb TSV/gz dump file and streams it into the local table.
func (s *Store) ImportURL(ctx context.Context, rawURL string) (DumpStats, error) {
	if s == nil || s.db == nil {
		return DumpStats{}, errors.New("predb: store not configured")
	}
	if !IsFetchURL(rawURL) {
		return DumpStats{}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return DumpStats{}, err
	}
	req.Header.Set("User-Agent", dumpUserAgent)
	req.Header.Set("Accept", "text/tab-separated-values, text/csv, text/plain, application/gzip, application/octet-stream, */*")
	resp, err := dumpHTTPClient.Do(req)
	if err != nil {
		return DumpStats{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DumpStats{}, fmt.Errorf("predb: dump http %d", resp.StatusCode)
	}
	if cl := resp.ContentLength; cl > maxDumpBytes {
		return DumpStats{}, fmt.Errorf("predb: dump too large (%d bytes)", cl)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "json") || strings.Contains(ct, "html") {
		return DumpStats{}, errors.New("predb: dump URL returned JSON/HTML, not a TSV dump")
	}
	r := io.LimitReader(resp.Body, maxDumpBytes+1)
	lowURL := strings.ToLower(rawURL)
	enc := strings.ToLower(resp.Header.Get("Content-Encoding") + " " + ct)
	gzipped := strings.Contains(enc, "gzip") ||
		strings.HasSuffix(strings.TrimSuffix(lowURL, "/"), ".gz") ||
		strings.Contains(lowURL, ".gz?")
	if gzipped {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return DumpStats{}, err
		}
		defer gz.Close()
		r = io.LimitReader(gz, maxDumpBytes+1)
	}
	br := bufio.NewReader(r)
	if err := rejectNonDump(br); err != nil {
		return DumpStats{}, err
	}
	return s.ImportDump(ctx, br)
}

func rejectNonDump(br *bufio.Reader) error {
	buf, err := br.Peek(256)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	s := strings.TrimSpace(string(buf))
	low := strings.ToLower(s)
	if strings.HasPrefix(s, "{") || strings.HasPrefix(low, "<!doctype") || strings.HasPrefix(low, "<html") {
		return errors.New("predb: dump URL returned JSON/HTML, not a TSV dump")
	}
	if !strings.Contains(s, "\t") && !strings.Contains(low, "title") {
		return errors.New("predb: dump URL does not look like nZEDb TSV")
	}
	return nil
}

// HeadStamp is the KV skip key for a dump URL (ETag / Last-Modified / length).
// When HEAD is unsupported, the stamp rotates once per UTC day.
func HeadStamp(ctx context.Context, rawURL string) (string, error) {
	if !IsFetchURL(rawURL) {
		return "", nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", dumpUserAgent)
	resp, err := dumpHTTPClient.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		if resp != nil {
			resp.Body.Close()
		}
		return fmt.Sprintf("%s|head-fail|%s", rawURL, time.Now().UTC().Format("2006-01-02")), nil
	}
	defer resp.Body.Close()
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	lm := resp.Header.Get("Last-Modified")
	clen := resp.Header.Get("Content-Length")
	if etag == "" && lm == "" && clen == "" {
		return fmt.Sprintf("%s|head-ok|%s", rawURL, time.Now().UTC().Format("2006-01-02")), nil
	}
	return fmt.Sprintf("%s|%s|%s|%s", rawURL, etag, lm, clen), nil
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
		if _, err := stmt.ExecContext(ctx, title, m, mm, sh, e.SizeBytes, e.Files, e.Filename, e.Category, e.PreAt, e.Nuked, source, now, e.RequestID, normalizePreDBGroup(e.GroupName)); err != nil {
			return n, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	return n, nil
}
