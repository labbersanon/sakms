package usenetsearch

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Name-recovery storage helpers (plan D3). All DB only.

// nameRetryBase is the delay before the next name-recovery try after
// `attempts` failures: base << attempts.
const nameRetryBase = 10 * time.Minute

// maxNameAttempts is the failed-attempt count at which an unresolved set is
// finalized (deleted).
const maxNameAttempts = 3

// DefaultUnnamedTTLDays is how long an unresolved staged set is kept.
const DefaultUnnamedTTLDays = 7

// PendingSet is one staged obfuscated set awaiting a name.
type PendingSet struct {
	Group        string
	Key          string
	PayloadBytes int64
	Attempts     int
	// CompletenessBP is the set's completeness in basis points (log only).
	CompletenessBP int
	// HasPar2 is true when the last recompute saw a PAR2/meta row.
	HasPar2 bool
	// PreDBOnly skips BODY: exhausted keepCompleteYield waiting on dump/IRC.
	PreDBOnly bool
	// AlreadyNamed is a hash_16K seed of a titled set (extract donors only).
	AlreadyNamed bool
}

// StagedBytes is the header-index occupancy of unresolved staged sets in
// group (COUNT of stored headers × approxHeaderRowBytes). Sets staged in
// this chunk are not counted until upsert.
//
// Claude 2026-10-16: cap is per-group, not a global SUM.
// Reason: boneless/teevee pending (~1 TiB real header bytes) filled a global
// unnamed_max_pct so moovee (0 pending) skip_obf_cap'd every new set.
// Troubleshooting: skip_obf_cap on a core with staged_obf=0 → that group's
// own pending_name occupancy is over unnamed_max_pct of MaxGB.
// Review if: a shared unnamed pool for step-1 cores is added as a Settings knob.
func (idx *Index) StagedBytes(ctx context.Context, group string) (int64, error) {
	var n int64
	// Claude 2026-10-16: exhausted kept yield sets do not fill the unnamed cap.
	// Reason: hash-named PAR2/RAR sets sit at name_attempts>=3 (keepCompleteYield
	// skipped the sweep) and used to keep every group in skip_obf_cap.
	// Troubleshooting: skip_obf_cap with only shelved complete pending → this
	// filter not applied. Review if: PreDB starts naming those hashes (retry).
	//
	// Claude 2026-10-19: unnamed cap uses header occupancy, not yEnc payload.
	// Reason: SUM(payload_bytes) is media size (one 20 GiB x264 set) while
	//   IndexMaxGB/unnamed_max_pct is COUNT(*)×200 B. Cores sat in skip_obf_cap
	//   with only tens of thousands of pending headers.
	// Troubleshooting: skip_obf_cap with pending payload ≫ occupancy → this
	//   JOIN not used. Review if: ApproxBytes stops using approxHeaderRowBytes.
	// Related files: ingest.go capStaged, index.go ApproxBytes
	err := idx.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM usenet_nntp_headers h
JOIN usenet_nntp_releases r ON r.group_name=h.group_name AND r.release_key=h.release_name
WHERE r.state='pending_name' AND r.group_name=? AND r.name_attempts < ?`,
		group, maxNameAttempts).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n * approxHeaderRowBytes, nil
}

// DuePendingNames returns up to limit staged sets in groups whose retry time
// has passed, recomputed, and at/above the completeness gate (a BODY fetch is
// only spent on sets that could become grab-complete). PreDB-only below-gate
// sets go through DuePendingPreDB instead. Only yield groups are
// queried (staging cores, package step ≤4, plus anime); within that set,
// staging cores are filled before the rest, newest first.
//
// Claude 2026-10-16: yield groups only; boneless is not padded in.
// Reason: 644 due boneless nicovideo dumps filled every namefix slot so
//
//	searchable stalled at 36 while anime/hdtv.x264 sat idle.
//
// Troubleshooting: namefix pass renamed=0 with due_now hundreds → boneless
//
//	still in the IN list.
//
// Review if: an operator knob to namefix step-5 firehose is added.
func (idx *Index) DuePendingNames(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	return idx.dueYieldPrefixed(ctx, groups, limit, idx.duePendingNamesIn)
}

func (idx *Index) duePendingNamesIn(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	if len(groups) == 0 || limit <= 0 {
		return nil, nil
	}
	// Claude 2026-10-18: BODY retries stop at maxNameAttempts; PreDB continues
	// via DuePendingPreDB (no BODY).
	// Reason: keepCompleteYield left hash-named 100% sets on a 10m<<n backoff
	//   forever; namefix spent 12–17 slots/pass re-fetching the same PAR2.
	// Troubleshooting: namefix unresolved=N with the same keys every loop →
	//   this filter missing. Review if: BODY is retried after a dump import.
	in, args := groupLimitArgs([]any{idx.MinCompletenessPct() * 100, time.Now().Unix(), maxNameAttempts}, groups, limit)
	// Claude 2026-10-20: RAR/SFV pending before mkv-only.
	// Reason: live duePendingNames ORDER BY ingested_at DESC filled the 40
	//   BODY slots with newest mkv-first posts; 77 rar_unsampled waited.
	// Troubleshooting: namefix crc=0 while pending95_par2 has rar → this
	//   CASE missing. Review if: crc_sampled column replaces the prefer.
	// Claude 2026-10-22: small complete sets before 471k-header mega packs.
	// Reason: ingested_at DESC put a 508k-header hdtv blob first; setRows
	//   ate ~45s of the 90s BODY budget. Review if: tiny 95% scraps starve
	//   real 2k-seg episodes (then a declared_segs floor).
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp, has_par2 FROM usenet_nntp_releases
WHERE state='pending_name' AND dirty=FALSE AND declared_segs>0 AND completeness_bp>=?
  AND next_name_at<=? AND name_attempts < ? AND group_name IN (`+in+`)
  AND NOT (`+passwordLockedSQL+`)
ORDER BY `+crcPreferOrderSQL+`, declared_segs ASC, ingested_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanPendingSets(rows, false)
}

// DuePendingIdentBody returns ≥80% yield pending_name sets still below the
// 95% BODY/autograb gate that have PAR2/meta. A first-segment BODY can
// recover a title even when the dump (through 2024) has no hash for them.
//
// Claude 2026-10-19: identification BODY at 80%, not only 95% grab-complete.
// Reason: live pending hashes are 2026 and miss the nZEDb dump; indexers
//
//	decode from PAR2 FileDesc regardless of payload completeness.
//
// Troubleshooting: 90% yield with has_par2 never BODY → this query's
//
//	completeness_bp < gate or has_par2 filter. Review if: BODY budget
//	(namefix_body_mb_per_hour) starts hitting BudgetStop every pass.
func (idx *Index) DuePendingIdentBody(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	return idx.dueYieldPrefixed(ctx, groups, limit, idx.duePendingIdentBodyIn)
}

func (idx *Index) duePendingIdentBodyIn(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	if len(groups) == 0 || limit <= 0 {
		return nil, nil
	}
	gateBP := idx.MinCompletenessPct() * 100
	in, args := groupLimitArgs([]any{spanRepairFloorBP, gateBP, time.Now().Unix(), maxNameAttempts}, groups, limit)
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp, has_par2 FROM usenet_nntp_releases
WHERE state='pending_name' AND dirty=FALSE AND declared_segs>0 AND completeness_bp>=? AND completeness_bp<?
  AND has_par2 AND next_name_at<=? AND name_attempts < ? AND group_name IN (`+in+`)
  AND NOT (`+passwordLockedSQL+`)
ORDER BY `+crcPreferOrderSQL+`, declared_segs ASC, completeness_bp DESC, ingested_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanPendingSets(rows, false)
}

func (idx *Index) dueYieldPrefixed(ctx context.Context, groups []string, limit int, query func(context.Context, []string, int) ([]PendingSet, error)) ([]PendingSet, error) {
	var yield []string
	for _, g := range groups {
		if IsNameFixYieldGroup(g) {
			yield = append(yield, g)
		}
	}
	cores, rest := splitCoreRest(yield)
	out, err := query(ctx, cores, limit)
	if err != nil || len(out) >= limit {
		return out, err
	}
	more, err := query(ctx, rest, limit-len(out))
	if err != nil {
		return out, err
	}
	return append(out, more...), nil
}

func scanPendingSets(rows *sql.Rows, preDBOnly bool) ([]PendingSet, error) {
	defer rows.Close()
	var out []PendingSet
	for rows.Next() {
		var p PendingSet
		if err := rows.Scan(&p.Group, &p.Key, &p.PayloadBytes, &p.Attempts, &p.CompletenessBP, &p.HasPar2); err != nil {
			return nil, err
		}
		p.PreDBOnly = preDBOnly
		out = append(out, p)
	}
	return out, rows.Err()
}

// Claude 2026-10-21: SFV, then has_par2 without rar/zip/7z, then rar/zip.
// Reason: live after 7z dequeue, crcPrefer ranked RAR ahead of has_par2 so
//
//	hash-rar (FileDesc=hash.part.rar, often rar5-enc) filled BODY=40 while
//	anime hash.mkv+PAR2 waited. PAR2 FileDesc on mkv/mp4 is the title.
//
// Troubleshooting: namefix pattern pack=rar-split leftover=41 with pending
//
//	hash.mkv+par2 attempts=0 → this CASE still ranks rar ahead of
//	has_par2-without-rar. Review if: unencrypted hash-rar CRC needs a
//	prefer rank (then after mkv+par2).
const crcPreferOrderSQL = `CASE WHEN EXISTS (
  SELECT 1 FROM usenet_nntp_headers h
  WHERE h.group_name=usenet_nntp_releases.group_name AND h.release_name=usenet_nntp_releases.release_key
    AND (LOWER(h.filename) LIKE '%.sfv%' OR LOWER(h.subject) LIKE '%.sfv%')
) THEN 0 WHEN has_par2 AND NOT EXISTS (
  SELECT 1 FROM usenet_nntp_headers h
  WHERE h.group_name=usenet_nntp_releases.group_name AND h.release_name=usenet_nntp_releases.release_key
    AND (LOWER(h.filename) LIKE '%.rar%' OR LOWER(h.filename) LIKE '%.zip%' OR LOWER(h.filename) LIKE '%.7z%'
      OR LOWER(h.subject) LIKE '%.rar%' OR LOWER(h.subject) LIKE '%.zip%' OR LOWER(h.subject) LIKE '%.7z%')
) THEN 1 WHEN EXISTS (
  SELECT 1 FROM usenet_nntp_headers h
  WHERE h.group_name=usenet_nntp_releases.group_name AND h.release_name=usenet_nntp_releases.release_key
    AND (LOWER(h.filename) LIKE '%.rar%' OR LOWER(h.filename) LIKE '%.zip%' OR LOWER(h.subject) LIKE '%.rar%' OR LOWER(h.subject) LIKE '%.zip%')
) THEN 2 WHEN has_par2 THEN 3 ELSE 4 END`

// passwordLockedSQL is true when headers are ngPost hash.7z or UUID .7z.enc.
// Those need an NZB password; BODY cannot recover a title. Dropped from the
// BODY due queues; PreDB may still hash-lookup then shelve.
// Claude 2026-10-21: keep password packs off DuePendingNames.
// Reason: live 1157 pending95 included .7z.enc + ngPost 7z with has_par2;
//
//	they ranked with anime mkv+PAR2 and burned the 20s BODY budget.
//
// Troubleshooting: body leftover=40 skip_7z=true with hash.mkv due → this
//
//	EXISTS missing from DuePendingNames. Review if: a poster uses 7z without
//	ngPost FROM/msgid (then pack=7z-split still BODYs for srrdb CRC).
const passwordLockedSQL = `EXISTS (
  SELECT 1 FROM usenet_nntp_headers h
  WHERE h.group_name=usenet_nntp_releases.group_name AND h.release_name=usenet_nntp_releases.release_key
    AND (
      LOWER(COALESCE(h.filename,'')) LIKE '%.7z.enc%' OR LOWER(h.subject) LIKE '%.7z.enc%'
      OR (
        (h.from_addr ~* '@ngPost\.com$' OR h.msgid ~* '@ngPost')
        AND (LOWER(COALESCE(h.filename,'')) LIKE '%.7z%' OR LOWER(h.subject) LIKE '%.7z%')
        AND LOWER(COALESCE(h.filename,'')) NOT LIKE '%.7z.enc%'
      )
    )
)`

// quotedIdentSubjectSQL matches a quoted payload name that is already a title:
// resolution in the quotes, a spacey or underscore-separated name, or a
// [Group] fansub — not hash.par2.
// Claude 2026-10-20: underscore titles without 1080p (Metropolis_Magazine_-_September_2026).
// Reason: DuePendingHeaderNames required a space or 1080p; those sets went
//
//	to BODY, burned RAR5 CRC slots, leftover=39. Review if: 576p / 4K-without-p.
const quotedIdentSubjectSQL = `(h.subject ~ '"[^"]*(1080|720|2160|480)p[^"]*"' OR h.subject ~ '"[^"]* [A-Za-z][^"]{6,}"' OR h.subject ~ '"[^"]*_[^"]*(19|20)[0-9]{2}[^"]*"' OR h.subject ~ '"\[[^"]{3,}\][^"]+"')`

// DuePendingHeaderNames returns ≥80% pending sets whose headers already quote
// a readable title. Namefix recovers those without PreDB HTTP or BODY.
//
// Claude 2026-10-19: own queue, ahead of PreDB/BODY.
// Reason: 95% yield quoted titles were DuePendingNames (BODY) and stripped
//
//	from preOnly; hash ByHash HTTP then burned both 20s windows (renamed=0).
//
// Troubleshooting: header=0 with Black Clover still pending95 → regex missed the quote shape.
// Review if: 576p / 4K-without-p quoted titles appear in HIGH.
//
// Claude 2026-10-19: ignore next_name_at on this queue.
// Reason: a PreDB miss calls ScheduleNameRetry(+1h). Live Madoka /
//
//	Suikoden quotes sat pending_name at 100% with next_name_at ~30m ahead
//	while Searchable stalled after the first header=10 pass.
//
// Troubleshooting: header=0, quoted 1080p still pending, next_name_at in
//
//	the future → this still filtering on backoff.
//
// Review if: BODY/PreDB queues should share this exception.
func (idx *Index) DuePendingHeaderNames(ctx context.Context, limit int) ([]PendingSet, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT r.group_name, r.release_key, r.payload_bytes, r.name_attempts, r.completeness_bp, r.has_par2
FROM usenet_nntp_releases r
WHERE r.state='pending_name' AND r.dirty=FALSE AND r.declared_segs>0 AND r.completeness_bp>=?
  AND EXISTS (
    SELECT 1 FROM usenet_nntp_headers h
    WHERE h.group_name=r.group_name AND h.release_name=r.release_key
      AND `+quotedIdentSubjectSQL+`
  )
ORDER BY r.completeness_bp DESC, r.ingested_at DESC
LIMIT ?`, spanRepairFloorBP, limit)
	if err != nil {
		return nil, err
	}
	return scanPendingSets(rows, true)
}

