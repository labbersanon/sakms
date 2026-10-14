package videoremux

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestToFragmentedMP4_CopiesViaFakeFFmpeg(t *testing.T) {
	src := filepath.Join(t.TempDir(), "clip.mkv")
	if err := os.WriteFile(src, []byte("matroska-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	origLook, origRun := lookPath, runCmd
	t.Cleanup(func() { lookPath = origLook; runCmd = origRun })
	lookPath = func(string) (string, error) { return "ffmpeg", nil }
	var gotArgs []string
	runCmd = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotArgs = append([]string{name}, args...)
		in := ""
		for i, a := range args {
			if a == "-i" && i+1 < len(args) {
				in = args[i+1]
			}
		}
		return exec.CommandContext(ctx, "cat", in)
	}

	var out bytes.Buffer
	if err := ToFragmentedMP4(context.Background(), src, &out); err != nil {
		t.Fatalf("ToFragmentedMP4: %v", err)
	}
	if out.String() != "matroska-bytes" {
		t.Fatalf("stdout = %q", out.String())
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"-c copy", "-f mp4", "pipe:1", "frag_keyframe+empty_moov+default_base_moof"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "-c:v libx264") || strings.Contains(joined, "-c:a aac") {
		t.Errorf("must not encode, got %q", joined)
	}
}

func TestToFragmentedMP4_MissingFFmpeg(t *testing.T) {
	origLook := lookPath
	t.Cleanup(func() { lookPath = origLook })
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	err := ToFragmentedMP4(context.Background(), "clip.mkv", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg not on PATH") {
		t.Fatalf("got %v", err)
	}
}

func TestToFragmentedMP4_FFmpegFailure(t *testing.T) {
	origLook, origRun := lookPath, runCmd
	t.Cleanup(func() { lookPath = origLook; runCmd = origRun })
	lookPath = func(string) (string, error) { return "ffmpeg", nil }
	runCmd = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo boom >&2; exit 1")
	}
	err := ToFragmentedMP4(context.Background(), "clip.mkv", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}
