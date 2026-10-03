package rename

import (
	"context"
	"path/filepath"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
)

// Claude 2026-10-02: Scan proposes moving already-tracked files into preset layout.
// Reason: known-map hides tracked paths from the orphan walk, so a messy
// library folder never got a hierarchy proposal after identity was assigned.
// Troubleshooting: tracked files stay in a dump folder after Scan.
// Review if: Movies Scan grows the same tracked-schema relocate pass.
func proposeTrackedSeriesSchemaMoves(ctx context.Context, libStore *library.Store, allSeries []library.Series, roots []string, preset naming.Preset, already []proposals.Proposal) []proposals.Proposal {
	if libStore == nil {
		return nil
	}
	seenPath := map[string]bool{}
	for _, p := range already {
		if p.SourcePath != "" {
			seenPath[p.SourcePath] = true
		}
	}
	var moves []proposals.Proposal
	for _, series := range allSeries {
		if series.TMDBID <= 0 || series.RootFolderPath == "" {
			continue
		}
		episodes, err := libStore.ListEpisodes(ctx, series.ID)
		if err != nil {
			continue
		}
		for _, ep := range episodes {
			if p, ok := trackedSeriesSchemaMove(series, ep, roots, preset, seenPath); ok {
				moves = append(moves, p)
			}
		}
	}
	return moves
}

func trackedSeriesSchemaMove(series library.Series, ep library.Episode, roots []string, preset naming.Preset, seenPath map[string]bool) (proposals.Proposal, bool) {
	if ep.FilePath == "" || seenPath[ep.FilePath] {
		return proposals.Proposal{}, false
	}
	if dummyMovieEpisodeParse(ep.SeasonNumber, []int{ep.EpisodeNumber}) {
		return proposals.Proposal{}, false
	}
	if rootContaining(ep.FilePath, roots) == "" {
		return proposals.Proposal{}, false
	}
	if naming.MatchesSeriesSchema(ep.FilePath, preset) {
		return proposals.Proposal{}, false
	}
	seenPath[ep.FilePath] = true
	p := proposals.Proposal{
		Mode:                mode.Series,
		Workflow:            proposals.Rename,
		Status:              proposals.Pending,
		SourceName:          filepath.Base(ep.FilePath),
		SourcePath:          ep.FilePath,
		RootFolderPath:      series.RootFolderPath,
		Title:               series.Title,
		Year:                series.Year,
		TMDBID:              series.TMDBID,
		TVDBID:              series.TVDBID,
		SeasonNumber:        ep.SeasonNumber,
		EpisodeNumber:       ep.EpisodeNumber,
		EpisodeTitle:        ep.Title,
		ExtraEpisodeNumbers: extraEpisodesFromPath(ep),
		TrackedID:           int(ep.ID),
		Reason:              "already in the library — move into Show (Year) [tmdbid-N]/Season NN/Show SxxExx Title.ext",
	}
	dest := ImportDestPath(p, preset)
	if dest == "" || dest == ep.FilePath {
		return proposals.Proposal{}, false
	}
	if !naming.MatchesSeriesSchema(dest, preset) {
		return proposals.Proposal{}, false
	}
	return p, true
}

func extraEpisodesFromPath(ep library.Episode) []int {
	_, parsed, ok := library.ParseEpisodeNumbersLoose(filepath.Base(ep.FilePath), filepath.Dir(ep.FilePath))
	if !ok || len(parsed) < 2 {
		return nil
	}
	if parsed[0] != ep.EpisodeNumber {
		return nil
	}
	return parsed[1:]
}
