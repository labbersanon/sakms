package rename

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/quality"
)

// TestApplyLibrarySeries_RangeAlternateNeverPromotes is plan §9.4's designated
// regression guard for TWO independent defects, both of which a prior critic
// round found and fixed in this plan. State both, because an assertion that
// only covers one leaves the other silently unguarded.
//
//  1. NEVER-PROMOTE (§6.3). The incoming range file wins on probed tier against
//     both occupied slots and STILL lands as an alternate: neither existing
//     primary is moved and neither library_episodes.file_path changes.
//
//  2. COMPLETE RESOLVED MAP (§7.2) — the discriminating assertion is the ROW
//     COUNT: exactly TWO non-primary library_episode_files rows, ONE PER
//     BUNDLED EPISODE, both naming the same moved alternate path. This is what
//     ApplyLibrarySeries' gate loop breaking early on the first occupied slot
//     fails: E2 is never queried, resolved[2] is absent, the write loop treats
//     it as "no row at all" and skips it — producing one alternate row instead
//     of two, with no error and no log line.
//
// Count the non-primary rows PER EPISODE, never the total on E1. E1's own list
// is 2 rows (its refreshed primary plus the alternate) under the bug as well —
// E2's list is what collapses from 2 to 1. Asserting on the status, on the
// moved file's name, or on E1's rows alone all still pass in the broken state.
//
// Two seeding details are load-bearing, per §9.4's own note:
//
//   - BOTH slots are occupied, each by its OWN distinct real file. This is the
//     only case in the plan where more than one slot in a range is occupied at
//     once, which is precisely what makes break-vs-no-break observable — the
//     sibling range tests seed one slot (unobservable) or leave a slot with no
//     row at all (a different branch). Distinct files also keep
//     CountEpisodesByFilePath's shared-primary refusal from firing, so
//     len(episodeNumbers) > 1 is the SOLE reason promotion is refused here.
//   - The orphan's probed tier is STRICTLY HIGHER than both primaries'. With
//     equal tiers the equal-tier-keeps-the-existing-primary rule would produce
//     the same outcome and the never-promote assertion would be vacuous.
func TestApplyLibrarySeries_RangeAlternateNeverPromotes(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	destRoot := filepath.Join(base, "TV")
	seasonDir := filepath.Join(destRoot, "Show Name (2020) [tmdbid-555]", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	e1Path := filepath.Join(seasonDir, "Show Name S01E01.mkv")
	e2Path := filepath.Join(seasonDir, "Show Name S01E02.mkv")
	for _, p := range []string{e1Path, e2Path} {
		if err := os.WriteFile(p, []byte("primary"), 0o644); err != nil {
			t.Fatalf("seeding %q: %v", p, err)
		}
	}
	orphan := filepath.Join(base, "incoming", "show.s01e01-e02.2160p.mkv")
	if err := os.MkdirAll(filepath.Dir(orphan), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(orphan, []byte("higher tier"), 0o644); err != nil {
		t.Fatalf("seeding orphan: %v", err)
	}

	libStore := newTestLibraryStore(t)
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 555, Title: "Show Name", Year: 2020, RootFolderPath: destRoot,
	})
	if err != nil {
		t.Fatalf("UpsertSeries: %v", err)
	}
	ep1, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: e1Path, QualityTier: "low",
	})
	if err != nil {
		t.Fatalf("UpsertEpisode E1: %v", err)
	}
	ep2, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: e2Path, QualityTier: "low",
	})
	if err != nil {
		t.Fatalf("UpsertEpisode E2: %v", err)
	}

	prober := mapProber{
		e1Path: &mediainfo.Probe{Height: 480, CodecName: "h264", BitRate: 800_000},
		e2Path: &mediainfo.Probe{Height: 480, CodecName: "h264", BitRate: 800_000},
		orphan: &mediainfo.Probe{Height: 2160, CodecName: "hevc", BitRate: 40_000_000},
	}
	p := proposals.Proposal{
		Status: proposals.Pending, Title: "Show Name", Year: 2020, TMDBID: 555,
		SeasonNumber: 1, EpisodeNumber: 1, ExtraEpisodeNumbers: []int{2},
		SourcePath: orphan, RootFolderPath: destRoot,
	}
	gotID, _, err := ApplyLibrarySeries(ctx, libStore, nil, nil, p, naming.Jellyfin, "low", prober)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}
	if gotID != ep1.ID {
		t.Errorf("episode id = %d, want the first occupied slot's %d", gotID, ep1.ID)
	}

	// (1) Never-promote: both primaries are untouched, on disk and in the DB.
	for _, want := range []struct {
		id   int64
		path string
		n    int
	}{{ep1.ID, e1Path, 1}, {ep2.ID, e2Path, 2}} {
		row, err := libStore.GetEpisode(ctx, series.ID, 1, want.n)
		if err != nil {
			t.Fatalf("GetEpisode S01E%02d: %v", want.n, err)
		}
		if row.FilePath != want.path {
			t.Errorf("S01E%02d file_path = %q, want the untouched primary %q — a range proposal must never promote",
				want.n, row.FilePath, want.path)
		}
		if _, err := os.Stat(want.path); err != nil {
			t.Errorf("S01E%02d's primary file is gone from disk (%v) — it was demoted/moved", want.n, err)
		}
	}

	// (2) Complete resolved map: one non-primary row PER bundled episode.
	var altPaths []string
	for _, want := range []struct {
		id int64
		n  int
	}{{ep1.ID, 1}, {ep2.ID, 2}} {
		files, err := libStore.ListEpisodeFiles(ctx, want.id)
		if err != nil {
			t.Fatalf("ListEpisodeFiles S01E%02d: %v", want.n, err)
		}
		var alts []library.EpisodeFile
		for _, f := range files {
			if !f.IsPrimary {
				alts = append(alts, f)
			}
		}
		if len(alts) != 1 {
			t.Fatalf("S01E%02d has %d non-primary rows, want exactly 1 — every bundled episode gets its own alternate row (§7.2's no-early-break map)",
				want.n, len(alts))
		}
		if alts[0].EpisodeID != want.id {
			t.Errorf("alternate row episode_id = %d, want %d", alts[0].EpisodeID, want.id)
		}
		altPaths = append(altPaths, alts[0].FilePath)
	}
	if altPaths[0] != altPaths[1] {
		t.Fatalf("the two alternate rows name different files (%q vs %q) — one physical file was relocated, so both rows must name it",
			altPaths[0], altPaths[1])
	}
	moved := altPaths[0]
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("alternate file %q is not on disk: %v", moved, err)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Errorf("the orphan is still at its source path %q — it was never relocated", orphan)
	}
	if wantDir := seasonDir; filepath.Dir(moved) != wantDir {
		t.Errorf("alternate landed in %q, want the season folder %q", filepath.Dir(moved), wantDir)
	}
	altBase := filepath.Base(moved)
	if !strings.Contains(altBase, "S01E01-E02") {
		t.Errorf("alternate name %q does not carry the bundled range", altBase)
	}
	if !strings.Contains(altBase, " - ") {
		t.Errorf("alternate name %q is missing the quality-token suffix that distinguishes it from the primary", altBase)
	}
}

