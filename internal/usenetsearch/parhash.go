package usenetsearch

import (
	"context"
	"strings"
	"time"
)

// Claude 2026-10-20: PAR2 hash_16K donor index (NNTmux fixNamesWithParHash).
// Reason: remaining complete hash.par2 sets have FileDesc names that are also
//   hashes; FileDesc is a random token; the 16KiB content MD5 still matches a named
//   sibling (other group / earlier FileDesc title).
// Troubleshooting: parhash_sampled=true and table empty → BODY had no FileDesc
//   hash field (zeros). named par2 with sampled=false → DueParHashSeed.
// Review if: CRC32/UID donor tables are added (same size-window pattern).
// Related files: par2names.go, namefix.go, migrations/0049_usenet_nntp_par_hashes.sql

const namefixHash16KTolerance = 0.05

// StoreParHashes upserts FileDesc hash_16K rows for one release. Zero hashes
// are skipped so empty test packets cannot collide every unnamed set.
func (idx *Index) StoreParHashes(ctx context.Context, group, key string, files []par2File) error {
	if idx == nil || idx.db == nil || group == "" || key == "" {
		return nil
	}
	now := time.Now().Unix()
	for _, f := range files {
		if f.Hash16K == "" {
			continue
		}
		if _, err := idx.db.ExecContext(ctx, `
INSERT INTO usenet_nntp_par_hashes (hash, group_name, release_key, file_length, stored_at)
VALUES (?,?,?,?,?)
ON CONFLICT (hash, group_name, release_key) DO UPDATE SET
  file_length=CASE WHEN EXCLUDED.file_length>0 THEN EXCLUDED.file_length ELSE usenet_nntp_par_hashes.file_length END,
  stored_at=EXCLUDED.stored_at`,
			f.Hash16K, group, key, f.Length, now); err != nil {
			return err
		}
	}
	return nil
}

// MarkParHashSampled records that this set's PAR2 was BODY-read for hashes,
// even when no FileDesc hash field was present, so seed does not loop.
func (idx *Index) MarkParHashSampled(ctx context.Context, group, key string) error {
	_, err := idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_releases SET parhash_sampled=TRUE, updated_at=?
WHERE group_name=? AND release_key=?`, time.Now().Unix(), group, key)
	return err
}

// ParHashesFor returns stored hash_16K values for one release.
func (idx *Index) ParHashesFor(ctx context.Context, group, key string) ([]string, error) {
	rows, err := idx.db.QueryContext(ctx, `
SELECT hash FROM usenet_nntp_par_hashes WHERE group_name=? AND release_key=?`, group, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

type parHashDonor struct {
	Name    string
	Payload int64
	Group   string
	Key     string
}

// LookupParHashDonor returns the named release whose PAR2 hash_16K overlaps
// this set, size within ±5% (NNTmux), closest payload, then lowest key.
func (idx *Index) LookupParHashDonor(ctx context.Context, group, key string, payload int64) (string, bool, error) {
	hashes, err := idx.ParHashesFor(ctx, group, key)
	if err != nil || len(hashes) == 0 {
		return "", false, err
	}
	donors, err := idx.namedParHashDonors(ctx, hashes, group, key)
	if err != nil {
		return "", false, err
	}
	best, ok := pickParHashDonor(donors, payload)
	if !ok || recoveredUnusable(best.Name) {
		return "", false, nil
	}
	return best.Name, true, nil
}

// PendingSharingParHashes returns unnamed sets that share any hash_16K with
// hashes, excluding exclude, size within ±5% of payload.
func (idx *Index) PendingSharingParHashes(ctx context.Context, hashes []string, payload int64, excludeGroup, excludeKey string) ([]PendingSet, error) {
	if len(hashes) == 0 || payload <= 0 {
		return nil, nil
	}
	ph, args := hashInArgs(hashes)
	args = append(args, excludeGroup, excludeKey)
	rows, err := idx.db.QueryContext(ctx, `
SELECT r.group_name, r.release_key, r.payload_bytes, r.name_attempts, r.completeness_bp, r.has_par2
FROM usenet_nntp_par_hashes h
JOIN usenet_nntp_releases r ON r.group_name=h.group_name AND r.release_key=h.release_key
WHERE h.hash IN (`+ph+`)
  AND r.state='pending_name'
  AND NOT (r.group_name=? AND r.release_key=?)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []PendingSet
	for rows.Next() {
		var p PendingSet
		if err := rows.Scan(&p.Group, &p.Key, &p.PayloadBytes, &p.Attempts, &p.CompletenessBP, &p.HasPar2); err != nil {
			return nil, err
		}
		id := p.Group + "\x00" + p.Key
		if seen[id] || !sizeWithinKnown(p.PayloadBytes, payload, namefixHash16KTolerance) {
			continue
		}
		seen[id] = true
		out = append(out, p)
	}
	return out, rows.Err()
}