// DuePendingPreDB returns ≥80% pending_name sets in any group for a local
// PreDB lookup (no BODY). Boneless and other firehose groups are included:
// a hash hit is free and is what NZB indexers do at ingest.
//
// Claude 2026-10-19: all groups, 200/pass, no payload floor.
// Reason: yield-only PreDB left 1883 complete boneless pending unnamed;
//
//	namefix ran twice in 2h with hash_hits=1.
//
// Claude 2026-10-21: Run() now processes these after yield BODY.
// Reason: live PreDB leftover=197 burned the first 20s; BODY leftover=41.
// Review if: firehose PreDB needs its own loop off crawl.
func (idx *Index) DuePendingPreDB(ctx context.Context, limit int) ([]PendingSet, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp, has_par2
FROM usenet_nntp_releases
WHERE state='pending_name' AND dirty=FALSE AND declared_segs>0 AND completeness_bp>=?
  AND next_name_at<=?
ORDER BY completeness_bp DESC, ingested_at DESC
LIMIT ?`, spanRepairFloorBP, time.Now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	return scanPendingSets(rows, true)
}

type prefileHit struct {
	PendingSet
	Title     string
	SizeBytes int64
}

type prefileName struct {
	PendingSet
	Filename string
}

// matchPrefileExactHits is the bulk half of NNTmux match:prefiles: pending
// ≥80% header.filename = predb.filename. Ignores next_name_at.
//
// Claude 2026-10-22: indexed join, not LIKE, not COUNT(*) of headers.
// Reason: dump/IRC [FN:] never reached BODY-path ByFilename; PreDB-only
//
//	waited on leftover park and next_name_at. Nested loop from pending_name
//	(thousands) into idx_usenet_nntp_predb_filename.
//
// Troubleshooting: matched=0 with a known dump filename on a pending set →
//
//	lower(filename) vs empty filename<>''. Review if: a proc_files column
//	stops re-scanning misses.
func (idx *Index) matchPrefileExactHits(ctx context.Context, limit int) ([]prefileHit, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp, has_par2, title, size_bytes
FROM (
  SELECT DISTINCT ON (r.group_name, r.release_key)
    r.group_name, r.release_key, r.payload_bytes, r.name_attempts, r.completeness_bp, r.has_par2,
    p.title, p.size_bytes
  FROM usenet_nntp_releases r
  INNER JOIN usenet_nntp_headers h
    ON h.group_name=r.group_name AND h.release_name=r.release_key AND h.filename<>''
  INNER JOIN usenet_nntp_predb p
    ON p.filename<>'' AND p.source<>'miss' AND lower(p.filename)=lower(h.filename)
  WHERE r.state='pending_name' AND r.dirty=FALSE AND r.declared_segs>0 AND r.completeness_bp>=?
  ORDER BY r.group_name, r.release_key, r.completeness_bp DESC
) x
ORDER BY completeness_bp DESC
LIMIT ?`, spanRepairFloorBP, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []prefileHit
	for rows.Next() {
		var h prefileHit
		if err := rows.Scan(&h.Group, &h.Key, &h.PayloadBytes, &h.Attempts, &h.CompletenessBP, &h.HasPar2, &h.Title, &h.SizeBytes); err != nil {
			return nil, err
		}
		h.PreDBOnly = true
		out = append(out, h)
	}
	return out, rows.Err()
}

