package rename

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/place"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/quality"
)

// Claude 2026-08-05: Rename alternate fold-in (promote/demote by probed tier)
// Reason: deep-interview-rename-alts-ai-fallback — duplicates stay under title; no Dedup/Purge
// Troubleshooting: byTMDB used to Unmatched; Apply now folds second file into library_item_files
// Review if: equal-tier policy changes from keep-existing-primary

// applyLibraryAlternate folds orphan videoPath into an already-tracked title.
// Equal probed tier keeps the existing primary (orphan becomes alternate).
func applyLibraryAlternate(
	ctx context.Context, libStore *library.Store, existing *library.Item,
	p proposals.Proposal, videoPath string, preset naming.Preset, settingsTier string, prober Prober,
) (itemID int64, changes []mode.PathChange, err error) {
	folder := filepath.Join(p.RootFolderPath, naming.MovieFolderName(preset, p.Title, p.Year, p.TMDBID))
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return 0, nil, fmt.Errorf("creating %q: %w", folder, err)
	}

	orphanMeta := probeFileMeta(ctx, prober, videoPath, settingsTier)
	primaryPath := existing.FilePath
	if primaryPath == "" {
		files, listErr := libStore.ListFiles(ctx, existing.ID)
		if listErr == nil {
			for _, f := range files {
				if f.IsPrimary {
					primaryPath = f.FilePath
					break
				}
			}
		}
	}
	primaryMeta := probeFileMeta(ctx, prober, primaryPath, existing.QualityTier)
	if primaryMeta.Tier == "" {
		primaryMeta.Tier = existing.QualityTier
	}

	orphanWins := quality.RankString(orphanMeta.Tier) > quality.RankString(primaryMeta.Tier)

	primaryDest := filepath.Join(folder, naming.MovieFileName(preset, p.Title, p.Year, p.TMDBID, filepath.Ext(videoPath)))
	if orphanWins {
		// Move existing primary to alternate name first (free primary filename).
		altName := naming.MovieAlternateFileName(preset, p.Title, p.Year, p.TMDBID,
			quality.ResolutionLabel(primaryMeta.Height), quality.CodecLabel(primaryMeta.Codec),
			quality.BitrateLabel(primaryMeta.BitRate), filepath.Ext(primaryPath))
		altDest := filepath.Join(folder, altName)
		movedPrimary, ch, moveErr := moveUnique(primaryPath, altDest)
		if moveErr != nil {
			return 0, changes, fmt.Errorf("demoting previous primary %q: %w", primaryPath, moveErr)
		}
		changes = append(changes, ch...)
		primaryPath = movedPrimary

		movedOrphan, ch, moveErr := moveUnique(videoPath, primaryDest)
		if moveErr != nil {
			return 0, changes, fmt.Errorf("promoting orphan to primary %q: %w", primaryDest, moveErr)
		}
		changes = append(changes, ch...)
		videoPath = movedOrphan

		if err := libStore.UpdateItemPrimaryPath(ctx, existing.ID, videoPath, orphanMeta.Tier, library.FileSize(videoPath)); err != nil {
			return 0, changes, err
		}
		if _, err := libStore.UpsertFile(ctx, library.ItemFile{
			ItemID: existing.ID, FilePath: videoPath, IsPrimary: true,
			QualityTier: orphanMeta.Tier, Size: library.FileSize(videoPath),
			Width: orphanMeta.Width, Height: orphanMeta.Height, VideoCodec: orphanMeta.Codec,
			BitRate: orphanMeta.BitRate, DurationSec: orphanMeta.Duration,
		}); err != nil {
			return 0, changes, err
		}
		if _, err := libStore.UpsertFile(ctx, library.ItemFile{
			ItemID: existing.ID, FilePath: primaryPath, IsPrimary: false,
			QualityTier: primaryMeta.Tier, Size: library.FileSize(primaryPath),
			Width: primaryMeta.Width, Height: primaryMeta.Height, VideoCodec: primaryMeta.Codec,
			BitRate: primaryMeta.BitRate, DurationSec: primaryMeta.Duration,
		}); err != nil {
			return 0, changes, err
		}
		return existing.ID, changes, nil
	}

	// Orphan loses or ties — keep existing primary; place orphan as alternate.
	altName := naming.MovieAlternateFileName(preset, p.Title, p.Year, p.TMDBID,
		quality.ResolutionLabel(orphanMeta.Height), quality.CodecLabel(orphanMeta.Codec),
		quality.BitrateLabel(orphanMeta.BitRate), filepath.Ext(videoPath))
	altDest := filepath.Join(folder, altName)
	movedOrphan, ch, moveErr := moveUnique(videoPath, altDest)
	if moveErr != nil {
		return 0, nil, fmt.Errorf("placing alternate %q: %w", altDest, moveErr)
	}
	changes = append(changes, ch...)

	// Refresh primary probe labels if we have them.
	if primaryPath != "" {
		if _, err := libStore.UpsertFile(ctx, library.ItemFile{
			ItemID: existing.ID, FilePath: primaryPath, IsPrimary: true,
			QualityTier: primaryMeta.Tier, Size: library.FileSize(primaryPath),
			Width: primaryMeta.Width, Height: primaryMeta.Height, VideoCodec: primaryMeta.Codec,
			BitRate: primaryMeta.BitRate, DurationSec: primaryMeta.Duration,
		}); err != nil {
			return 0, changes, err
		}
	}
	if _, err := libStore.UpsertFile(ctx, library.ItemFile{
		ItemID: existing.ID, FilePath: movedOrphan, IsPrimary: false,
		QualityTier: orphanMeta.Tier, Size: library.FileSize(movedOrphan),
		Width: orphanMeta.Width, Height: orphanMeta.Height, VideoCodec: orphanMeta.Codec,
		BitRate: orphanMeta.BitRate, DurationSec: orphanMeta.Duration,
	}); err != nil {
		return 0, changes, err
	}
	return existing.ID, changes, nil
}

type fileMeta struct {
	Tier     string
	Width    int
	Height   int
	Codec    string
	BitRate  int64
	Duration float64
}

func probeFileMeta(ctx context.Context, prober Prober, path, fallbackTier string) fileMeta {
	m := fileMeta{Tier: fallbackTier}
	if path == "" || prober == nil {
		return m
	}
	p, err := prober.Probe(ctx, path)
	if err != nil || p == nil {
		return m
	}
	m.Width, m.Height, m.Codec, m.BitRate, m.Duration = p.Width, p.Height, p.CodecName, p.BitRate, p.Duration
	if t := quality.TierFromProbe(p); t != "" {
		m.Tier = string(t)
	}
	return m
}

func moveUnique(source, dest string) (string, []mode.PathChange, error) {
	if source == dest {
		return dest, nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", nil, err
	}
	unique, err := place.UniquePath(dest, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if err != nil {
		return "", nil, err
	}
	if err := os.Rename(source, unique); err != nil {
		return "", nil, fmt.Errorf("moving %q to %q: %w", source, unique, err)
	}
	return unique, []mode.PathChange{
		{Path: source, Kind: mode.Deleted},
		{Path: unique, Kind: mode.Created},
	}, nil
}

// acceptDuplicatePending fills a Pending proposal for a TMDB id already in library.
func acceptDuplicatePending(p *proposals.Proposal, title string, tmdbID, year int, root string) {
	p.Status = proposals.Pending
	p.Title = title
	p.TMDBID = tmdbID
	p.Year = year
	p.RootFolderPath = root
	p.Reason = fmt.Sprintf("alternate: already in library as %q — apply will fold as primary or alternate by quality", title)
}