// DueParHashSeed returns unsampled PAR2 sets: up to half pending (so shelved
// hash.par2 can store hashes while named donors still exist), the rest named.
// Named leftover fills any unused pending slots.
func (idx *Index) DueParHashSeed(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	if limit <= 0 {
		return nil, nil
	}
	var yield []string
	for _, g := range groups {
		if IsNameFixYieldGroup(g) {
			yield = append(yield, g)
		}
	}
	// Claude 2026-10-20: reserve half the seed for pending hash.par2.
	// Reason: live named_unsampled=156 filled all 20 slots every pass;
	//   pending95_par2 never stored hash_16K (dup_hashes stayed 0).
	// Troubleshooting: parhash=20 with no pending keys → this split
	//   missing or pending is not has_par2/attempts>=max.
	// Review if: named seed has drained (then pending can take the rest).
	pending, err := idx.dueParHashSeedPending(ctx, yield, limit/2)
	if err != nil {
		return nil, err
	}
	named, err := idx.dueParHashSeedNamed(ctx, limit-len(pending))
	if err != nil {
		return pending, err
	}
	return append(named, pending...), nil
}

func (idx *Index) dueParHashSeedNamed(ctx context.Context, limit int) ([]PendingSet, error) {
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp, has_par2
FROM usenet_nntp_releases
WHERE state='named' AND has_par2 AND NOT parhash_sampled AND display_name<>''
ORDER BY ingested_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	sets, err := scanPendingSets(rows)
	for i := range sets {
		sets[i].AlreadyNamed = true
	}
	return sets, err
}

func (idx *Index) dueParHashSeedPending(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	if len(groups) == 0 || limit <= 0 {
		return nil, nil
	}
	gateBP := idx.MinCompletenessPct() * 100
	in, args := groupLimitArgs([]any{gateBP, maxNameAttempts}, groups, limit)
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp, has_par2
FROM usenet_nntp_releases
WHERE state='pending_name' AND has_par2 AND NOT parhash_sampled AND dirty=FALSE
  AND declared_segs>0 AND completeness_bp>=? AND name_attempts>=? AND group_name IN (`+in+`)
ORDER BY ingested_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanPendingSets(rows)
}

func (idx *Index) namedParHashDonors(ctx context.Context, hashes []string, excludeGroup, excludeKey string) ([]parHashDonor, error) {
	ph, args := hashInArgs(hashes)
	args = append(args, excludeGroup, excludeKey)
	rows, err := idx.db.QueryContext(ctx, `
SELECT r.display_name, r.payload_bytes, r.group_name, r.release_key
FROM usenet_nntp_par_hashes h
JOIN usenet_nntp_releases r ON r.group_name=h.group_name AND r.release_key=h.release_key
WHERE h.hash IN (`+ph+`)
  AND r.state='named'
  AND r.display_name<>''
  AND NOT (r.group_name=? AND r.release_key=?)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []parHashDonor
	for rows.Next() {
		var d parHashDonor
		if err := rows.Scan(&d.Name, &d.Payload, &d.Group, &d.Key); err != nil {
			return nil, err
		}
		id := d.Group + "\x00" + d.Key
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, d)
	}
	return out, rows.Err()
}

func pickParHashDonor(donors []parHashDonor, payload int64) (parHashDonor, bool) {
	var best parHashDonor
	bestDelta := int64(-1)
	for _, d := range donors {
		if !sizeWithinKnown(d.Payload, payload, namefixHash16KTolerance) {
			continue
		}
		delta := d.Payload - payload
		if delta < 0 {
			delta = -delta
		}
		better := bestDelta < 0 || delta < bestDelta
		if !better && delta == bestDelta {
			better = d.Group < best.Group || (d.Group == best.Group && d.Key < best.Key)
		}
		if better {
			best = d
			bestDelta = delta
		}
	}
	return best, bestDelta >= 0
}

func hashInArgs(hashes []string) (string, []any) {
	ph := make([]string, len(hashes))
	args := make([]any, 0, len(hashes)+2)
	for i, h := range hashes {
		ph[i] = "?"
		args = append(args, h)
	}
	return strings.Join(ph, ","), args
}
