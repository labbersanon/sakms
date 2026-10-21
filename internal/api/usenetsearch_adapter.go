package api

import (
	"time"

	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

// Claude 2026-09-17: adapt usenetsearch.Candidate → prowlarr.Release for RunAutoGrab.
// Reason: v1 keeps the scorer/DTOs on prowlarr.Release. A second non-Prowlarr
//   source (Settings Newznab) uses adaptNewznabReleases, not this helper.
// Troubleshooting: bitrate floor wrong → check PayloadBytes vs WireBytes.
// Review if: neutral release.Candidate type replaces prowlarr.Release in AutoGrab.

func adaptNativeCandidates(cs []usenetsearch.Candidate) []prowlarr.Release {
	out := make([]prowlarr.Release, 0, len(cs))
	for _, c := range cs {
		// Claude 2026-10-19: drop !Complete even if Search listed a PreDB name.
		// Reason: autograb must stay on the 95% gate. Review if: GrabReady is
		//   the only filter and this skip is redundant.
		if !c.Complete {
			continue
		}
		loc := usenetsearch.EncodeLocator(c.Group, c.SeedMsgID, c.Name, c.SegmentCount, c.WireBytes)
		pub := ""
		if !c.PostedAt.IsZero() {
			pub = c.PostedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, prowlarr.Release{
			GUID:        loc,
			Title:       c.Name,
			Indexer:     "nntp:" + c.Group,
			Protocol:    prowlarr.Usenet,
			Size:        c.PayloadBytes,
			Seeders:     0,
			DownloadURL: loc,
			PublishDate: pub,
			Categories:  nil,
		})
	}
	return out
}
