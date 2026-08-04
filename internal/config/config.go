// Package config loads SAK's runtime configuration from the environment.
package config

import (
	"cmp"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	// DataDir holds secret.key, downloads staging, and (post-Postgres cutover)
	// a dated sakms.db backup — not the live database file.
	DataDir string
	// DatabaseURL is the Postgres DSN (postgres://…). Prefer DatabaseURLFile
	// in production so the password never lands in compose env.
	// Claude 2026-08-04: added for SQLite→Postgres migration.
	// Reason: Postgres-only open path; no sakms.db path DSN.
	// Review if: dual-driver returns (spec forbids).
	DatabaseURL string
	// DatabaseURLFile, if set, is a path whose contents override DatabaseURL
	// (trimmed). Used with the compose init-container / *_FILE pattern.
	DatabaseURLFile string
	// DBMaxOpenConns / DBMaxIdleConns size the sql.DB pool (Postgres).
	DBMaxOpenConns int
	DBMaxIdleConns int
	// APIKey, if set, is the X-Api-Key clients must send to authenticate
	// without a session cookie (see internal/auth). Deliberately has no
	// default and is read via plain os.Getenv below, NOT cmp.Or like
	// Addr/DataDir — an empty string here is itself the meaningful
	// "not set, fall through to auto-generation" sentinel, not a value
	// that needs a fallback.
	APIKey string
	// BundledOllamaModel, if set, names the Ollama model the opt-in
	// `ai`-variant image (see Dockerfile) pulls and serves in-container.
	// Same empty-is-the-sentinel reasoning as APIKey: blank means "not the
	// ai-variant image, don't seed anything" — the default (non-ai) image
	// never sets this env var, so existing installs are unaffected.
	BundledOllamaModel string
}

// FromEnv reads Config from the environment, applying defaults for anything unset.
func FromEnv() Config {
	return Config{
		Addr:               cmp.Or(os.Getenv("SAKMS_ADDR"), ":8080"),
		DataDir:            cmp.Or(os.Getenv("SAKMS_DATA_DIR"), "./data"),
		DatabaseURL:        os.Getenv("SAKMS_DATABASE_URL"),
		DatabaseURLFile:    os.Getenv("SAKMS_DATABASE_URL_FILE"),
		DBMaxOpenConns:     envInt("SAKMS_DB_MAX_OPEN_CONNS", 20),
		DBMaxIdleConns:     envInt("SAKMS_DB_MAX_IDLE_CONNS", 5),
		APIKey:             os.Getenv("SAKMS_API_KEY"),
		BundledOllamaModel: os.Getenv("SAKMS_BUNDLED_OLLAMA_MODEL"),
	}
}

// ResolveDatabaseURL returns the DSN from DatabaseURLFile (preferred) or
// DatabaseURL. File contents are trimmed of surrounding whitespace/newlines.
func (c Config) ResolveDatabaseURL() (string, error) {
	if c.DatabaseURLFile != "" {
		b, err := os.ReadFile(c.DatabaseURLFile)
		if err != nil {
			return "", fmt.Errorf("reading SAKMS_DATABASE_URL_FILE %q: %w", c.DatabaseURLFile, err)
		}
		dsn := strings.TrimSpace(string(b))
		if dsn == "" {
			return "", fmt.Errorf("SAKMS_DATABASE_URL_FILE %q is empty", c.DatabaseURLFile)
		}
		return dsn, nil
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return "", fmt.Errorf("set SAKMS_DATABASE_URL or SAKMS_DATABASE_URL_FILE")
	}
	return strings.TrimSpace(c.DatabaseURL), nil
}

func envInt(key string, def int) int {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
