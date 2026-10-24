package usenet

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsSingleRar(t *testing.T) {
	if !isSingleRar("Movie.rar", []string{"Movie.rar"}) {
		t.Fatal("bare rar")
	}
	if isSingleRar("Movie.part01.rar", []string{"Movie.part01.rar"}) {
		t.Fatal("orphan part01 is not a single rar")
	}
	if isSingleRar("Movie.rar", []string{"Movie.rar", "Movie.r00"}) {
		t.Fatal("rar+r00 is a volume set")
	}
}

func TestDirectUnpack_FeedsContinueWithoutDeleting(t *testing.T) {
	dir := t.TempDir()
	part1 := filepath.Join(dir, "set.part01.rar")
	part2 := filepath.Join(dir, "set.part02.rar")
	for _, p := range []string{part1, part2} {
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
		dest := args[len(args)-1]
		dest = strings.TrimRight(dest, string(filepath.Separator))
		script := "#!/bin/sh\n" +
			"printf aaaa > \"$1/out.mkv\"\n" +
			"printf 'Insert disk with set.part02.rar [C]ontinue, [Q]uit\\n' >&2\n" +
			"read line\n" +
			"printf bbbb >> \"$1/out.mkv\"\n"
		helper := filepath.Join(t.TempDir(), "fake-unrar.sh")
		if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, helper, dest)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	du := tickDirectUnpack(ctx, dir, nil, []string{"set.part01.rar"})
	if du != nil {
		t.Fatal("must not start before part02 is assembled")
	}
	du = tickDirectUnpack(ctx, dir, du, []string{"set.part01.rar", "set.part02.rar"})
	if du == nil {
		t.Fatal("expected direct unpack after part02")
	}
	du.Wait(ctx)
	body, err := os.ReadFile(filepath.Join(dir, "out.mkv"))
	if err != nil {
		t.Fatalf("extracted: %v", err)
	}
	if string(body) != "aaaabbbb" {
		t.Fatalf("mkv=%q", body)
	}
	if _, err := os.Stat(part1); err != nil {
		t.Fatalf("part01 must remain until final unpack: %v", err)
	}
	if _, err := os.Stat(part2); err != nil {
		t.Fatalf("part02 must remain until final unpack: %v", err)
	}
	if !du.Extracted() {
		t.Fatal("expected extracted")
	}
	du.Stop()
}
