package rename

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
)

type mapProber map[string]*mediainfo.Probe

func (m mapProber) Probe(_ context.Context, path string) (*mediainfo.Probe, error) {
	if p, ok := m[path]; ok {
		return p, nil
	}
	return &mediainfo.Probe{Height: 480, CodecName: "h264", BitRate: 1_000_000}, nil
}

func TestApplyLibrary_AlternateOrphanLoses(t *testing.T) {
	root := t.TempDir()
	libDir := filepath.Join(root, "A Beautiful Mind (2001) [tmdbid-453]")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(libDir, "A Beautiful Mind (2001) [tmdbid-453].mkv")
	if err := os.WriteFile(primary, []byte("primary"), 0o644); err != nil {
		t.Fatal(err)
	}
	orphanDir := filepath.Join(root, "orphan")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(orphanDir, "mind-alt.mkv")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore := newTestLibraryStore(t)
	item, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 453, Title: "A Beautiful Mind", Year: 2001,
		FilePath: primary, RootFolderPath: root, QualityTier: "high",
	})
	if err != nil {
		t.Fatal(err)
	}

	prober := mapProber{
		primary: {Height: 1080, CodecName: "hevc", BitRate: 15_000_000},
		orphan:  {Height: 480, CodecName: "h264", BitRate: 1_000_000},
	}
	p := proposals.Proposal{
		Status: proposals.Pending, Title: "A Beautiful Mind", Year: 2001, TMDBID: 453,
		SourcePath: orphan, RootFolderPath: root,
	}
	id, _, err := ApplyLibrary(context.Background(), libStore, p, naming.Jellyfin, "medium", prober)
	if err != nil {
		t.Fatalf("ApplyLibrary: %v", err)
	}
	if id != item.ID {
		t.Fatalf("expected same item id %d, got %d", item.ID, id)
	}
	files, err := libStore.ListFiles(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	var primaryCount, altCount int
	for _, f := range files {
		if f.IsPrimary {
			primaryCount++
			if f.FilePath != primary {
				t.Errorf("primary path changed unexpectedly: %s", f.FilePath)
			}
		} else {
			altCount++
			if !strings.Contains(filepath.Base(f.FilePath), " - ") {
				t.Errorf("alternate name missing quality tokens: %s", f.FilePath)
			}
		}
	}
	if primaryCount != 1 || altCount != 1 {
		t.Fatalf("expected 1 primary + 1 alt, got primary=%d alt=%d", primaryCount, altCount)
	}
}

func TestApplyLibrary_AlternateOrphanWinsPromote(t *testing.T) {
	root := t.TempDir()
	libDir := filepath.Join(root, "A Beautiful Mind (2001) [tmdbid-453]")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(libDir, "A Beautiful Mind (2001) [tmdbid-453].mkv")
	if err := os.WriteFile(primary, []byte("old-primary"), 0o644); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "better.mkv")
	if err := os.WriteFile(orphan, []byte("better"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore := newTestLibraryStore(t)
	item, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 453, Title: "A Beautiful Mind", Year: 2001,
		FilePath: primary, RootFolderPath: root, QualityTier: "low",
	})
	if err != nil {
		t.Fatal(err)
	}

	prober := mapProber{
		primary: {Height: 480, CodecName: "h264", BitRate: 800_000},
		orphan:  {Height: 2160, CodecName: "hevc", BitRate: 40_000_000},
	}
	p := proposals.Proposal{
		Status: proposals.Pending, Title: "A Beautiful Mind", Year: 2001, TMDBID: 453,
		SourcePath: orphan, RootFolderPath: root,
	}
	_, _, err = ApplyLibrary(context.Background(), libStore, p, naming.Jellyfin, "low", prober)
	if err != nil {
		t.Fatalf("ApplyLibrary: %v", err)
	}
	got, err := libStore.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantPrimary := filepath.Join(libDir, "A Beautiful Mind (2001) [tmdbid-453].mkv")
	if got.FilePath != wantPrimary {
		t.Errorf("denormalized primary = %q, want %q", got.FilePath, wantPrimary)
	}
	files, err := libStore.ListFiles(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %+v", files)
	}
	for _, f := range files {
		if f.IsPrimary && f.FilePath != wantPrimary {
			t.Errorf("primary file path = %q", f.FilePath)
		}
		if !f.IsPrimary && !strings.Contains(filepath.Base(f.FilePath), " - ") {
			t.Errorf("demoted primary should be alternate-named, got %s", f.FilePath)
		}
	}
}

func TestApplyLibrary_EqualTierKeepExistingPrimary(t *testing.T) {
	root := t.TempDir()
	libDir := filepath.Join(root, "Film (2000) [tmdbid-1]")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(libDir, "Film (2000) [tmdbid-1].mkv")
	if err := os.WriteFile(primary, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "b.mkv")
	if err := os.WriteFile(orphan, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Film", Year: 2000,
		FilePath: primary, RootFolderPath: root, QualityTier: "medium",
	}); err != nil {
		t.Fatal(err)
	}
	same := &mediainfo.Probe{Height: 1080, CodecName: "h264", BitRate: 5_000_000}
	prober := mapProber{primary: same, orphan: same}
	p := proposals.Proposal{
		Status: proposals.Pending, Title: "Film", Year: 2000, TMDBID: 1,
		SourcePath: orphan, RootFolderPath: root,
	}
	_, _, err := ApplyLibrary(context.Background(), libStore, p, naming.Jellyfin, "medium", prober)
	if err != nil {
		t.Fatal(err)
	}
	items, err := libStore.List(context.Background(), mode.Movies)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %v len=%d", err, len(items))
	}
	files, err := libStore.ListFiles(context.Background(), items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.IsPrimary && f.FilePath != primary {
			t.Fatalf("equal tier must keep existing primary, got %s", f.FilePath)
		}
	}
}
