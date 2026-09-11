package stagingsweep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/grabs"
)

type fakeGrabs struct {
	byGID map[string]*grabs.Grab
	err   error
}

func (f *fakeGrabs) GetByDownloadGID(_ context.Context, gid string) (*grabs.Grab, error) {
	if f.err != nil {
		return nil, f.err
	}
	g, ok := f.byGID[gid]
	if !ok {
		return nil, grabs.ErrNotFound
	}
	return g, nil
}

func TestSweep_DeletesImportedAndAgedOrphansOnly(t *testing.T) {
	root := t.TempDir()
	imported := filepath.Join(root, "nzb-1111111111111111")
	orphanOld := filepath.Join(root, "nzb-2222222222222222")
	orphanNew := filepath.Join(root, "nzb-3333333333333333")
	active := filepath.Join(root, "nzb-4444444444444444")
	foreign := filepath.Join(root, "not-sakms")
	for _, d := range []string{imported, orphanOld, orphanNew, active, foreign} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "x.rar"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(orphanOld, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	store := &fakeGrabs{byGID: map[string]*grabs.Grab{
		"nzb-1111111111111111": {Status: grabs.Imported, DownloadGID: "nzb-1111111111111111"},
		"nzb-4444444444444444": {Status: grabs.Downloading, DownloadGID: "nzb-4444444444444444"},
	}}

	n, err := Sweep(context.Background(), time.Now(), root, 7*24*time.Hour, store)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed=%d want 2 (imported + old orphan)", n)
	}
	assertGone(t, imported)
	assertGone(t, orphanOld)
	assertExists(t, orphanNew)
	assertExists(t, active)
	assertExists(t, foreign)
}

func TestSweep_SkipsOrphansWhenAgeDisabled(t *testing.T) {
	root := t.TempDir()
	orphan := filepath.Join(root, "nzb-5555555555555555")
	if err := os.Mkdir(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	_ = os.Chtimes(orphan, oldTime, oldTime)
	store := &fakeGrabs{byGID: map[string]*grabs.Grab{}}
	n, err := Sweep(context.Background(), time.Now(), root, 0, store)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("removed=%d want 0 when orphan age disabled", n)
	}
	assertExists(t, orphan)
}

func TestSweep_SkipsOnLookupError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "nzb-6666666666666666")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := &fakeGrabs{err: errors.New("db down")}
	n, err := Sweep(context.Background(), time.Now(), root, 7*24*time.Hour, store)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("removed=%d want 0 on lookup error", n)
	}
	assertExists(t, dir)
}

func assertGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("%s should be gone, err=%v", p, err)
	}
}

func assertExists(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s should exist: %v", p, err)
	}
}

func TestSweep_DeletesAgedFailedGrab(t *testing.T) {
	root := t.TempDir()
	failedOld := filepath.Join(root, "nzb-7777777777777777")
	failedNew := filepath.Join(root, "nzb-8888888888888888")
	for _, d := range []string{failedOld, failedNew} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "x.rar"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(failedOld, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	store := &fakeGrabs{byGID: map[string]*grabs.Grab{
		"nzb-7777777777777777": {Status: grabs.Failed, DownloadGID: "nzb-7777777777777777"},
		"nzb-8888888888888888": {Status: grabs.Failed, DownloadGID: "nzb-8888888888888888"},
	}}
	n, err := Sweep(context.Background(), time.Now(), root, 7*24*time.Hour, store)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed=%d want 1 (aged failed only)", n)
	}
	assertGone(t, failedOld)
	assertExists(t, failedNew)
}
