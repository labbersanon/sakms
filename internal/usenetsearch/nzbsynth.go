package usenetsearch

import (
	"errors"

	"github.com/labbersanon/sakms/internal/usenet"
)

// ErrIncomplete is returned by ToNZB when Candidate.Complete is false.
// Search may still list PreDB-named titles below the completeness gate;
// autograb and dispatch must not assemble an NZB from those rows.
var ErrIncomplete = errors.New("release is below the completeness gate")

// ToNZB synthesizes an in-memory NZB from a Candidate. Pure, no I/O.
//
// Claude 2026-10-19: refuse !Complete (docs already required this re-check).
// Reason: PreDB names are searchable below 95%; grabbing those NZBs would
//   dispatch a holey article set. Troubleshooting: grab 502 on a listed
//   native title → CompletenessPct / GrabReady. Review if: operator opts
//   into grabbing below-gate PreDB names.
func ToNZB(c Candidate) (*usenet.NZB, error) {
	if !c.Complete {
		return nil, ErrIncomplete
	}
	nzb := &usenet.NZB{}
	date := c.PostedAt.Unix()
	if date < 0 {
		date = 0
	}
	for _, f := range c.Files {
		nf := usenet.NZBFile{
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
