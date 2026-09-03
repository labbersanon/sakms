package usenet

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveLeaders_SkipsRarVolumes(t *testing.T) {
	names := []string{
		"release.part02.rar",
		"release.part01.rar",
		"release.part03.rar",
		"bonus.zip",
		"other.r00",
		"other.rar",
		"video.mkv",
	}
	got := archiveLeaders(names)
	want := []string{"bonus.zip", "other.rar", "release.part01.rar"}
	if len(got) != len(want) {
		t.Fatalf("leaders = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("leaders[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestUnpackArchives_NoArchives_NoOp(t *testing.T) {
	dir := t.TempDir()
	mkv := filepath.Join(dir, "a.mkv")
	if err := os.WriteFile(mkv, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := unpackArchives(dir, []string{mkv})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 1 || got[0] != mkv {
		t.Fatalf("got %v, want [%s]", got, mkv)
	}
}

func TestUnpackArchives_ZipExtractsAndDeletesArchives(t *testing.T) {
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not on PATH")
	}
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "pack.zip")
	if err := writeZipWithFile(zipPath, "movie.mkv", []byte("fake-video")); err != nil {
		t.Fatal(err)
	}
	// Decoy volume-looking sibling should not block zip leader.
	if err := os.WriteFile(filepath.Join(dir, "pack.sfv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := unpackArchives(dir, []string{zipPath})
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	mkv := filepath.Join(dir, "movie.mkv")
	if _, err := os.Stat(mkv); err != nil {
		t.Fatalf("expected extracted video: %v", err)
	}
	if _, err := os.Stat(zipPath); !os.IsNotExist(err) {
		t.Fatalf("zip should be deleted after success, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pack.sfv")); !os.IsNotExist(err) {
		t.Fatalf("sfv should be deleted after success, stat err=%v", err)
	}
	found := false
	for _, p := range got {
		if p == mkv {
			found = true
		}
	}
	if !found {
		t.Fatalf("returned files missing mkv: %v", got)
	}
}

func TestUnpackArchives_FakeUnrarRunner(t *testing.T) {
	dir := t.TempDir()
	rar := filepath.Join(dir, "set.part01.rar")
	vol := filepath.Join(dir, "set.part02.rar")
	for _, p := range []string{rar, vol} {
		if err := os.WriteFile(p, []byte("rar"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	oldLook, oldCmd := lookPath, unpackCommand
	t.Cleanup(func() {
		lookPath = oldLook
		unpackCommand = oldCmd
	})
	lookPath = func(file string) (string, error) {
		if file == "unrar" {
			return "/bin/unrar-fake", nil
		}
		return "", exec.ErrNotFound
	}
	unpackCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// Simulate unrar e by writing the video into dest (last arg).
		dest := args[len(args)-1]
		dest = strings.TrimRight(dest, string(filepath.Separator))
		script := "#!/bin/sh\nprintf fake > \"$1/out.mkv\"\n"
		helper := filepath.Join(t.TempDir(), "fake-unrar.sh")
		if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, helper, dest)
	}

	got, err := unpackArchives(dir, []string{rar, vol})
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.mkv")); err != nil {
		t.Fatalf("expected out.mkv: %v", err)
	}
	if _, err := os.Stat(rar); !os.IsNotExist(err) {
		t.Fatalf("part01 should be deleted, err=%v", err)
	}
	if _, err := os.Stat(vol); !os.IsNotExist(err) {
		t.Fatalf("part02 should be deleted, err=%v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected refreshed file list")
	}
}

func writeZipWithFile(zipPath, name string, body []byte) error {
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	wf, err := w.Create(name)
	if err != nil {
		return err
	}
	if _, err := wf.Write(body); err != nil {
		return err
	}
	return w.Close()
}
