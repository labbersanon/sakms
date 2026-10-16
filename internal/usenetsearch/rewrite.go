package usenetsearch

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/settings"
)

// Claude 2026-10-16: rewrite existing headers onto collection stems (plan D5).
// Reason: ParseSubject used to leave [k/N] in ReleaseName; 3M already-stored
//   rows keep those keys until re-parsed. Watermarks stay put (no re-OVER).
// Troubleshooting: log "collection rewrite rows=N keys_changed=M"; done flag
//   usenet_nntp_collection_rewrite_v2. Mega-buckets: obf keys with >20k headers
//   are deleted before releases are rebuilt.
// Review if: a v3 rewrite is needed after another stem change.
// Related files: .omc/plans/nntp-collection-grouping.md, subject.go, ingest.go

const (
	rewriteBatchSize    = 5000
	megaBucketHeaderCap = 20_000
	rewriteCursorSep    = "\n"
)

func (s *Service) runCollectionRewrite(ctx context.Context) {
	s.mu.Lock()
	idx := s.idx
	sqlDB := s.sqlDB
	nameFix := s.cfg.NameFixEnabled
	s.mu.Unlock()
	if idx == nil || sqlDB == nil {
		return
	}
	st := settings.New(sqlDB)
	done, err := st.GetBool(ctx, KeyCollectionRewriteDone, false)
	if err != nil || done {
		return
	}
	cur, err := st.Get(ctx, KeyCollectionRewriteCursor)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		log.Printf("usenetsearch: collection rewrite cursor: %v", err)
		return
	}
	lastGroup, lastMsgid, _ := strings.Cut(cur, rewriteCursorSep)
	var rowsSeen, keysChanged int
	started := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		n, changed, nextGroup, nextMsgid, err := idx.rewriteHeaderPage(ctx, lastGroup, lastMsgid, rewriteBatchSize, nameFix)
		if err != nil {
			log.Printf("usenetsearch: collection rewrite: %v", err)
			return
		}
		rowsSeen += n
		keysChanged += changed
		if n == 0 {
			break
		}
		lastGroup, lastMsgid = nextGroup, nextMsgid
		if err := st.Set(ctx, KeyCollectionRewriteCursor, lastGroup+rewriteCursorSep+lastMsgid); err != nil {
			log.Printf("usenetsearch: collection rewrite save cursor: %v", err)
			return
		}
		log.Printf("usenetsearch: collection rewrite rows=%d keys_changed=%d", rowsSeen, keysChanged)
	}
	megaDeleted, err := idx.deleteObfMegaBuckets(ctx, megaBucketHeaderCap)
	if err != nil {
		log.Printf("usenetsearch: collection rewrite mega-bucket: %v", err)
		return
	}
	if err := idx.rebuildReleasesFromHeaders(ctx); err != nil {
		log.Printf("usenetsearch: collection rewrite rebuild releases: %v", err)
		return
	}
	if err := st.SetBool(ctx, KeyCollectionRewriteDone, true); err != nil {
		log.Printf("usenetsearch: collection rewrite done flag: %v", err)
		return
	}
	_ = st.Set(ctx, KeyCollectionRewriteCursor, "")
	log.Printf("usenetsearch: collection rewrite done rows=%d keys_changed=%d mega_deleted=%d elapsed_ms=%d",
		rowsSeen, keysChanged, megaDeleted, time.Since(started).Milliseconds())
}

func (idx *Index) rewriteHeaderPage(ctx context.Context, lastGroup, lastMsgid string, limit int, nameFix bool) (n, changed int, nextGroup, nextMsgid string, err error) {
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, msg_num, msgid, subject, from_addr, posted_at, bytes,
       release_name, part_n, part_m, filename, obfuscated, is_meta, file_n, file_m
FROM usenet_nntp_headers
WHERE (group_name > ?) OR (group_name = ? AND msgid > ?)
ORDER BY group_name, msgid
LIMIT ?`, lastGroup, lastGroup, lastMsgid, limit)
	if err != nil {
		return 0, 0, lastGroup, lastMsgid, err
	}
	defer rows.Close()

	type stored struct {
		group, msgid, subject, from, release string
		num                                  int
		posted, bytes                        int64
		partN, partM, fileN, fileM           int
		filename                             string
		obf, meta                            bool
	}
	var page []stored
	for rows.Next() {
		var r stored
		if err := rows.Scan(&r.group, &r.num, &r.msgid, &r.subject, &r.from, &r.posted, &r.bytes,
			&r.release, &r.partN, &r.partM, &r.filename, &r.obf, &r.meta, &r.fileN, &r.fileM); err != nil {
			return 0, 0, lastGroup, lastMsgid, err
		}
		page = append(page, r)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, lastGroup, lastMsgid, err
	}
	if len(page) == 0 {
		return 0, 0, lastGroup, lastMsgid, nil
	}

	tx, err := idx.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, lastGroup, lastMsgid, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
UPDATE usenet_nntp_headers
SET release_name=?, part_n=?, part_m=?, filename=?, obfuscated=?, is_meta=?, file_n=?, file_m=?
WHERE group_name=? AND msgid=?`)
	if err != nil {
		return 0, 0, lastGroup, lastMsgid, err
	}
	defer stmt.Close()

	for _, r := range page {
		h := parseHeader(r.group, r.subject, r.from, r.msgid, r.num, r.posted, r.bytes, nameFix)
		if h.ReleaseName == r.release && h.PartN == r.partN && h.PartM == r.partM &&
			h.Filename == r.filename && h.Obfuscated == r.obf && h.IsMeta == r.meta &&
			h.FileN == r.fileN && h.FileM == r.fileM {
			continue
		}
		if _, err := stmt.ExecContext(ctx, h.ReleaseName, h.PartN, h.PartM, h.Filename, h.Obfuscated, h.IsMeta, h.FileN, h.FileM, r.group, r.msgid); err != nil {
			return 0, 0, lastGroup, lastMsgid, err
		}
		changed++
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, lastGroup, lastMsgid, err
	}
	last := page[len(page)-1]
	return len(page), changed, last.group, last.msgid, nil
}

func (idx *Index) deleteObfMegaBuckets(ctx context.Context, cap int) (int, error) {
	res, err := idx.db.ExecContext(ctx, `
DELETE FROM usenet_nntp_headers h
USING (
  SELECT group_name, release_name FROM usenet_nntp_headers
  WHERE release_name LIKE 'obf:%'
  GROUP BY group_name, release_name
  HAVING COUNT(*) > ?
) t
WHERE h.group_name=t.group_name AND h.release_name=t.release_name`, cap)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (idx *Index) rebuildReleasesFromHeaders(ctx context.Context) error {
	tx, err := idx.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM usenet_nntp_releases`); err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO usenet_nntp_releases (
  group_name, release_key, display_name, name_source, name_confidence,
  state, ingested_at, updated_at, dirty)
SELECT h.group_name, h.release_name,
  CASE WHEN h.release_name LIKE 'obf:%' THEN '' ELSE h.release_name END,
  CASE WHEN h.release_name LIKE 'obf:%' THEN '' ELSE 'subject' END,
  CASE WHEN h.release_name LIKE 'obf:%' THEN '' ELSE 'subject' END,
  CASE WHEN h.release_name LIKE 'obf:%' THEN 'pending_name' ELSE 'named' END,
  MIN(h.ingested_at), ?, TRUE
FROM usenet_nntp_headers h
WHERE h.release_name <> ''
GROUP BY h.group_name, h.release_name`, now); err != nil {
		return err
	}
	return tx.Commit()
}