// ---------------------------------------------------------------------------
// Shared scaffolding for plan §9.3/§9.4's ApplyLibrarySeries tests.
//
// Every test below seeds the same shape US-008's RangeAlternateNeverPromotes
// above seeds inline: a temp dest root, a "Season 01" folder under the Jellyfin
// show-folder name, a real Postgres-backed store via newTestLibraryStore, and
// one library_series row. Extracted rather than copy-pasted ten times so the
// seeding stays identical across the whole file — the tests differ only in what
// they put IN that season folder, which is the part worth reading.
// ---------------------------------------------------------------------------

const (
	altTestTitle  = "Show Name"
	altTestYear   = 2020
	altTestTMDBID = 555
)

type seriesAltFixture struct {
	base      string
	destRoot  string
	seasonDir string
	libStore  *library.Store
	series    library.Series
}

func newSeriesAltFixture(t *testing.T, ctx context.Context) seriesAltFixture {
	t.Helper()
	base := t.TempDir()
	destRoot := filepath.Join(base, "TV")
	seasonDir := filepath.Join(destRoot,
		naming.SeriesFolderName(naming.Jellyfin, altTestTitle, altTestYear, altTestTMDBID),
		naming.SeasonDirName(1))
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatalf("mkdir season dir: %v", err)
	}
	libStore := newTestLibraryStore(t)
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: altTestTMDBID, Title: altTestTitle, Year: altTestYear, RootFolderPath: destRoot,
	})
	if err != nil {
		t.Fatalf("UpsertSeries: %v", err)
	}
	return seriesAltFixture{base: base, destRoot: destRoot, seasonDir: seasonDir, libStore: libStore, series: series}
}

// proposal builds the Pending Series proposal every test applies. episodes[0]
// is the primary number; any remainder becomes ExtraEpisodeNumbers, so a range
// proposal and a single-episode one are built the same way.
func (f seriesAltFixture) proposal(sourcePath string, episodes ...int) proposals.Proposal {
	return proposals.Proposal{
		Status: proposals.Pending, Title: altTestTitle, Year: altTestYear, TMDBID: altTestTMDBID,
		SeasonNumber: 1, EpisodeNumber: episodes[0], ExtraEpisodeNumbers: episodes[1:],
		SourcePath: sourcePath, RootFolderPath: f.destRoot,
	}
}

// writeFile seeds a real file with distinctive content. Content, not path, is
// how these tests identify a file after ApplyLibrarySeries has relocated it —
// a promote and a no-op can land two DIFFERENT files on the SAME destination
// path string, so asserting on the path alone cannot tell them apart.
func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %q: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("seeding %q: %v", path, err)
	}
	return path
}

func fileContent(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	return string(b)
}

// episodeFileRows splits an episode's library_episode_files rows into primaries
// and alternates. Counting them separately is the discriminating assertion in
// most of these tests, so it is worth not re-writing the loop each time.
func episodeFileRows(t *testing.T, ctx context.Context, libStore *library.Store, episodeID int64) (primary, alternates []library.EpisodeFile) {
	t.Helper()
	files, err := libStore.ListEpisodeFiles(ctx, episodeID)
	if err != nil {
		t.Fatalf("ListEpisodeFiles(%d): %v", episodeID, err)
	}
	for _, f := range files {
		if f.IsPrimary {
			primary = append(primary, f)
		} else {
			alternates = append(alternates, f)
		}
	}
	return primary, alternates
}

// wantOrdinaryName is the destination an ORDINARY (non-fold-in) Apply produces.
func wantOrdinaryName(seasonDir string, episodeNumber int, episodeTitle, ext string) string {
	return filepath.Join(seasonDir,
		naming.EpisodeFileName(naming.Jellyfin, altTestTitle, 1, episodeNumber, episodeTitle, ext))
}

// wantAlternateName is the destination the FOLD-IN path produces for a file
// whose probe is pr. Built from the same naming+quality calls the production
// code makes, so it pins WHICH probe drives the token suffix — swapping
// orphanMeta for primaryMeta in series_alternates.go changes the tokens and
// fails every test that uses this.
func wantAlternateName(seasonDir string, episodeNumbers []int, episodeTitle string, pr *mediainfo.Probe, ext string) string {
	return filepath.Join(seasonDir, naming.EpisodeAlternateFileName(
		naming.Jellyfin, altTestTitle, 1, episodeNumbers, episodeTitle,
		quality.ResolutionLabel(pr.Height), quality.CodecLabel(pr.CodecName), quality.BitrateLabel(pr.BitRate), ext))
}

// Probes used across these tests, named for the quality.Tier each maps to via
// quality.TierFromProbe, because the tier — not the raw numbers — is what every
// promote/demote assertion actually turns on.
var (
	probeLossless = &mediainfo.Probe{Height: 2160, CodecName: "hevc", BitRate: 40_000_000} // Lossless
	probeHigh     = &mediainfo.Probe{Height: 1080, CodecName: "hevc", BitRate: 15_000_000} // High
	probeMedium   = &mediainfo.Probe{Height: 720, CodecName: "h264", BitRate: 3_000_000}   // Medium
	probeLow      = &mediainfo.Probe{Height: 480, CodecName: "h264", BitRate: 1_000_000}   // Low
)

