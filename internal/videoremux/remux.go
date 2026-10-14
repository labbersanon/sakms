// Package videoremux stream-copies Matroska into fragmented MP4 for <video>.
package videoremux

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// lookPath / runCmd are swappable in tests so a missing or fake ffmpeg
// never shells the real binary.
var lookPath = exec.LookPath

var runCmd = func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// ToFragmentedMP4 copies the first video (and first audio, if any) from src
// into fragmented MP4 on dst. Codecs are not re-encoded.
//
// Claude 2026-10-14: in-app Play for .mkv without HLS/transcode.
// Reason: browsers cannot decode Matroska; FileFlows remux-to-mp4 was the
// only way those files played. Stream-copy to fMP4 is Direct Stream, not
// the ROADMAP legal-gated HLS encode.
// Troubleshooting: ffmpeg missing → error; AC3/DTS/HEVC may still fail in
// the browser after remux (same as a native mp4 with those codecs).
// Review if: HLS transcode lands, or this starts encoding audio/video.
func ToFragmentedMP4(ctx context.Context, src string, dst io.Writer) error {
	bin, err := lookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("mkv remux: ffmpeg not on PATH: %w", err)
	}
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", src,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-c", "copy",
		"-sn", "-dn",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4",
		"pipe:1",
	}
	cmd := runCmd(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stdout = dst
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("mkv remux: %s", msg)
	}
	return nil
}
