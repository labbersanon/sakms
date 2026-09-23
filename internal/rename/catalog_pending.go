package rename

import (
	"context"
	"log"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// catalogPendingSeries writes Library rows for Rename Pending proposals
// without moving files. Organize identify stays the source of truth; this
// is persist-in-place, not a second matcher.
//
// Claude 2026-09-23: anthology/year-season Pending never entered Library.
// Reason: Apply is the only previous write path and it relocates files.
// Review if: operators want unmatched leftovers listed in Library.
func catalogPendingSeries(ctx context.Context, sess *mode.Session, libStore *library.Store, roots []string, found []proposals.Proposal) {
	if libStore == nil {
		return
	}
	for _, p := range found {
		if p.Status != proposals.Pending || p.SourcePath == "" || p.TMDBID == 0 {
			continue
		}
		foundRoot := rootContaining(p.SourcePath, roots)
		if foundRoot == "" {
			foundRoot = p.RootFolderPath
		}
		eps := []int{p.EpisodeNumber}
		eps = append(eps, p.ExtraEpisodeNumbers...)
		_, err := upsertCatalogedEpisode(ctx, sess, libStore, catalogEpisode{
			TMDBID: p.TMDBID, TVDBID: p.TVDBID, Title: p.Title, Year: p.Year,
			Season: p.SeasonNumber, Episodes: eps, VideoPath: p.SourcePath, FoundRoot: foundRoot,
			AttachExtra: false,
		})
		if err != nil {
			log.Printf("rename catalog pending %q: %v", p.SourcePath, err)
		}
	}
}
