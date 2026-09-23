// Package nfo reads Kodi/Jellyfin-format .nfo sidecar files alongside video
// files and extracts metadata — primarily the TMDB ID, which lets ScanLibrary
// skip the fuzzy filename search when an authoritative ID is already on disk.
//
// Both common XML shapes are handled:
//   <tmdbid>603</tmdbid>                         (Kodi flat field)
//   <uniqueid type="tmdb">603</uniqueid>          (Jellyfin / newer Kodi)
//
// Two sidecar path conventions are tried, in preference order:
//  1. Same-basename sidecar: /path/to/Movie.mkv  → /path/to/Movie.nfo
//  2. Folder-level sidecar:  /path/to/Movie.mkv  → /path/to/movie.nfo
//
// Errors from open/parse are silently swallowed by ReadSidecar — a missing or
// malformed .nfo is treated as "no hint available," never a fatal scan error.
package nfo

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MovieNFO holds the fields SAK reads from a movie .nfo sidecar.
// Zero values indicate the field was absent or unparseable.
type MovieNFO struct {
	TMDBID int
	TVDBID int
	IMDBID string
	Title  string
	Year   int
	Plot   string
}

// xmlMovie is the raw XML shape — handles both the flat <tmdbid> field and the
// <uniqueid type="tmdb"> variant used by Jellyfin and newer Kodi builds.
type xmlMovie struct {
	XMLName   xml.Name `xml:"movie"`
	Title     string   `xml:"title"`
	Year      int      `xml:"year"`
	Plot      string   `xml:"plot"`
	TMDBIDTag int      `xml:"tmdbid"`
	TVDBIDTag int      `xml:"tvdbid"`
	IMDBIDTag string   `xml:"imdb_id"`
	IMDBIDAlt string   `xml:"imdbid"`
	UniqueIDs []xmlUID `xml:"uniqueid"`
}

// xmlUID uses a string Value so that non-numeric IDs like IMDB's "tt0133093"
// don't cause xml.Decode to fail — only the tmdb type is converted to int.
type xmlUID struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

// SidecarPaths returns the candidate .nfo paths for an entry, in preference
// order. The caller should try each in turn and stop at the first that exists.
//
// Movies are often stored as a folder ("Movie Title (2001)/movie.mkv").
// ScanRootFolder returns the folder as the atomic entry, so entryPath may be a
// directory. In that case the sidecar is inside: "{dir}/{dirname}.nfo" (Kodi
// default) or "{dir}/movie.nfo" (folder-level alternative). When entryPath is
// a plain file, the same-basename sidecar and the parent-folder movie.nfo are
// tried.
func SidecarPaths(entryPath string) []string {
	if info, err := os.Stat(entryPath); err == nil && info.IsDir() {
		name := filepath.Base(entryPath)
		return []string{
			filepath.Join(entryPath, name+".nfo"),
			filepath.Join(entryPath, "movie.nfo"),
		}
	}
	ext := filepath.Ext(entryPath)
	baseSidecar := strings.TrimSuffix(entryPath, ext) + ".nfo"
	folderSidecar := filepath.Join(filepath.Dir(entryPath), "movie.nfo")
	if baseSidecar == folderSidecar {
		return []string{baseSidecar}
	}
	return []string{baseSidecar, folderSidecar}
}

// Read parses a .nfo file at the given path and returns the extracted fields.
func Read(path string) (MovieNFO, error) {
	f, err := os.Open(path)
	if err != nil {
		return MovieNFO{}, err
	}
	defer f.Close()

	var raw xmlMovie
	if err := xml.NewDecoder(f).Decode(&raw); err != nil {
		return MovieNFO{}, err
	}

	m := MovieNFO{Title: raw.Title, Year: raw.Year, Plot: strings.TrimSpace(raw.Plot)}

	// Flat <tmdbid> field takes precedence; fall back to <uniqueid type="tmdb">.
	if raw.TMDBIDTag != 0 {
		m.TMDBID = raw.TMDBIDTag
	} else {
		for _, uid := range raw.UniqueIDs {
			if uid.Type == "tmdb" {
				if id, err := strconv.Atoi(strings.TrimSpace(uid.Value)); err == nil && id != 0 {
					m.TMDBID = id
					break
				}
			}
		}
	}
	m.TVDBID = raw.TVDBIDTag
	if m.TVDBID == 0 {
		for _, uid := range raw.UniqueIDs {
			if uid.Type == "tvdb" {
				if id, err := strconv.Atoi(strings.TrimSpace(uid.Value)); err == nil && id != 0 {
					m.TVDBID = id
					break
				}
			}
		}
	}
	m.IMDBID = strings.TrimSpace(raw.IMDBIDTag)
	if m.IMDBID == "" {
		m.IMDBID = strings.TrimSpace(raw.IMDBIDAlt)
	}
	if m.IMDBID == "" {
		for _, uid := range raw.UniqueIDs {
			if uid.Type == "imdb" {
				m.IMDBID = strings.TrimSpace(uid.Value)
				break
			}
		}
	}

	return m, nil
}

