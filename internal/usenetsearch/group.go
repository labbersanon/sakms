package usenetsearch

import (
	"database/sql"
	"time"
)

// assembleFromRows builds one Candidate from ordered header rows for a release.
//
// Claude 2026-10-15: completeness now comes from computeCompleteness (strict
// payload gate, plan nntp-gap-fill.md D4) instead of the old lenient rules
// (want := max part seen, SegmentCount/50 tolerance, HasPAR2 <=2% override).
// release is the immutable grouping key (headers.release_name); display is
// the searchable name (usenet_nntp_releases.display_name) and becomes
// Candidate.Name. gatePct is the strict threshold used for Candidate.Complete.
// Troubleshooting: Complete=false on a release that "looks" whole → inspect
// CompletenessPct/MissingParts; undeclared part_m reports 0%.
// Review if: Candidate gains per-file completeness.
func assembleFromRows(group, release, display string, rows *sql.Rows, gatePct int) (Candidate, error) {
	if display == "" {
		display = release
	}
	c := Candidate{Name: display, Group: group}
	files := map[string]*CandidateFile{}
	aggs := map[string]*fileAgg{}
	var order []string
	var wire int64
	var payload int64
	var poster string
	var posted int64
	var fileM int

	for rows.Next() {
		var msgid, subject, from, filename string
		var postedAt, bytes, yenc int64
		var partN, partM, rowFileM int
		var isMeta bool
		if err := rows.Scan(&msgid, &subject, &from, &postedAt, &bytes, &partN, &partM, &filename, &yenc, &isMeta, &rowFileM); err != nil {
			return Candidate{}, err
		}
		if poster == "" {
			poster = from
		}
		if postedAt > posted {
			posted = postedAt
		}
		if rowFileM > fileM {
			fileM = rowFileM
		}
		meta := skipPayload(subject, filename, isMeta)
		if isMeta {
			c.HasPAR2 = true
		}
		fn := filename
		if fn == "" {
			fn = rePartNM.ReplaceAllString(subject, "")
		}
		if files[fn] == nil {
			files[fn] = &CandidateFile{Subject: subject, Filename: filename}
			aggs[fn] = &fileAgg{Meta: meta, Seen: map[int]bool{}}
			order = append(order, fn)
		}
		agg := aggs[fn]
		if partM > agg.PartM {
			agg.PartM = partM
		}
		num := partN
		if num <= 0 {
			num = len(files[fn].Segs) + 1
		}
		if agg.Seen[num] {
			continue
		}
		agg.Seen[num] = true
		files[fn].Segs = append(files[fn].Segs, CandidateSeg{
			Bytes:  bytes,
			Number: num,
			MsgID:  msgid,
		})
		wire += bytes
		if !meta {
			addFilePayload(&payload, agg, yenc, bytes)
		}
		c.SegmentCount++
	}
	if err := rows.Err(); err != nil {
		return Candidate{}, err
	}
	fileAggs := make([]fileAgg, 0, len(order))
	for _, fn := range order {
		c.Files = append(c.Files, *files[fn])
		fileAggs = append(fileAggs, *aggs[fn])
	}
	c.FileCount = len(c.Files)
	c.WireBytes = wire
	c.PayloadBytes = payload
	c.Poster = poster
	if posted > 0 {
		c.PostedAt = time.Unix(posted, 0).UTC()
	}
	if len(c.Files) > 0 && len(c.Files[0].Segs) > 0 {
		c.SeedMsgID = c.Files[0].Segs[0].MsgID
	}
	if identShapedName(release) || identShapedName(display) {
		fileM = 0
	}
	comp := computeCompleteness(fileAggs, fileM)
	c.MissingParts = comp.Missing()
	c.CompletenessPct = comp.Pct()
	c.Complete = comp.Meets(gatePct)
	return c, nil
}