// ---------------------------------------------------------------------------
// §9.3 — the core three (four)
// ---------------------------------------------------------------------------

// TestApplyLibrarySeries_AlternateOrphanLoses is plan §9.3's direct counterpart
// to Movies' TestApplyLibrary_AlternateOrphanLoses (rename_alternates_test.go:26).
// A tracked S01E01 at 1080p/hevc (High) meets an incoming orphan at 480p/h264
// (Low): the orphan loses on probed tier and must land as a NON-primary
// alternate beside the primary, never replace it.
//
// The returned episodeID assertion is the one that is easy to leave out and
// worth keeping: ApplyLibrarySeries returns the FOLD-IN target's id here, not a
// freshly-minted row's, and a regression that minted a new episode row instead
// of folding in would still satisfy every file-path assertion below.
func TestApplyLibrarySeries_AlternateOrphanLoses(t *testing.T) {
	ctx := context.Background()
	f := newSeriesAltFixture(t, ctx)

	primaryPath := writeFile(t, wantOrdinaryName(f.seasonDir, 1, "", ".mkv"), "existing-primary")
	orphan := writeFile(t, filepath.Join(f.base, "incoming", "show.s01e01.480p.mkv"), "incoming-orphan")

	ep1, err := f.libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: f.series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: primaryPath, QualityTier: "high",
	})
	if err != nil {
		t.Fatalf("UpsertEpisode: %v", err)
	}

	prober := mapProber{primaryPath: probeHigh, orphan: probeLow}
	gotID, _, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(orphan, 1), naming.Jellyfin, "high", prober)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}
	if gotID != ep1.ID {
		t.Errorf("episode id = %d, want the already-tracked slot's %d — the orphan must fold in, not mint a row", gotID, ep1.ID)
	}

	row, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, 1)
	if err != nil {
		t.Fatalf("GetEpisode: %v", err)
	}
	if row.FilePath != primaryPath {
		t.Errorf("library_episodes.file_path = %q, want the untouched primary %q", row.FilePath, primaryPath)
	}
	if got := fileContent(t, row.FilePath); got != "existing-primary" {
		t.Errorf("the primary path now holds %q, want the original %q — the orphan was promoted", got, "existing-primary")
	}

	primaries, alternates := episodeFileRows(t, ctx, f.libStore, ep1.ID)
	if len(primaries) != 1 || len(alternates) != 1 {
		t.Fatalf("got %d primary + %d alternate library_episode_files rows, want exactly 1 + 1",
			len(primaries), len(alternates))
	}
	if primaries[0].FilePath != primaryPath {
		t.Errorf("primary row = %q, want %q", primaries[0].FilePath, primaryPath)
	}
	wantAlt := wantAlternateName(f.seasonDir, []int{1}, "", probeLow, ".mkv")
	if alternates[0].FilePath != wantAlt {
		t.Errorf("alternate row = %q, want the EpisodeAlternateFileName destination %q", alternates[0].FilePath, wantAlt)
	}
	if got := fileContent(t, wantAlt); got != "incoming-orphan" {
		t.Errorf("alternate destination holds %q, want the orphan's %q", got, "incoming-orphan")
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Errorf("the orphan is still at its source path %q — it was never relocated", orphan)
	}
}

// TestApplyLibrarySeries_AlternateOrphanWins is §9.3's reversed-tier case, the
// counterpart to Movies' TestApplyLibrary_AlternateOrphanWinsPromote
// (rename_alternates_test.go:95). BOTH halves of the swap must be asserted —
// the promote AND the demote — because a half-done swap is silent: promoting
// without demoting leaves two rows flagged primary (or the partial unique index
// rejects the write), and demoting without promoting strands the winner.
//
// Content, not path, identifies the winner. The promoted orphan lands on the
// SAME path string the old primary occupied (both are EpisodeFileName's
// destination), so `row.FilePath == primaryDest` is true before and after and
// proves nothing on its own.
func TestApplyLibrarySeries_AlternateOrphanWins(t *testing.T) {
	ctx := context.Background()
	f := newSeriesAltFixture(t, ctx)

	primaryDest := wantOrdinaryName(f.seasonDir, 1, "", ".mkv")
	writeFile(t, primaryDest, "old-primary")
	orphan := writeFile(t, filepath.Join(f.base, "incoming", "show.s01e01.2160p.mkv"), "better-orphan")

	ep1, err := f.libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: f.series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: primaryDest, QualityTier: "low",
	})
	if err != nil {
		t.Fatalf("UpsertEpisode: %v", err)
	}

	prober := mapProber{primaryDest: probeLow, orphan: probeLossless}
	gotID, _, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(orphan, 1), naming.Jellyfin, "low", prober)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}
	if gotID != ep1.ID {
		t.Errorf("episode id = %d, want the already-tracked slot's %d", gotID, ep1.ID)
	}

	row, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, 1)
	if err != nil {
		t.Fatalf("GetEpisode: %v", err)
	}
	if row.FilePath != primaryDest {
		t.Errorf("library_episodes.file_path = %q, want the primary destination %q", row.FilePath, primaryDest)
	}
	if got := fileContent(t, row.FilePath); got != "better-orphan" {
		t.Errorf("the denormalized primary holds %q, want the promoted orphan's %q", got, "better-orphan")
	}

	primaries, alternates := episodeFileRows(t, ctx, f.libStore, ep1.ID)
	if len(primaries) != 1 || len(alternates) != 1 {
		t.Fatalf("got %d primary + %d alternate rows, want exactly 1 + 1 — a half-done promote/demote swap",
			len(primaries), len(alternates))
	}
	if primaries[0].FilePath != primaryDest {
		t.Errorf("primary row = %q, want the promoted orphan at %q", primaries[0].FilePath, primaryDest)
	}
	wantDemoted := wantAlternateName(f.seasonDir, []int{1}, "", probeLow, ".mkv")
	if alternates[0].FilePath != wantDemoted {
		t.Errorf("demoted row = %q, want the previous primary at its alternate name %q", alternates[0].FilePath, wantDemoted)
	}
	if got := fileContent(t, wantDemoted); got != "old-primary" {
		t.Errorf("demoted destination holds %q, want the previous primary's %q", got, "old-primary")
	}
}