// pendingPrefileFilenames returns up to 8 stored filenames per ≥80% pending
// set for Store.Search (cleaned stem / title). Ignores next_name_at.
func (idx *Index) pendingPrefileFilenames(ctx context.Context, limit int) ([]prefileName, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT p.group_name, p.release_key, p.payload_bytes, p.name_attempts, p.completeness_bp, p.has_par2, h.filename
FROM (
  SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp, has_par2
  FROM usenet_nntp_releases
  WHERE state='pending_name' AND dirty=FALSE AND declared_segs>0 AND completeness_bp>=?
  ORDER BY completeness_bp DESC, ingested_at DESC
  LIMIT ?
) p
JOIN LATERAL (
  SELECT filename FROM usenet_nntp_headers h
  WHERE h.group_name=p.group_name AND h.release_name=p.release_key AND h.filename<>''
  GROUP BY filename
  LIMIT 8
) h ON true`, spanRepairFloorBP, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []prefileName
	for rows.Next() {
		var c prefileName
		if err := rows.Scan(&c.Group, &c.Key, &c.PayloadBytes, &c.Attempts, &c.CompletenessBP, &c.HasPar2, &c.Filename); err != nil {
			return nil, err
		}
		c.PreDBOnly = true
		out = append(out, c)
	}
	return out, rows.Err()
}

func groupLimitArgs(prefix []any, groups []string, limit int) (string, []any) {
	ph := make([]string, len(groups))
	args := make([]any, 0, len(prefix)+len(groups)+1)
	args = append(args, prefix...)
	for i, g := range groups {
		ph[i] = "?"
		args = append(args, g)
	}
	return strings.Join(ph, ","), append(args, limit)
}

// ScheduleNameRetry sets next_name_at without incrementing name_attempts
// (PreDB-only exhausted yield waiting on dump/IRC).
func (idx *Index) ScheduleNameRetry(ctx context.Context, group, key string, wait time.Duration) error {
	if wait <= 0 {
		wait = exhaustedPreDBRetry
	}
	_, err := idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_releases SET next_name_at=?, updated_at=? WHERE group_name=? AND release_key=?`,
		time.Now().Add(wait).Unix(), time.Now().Unix(), group, key)
	return err
}

