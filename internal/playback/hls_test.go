package playback

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAllowedSegment(t *testing.T) {
	for _, ok := range []string{"index.m3u8", "init.mp4", "seg0.m4s", "seg12.m4s"} {
		if !AllowedSegment(ok) {
			t.Errorf("want allow %q", ok)
		}
	}
	for _, bad := range []string{"", "foo.ts", "../index.m3u8", "seg/0.m4s", "seg.m4s", "SECRETS"} {
		if AllowedSegment(bad) {
			t.Errorf("must reject %q", bad)
		}
	}
}

func TestFFmpegHLSArgs_RemuxCopy(t *testing.T) {
	args := ffmpegHLSArgs("/src.mkv", "/tmp/s", Remux, false, true)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-c copy", "-hls_segment_type fmp4", "index.m3u8", "-i /src.mkv"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "libx264") || strings.Contains(joined, "h264_nvenc") || strings.Contains(joined, "-c:a aac") {
		t.Errorf("remux must not encode: %q", joined)
	}
}

func TestFFmpegHLSArgs_DirectStreamAAC(t *testing.T) {
	args := ffmpegHLSArgs("/src.mkv", "/tmp/s", DirectStream, false, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-c:v copy") || !strings.Contains(joined, "-c:a aac") {
		t.Fatalf("directstream args = %q", joined)
	}
	if strings.Contains(joined, "libx264") {
		t.Fatalf("must not transcode video: %q", joined)
	}
}

func TestFFmpegHLSArgs_TranscodeNVENC(t *testing.T) {
	args := ffmpegHLSArgs("/src.mkv", "/tmp/s", Transcode, true, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "h264_nvenc") || !strings.Contains(joined, "-c:a aac") {
		t.Fatalf("transcode nvenc args = %q", joined)
	}
}

func TestFFmpegHLSArgs_TranscodeLibx264(t *testing.T) {
	args := ffmpegHLSArgs("/src.mkv", "/tmp/s", Transcode, false, false)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "libx264") {
		t.Fatalf("cpu transcode args = %q", joined)
	}
	if strings.Contains(joined, "-c:a aac") || strings.Contains(joined, "-map 0:a:0") {
		t.Fatalf("video-only must not map audio: %q", joined)
	}
}

func TestHubServe_FakeSession(t *testing.T) {
	src := filepath.Join(t.TempDir(), "clip.mkv")
	if err := os.WriteFile(src, []byte("mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHub()
	h.tmpRoot = t.TempDir()
	h.idle = time.Hour
	h.probe = func(context.Context, string) (string, string, error) {
		return "h264", "aac", nil
	}
	h.startFake = func(ctx context.Context, in, outDir string, d Decision) error {
		if d != Remux {
			t.Errorf("decision = %s, want remux", d)
		}
		pl := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n")
		return os.WriteFile(filepath.Join(outDir, "index.m3u8"), pl, 0o644)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hls/index.m3u8", nil)
	h.Serve(rr, req, src, "index.m3u8")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "#EXTM3U") {
		t.Fatalf("body %q", rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "mpegurl") {
		t.Fatalf("Content-Type %q", ct)
	}
}

func TestHubServe_RejectsTraversal(t *testing.T) {
	h := NewHub()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hls/x", nil)
	h.Serve(rr, req, "/nope", "../index.m3u8")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rr.Code)
	}
}

func TestHubServe_MissingSource(t *testing.T) {
	h := NewHub()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hls/index.m3u8", nil)
	h.Serve(rr, req, filepath.Join(t.TempDir(), "missing.mkv"), "index.m3u8")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rr.Code)
	}
}

func TestHubServe_ProbeError(t *testing.T) {
	src := filepath.Join(t.TempDir(), "clip.mkv")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHub()
	h.probe = func(context.Context, string) (string, string, error) {
		return "", "", io.EOF
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hls/index.m3u8", nil)
	h.Serve(rr, req, src, "index.m3u8")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rr.Code)
	}
}
