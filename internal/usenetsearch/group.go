package usenetsearch

import (
	"database/sql"
	"strings"
	"time"
)

// assembleFromRows builds one Candidate from ordered header rows for a release.
func assembleFromRows(group, release string, rows *sql.Rows) (Candidate, error) {
	c := Candidate{Name: release, Group: group}
	type fileKey struct {
		fn string
	}
	files := map[string]*CandidateFile{}
	var order []string
	var maxM int
	seenParts := map[string]map[int]bool{} // filename -> partN
	var wire int64
	var payload int64
	var poster string
	var posted int64

	for rows.Next() {
		var msgid, subject, from, filename string
		var postedAt, bytes, yenc int64
		var partN, partM int
		var isMeta bool
		if err := rows.Scan(&msgid, &subject, &from, &postedAt, &bytes, &partN, &partM, &filename, &yenc, &isMeta); err != nil {
			return Candidate{}, err
		}
		if poster == "" {
			poster = from
		}
		if postedAt > posted {
			posted = postedAt
		}
		if isMeta {
			c.HasPAR2 = true
		}
		fn := filename
		if fn == "" {
			fn = subject
		}
		if files[fn] == nil {
			files[fn] = &CandidateFile{Subject: subject, Filename: filename}
			order = append(order, fn)
			seenParts[fn] = map[int]bool{}
		}
		num := partN
		if num <= 0 {
			num = len(files[fn].Segs) + 1
		}
		if seenParts[fn][num] {
			continue
		}
		seenParts[fn][num] = true
		if partM > maxM {
			maxM = partM
		}
		files[fn].Segs = append(files[fn].Segs, CandidateSeg{
			Bytes:  bytes,
			Number: num,
			MsgID:  msgid,
		})
		wire += bytes
		if yenc > 0 {
			payload += yenc
		} else {
			payload += int64(float64(bytes) / 1.37)
		}
		c.SegmentCount++
	}
	if err := rows.Err(); err != nil {
		return Candidate{}, err
	}
	for _, fn := range order {
		f := files[fn]
		c.Files = append(c.Files, *f)
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
	// Completeness: for each file with known part_m, require contiguous 1..m
	missing := 0
	complete := true
	for _, f := range c.Files {
		if IsMetaSubject(f.Subject, f.Filename) {
			continue
		}
		maxPart := 0
		have := map[int]bool{}
		for _, s := range f.Segs {
			have[s.Number] = true
			if s.Number > maxPart {
				maxPart = s.Number
			}
		}
		want := maxPart
		// if subject carried part_m on any seg we don't have it here; use max seen
		for i := 1; i <= want; i++ {
			if !have[i] {
				missing++
				complete = false
			}
		}
	}
	c.MissingParts = missing
	c.Complete = complete || (missing == 0 && c.SegmentCount > 0)
	if c.HasPAR2 && missing > 0 && missing*100/max(1, c.SegmentCount) <= 2 {
		c.Complete = true
	}
	_ = strings.TrimSpace
	return c, nil
}