// SetRows loads every header row of one release/staged-set key.
func (idx *Index) SetRows(ctx context.Context, group, key string) ([]HeaderRow, error) {
	return idx.setRows(ctx, group, key, 0)
}

func (idx *Index) setRows(ctx context.Context, group, key string, limit int) ([]HeaderRow, error) {
	q := `
SELECT msg_num, msgid, subject, from_addr, posted_at, bytes, part_n, part_m, filename, yenc_size, is_meta, file_n, file_m
FROM usenet_nntp_headers WHERE group_name=? AND release_name=? ORDER BY filename, part_n, msg_num`
	args := []any{group, key}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return idx.scanHeaderRows(ctx, group, key, q, args...)
}

// setRowsBodyTargets loads only articles pickBodyTargets needs (main PAR2,
// sfv/nfo, first+last archive volume). Full setRows of a 471k-header pack
// ate ~45s before BodyHead.
func (idx *Index) setRowsBodyTargets(ctx context.Context, group, key string) ([]HeaderRow, error) {
	// Claude 2026-10-22: BODY/parhash skip vol PAR2 and middle yEnc parts.
	// Reason: live 95% pending has_par2 included a 508k-header hdtv set
	//   (37k vol PAR2). 90s BODY still leftover=41. Review if: empty
	//   filename posts (then subject LIKE, still exclude %.vol%).
	q := `
SELECT msg_num, msgid, subject, from_addr, posted_at, bytes, part_n, part_m, filename, yenc_size, is_meta, file_n, file_m
FROM usenet_nntp_headers WHERE group_name=? AND release_name=?
 AND (
  (LOWER(namefix_fn) LIKE '%.par2' AND LOWER(namefix_fn) NOT LIKE '%.vol%')
  OR LOWER(namefix_fn) LIKE '%.sfv%'
  OR LOWER(namefix_fn) LIKE '%.nfo%'
  OR (
    (LOWER(namefix_fn) LIKE '%.rar%' OR LOWER(namefix_fn) LIKE '%.zip%' OR LOWER(namefix_fn) LIKE '%.7z%')
    AND (part_n <= 1 OR (part_m > 0 AND part_n = part_m))
  )
 )
 ORDER BY filename, part_n, msg_num`
	q = strings.ReplaceAll(q, "namefix_fn", "CASE WHEN filename<>'' THEN filename ELSE subject END")
	return idx.scanHeaderRows(ctx, group, key, q, group, key)
}