// TestApplyLibrarySeries_AlternateEqualTierKeepsExistingPrimary pins §9.3's
// equal-tier policy: series_alternates.go's comparison is `>` and must never
// become `>=`. Both files probe IDENTICALLY, so the ONLY thing deciding the
// outcome is that operator.
//
// The content assertions are what make this non-vacuous. With `>=` the incoming
// file is promoted onto the very same destination path the existing primary
// already occupies, so every path-shaped assertion still passes — only the bytes
// at that path change.
func TestApplyLibrarySeries_AlternateEqualTierKeepsExistingPrimary(t *testing.T) {
	ctx := context.Background()
	f := newSeriesAltFixture(t, ctx)

	primaryDest := wantOrdinaryName(f.seasonDir, 1, "", ".mkv")
	writeFile(t, primaryDest, "existing-primary")
	orphan := writeFile(t, filepath.Join(f.base, "incoming", "same-tier.mkv"), "incoming-tie")

	ep1, err := f.libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: f.series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: primaryDest, QualityTier: "medium",
	})
	if err != nil {
		t.Fatalf("UpsertEpisode: %v", err)
	}

	same := &mediainfo.Probe{Height: 1080, CodecName: "h264", BitRate: 5_000_000} // Medium, both files
	prober := mapProber{primaryDest: same, orphan: same}
	if _, _, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(orphan, 1), naming.Jellyfin, "medium", prober); err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}

	row, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, 1)
	if err != nil {
		t.Fatalf("GetEpisode: %v", err)
	}
	if row.FilePath != primaryDest {
		t.Errorf("library_episodes.file_path = %q, want %q", row.FilePath, primaryDest)
	}
	if got := fileContent(t, row.FilePath); got != "existing-primary" {
		t.Errorf("equal tier promoted the incoming file: primary holds %q, want %q — the comparison must be > and not >=",
			got, "existing-primary")
	}

	primaries, alternates := episodeFileRows(t, ctx, f.libStore, ep1.ID)
	if len(primaries) != 1 || len(alternates) != 1 {
		t.Fatalf("got %d primary + %d alternate rows, want exactly 1 + 1", len(primaries), len(alternates))
	}
	if primaries[0].FilePath != primaryDest {
		t.Errorf("primary row = %q, want the kept existing primary %q", primaries[0].FilePath, primaryDest)
	}
	wantAlt := wantAlternateName(f.seasonDir, []int{1}, "", same, ".mkv")
	if alternates[0].FilePath != wantAlt {
		t.Errorf("alternate row = %q, want %q", alternates[0].FilePath, wantAlt)
	}
	if got := fileContent(t, wantAlt); got != "incoming-tie" {
		t.Errorf("alternate holds %q, want the incoming file's %q", got, "incoming-tie")
	}
}

// TestApplyLibrarySeries_ApplyOrderIndependent is plan §0.1's PROPERTY, and the
// test that proves the softened Scan-time guards from US-004/US-005 are safe.
//
// Softening those guards means BOTH same-slot files can be Pending at once, so
// the operator decides the Apply order — and nothing in Scan can predict it. The
// property that makes that acceptable is that Apply converges: whichever order
// runs, the higher-tier file ends up primary. This test runs the identical
// assertion set (assertHighTierWon) after both orderings.
//
// Two seeding details are load-bearing:
//
//   - NEITHER file is tracked at the start. The FIRST Apply therefore takes the
//     ORDINARY path (mint the row, relocate to EpisodeFileName) and the SECOND
//     takes the fold-in path. That asymmetry is the whole point — order
//     independence is a property OF the two paths together, not of either alone.
//
//   - The loser is 720p/Medium, deliberately NOT 480p. mapProber's unmapped
//     fallback is 480p/h264 (rename_alternates_test.go:23) == Low. With a Low
//     loser, a subtest that forgot to map the relocated primary's new path would
//     compare Low against Low, the orphan would lose for the WRONG reason, and
//     the "high tier wins" subtest would pass vacuously. Medium-vs-fallback-Low
//     inverts the outcome instead, so the mistake fails loudly.
func TestApplyLibrarySeries_ApplyOrderIndependent(t *testing.T) {
	// assertHighTierWon is deliberately shared by both subtests: order
	// independence means the SAME assertion holds either way, so running two
	// hand-written near-copies would weaken exactly the claim being made.
	assertHighTierWon := func(t *testing.T, ctx context.Context, f seriesAltFixture, primaryDest string) {
		t.Helper()
		row, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, 1)
		if err != nil {
			t.Fatalf("GetEpisode: %v", err)
		}
		if row.FilePath != primaryDest {
			t.Errorf("library_episodes.file_path = %q, want the primary destination %q", row.FilePath, primaryDest)
		}
		if got := fileContent(t, row.FilePath); got != "high-tier" {
			t.Errorf("primary holds %q, want %q — the winner depends on Apply ORDER, which §0.1 forbids", got, "high-tier")
		}
		primaries, alternates := episodeFileRows(t, ctx, f.libStore, row.ID)
		if len(primaries) != 1 || len(alternates) != 1 {
			t.Fatalf("got %d primary + %d alternate rows, want exactly 1 + 1", len(primaries), len(alternates))
		}
		if primaries[0].FilePath != primaryDest {
			t.Errorf("primary row = %q, want %q", primaries[0].FilePath, primaryDest)
		}
		if got := fileContent(t, alternates[0].FilePath); got != "medium-tier" {
			t.Errorf("alternate holds %q, want the loser's %q", got, "medium-tier")
		}
	}

	for _, tc := range []struct {
		name string
		// first is the content of the file applied FIRST; it is the one that
		// takes the ordinary path and therefore the one whose probe must be
		// mapped onto the relocated primary destination.
		firstIsHigh bool
	}{
		{name: "high tier applied first", firstIsHigh: true},
		{name: "high tier applied second", firstIsHigh: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newSeriesAltFixture(t, ctx)

			high := writeFile(t, filepath.Join(f.base, "incoming", "show.s01e01.2160p.mkv"), "high-tier")
			medium := writeFile(t, filepath.Join(f.base, "incoming", "show.s01e01.720p.mkv"), "medium-tier")
			primaryDest := wantOrdinaryName(f.seasonDir, 1, "", ".mkv")

			// The first-applied file is relocated ONTO primaryDest by the
			// ordinary path, so the second Apply probes it under its NEW name.
			// mapProber is path-keyed, so that new name needs its own entry —
			// which probe belongs there is exactly what differs per ordering.
			first, second := high, medium
			relocatedProbe := probeLossless
			if !tc.firstIsHigh {
				first, second = medium, high
				relocatedProbe = probeMedium
			}
			prober := mapProber{high: probeLossless, medium: probeMedium, primaryDest: relocatedProbe}

			if _, _, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(first, 1), naming.Jellyfin, "medium", prober); err != nil {
				t.Fatalf("first ApplyLibrarySeries: %v", err)
			}
			if _, _, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(second, 1), naming.Jellyfin, "medium", prober); err != nil {
				t.Fatalf("second ApplyLibrarySeries: %v", err)
			}
			assertHighTierWon(t, ctx, f, primaryDest)
		})
	}
}

