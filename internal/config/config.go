// Package config loads SAK's runtime configuration from the environment.
package config

import (
	"cmp"
	"os"
)

// SidecarExts are files that must never be treated as orphaned media content
// needing identification — e.g. Jellyfin-generated .trickplay seek-preview
// files, which Radarr/Sonarr's own unmappedFolders listing otherwise reports
// as "unmapped".
var SidecarExts = map[string]bool{
	".nfo": true, ".jpg": true, ".jpeg": true, ".png": true, ".txt": true,
	".srt": true, ".sub": true, ".vtt": true, ".edl": true, ".bif": true,
	".log": true, ".trickplay": true,
}

// ExcludedDirNames are bonus-content subdirectory names (case-insensitive)
// that library.ScanRootFolder's recursive walk must never report or descend
// into — relevant once recursion can open up an already-organized movie/show
// folder (because one of its files is newly tracked) and expose a Sample/
// Extras folder inside it for the first time. Deliberately excludes anything
// "specials"-shaped: Jellyfin's own Series convention uses a literal
// "Specials" season folder for Season 0, which must stay visible.
var ExcludedDirNames = map[string]bool{
	"sample": true, "samples": true, "extras": true, "featurettes": true,
	"behind the scenes": true, "deleted scenes": true, "trailers": true,
	"interviews": true, "shorts": true, "subs": true, "subtitles": true,
}

// Config holds settings resolved once at startup.
type Config struct {
	// Addr is the HTTP listen address, e.g. ":8080".
	Addr string
	// DataDir holds sakms.db and anything else SAK owns on disk.
	DataDir string
	// APIKey, if set, is the X-Api-Key clients must send to authenticate
	// without a session cookie (see internal/auth). Deliberately has no
	// default and is read via plain os.Getenv below, NOT cmp.Or like
	// Addr/DataDir — an empty string here is itself the meaningful
	// "not set, fall through to auto-generation" sentinel, not a value
	// that needs a fallback.
	APIKey string
}

// FromEnv reads Config from the environment, applying defaults for anything unset.
func FromEnv() Config {
	return Config{
		Addr:    cmp.Or(os.Getenv("SAKMS_ADDR"), ":8080"),
		DataDir: cmp.Or(os.Getenv("SAKMS_DATA_DIR"), "./data"),
		APIKey:  os.Getenv("SAKMS_API_KEY"),
	}
}
