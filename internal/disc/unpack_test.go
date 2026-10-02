package disc

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fakeFFJSON(dur string, chapters int) []byte {
	type ch struct {
		StartTime string `json:"start_time"`
	}
	chs := make([]ch, chapters)
	for i := range chs {
		chs[i].StartTime = "0"
	}
	b, _ := json.Marshal(struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Chapters []ch `json:"chapters"`
	}{
		Format: struct {
			Duration string `json:"duration"`
		}{Duration: dur},
		Chapters: chs,
	})
	return b
}

func TestUnpack_ExtractsThenDeletes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "show.iso")
	if err := os.WriteFile(src, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}

	origLook, origRun := lookPath, runCmd
	t.Cleanup(func() { lookPath = origLook; runCmd = origRun })
	lookPath = func(name string) (string, error) { return name, nil }
	runCmd = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// 7z listing: no VolumeId → filename stem
		if name == "7z" {
			return exec.CommandContext(ctx, "true")
		}
		if name == "ffprobe" {
			title := ""
			for i, a := range args {
				if a == "-title" && i+1 < len(args) {
					title = args[i+1]
				}
			}
			switch title {
			case "1":
				return scriptCmd(ctx, fakeFFJSON("6386", 15))
			case "2":
				return scriptCmd(ctx, fakeFFJSON("433", 0))
			case "3":
				return scriptCmd(ctx, fakeFFJSON("400", 0))
			default:
				return failCmd(ctx)
			}
		}
		if name == "ffmpeg" {
			dest := args[len(args)-1]
			return writeCmd(ctx, dest, bytes.Repeat([]byte("M"), minOutputB))
		}
		return failCmd(ctx)
	}

	res, err := Unpack(context.Background(), src, Options{})
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if !res.DeletedSource {
		t.Fatal("expected ISO deleted")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("ISO still present: %v", err)
	}
	if len(res.Outputs) != 2 {
		t.Fatalf("outputs = %d, want 2 shorts", len(res.Outputs))
	}
	for _, p := range res.Outputs {
		if !strings.Contains(p, "show - t") || !strings.HasSuffix(p, ".mkv") {
			t.Fatalf("output name %q", p)
		}
		st, err := os.Stat(p)
		if err != nil || st.Size() < minOutputB {
			t.Fatalf("missing/small output %s: %v", p, err)
		}
	}
	if _, err := os.Stat(SidecarPath(src)); err != nil {
		t.Fatalf("sidecar: %v", err)
	}
}

func TestUnpack_KeepsISOOnExtractFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "show.iso")
	if err := os.WriteFile(src, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	origLook, origRun := lookPath, runCmd
	t.Cleanup(func() { lookPath = origLook; runCmd = origRun })
	lookPath = func(name string) (string, error) { return name, nil }
	runCmd = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "7z" {
			return exec.CommandContext(ctx, "true")
		}
		if name == "ffprobe" {
			title := ""
			for i, a := range args {
				if a == "-title" && i+1 < len(args) {
					title = args[i+1]
				}
			}
			if title == "1" {
				return scriptCmd(ctx, fakeFFJSON("5400", 8))
			}
			return failCmd(ctx)
		}
		return failCmd(ctx)
	}
	_, err := Unpack(context.Background(), src, Options{})
	if err == nil {
		t.Fatal("expected extract error")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("ISO should remain: %v", err)
	}
}

func TestUnpack_RejectsNonDisc(t *testing.T) {
	_, err := Unpack(context.Background(), "/tmp/a.mkv", Options{})
	if err != ErrNotDisc {
		t.Fatalf("err = %v", err)
	}
}

func scriptCmd(ctx context.Context, stdout []byte) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", "printf '%s' "+shellQuote(string(stdout)))
}

func writeCmd(ctx context.Context, dest string, body []byte) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", "printf '%s' "+shellQuote(string(body))+" > "+shellQuote(dest))
}

func failCmd(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", "echo fail >&2; exit 1")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