// ---------------------------------------------------------------------------
// §9.4 — gate and edge-case tests
// ---------------------------------------------------------------------------

// TestApplyLibrarySeries_FilelessCatalogRowIsNotAnAlternate is plan §0.3's
// guard, and §9.4 calls it THE SINGLE HIGHEST-VALUE TEST IN THE PLAN.
//
// library_episodes legitimately carries FILELESS rows (UpsertEpisodeCatalog
// writes an EMPTY file_path) so that "which episodes am I missing?" is a plain
// query. ApplyLibrarySeries' fold-in gate therefore keys on a NON-EMPTY
// FilePath, not on row existence: gate on existence and EVERY ordinary Series
// Apply for any catalog-synced show — i.e. essentially every real library —
// routes through the alternate path instead.
//
// THE ASSERTION IS BYTE-EQUALITY ON THE DESTINATION, not the absence of an
// "- alternate" suffix. EpisodeAlternateFileName only emits that literal when
// all three quality tokens are empty; with a real probe it emits
// " - 480p H264 …" instead, so a substring check for "- alternate" passes under
// the very bug this test exists to catch.
//
// Title/AirDate are asserted too: the catalog row's metadata must survive the
// Apply (resolveEpisodeMeta reads it, the ordinary path preserves it), and it
// is also what makes the expected filename carry "Pilot".
func TestApplyLibrarySeries_FilelessCatalogRowIsNotAnAlternate(t *testing.T) {
	ctx := context.Background()
	f := newSeriesAltFixture(t, ctx)

	if err := f.libStore.UpsertEpisodeCatalog(ctx, f.series.ID, 1, 1, "Pilot", "2020-01-01"); err != nil {
		t.Fatalf("UpsertEpisodeCatalog: %v", err)
	}
	// The catalog row must be fileless, or this test is seeded wrong and would
	// pass for the wrong reason.
	seeded, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, 1)
	if err != nil {
		t.Fatalf("GetEpisode after UpsertEpisodeCatalog: %v", err)
	}
	if seeded.FilePath != "" {
		t.Fatalf("seeded catalog row has file_path %q, want empty — UpsertEpisodeCatalog must never set one", seeded.FilePath)
	}

	orphan := writeFile(t, filepath.Join(f.base, "incoming", "show.s01e01.mkv"), "the-only-copy")
	prober := mapProber{orphan: probeLow}

	gotID, _, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(orphan, 1), naming.Jellyfin, "low", prober)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}
	if gotID != seeded.ID {
		t.Errorf("episode id = %d, want the existing catalog row's %d", gotID, seeded.ID)
	}

	want := wantOrdinaryName(f.seasonDir, 1, "Pilot", ".mkv")
	row, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, 1)
	if err != nil {
		t.Fatalf("GetEpisode: %v", err)
	}
	if row.FilePath != want {
		t.Errorf("library_episodes.file_path = %q, want the ORDINARY EpisodeFileName destination %q — a fileless catalog row is not an occupied slot",
			row.FilePath, want)
	}
	if got := fileContent(t, want); got != "the-only-copy" {
		t.Errorf("ordinary destination holds %q, want %q", got, "the-only-copy")
	}
	if row.Title != "Pilot" || row.AirDate != "2020-01-01" {
		t.Errorf("catalog metadata was clobbered: title=%q air_date=%q, want %q/%q",
			row.Title, row.AirDate, "Pilot", "2020-01-01")
	}

	primaries, alternates := episodeFileRows(t, ctx, f.libStore, row.ID)
	if len(primaries) != 1 || len(alternates) != 0 {
		t.Fatalf("got %d primary + %d alternate rows, want exactly 1 + 0 — the fold-in path wrote a non-primary row",
			len(primaries), len(alternates))
	}
	if primaries[0].FilePath != want {
		t.Errorf("primary row = %q, want %q", primaries[0].FilePath, want)
	}
}

