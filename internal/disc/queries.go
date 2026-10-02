package disc

// Claude 2026-10-02: SearchQueries strips volume/disc edition tokens.
// Reason: TMDB identify after ISO pick; labels like V5_D1 are not the title.
// Troubleshooting: empty hits — filename stem is also queried.
// Review if: Phase 5 episode naming uses the same cleaner.
//
// Claude 2026-10-02: WikiQuery keeps Volume N and drops Disc N.
// Reason: Wikipedia set pages are "Golden Collection: Volume 5", not the show
//   name TMDB wants; Disc N is a section on that page, not the search string.
// Troubleshooting: V5_D1 must become "… Volume 5" with DiscNumber=1.
// Review if: a disc DB starts returning per-title names (OVID does not).
//
// Claude 2026-10-02: glued vNdM is volume N, disc M.
// Reason: looneytunesgoldenv5d1 / GOLDEN_V5D1 has no word break, so \b v/d
//   tokens never matched. Operators pick the catalog title from a dropdown;
//   TMDB/wiki must still prefer Volume 5 Disc 1.
// Troubleshooting: wrong Vol. 1 chip first — ParseEdition missed v5d1.
// Review if: filenames start using "disc" spelled out without a volume.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	// discEditionRe used to strip every edition token from TMDB queries.
	// Commented out 2026-10-02: that dropped Volume N, so Golden V5D1
	// searched "LOONEY TUNES GOLDEN" and ranked Vol. 1 first.
	// discEditionRe = regexp.MustCompile(`(?i)\b(?:volume|vol|disc|disk|dvd|v|d)\s*\d+\b`)
	discOnlyRe    = regexp.MustCompile(`(?i)\b(?:disc|disk|d)\s*\d+\b`)
	discNumRe     = regexp.MustCompile(`(?i)\b(?:disc|disk|d)\s*(\d+)\b`)
	volumeNumRe   = regexp.MustCompile(`(?i)\b(?:volume|vol|v)\s*(\d+)\b`)
	compactVDRe   = regexp.MustCompile(`(?i)v(\d+)\s*[_-]?\s*d(\d+)`)
	volumeTitleRe = regexp.MustCompile(`(?i)\b(?:volume|vol\.?)\s*(\d+)\b|\bv(\d+)\b`)
)

// ParseEdition reads Volume and Disc from a volume label or filename.
// Compact v5d1 / V5_D1 / V5 D1 is volume 5, disc 1 — V is never "video".
func ParseEdition(volume, src string) (vol, discN int) {
	stem := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	candidates := []string{
		volume,
		stem,
		strings.ReplaceAll(volume, "_", " "),
		strings.ReplaceAll(stem, "_", " "),
		strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(volume+" "+stem), "_", ""), " ", ""),
	}
	for _, s := range candidates {
		if m := compactVDRe.FindStringSubmatch(s); len(m) == 3 {
			vol = atoiPos(m[1])
			discN = atoiPos(m[2])
			if vol > 0 {
				return vol, discN
			}
		}
	}
	spacedAll := spaced(volume) + " " + spaced(stem)
	return lastCap(volumeNumRe, spacedAll), lastCap(discNumRe, spacedAll)
}

// SearchQueries builds TMDB lookup strings from a volume label and filename.
func SearchQueries(volume, src string) []string {
	stem := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	vol, _ := ParseEdition(volume, src)
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		q := strings.Join(strings.Fields(strings.ReplaceAll(raw, "_", " ")), " ")
		if q == "" {
			return
		}
		key := strings.ToLower(q)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, q)
	}
	cleanVol := stripEdition(spaced(volume))
	cleanStem := stripEdition(spaced(stem))
	if vol > 0 {
		add(fmt.Sprintf("%s Volume %d", cleanVol, vol))
		add(fmt.Sprintf("%s Volume %d", cleanStem, vol))
	}
	add(cleanVol)
	add(cleanStem)
	add(volume)
	add(stem)
	return out
}

// WikiQuery is the Wikipedia search string: keep Volume N, drop Disc N.
func WikiQuery(volume, src string) string {
	vol, _ := ParseEdition(volume, src)
	raw := spaced(volume)
	if raw == "" {
		raw = spaced(strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)))
	}
	raw = compactVDRe.ReplaceAllString(raw, " ")
	raw = discOnlyRe.ReplaceAllString(raw, " ")
	raw = volumeNumRe.ReplaceAllString(raw, "Volume $1")
	raw = strings.Join(strings.Fields(raw), " ")
	if vol > 0 && !strings.Contains(strings.ToLower(raw), "volume ") {
		raw = strings.TrimSpace(raw + fmt.Sprintf(" Volume %d", vol))
	}
	return raw
}

// DiscNumber is the 1-based disc index from the volume label or filename.
func DiscNumber(volume, src string) int {
	_, n := ParseEdition(volume, src)
	if n < 1 {
		return 1
	}
	return n
}

// VolumeNumber is the set volume from vNdM / "Volume N", or 0 if unknown.
func VolumeNumber(volume, src string) int {
	n, _ := ParseEdition(volume, src)
	return n
}

// VolumeTitleScore is 1 when title names the same Volume N (Vol. 5, V5).
func VolumeTitleScore(title string, volume int) int {
	if volume < 1 {
		return 0
	}
	for _, m := range volumeTitleRe.FindAllStringSubmatch(title, -1) {
		n := atoiPos(m[1])
		if n == 0 {
			n = atoiPos(m[2])
		}
		if n == volume {
			return 1
		}
	}
	return 0
}

func stripEdition(s string) string {
	s = compactVDRe.ReplaceAllString(s, " ")
	s = discOnlyRe.ReplaceAllString(s, " ")
	s = volumeNumRe.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func lastCap(re *regexp.Regexp, s string) int {
	n := 0
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if v := atoiPos(m[1]); v > 0 {
			n = v
		}
	}
	return n
}

func atoiPos(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 {
		return 0
	}
	return v
}

func spaced(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "_", " ")), " ")
}