// ReadSidecar tries each candidate .nfo path for videoPath and returns the
// first successfully parsed result. Returns a zero MovieNFO (and no error) if
// no readable sidecar exists — never returns an error from missing/bad files.
func ReadSidecar(videoPath string) MovieNFO {
	for _, p := range SidecarPaths(videoPath) {
		if m, err := Read(p); err == nil {
			return m
		}
	}
	return MovieNFO{}
}

// SeriesNFO holds the fields SAK reads from a tvshow.nfo or episode .nfo
// sidecar. TMDBID is the show-level id (not episode-level) — the one that
// identifies the series in TMDB's /tv/{id} endpoint, which is what SAK
// uses as the library key. Zero values indicate the field was absent.
type SeriesNFO struct {
	TMDBID int
	TVDBID int
	IMDBID string
	Title  string
	Year   int
	Plot   string
}

// xmlTVShow is the raw XML shape for Kodi/Jellyfin tvshow.nfo files.
// Uses the same <uniqueid type="tmdb"> field as movie .nfo; the flat
// <tmdbid> tag is also supported for older Kodi versions.
type xmlTVShow struct {
	XMLName   xml.Name `xml:"tvshow"`
	Title     string   `xml:"title"`
	Year      int      `xml:"year"`
	Plot      string   `xml:"plot"`
	TMDBIDTag int      `xml:"tmdbid"`
	TVDBIDTag int      `xml:"tvdbid"`
	IMDBIDTag string   `xml:"imdb_id"`
	IMDBIDAlt string   `xml:"imdbid"`
	UniqueIDs []xmlUID `xml:"uniqueid"`
}

// SeriesSidecarPaths returns candidate tvshow.nfo and episode .nfo paths for
// a series episode file, in preference order:
//
//  1. {videoDir}/../tvshow.nfo — series root when episode is in a season subfolder
//  2. {videoDir}/tvshow.nfo — series root when episode is directly in series folder
//  3. {videoPath no-ext}.nfo — episode-specific sidecar (Kodi/Jellyfin convention)
func SeriesSidecarPaths(videoPath string) []string {
	dir := filepath.Dir(videoPath)
	ext := filepath.Ext(videoPath)
	episodeSidecar := strings.TrimSuffix(videoPath, ext) + ".nfo"
	paths := []string{
		filepath.Join(dir, "..", "tvshow.nfo"),
		filepath.Join(dir, "tvshow.nfo"),
		episodeSidecar,
	}
	// Deduplicate: if dir has no parent (already at root), both tvshow.nfo paths
	// resolve to the same file — keep only the first occurrence.
	seen := make(map[string]bool)
	out := paths[:0]
	for _, p := range paths {
		if clean := filepath.Clean(p); !seen[clean] {
			seen[clean] = true
			out = append(out, p)
		}
	}
	return out
}

// ReadSeries parses a tvshow.nfo file at the given path and returns the
// extracted fields. It handles both the flat <tmdbid> field and the
// <uniqueid type="tmdb"> variant.
func ReadSeries(path string) (SeriesNFO, error) {
	f, err := os.Open(path)
	if err != nil {
		return SeriesNFO{}, err
	}
	defer f.Close()

	var raw xmlTVShow
	if err := xml.NewDecoder(f).Decode(&raw); err != nil {
		return SeriesNFO{}, err
	}

	s := SeriesNFO{Title: raw.Title, Year: raw.Year, Plot: strings.TrimSpace(raw.Plot)}
	if raw.TMDBIDTag != 0 {
		s.TMDBID = raw.TMDBIDTag
	} else {
		for _, uid := range raw.UniqueIDs {
			if uid.Type == "tmdb" {
				if id, err := strconv.Atoi(strings.TrimSpace(uid.Value)); err == nil && id != 0 {
					s.TMDBID = id
					break
				}
			}
		}
	}
	s.TVDBID = raw.TVDBIDTag
	if s.TVDBID == 0 {
		for _, uid := range raw.UniqueIDs {
			if uid.Type == "tvdb" {
				if id, err := strconv.Atoi(strings.TrimSpace(uid.Value)); err == nil && id != 0 {
					s.TVDBID = id
					break
				}
			}
		}
	}
	s.IMDBID = strings.TrimSpace(raw.IMDBIDTag)
	if s.IMDBID == "" {
		s.IMDBID = strings.TrimSpace(raw.IMDBIDAlt)
	}
	if s.IMDBID == "" {
		for _, uid := range raw.UniqueIDs {
			if uid.Type == "imdb" {
				s.IMDBID = strings.TrimSpace(uid.Value)
				break
			}
		}
	}
	return s, nil
}

