package purge

import (
	"context"
	"errors"
	"fmt"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// ErrRemoveSpec is a caller-fixable RemoveOwned request (wrong series flags,
// empty season list, negative season). The API maps it to 400.
var ErrRemoveSpec = errors.New("invalid remove spec")

// RemoveSpec is the Series-only body for RemoveOwned. Movies and Adult ignore it.
type RemoveSpec struct {
	EntireSeries bool
	Seasons      []int
}

// RemoveOwned permanently deletes tracked media files and library rows — the
// same disk contract as ApplyLibrary / ApplyLibrarySeries / ApplyLibraryAdult,
// without a Purge proposal. Series may delete the whole show or selected seasons.
//
// Claude 2026-10-01: owned DetailPopup "Remove from library".
// Reason: Purge is staged-for-approval; this is an explicit one-title confirm.
// Troubleshooting: files stayed on disk after the library row vanished.
// Review if: Purge Apply* becomes a thin wrapper around this.
func RemoveOwned(ctx context.Context, libStore *library.Store, m mode.Mode, id int64, spec RemoveSpec) (changes []mode.PathChange, gone bool, err error) {
	switch m {
	case mode.Movies:
		changes, err = removeOwnedMovie(ctx, libStore, id)
		return changes, err == nil, err
	case mode.Adult:
		changes, err = removeOwnedAdult(ctx, libStore, id)
		return changes, err == nil, err
	case mode.Series:
		return removeOwnedSeries(ctx, libStore, id, spec)
	default:
		return nil, false, fmt.Errorf("%w: unknown mode %q", ErrRemoveSpec, m)
	}
}

func removeOwnedMovie(ctx context.Context, libStore *library.Store, id int64) ([]mode.PathChange, error) {
	item, err := libStore.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	files, err := libStore.ListFiles(ctx, item.ID)
	if err != nil {
		return nil, fmt.Errorf("listing files for library item %d: %w", item.ID, err)
	}
	paths := make([]string, 0, 1+len(files))
	paths = append(paths, item.FilePath)
	for _, f := range files {
		paths = append(paths, f.FilePath)
	}
	changes, err := removeTrackedMedia(paths, item.RootFolderPath)
	if err != nil {
		return changes, err
	}
	pruneDeleted(ctx, libStore, changes)
	if err := libStore.Delete(ctx, item.ID); err != nil {
		return changes, err
	}
	return changes, nil
}

func removeOwnedAdult(ctx context.Context, libStore *library.Store, id int64) ([]mode.PathChange, error) {
	scene, err := libStore.GetSceneByID(ctx, id)
	if err != nil {
		return nil, err
	}
	files, err := libStore.ListSceneFiles(ctx, scene.ID)
	if err != nil {
		return nil, fmt.Errorf("listing files for scene %d: %w", scene.ID, err)
	}
	paths := make([]string, 0, 1+len(files))
	paths = append(paths, scene.FilePath)
	for _, f := range files {
		paths = append(paths, f.FilePath)
	}
	changes, err := removeTrackedMedia(paths, scene.RootFolderPath)
	if err != nil {
		return changes, err
	}
	pruneDeleted(ctx, libStore, changes)
	if err := libStore.DeleteScene(ctx, scene.ID); err != nil {
		return changes, err
	}
	return changes, nil
}

func removeOwnedSeries(ctx context.Context, libStore *library.Store, id int64, spec RemoveSpec) ([]mode.PathChange, bool, error) {
	seasons, err := normalizeSeasons(spec)
	if err != nil {
		return nil, false, err
	}
	series, err := libStore.GetSeries(ctx, id)
	if err != nil {
		return nil, false, err
	}
	episodes, err := libStore.ListEpisodes(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("loading episodes for series %d: %w", id, err)
	}

	entire := spec.EntireSeries
	var removeEps []library.Episode
	var keepEps []library.Episode
	if entire {
		removeEps = episodes
	} else {
		want := make(map[int]struct{}, len(seasons))
		for _, n := range seasons {
			want[n] = struct{}{}
		}
		for _, ep := range episodes {
			if _, ok := want[ep.SeasonNumber]; ok {
				removeEps = append(removeEps, ep)
			} else {
				keepEps = append(keepEps, ep)
			}
		}
		if len(removeEps) == 0 {
			return nil, false, fmt.Errorf("%w: no episodes in the selected seasons", ErrRemoveSpec)
		}
	}

	removePaths, err := collectEpisodePaths(ctx, libStore, removeEps)
	if err != nil {
		return nil, false, err
	}
	keepPaths, err := collectEpisodePaths(ctx, libStore, keepEps)
	if err != nil {
		return nil, false, err
	}
	keepSet := make(map[string]struct{}, len(keepPaths))
	for _, p := range uniqueNonEmptyPaths(keepPaths) {
		keepSet[p] = struct{}{}
	}
	var deletePaths []string
	for _, p := range uniqueNonEmptyPaths(removePaths) {
		if _, ok := keepSet[p]; ok {
			continue
		}
		deletePaths = append(deletePaths, p)
	}

	changes, err := removeTrackedMediaKeeping(deletePaths, series.RootFolderPath, keepPaths)
	if err != nil {
		return changes, false, err
	}
	pruneDeleted(ctx, libStore, changes)

	if entire || len(keepEps) == 0 {
		if err := libStore.DeleteSeries(ctx, id); err != nil {
			return changes, false, err
		}
		return changes, true, nil
	}
	for _, ep := range removeEps {
		if err := libStore.DeleteEpisode(ctx, ep.ID); err != nil {
			return changes, false, err
		}
	}
	for _, n := range seasons {
		if err := libStore.ClearSeasonMonitored(ctx, id, n); err != nil {
			return changes, false, err
		}
	}
	return changes, false, nil
}

func normalizeSeasons(spec RemoveSpec) ([]int, error) {
	if spec.EntireSeries && len(spec.Seasons) > 0 {
		return nil, fmt.Errorf("%w: entireSeries and seasons are mutually exclusive", ErrRemoveSpec)
	}
	if spec.EntireSeries {
		return nil, nil
	}
	if len(spec.Seasons) == 0 {
		return nil, fmt.Errorf("%w: series remove requires entireSeries or seasons", ErrRemoveSpec)
	}
	seen := make(map[int]struct{}, len(spec.Seasons))
	out := make([]int, 0, len(spec.Seasons))
	for _, n := range spec.Seasons {
		if n < 0 {
			return nil, fmt.Errorf("%w: season numbers must be >= 0", ErrRemoveSpec)
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out, nil
}

func collectEpisodePaths(ctx context.Context, libStore *library.Store, episodes []library.Episode) ([]string, error) {
	var paths []string
	for _, ep := range episodes {
		paths = append(paths, ep.FilePath)
		files, err := libStore.ListEpisodeFiles(ctx, ep.ID)
		if err != nil {
			return nil, fmt.Errorf("listing files for episode %d: %w", ep.ID, err)
		}
		for _, f := range files {
			paths = append(paths, f.FilePath)
		}
	}
	return paths, nil
}

func pruneDeleted(ctx context.Context, libStore *library.Store, changes []mode.PathChange) {
	for _, c := range changes {
		libStore.PruneVMAFScoresForPath(ctx, c.Path)
	}
}
