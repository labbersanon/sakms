package usenetsearch

import "github.com/labbersanon/sakms/internal/usenet"

// ToNZB synthesizes an in-memory NZB from a Candidate. Pure, no I/O.
func ToNZB(c Candidate) (*usenet.NZB, error) {
	nzb := &usenet.NZB{}
	date := c.PostedAt.Unix()
	if date < 0 {
		date = 0
	}
	for _, f := range c.Files {
		// Claude 2026-09-27: copy Candidate.Poster onto each synthesized file.
		// Reason: native NNTP has no NZB XML; nzbSource reads file.Poster.
		// Troubleshooting: native grab popup Posted by empty — check Candidate.Poster.
		// Review if: per-file From is stored instead of one release poster.
		nf := usenet.NZBFile{
			Poster:  c.Poster,
			Subject: f.Subject,
			Date:    date,
			Groups:  []string{c.Group},
		}
		if nf.Subject == "" {
			nf.Subject = f.Filename
		}
		for _, s := range f.Segs {
			nf.Segs = append(nf.Segs, usenet.NZBSegment{
				Bytes:  s.Bytes,
				Number: s.Number,
				MsgID:  s.MsgID,
			})
		}
		if len(nf.Segs) == 0 {
			continue
		}
		nzb.Files = append(nzb.Files, nf)
	}
	return nzb, nil
}

// AssemblerFunc adapts ToNZB to Assembler.
type AssemblerFunc struct{}

func (AssemblerFunc) ToNZB(c Candidate) (*usenet.NZB, error) { return ToNZB(c) }
