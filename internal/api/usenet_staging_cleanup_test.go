package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/usenet"
)

func TestClearOwnedUsenetStaging_RemovesOwnedOnly(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "nzb-aaaaaaaaaaaaaaaa")
	foreign := filepath.Join(root, "other-client")
	for _, d := range []string{owned, foreign} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "x.mkv"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(owned, usenet.OwnedMarkerFile), []byte("sakms\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	nzb := usenet.New(usenet.Config{StagingDir: root})
	clearOwnedUsenetStaging(nzb, "nzb-aaaaaaaaaaaaaaaa")
	clearOwnedUsenetStaging(nzb, "other-client")

	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned staging should be gone, err=%v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign staging must survive: %v", err)
	}
}

func TestUsenetCompleteImporter_ClearsOwnedStagingAfterImport(t *testing.T) {
	ctx := context.Background()
	connStore, _, settingsStore, grabsStore, libStore, _, _, _, _, _ := testStores(t)

	staging := t.TempDir()
	moviesRoot := t.TempDir()
	nzb := usenet.New(usenet.Config{StagingDir: staging})
	gid := "nzb-bbbbbbbbbbbbbbbb"
	gidDir := filepath.Join(staging, gid)
	if err := os.MkdirAll(gidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gidDir, usenet.OwnedMarkerFile), []byte("sakms\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(gidDir, "Some.Movie.2020.mkv")
	if err := os.WriteFile(video, []byte("fake video"), 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: moviesRoot, DownloadURL: "https://indexer.example/nzb?id=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := grabsStore.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatal(err)
	}

	cb := UsenetCompleteImporter(nil, connStore, nil, settingsStore, grabsStore, libStore, nil, nil, nzb, nil)
	cb(gid, []string{video})

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != grabs.Imported {
		t.Fatalf("status = %q, want imported", got.Status)
	}
	if _, err := os.Stat(gidDir); !os.IsNotExist(err) {
		t.Fatalf("owned staging dir still present after automatic import: %v", err)
	}
}