// ReadSeriesSidecar tries each candidate path from SeriesSidecarPaths and
// returns the first successfully parsed result. Returns a zero SeriesNFO if
// no readable sidecar is found — never returns an error for missing/bad files.
// Requires TMDBID != 0 (Rename fast-path); use ReadSeriesSidecarAny for TVDB-only.
func ReadSeriesSidecar(videoPath string) SeriesNFO {
	for _, p := range SeriesSidecarPaths(videoPath) {
		if s, err := ReadSeries(p); err == nil && s.TMDBID != 0 {
			return s
		}
	}
	return SeriesNFO{}
}

// ReadSeriesSidecarAny is ReadSeriesSidecar without the TMDBID!=0 gate — used
// when repairing library rows that only have TVDB/IMDB in an existing NFO.
func ReadSeriesSidecarAny(videoPath string) SeriesNFO {
	for _, p := range SeriesSidecarPaths(videoPath) {
		if s, err := ReadSeries(p); err == nil && (s.TMDBID != 0 || s.TVDBID != 0 || s.Title != "") {
			return s
		}
	}
	return SeriesNFO{}
}

// ReadSeriesFile parses tvshow.nfo at an absolute path (series root).
func ReadSeriesFile(path string) SeriesNFO {
	s, err := ReadSeries(path)
	if err != nil {
		return SeriesNFO{}
	}
	return s
}

// WriteMovie writes a Jellyfin/Kodi-compatible movie.nfo (no lockdata).
func WriteMovie(path string, m MovieNFO) error {
	type uid struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	}
	type doc struct {
		XMLName   xml.Name `xml:"movie"`
		Title     string   `xml:"title"`
		Year      int      `xml:"year,omitempty"`
		Plot      string   `xml:"plot,omitempty"`
		TMDBID    int      `xml:"tmdbid,omitempty"`
		TVDBID    int      `xml:"tvdbid,omitempty"`
		IMDBID    string   `xml:"imdb_id,omitempty"`
		UniqueIDs []uid    `xml:"uniqueid"`
	}
	out := doc{Title: m.Title, Year: m.Year, Plot: m.Plot, TMDBID: m.TMDBID, TVDBID: m.TVDBID, IMDBID: m.IMDBID}
	if m.TMDBID != 0 {
		out.UniqueIDs = append(out.UniqueIDs, uid{Type: "tmdb", Value: strconv.Itoa(m.TMDBID)})
	}
	if m.TVDBID != 0 {
		out.UniqueIDs = append(out.UniqueIDs, uid{Type: "tvdb", Value: strconv.Itoa(m.TVDBID)})
	}
	if m.IMDBID != "" {
		out.UniqueIDs = append(out.UniqueIDs, uid{Type: "imdb", Value: m.IMDBID})
	}
	return writeXML(path, out)
}

// WriteSeries writes a Jellyfin/Kodi-compatible tvshow.nfo (no lockdata).
func WriteSeries(path string, s SeriesNFO) error {
	type uid struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	}
	type doc struct {
		XMLName   xml.Name `xml:"tvshow"`
		Title     string   `xml:"title"`
		Year      int      `xml:"year,omitempty"`
		Plot      string   `xml:"plot,omitempty"`
		TMDBID    int      `xml:"tmdbid,omitempty"`
		TVDBID    int      `xml:"tvdbid,omitempty"`
		IMDBID    string   `xml:"imdb_id,omitempty"`
		UniqueIDs []uid    `xml:"uniqueid"`
	}
	out := doc{Title: s.Title, Year: s.Year, Plot: s.Plot, TMDBID: s.TMDBID, TVDBID: s.TVDBID, IMDBID: s.IMDBID}
	if s.TMDBID != 0 {
		out.UniqueIDs = append(out.UniqueIDs, uid{Type: "tmdb", Value: strconv.Itoa(s.TMDBID)})
	}
	if s.TVDBID != 0 {
		out.UniqueIDs = append(out.UniqueIDs, uid{Type: "tvdb", Value: strconv.Itoa(s.TVDBID)})
	}
	if s.IMDBID != "" {
		out.UniqueIDs = append(out.UniqueIDs, uid{Type: "imdb", Value: s.IMDBID})
	}
	return writeXML(path, out)
}

func writeXML(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(f)
	enc.Indent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	if _, err := f.WriteString("\n"); err != nil {
		return err
	}
	return nil
}
