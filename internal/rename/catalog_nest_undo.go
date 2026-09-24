package rename

import (
	"context"
	"log"
	"path/filepath"
	"sync"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

var (
	defaultPropMu    sync.RWMutex
	defaultPropStore *proposals.Store
)

// SetDefaultProposalStore registers the process-wide proposals store nest
// identification archives through. Same shape as SetDefaultUndoStore: catalog
// callers stay signature-stable, and a missing store is a silent no-op.
func SetDefaultProposalStore(s *proposals.Store) {
	defaultPropMu.Lock()
	defaultPropStore = s
	defaultPropMu.Unlock()
}

func DefaultProposalStore() *proposals.Store {
	defaultPropMu.RLock()
	defer defaultPropMu.RUnlock()
	return defaultPropStore
}

// Claude 2026-09-24: nest writes a synthetic Applied proposal (SourcePath ==
// DestPath) so Organize Undo can reverse identification without moving files.
// Reason: Save/Rescan catalogs in place; wrong nests need the same Recently
//   Applied / Undo path as Apply. viaAlternateFold skips the file move.
// Troubleshooting: a nested short has no Undo row after Save.
// Review if: nest starts as a Pending proposal instead of Applied.
func recordNestIdentification(ctx context.Context, libStore *library.Store, parent library.Series, season, ep int, videoPath, foundRoot string, prior *library.Episode) {
	undoStore := DefaultUndoStore()
	propStore := DefaultProposalStore()
	if undoStore == nil || propStore == nil || libStore == nil || videoPath == "" {
		return
	}
	got, err := libStore.GetEpisode(ctx, parent.ID, season, ep)
	if err != nil || got == nil {
		return
	}
	inserted, err := propStore.InsertApplied(ctx, proposals.Proposal{
		Mode:           mode.Series,
		Workflow:       proposals.Rename,
		SourceName:     filepath.Base(videoPath),
		SourcePath:     videoPath,
		RootFolderPath: foundRoot,
		Title:          parent.Title,
		Year:           parent.Year,
		TMDBID:         parent.TMDBID,
		TVDBID:         parent.TVDBID,
		SeasonNumber:   season,
		EpisodeNumber:  ep,
		Reason:         "nested short identified in place",
		TrackedID:      int(parent.ID),
	})
	if err != nil {
		log.Printf("rename: nest archive for %q: inserting applied proposal: %v", videoPath, err)
		return
	}
	snapshot := inserted
	snapshot.Status = proposals.Dismissed
	snapshot.AppliedAt = ""
	resolved := map[int]*library.Episode{ep: prior}
	recordUndoArchive(ctx, mode.Series, snapshot, videoPath,
		episodeTouchedRows([]library.Episode{*got}, resolved),
		videoPath, library.FileSize(videoPath), true, false)
}
