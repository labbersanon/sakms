// Package playback is the Jellyfin-like in-app Play path: Direct Play when
// the container is already browser-safe, otherwise HLS remux / audio AAC /
// H.264 transcode.
package playback

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/labbersanon/sakms/internal/config"
)

// Decision is the StreamBuilder rank for one file (Jellyfin web order).
type Decision string

const (
	// Remux copies video+audio into HLS fMP4 (container change only).
	Remux Decision = "remux"
	// DirectStream copies video and encodes audio to AAC.
	DirectStream Decision = "directstream"
	// Transcode encodes video to H.264 and audio to AAC.
	Transcode Decision = "transcode"
)

// DirectPlayContainer reports whether <video> can decode this container
// without HLS. Codec mismatches inside these files still fail natively —
// same as today's mp4 ServeContent path. Probe+HLS runs only for everything else.
func DirectPlayContainer(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".webm", ".mov":
		return true
	default:
		return false
	}
}

// PlayableInApp is every library video (Jellyfin VideoExts minus disc images).
func PlayableInApp(path string) bool {
	return config.IsLibraryVideoFile(path)
}

// VideoURL is the in-app <video> src for a resolved serve URL (path + query).
// Direct Play containers keep the byte URL; others get .../hls/index.m3u8.
func VideoURL(serveURL, filePath string) string {
	if DirectPlayContainer(filePath) {
		return serveURL
	}
	return PlaylistURL(serveURL)
}

// PlaylistURL inserts /hls/index.m3u8 before the query string.
func PlaylistURL(serveURL string) string {
	u, err := url.Parse(serveURL)
	if err != nil {
		return serveURL
	}
	p := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(p, "/hls/index.m3u8") {
		return serveURL
	}
	u.Path = p + "/hls/index.m3u8"
	return u.String()
}

// Decide picks Remux, DirectStream, or Transcode from ffprobe codec names.
// Empty audio (video-only) is copy-safe.
func Decide(videoCodec, audioCodec string) Decision {
	vOK := browserVideo(videoCodec)
	aOK := audioCodec == "" || browserAudio(audioCodec)
	if vOK && aOK {
		return Remux
	}
	if vOK {
		return DirectStream
	}
	return Transcode
}

func browserVideo(codec string) bool {
	switch normalizeCodec(codec) {
	case "h264", "vp8", "vp9", "av1":
		return true
	default:
		return false
	}
}

func browserAudio(codec string) bool {
	switch normalizeCodec(codec) {
	case "aac", "mp3", "opus":
		return true
	default:
		return false
	}
}

func normalizeCodec(s string) string {
	c := strings.ToLower(strings.TrimSpace(s))
	switch c {
	case "avc", "avc1", "h.264":
		return "h264"
	case "hev1", "h265", "h.265":
		return "hevc"
	case "mp4a":
		return "aac"
	case "ac-3":
		return "ac3"
	case "ec-3", "eac-3":
		return "eac3"
	case "dca":
		return "dts"
	default:
		return c
	}
}