// TestApplyLibrarySeries_StaleRowPointingAtDeletedFile covers §9.4's stale-row
// case: library_episodes still names a file someone deleted out from under SAK.
// ApplyLibrarySeries' gate calls fileExists (series_alternates.go:301) and falls
// THROUGH to the ordinary path, so the incoming file simply becomes the new
// primary rather than folding in as an alternate to a file that is not there.
//
// Without the fileExists check the fold-in path runs with a primaryPath that
// cannot be stat'd or renamed, which is a hard error rather than a wrong-looking
// filename — so the t.Fatalf on err is a real assertion here, not boilerplate.
//
// DO NOT assert a single library_episode_files row. SyncPrimaryEpisodeFile
// DEMOTES the stale row rather than deleting it (its doc comment states that
// scope explicitly, and .omc/plans/autopilot-impl.md §11.11 records it as an
// accepted gap Movies shares), so the stale path legitimately survives as a
// non-primary row. What must be true is that the PRIMARY row names the new file.
func TestApplyLibrarySeries_StaleRowPointingAtDeletedFile(t *testing.T) {
	ctx := context.Background()
	f := newSeriesAltFixture(t, ctx)

	// Deliberately NOT the canonical EpisodeFileName destination — if the stale
	// path and the ordinary destination were the same string, "the row now names
	// the new file" would be unfalsifiable.
	stale := writeFile(t, filepath.Join(f.seasonDir, "Show Name S01E01 old-copy.mkv"), "deleted-later")
	ep1, err := f.libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: f.series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: stale, QualityTier: "high",
	})
	if err != nil {
		t.Fatalf("UpsertEpisode: %v", err)
	}
	if err := os.Remove(stale); err != nil {
		t.Fatalf("removing the seeded file to make the row stale: %v", err)
	}

	orphan := writeFile(t, filepath.Join(f.base, "incoming", "show.s01e01.mkv"), "replacement")
	// Low tier on purpose: if the gate wrongly treated the stale row as
	// occupied, a Low orphan would LOSE the tier comparison and land as an
	// alternate — the loudest possible wrong answer for this fixture.
	prober := mapProber{orphan: probeLow}

	gotID, _, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(orphan, 1), naming.Jellyfin, "low", prober)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}
	if gotID != ep1.ID {
		t.Errorf("episode id = %d, want the existing (stale) row's %d", gotID, ep1.ID)
	}

	want := wantOrdinaryName(f.seasonDir, 1, "", ".mkv")
	row, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, 1)
	if err != nil {
		t.Fatalf("GetEpisode: %v", err)
	}
	if row.FilePath != want {
		t.Errorf("library_episodes.file_path = %q, want the ORDINARY destination %q — a row naming a deleted file is not an occupied slot",
			row.FilePath, want)
	}
	if got := fileContent(t, want); got != "replacement" {
		t.Errorf("ordinary destination holds %q, want %q", got, "replacement")
	}
	primary, err := f.libStore.PrimaryEpisodeFile(ctx, ep1.ID)
	if err != nil {
		t.Fatalf("PrimaryEpisodeFile: %v", err)
	}
	if primary.FilePath != want {
		t.Errorf("primary library_episode_files row = %q, want %q", primary.FilePath, want)
	}
}

// TestApplyLibrarySeries_ReApplySameFileIsNotItsOwnAlternate covers §9.4's
// self-collision case: existing.FilePath == p.SourcePath, i.e. re-applying a
// proposal for a file already sitting at its correct destination. Without the
// gate's `existing.FilePath == p.SourcePath` skip the file would fold in as its
// OWN alternate — moveUnique would rename it to a "- alternate" name and mint a
// second library_episode_files row for one physical file.
//
// The seeded path is built from the SAME naming calls RelocateEpisodeRange uses
// so it is byte-exact; one character off and the relocate takes place.UniquePath's
// "(1)" branch and the no-move assertion fails for an unrelated reason.
func TestApplyLibrarySeries_ReApplySameFileIsNotItsOwnAlternate(t *testing.T) {
	ctx := context.Background()
	f := newSeriesAltFixture(t, ctx)

	canonical := writeFile(t, wantOrdinaryName(f.seasonDir, 1, "", ".mkv"), "already-placed")
	ep1, err := f.libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: f.series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: canonical, QualityTier: "high",
	})
	if err != nil {
		t.Fatalf("UpsertEpisode: %v", err)
	}

	prober := mapProber{canonical: probeHigh}
	gotID, changes, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(canonical, 1), naming.Jellyfin, "high", prober)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}
	if gotID != ep1.ID {
		t.Errorf("episode id = %d, want %d", gotID, ep1.ID)
	}
	if len(changes) != 0 {
		t.Errorf("got %d path changes (%+v), want none — nothing moved", len(changes), changes)
	}

	row, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, 1)
	if err != nil {
		t.Fatalf("GetEpisode: %v", err)
	}
	if row.FilePath != canonical {
		t.Errorf("library_episodes.file_path = %q, want the unchanged %q", row.FilePath, canonical)
	}
	if got := fileContent(t, canonical); got != "already-placed" {
		t.Errorf("the canonical path holds %q, want %q", got, "already-placed")
	}

	// One physical file, so exactly one row and exactly one directory entry —
	// a self-fold-in would produce a second of each.
	primaries, alternates := episodeFileRows(t, ctx, f.libStore, ep1.ID)
	if len(primaries) != 1 || len(alternates) != 0 {
		t.Fatalf("got %d primary + %d alternate rows, want exactly 1 + 0 — the file folded in as its own alternate",
			len(primaries), len(alternates))
	}
	if primaries[0].FilePath != canonical {
		t.Errorf("primary row = %q, want %q", primaries[0].FilePath, canonical)
	}
	entries, err := os.ReadDir(f.seasonDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(canonical) {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("season folder holds %v, want only %q", names, filepath.Base(canonical))
	}
}