func (idx *Index) scanHeaderRows(ctx context.Context, group, key, q string, args ...any) ([]HeaderRow, error) {
	rows, err := idx.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeaderRow
	for rows.Next() {
		h := HeaderRow{Group: group, ReleaseName: key}
		if err := rows.Scan(&h.MsgNum, &h.MsgID, &h.Subject, &h.From, &h.PostedAt, &h.Bytes, &h.PartN, &h.PartM,
			&h.Filename, &h.YencSize, &h.IsMeta, &h.FileN, &h.FileM); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// RenameRelease sets the recovered display name on a staged set and recomputes
// completeness. A 95%+ set, or a PreDB-exact name at ≥80%, becomes searchable
// in this pass. Headers are untouched, so the rename is reversible (undo SQL
// in docs/usenet-nntp-native.md).
//
// Claude 2026-10-16: recompute here, do not wait for RecomputeDirty.
// Reason: namefix ran after recompute, so renamed 100% sets sat dirty behind 95k pending and searchable stayed at 12.
// Troubleshooting: renamed= log with searchable still false → recomputeOne failed.
// Review if: RenameRelease is called from a second site that must not hit the DB twice.
func (idx *Index) RenameRelease(ctx context.Context, group, key, display, source, confidence string) error {
	_, err := idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_releases SET display_name=?, name_source=?, name_confidence=?,
  state='named', dirty=TRUE, updated_at=? WHERE group_name=? AND release_key=?`,
		display, source, confidence, time.Now().Unix(), group, key)
	if err != nil {
		return err
	}
	_, _, _, err = idx.recomputeOne(ctx, releaseKey{group, key}, idx.MinCompletenessPct())
	return err
}

// ShelveBodyAttempts stops further BODY on a complete yield set whose PAR2/RAR
// FileDesc is the same hash. DuePendingNames filters name_attempts>=max;
// DuePendingPreDB does not, so dump/IRC can still rename.
//
// Claude 2026-10-19: one hash BODY, not three.
// Reason: live namefix spent 2×~750KB/pass on FileDesc that accept() already
//
//	rejects, then retried until maxNameAttempts while PreDB never ran.
//
// Troubleshooting: body_fetches=2 unresolved=1 on the same hash key every
//
//	loop → this was not called. Review if: ident BODY at 80% still needs
//	a second non-hash sample (NFO).
func (idx *Index) ShelveBodyAttempts(ctx context.Context, group, key string) error {
	now := time.Now().Unix()
	_, err := idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_releases SET name_attempts=?, next_name_at=?, updated_at=?
WHERE group_name=? AND release_key=?`, maxNameAttempts, now, now, group, key)
	return err
}

// BumpNameAttempt records a failed recovery try and schedules the next one.
func (idx *Index) BumpNameAttempt(ctx context.Context, group, key string, attempts int) error {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 8 {
		attempts = 8
	}
	next := time.Now().Add(nameRetryBase << attempts).Unix()
	_, err := idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_releases SET name_attempts=name_attempts+1, next_name_at=?, updated_at=?
WHERE group_name=? AND release_key=?`, next, time.Now().Unix(), group, key)
	return err
}

// FinalizeUnnamed skip-stubs unnamed sets, then deletes headers + release
// rows so a later OVER of the same key does not rebuild them.
func (idx *Index) FinalizeUnnamed(ctx context.Context, keys []releaseKey) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	now := time.Now().Unix()
	for _, k := range keys {
		if _, err := idx.db.ExecContext(ctx,
			`UPDATE usenet_nntp_releases SET state=?, updated_at=? WHERE group_name=? AND release_key=?`,
			stateUnnamedFinal, now, k.group, k.key); err != nil {
			return 0, err
		}
	}
	return idx.skipAndDelete(ctx, keys, skipReasonUnnamed)
}

// keepCompleteYield reports whether a staged set is real yield-group inventory
// that namefix may still recover (do not TTL / attempt-cap delete it).
func keepCompleteYield(group string, completenessBP int, payload int64) bool {
	return IsNameFixYieldGroup(group) && completenessBP >= spanRepairFloorBP && payload >= namefixMinPayload
}

// SweepUnnamed finalizes staged sets older than ttl or out of attempts.
//
// Claude 2026-10-16: do not delete ≥80% complete yield sets with real payload.
// Reason: 51 complete moovee/hdtv/tv/xxx sets (0.3–4.8GB) failed BODY twice
// (hash-named PAR2/RAR) and would be swept on attempt 3 — searchable bursted
// then stopped. Firehose/junk still TTL.
// Troubleshooting: namefix finalized=N while yield complete pending vanishes
// → this filter not applied. Review if: PreDB names those hashes (then TTL).
func (idx *Index) SweepUnnamed(ctx context.Context, ttl time.Duration) (int, error) {
	cutoff := time.Now().Add(-ttl).Unix()
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, completeness_bp, payload_bytes FROM usenet_nntp_releases
WHERE state='pending_name' AND (ingested_at < ? OR name_attempts >= ?) LIMIT ?`,
		cutoff, maxNameAttempts, incompleteSweepBatch)
	if err != nil {
		return 0, err
	}
	var keys []releaseKey
	for rows.Next() {
		var k releaseKey
		var bp int
		var payload int64
		if err := rows.Scan(&k.group, &k.key, &bp, &payload); err != nil {
			rows.Close()
			return 0, err
		}
		if keepCompleteYield(k.group, bp, payload) {
			continue
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if _, err := idx.FinalizeUnnamed(ctx, keys); err != nil {
		return 0, err
	}
	return len(keys), nil
}

// implausibleYencSweepBatch bounds pending_name sets deleted per sweep call
// for unique-ID yEnc (n/m) floods (see maxYencPartM).
const implausibleYencSweepBatch = 2000

// SweepImplausibleYenc deletes pending_name sets whose stored yEnc part_m is
// above maxYencPartM (unique-ID floods that can never meet the completeness gate).
//
// Claude 2026-10-16: ingest now refuses these pairs; this drains the existing pile.
// Reason: 104k live pending_name rows were seen=1 / declared≈967646, so namefix had nothing complete on cores.
// Troubleshooting: pending_name still ~100k after deploy → this query not running or part_m already ≤ maxYencPartM.
// Review if: maxYencPartM changes; dirty the leftover rows instead of deleting.
func (idx *Index) SweepImplausibleYenc(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = implausibleYencSweepBatch
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT r.group_name, r.release_key
FROM usenet_nntp_releases r
WHERE r.state='pending_name'
  AND EXISTS (
    SELECT 1 FROM usenet_nntp_headers h
    WHERE h.group_name=r.group_name AND h.release_name=r.release_key AND h.part_m>?
  )
LIMIT ?`, maxYencPartM, limit)
	if err != nil {
		return 0, err
	}
	var keys []releaseKey
	for rows.Next() {
		var k releaseKey
		if err := rows.Scan(&k.group, &k.key); err != nil {
			rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if _, err := idx.skipAndDelete(ctx, keys, skipReasonImplausible); err != nil {
		return 0, err
	}
	return len(keys), nil
}
