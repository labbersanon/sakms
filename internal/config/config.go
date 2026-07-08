// Package config loads Tidyarr's runtime configuration from the environment.
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

// Config holds settings resolved once at startup.
type Config struct {
	// Addr is the HTTP listen address, e.g. ":8080".
	Addr string
	// DataDir holds tidyarr.db and anything else Tidyarr owns on disk.
	DataDir string
}

// FromEnv reads Config from the environment, applying defaults for anything unset.
func FromEnv() Config {
	return Config{
		Addr:    cmp.Or(os.Getenv("TIDYARR_ADDR"), ":8080"),
		DataDir: cmp.Or(os.Getenv("TIDYARR_DATA_DIR"), "./data"),
	}
}