// TestApplyLibrarySeries_RangeAlternatePartiallyCatalogued is §6.3's SKIP rule.
// A S01E01-E02 range proposal arrives where E2 is occupied and E1 has NO
// library_episodes row at all — not even a fileless catalog one.
//
// The write loop must SKIP E1 rather than mint a row for it: minting one would
// fabricate an episode whose only file is a non-primary alternate — a slot with
// an alternate and no primary — for an episode nothing has ever tracked. So a
// range alternate writes between 1 and N rows, not always N.
//
// This is deliberately NOT a duplicate of its two siblings, and the row COUNT is
// what separates them: RangeAlternateNeverPromotes seeds both slots occupied and
// gets TWO alternate rows; RangeGateFiresOnNonPrimaryOccupiedSlot seeds the free
// slot with a FILELESS row and also gets TWO; this one leaves E1 with no row and
// gets exactly ONE. Assert the count, or the three collapse into one test.
func TestApplyLibrarySeries_RangeAlternatePartiallyCatalogued(t *testing.T) {
	ctx := context.Background()
	f := newSeriesAltFixture(t, ctx)

	e2Path := writeFile(t, filepath.Join(f.seasonDir, "Show Name S01E02.mkv"), "e2-primary")
	ep2, err := f.libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: f.series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: e2Path, QualityTier: "low",
	})
	if err != nil {
		t.Fatalf("UpsertEpisode E2: %v", err)
	}

	orphan := writeFile(t, filepath.Join(f.base, "incoming", "show.s01e01-e02.2160p.mkv"), "range-orphan")
	prober := mapProber{e2Path: probeLow, orphan: probeLossless}

	gotID, _, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(orphan, 1, 2), naming.Jellyfin, "low", prober)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}
	if gotID != ep2.ID {
		t.Errorf("episode id = %d, want the only occupied slot's %d", gotID, ep2.ID)
	}

	// E1 must still have no row at all — never minted.
	if _, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, 1); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("GetEpisode S01E01 err = %v, want library.ErrNotFound — a row was minted for an episode nothing tracks", err)
	}

	// E2 keeps its primary (a range proposal never promotes) and gains exactly
	// one alternate row.
	e2Row, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, 2)
	if err != nil {
		t.Fatalf("GetEpisode S01E02: %v", err)
	}
	if e2Row.FilePath != e2Path {
		t.Errorf("S01E02 file_path = %q, want the untouched %q", e2Row.FilePath, e2Path)
	}
	if got := fileContent(t, e2Path); got != "e2-primary" {
		t.Errorf("E2's primary holds %q, want %q", got, "e2-primary")
	}
	primaries, alternates := episodeFileRows(t, ctx, f.libStore, ep2.ID)
	if len(primaries) != 1 || len(alternates) != 1 {
		t.Fatalf("E2 has %d primary + %d alternate rows, want exactly 1 + 1", len(primaries), len(alternates))
	}
	wantAlt := wantAlternateName(f.seasonDir, []int{1, 2}, "", probeLossless, ".mkv")
	if alternates[0].FilePath != wantAlt {
		t.Errorf("alternate row = %q, want %q", alternates[0].FilePath, wantAlt)
	}
	if got := fileContent(t, wantAlt); got != "range-orphan" {
		t.Errorf("alternate destination holds %q, want %q", got, "range-orphan")
	}
}

// TestApplyLibrarySeries_RangeGateFiresOnNonPrimaryOccupiedSlot is §7.2's
// WIDENED GATE: ApplyLibrarySeries checks EVERY number in the range for an
// occupied slot, not just p.EpisodeNumber.
//
// WITHOUT the widened gate this test fails SILENTLY, which is why it exists.
// With a primary-only gate and E1 free, `occupied` stays nil, the ordinary path
// runs, and UpsertEpisodes' ON CONFLICT(series_id, season_number, episode_number)
// repoints E2's file_path at the incoming file — stranding E2's real file on
// disk with no row naming it, no error and no log line. There is nothing to
// assert on except E2's file_path and E2's file still being where it was.
//
// The free slot carries a FILELESS catalog row (not "no row at all") on purpose:
// that is what keeps this complementary to RangeAlternatePartiallyCatalogued,
// which deliberately leaves its free slot rowless. The difference is observable
// in the alternate ROW COUNT — two here, one there.
//
// The inverse ordering runs too, so the gate loop cannot be accidentally written
// to inspect only the last number of the range.
func TestApplyLibrarySeries_RangeGateFiresOnNonPrimaryOccupiedSlot(t *testing.T) {
	for _, tc := range []struct {
		name         string
		occupiedEp   int
		filelessEp   int
		occupiedName string
	}{
		{name: "E1 fileless, E2 occupied", occupiedEp: 2, filelessEp: 1, occupiedName: "Show Name S01E02.mkv"},
		{name: "E1 occupied, E2 fileless", occupiedEp: 1, filelessEp: 2, occupiedName: "Show Name S01E01.mkv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newSeriesAltFixture(t, ctx)

			occupiedPath := writeFile(t, filepath.Join(f.seasonDir, tc.occupiedName), "occupied-primary")
			occupiedRow, err := f.libStore.UpsertEpisode(ctx, library.Episode{
				SeriesID: f.series.ID, SeasonNumber: 1, EpisodeNumber: tc.occupiedEp,
				FilePath: occupiedPath, QualityTier: "low",
			})
			if err != nil {
				t.Fatalf("UpsertEpisode E%d: %v", tc.occupiedEp, err)
			}
			if err := f.libStore.UpsertEpisodeCatalog(ctx, f.series.ID, 1, tc.filelessEp, "", ""); err != nil {
				t.Fatalf("UpsertEpisodeCatalog E%d: %v", tc.filelessEp, err)
			}

			orphan := writeFile(t, filepath.Join(f.base, "incoming", "show.s01e01-e02.2160p.mkv"), "range-orphan")
			prober := mapProber{occupiedPath: probeLow, orphan: probeLossless}

			gotID, _, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(orphan, 1, 2), naming.Jellyfin, "low", prober)
			if err != nil {
				t.Fatalf("ApplyLibrarySeries: %v", err)
			}
			if gotID != occupiedRow.ID {
				t.Errorf("episode id = %d, want the occupied slot's %d — the fold-in gate never fired", gotID, occupiedRow.ID)
			}

			// THE assertion: the occupied slot survives untouched, in the DB and
			// on disk. A primary-only gate overwrites both silently.
			after, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, tc.occupiedEp)
			if err != nil {
				t.Fatalf("GetEpisode E%d: %v", tc.occupiedEp, err)
			}
			if after.FilePath != occupiedPath {
				t.Errorf("S01E%02d file_path = %q, want the untouched %q — the ordinary path overwrote an occupied slot",
					tc.occupiedEp, after.FilePath, occupiedPath)
			}
			if got := fileContent(t, occupiedPath); got != "occupied-primary" {
				t.Errorf("the occupied slot's file holds %q, want %q", got, "occupied-primary")
			}

			// The fold-in path ran: the incoming file is at its alternate name.
			wantAlt := wantAlternateName(f.seasonDir, []int{1, 2}, "", probeLossless, ".mkv")
			if got := fileContent(t, wantAlt); got != "range-orphan" {
				t.Errorf("alternate destination %q holds %q, want %q", wantAlt, got, "range-orphan")
			}

			// One alternate row per bundled slot — BOTH have a library_episodes
			// row here (one occupied, one fileless), so the complete resolved
			// map must produce two.
			occupiedPrimaries, occupiedAlts := episodeFileRows(t, ctx, f.libStore, occupiedRow.ID)
			if len(occupiedPrimaries) != 1 || len(occupiedAlts) != 1 {
				t.Fatalf("occupied slot has %d primary + %d alternate rows, want exactly 1 + 1",
					len(occupiedPrimaries), len(occupiedAlts))
			}
			if occupiedAlts[0].FilePath != wantAlt {
				t.Errorf("occupied slot's alternate row = %q, want %q", occupiedAlts[0].FilePath, wantAlt)
			}
			filelessRow, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, tc.filelessEp)
			if err != nil {
				t.Fatalf("GetEpisode E%d: %v", tc.filelessEp, err)
			}
			if filelessRow.FilePath != "" {
				t.Errorf("the fileless slot gained file_path %q, want it to stay empty — an alternate is not a primary",
					filelessRow.FilePath)
			}
			filelessPrimaries, filelessAlts := episodeFileRows(t, ctx, f.libStore, filelessRow.ID)
			if len(filelessPrimaries) != 0 || len(filelessAlts) != 1 {
				t.Fatalf("fileless slot has %d primary + %d alternate rows, want exactly 0 + 1 — §7.2's complete map owes it one",
					len(filelessPrimaries), len(filelessAlts))
			}
			if filelessAlts[0].FilePath != wantAlt {
				t.Errorf("fileless slot's alternate row = %q, want %q", filelessAlts[0].FilePath, wantAlt)
			}
		})
	}
}

