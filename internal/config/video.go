package config

import (
	"path/filepath"
	"strings"
)

// Claude 2026-08-05: Jellyfin-parity video allowlist for Rename/Dedup resolve gates
// Reason: Short 8-ext private maps let .plexmatch / trickplay tiles become proposals; user rejected folder-name excludes
// Troubleshooting: Non-video rows in Organize Rename/Dedup — gate with IsVideoExt/IsVideoFile, not ExcludedDirNames
// Review if: Jellyfin MimeTypes._videoFileExtensions diverges from this set
// Related files: internal/library/library.go, internal/library/library_series.go, internal/dedup/dedup.go, internal/rename/*
//
// Claude 2026-10-02: .iso/.img stay in VideoExts (Jellyfin parity) but are
//   not resolvable library videos — Organize Discs is the only ISO path.
// Reason: loose Golden/Animaniacs ISOs were showing up as Rename rows.
// Troubleshooting: ISO in Rename — ResolveVideoFile must skip IsDiscImage.
// Review if: VIDEO_TS folders become a third disc source.

// VideoExts are extensions treated as playable video files — mirrors Jellyfin
// MediaBrowser.Model/Net/MimeTypes._videoFileExtensions (case-insensitive).
var VideoExts = map[string]bool{
	".3gp": true, ".asf": true, ".avi": true, ".divx": true, ".dvr-ms": true,
	".f4v": true, ".flv": true, ".img": true, ".iso": true, ".m2t": true,
	".m2ts": true, ".m2v": true, ".m4v": true, ".mk3d": true, ".mkv": true,
	".mov": true, ".mp4": true, ".mpg": true, ".mpeg": true, ".mts": true,
	".ogg": true, ".ogm": true, ".ogv": true, ".rec": true, ".ts": true,
	".rmvb": true, ".vob": true, ".webm": true, ".wmv": true, ".wtv": true,
}

// IsVideoExt reports whether ext (with or without a leading dot) is in VideoExts.
func IsVideoExt(ext string) bool {
	e := strings.ToLower(strings.TrimSpace(ext))
	if e == "" {
		return false
	}
	if e[0] != '.' {
		e = "." + e
	}
	return VideoExts[e]
}

// IsVideoFile reports whether path's extension is an allowlisted video format.
func IsVideoFile(path string) bool {
	return IsVideoExt(filepath.Ext(path))
}

// IsDiscImage reports whether path is a DVD image (.iso / .img). These are
// in VideoExts for Jellyfin parity but are not Rename/Dedup/Import videos.
func IsDiscImage(path string) bool {
	e := strings.ToLower(filepath.Ext(path))
	return e == ".iso" || e == ".img"
}

// IsLibraryVideoFile is a playable library video: VideoExts minus disc images.
func IsLibraryVideoFile(path string) bool {
	return IsVideoFile(path) && !IsDiscImage(path)
}