// TestApplyLibrarySeries_SharedPrimaryIsNeverDemoted is §6.2 step 3a — the
// mirror hazard Movies has no analogue for, so mirroring applyLibraryAlternate
// cannot catch it.
//
// The tracked primary is a BUNDLED S01E01-E02 file: two library_episodes rows,
// ONE path. A higher-tier SINGLE-episode file for E01 arrives, so neither the
// len(episodeNumbers) > 1 never-promote rule (the incoming proposal is single)
// nor the tier comparison (the incoming file genuinely wins) refuses the
// promotion — only CountEpisodesByFilePath's shared-path check does.
//
// Without step 3a this fails SILENTLY: the promote branch moveUniques the
// bundled file to an alternate name and then calls UpdateEpisodePrimaryPath for
// E01's row ONLY, leaving E02's file_path naming a path that no longer exists.
// Hence the os.Stat on E02's recorded path — a string-equality check alone would
// still pass, because E02's row is never written in that scenario.
func TestApplyLibrarySeries_SharedPrimaryIsNeverDemoted(t *testing.T) {
	ctx := context.Background()
	f := newSeriesAltFixture(t, ctx)

	bundled := writeFile(t, filepath.Join(f.seasonDir, "Show Name S01E01-E02.mkv"), "bundled-primary")
	seeded, err := f.libStore.UpsertEpisodes(ctx, []library.Episode{
		{SeriesID: f.series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: bundled, QualityTier: "low"},
		{SeriesID: f.series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: bundled, QualityTier: "low"},
	})
	if err != nil {
		t.Fatalf("UpsertEpisodes: %v", err)
	}
	ep1, ep2 := seeded[0], seeded[1]

	orphan := writeFile(t, filepath.Join(f.base, "incoming", "show.s01e01.2160p.mkv"), "higher-tier-single")
	prober := mapProber{bundled: probeLow, orphan: probeLossless}

	gotID, _, err := ApplyLibrarySeries(ctx, f.libStore, nil, nil, f.proposal(orphan, 1), naming.Jellyfin, "low", prober)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}
	if gotID != ep1.ID {
		t.Errorf("episode id = %d, want E01's %d", gotID, ep1.ID)
	}

	// The bundled file did not move, and BOTH rows still resolve to it.
	if got := fileContent(t, bundled); got != "bundled-primary" {
		t.Errorf("the bundled path holds %q, want %q — it was demoted despite being shared", got, "bundled-primary")
	}
	for _, want := range []struct {
		n  int
		id int64
	}{{1, ep1.ID}, {2, ep2.ID}} {
		row, err := f.libStore.GetEpisode(ctx, f.series.ID, 1, want.n)
		if err != nil {
			t.Fatalf("GetEpisode S01E%02d: %v", want.n, err)
		}
		if row.FilePath != bundled {
			t.Errorf("S01E%02d file_path = %q, want the shared bundled path %q", want.n, row.FilePath, bundled)
		}
		if _, err := os.Stat(row.FilePath); err != nil {
			t.Errorf("S01E%02d's recorded file_path %q does not exist (%v) — the bundled primary was stranded",
				want.n, row.FilePath, err)
		}
	}

	// The higher-tier arrival still landed, as E01's alternate.
	wantAlt := wantAlternateName(f.seasonDir, []int{1}, "", probeLossless, ".mkv")
	primaries, alternates := episodeFileRows(t, ctx, f.libStore, ep1.ID)
	if len(primaries) != 1 || len(alternates) != 1 {
		t.Fatalf("E01 has %d primary + %d alternate rows, want exactly 1 + 1", len(primaries), len(alternates))
	}
	if primaries[0].FilePath != bundled {
		t.Errorf("E01 primary row = %q, want the bundled file %q", primaries[0].FilePath, bundled)
	}
	if alternates[0].FilePath != wantAlt {
		t.Errorf("E01 alternate row = %q, want %q", alternates[0].FilePath, wantAlt)
	}
	if got := fileContent(t, wantAlt); got != "higher-tier-single" {
		t.Errorf("alternate destination holds %q, want %q", got, "higher-tier-single")
	}
	// E02 is not in the incoming proposal's range, so it gains no alternate row.
	_, e2Alts := episodeFileRows(t, ctx, f.libStore, ep2.ID)
	if len(e2Alts) != 0 {
		t.Errorf("E02 gained %d alternate rows, want 0 — a single-episode proposal writes rows only for its own slot", len(e2Alts))
	}
}
