package predb

import (
	"regexp"
	"strings"
	"unicode"
)

// Claude 2026-10-22: NNTmux FileNameCleaner subset for match:prefiles.
// Reason: sakms only did exact md5/title/filename equality; NNTmux cleans
//   yEnc/NZB names then searches PreDB title+filename. Random ngPost stems
//   stay hashed (LooksLikeHashed) and still need [FN:] / FileDesc.
// Troubleshooting: hash_hits=0 with a scene-shaped .mkv in headers →
//   CleanForMatching emptied it.
// Review if: pg_trgm Search is added (then keep this cleaner as the query).

var (
	reArchiveExt = regexp.MustCompile(`(?i)(\.part\d{1,4}\.rar|\.r\d{2,4}|\.rar|\.zip|\.7z\.\d{3}|\.7z|\.vol\d+[+\-]\d+\.par2?|\.par2?|\.(tar|gz|bz2|xz))$`)
	reMediaExt   = regexp.MustCompile(`(?i)\.(mkv|avi|mp4|m4v|wmv|mpg|mpeg|mov|ts|m2ts|vob|nfo|sfv|nzb|srr|srs|jpg|png|txt|iso)$`)
	rePartVol    = regexp.MustCompile(`(?i)[.\-_]?(part|vol|cd|dvd|disc|disk)\d*$`)
	reSampleSfx  = regexp.MustCompile(`(?i)[.\-_](sample|proof|subs?|thumbs?|cover)[.\-_]?$`)
	reHexName    = regexp.MustCompile(`(?i)^[a-f0-9]{16,}$`)
	reHashTok    = regexp.MustCompile(`(?i)(?:^|[^a-f0-9])([a-f0-9]{40}|[a-f0-9]{32})(?:[^a-f0-9]|$)`)
)

// CleanForMatching strips archive/media/part noise the way NNTmux does
// before match:prefiles. Empty means the name is not searchable.
func CleanForMatching(fileName string) string {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" || strings.HasPrefix(fileName, ".") {
		return ""
	}
	if i := strings.LastIndexAny(fileName, `/\`); i >= 0 {
		fileName = fileName[i+1:]
	}
	fileName = reSampleSfx.ReplaceAllString(fileName, "")
	fileName = reArchiveExt.ReplaceAllString(fileName, "")
	fileName = reMediaExt.ReplaceAllString(fileName, "")
	fileName = rePartVol.ReplaceAllString(fileName, "")
	fileName = strings.Trim(fileName, " \t.-_")
	if fileName == "" || looksLikeStructuralJunk(fileName) {
		return ""
	}
	return fileName
}

// LooksLikeHashed reports a hex blob or separator-free random token.
func LooksLikeHashed(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	base := CleanForMatching(name)
	if base == "" {
		base = name
	}
	if reHexName.MatchString(base) {
		return true
	}
	if len(base) < 16 || strings.ContainsAny(base, ".-_ ") {
		return false
	}
	return mixedChurn(base)
}

// HashTokens returns isolated 32/40-hex strings (md5/sha1 of a PreDB title).
func HashTokens(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reHashTok.FindAllStringSubmatch(s, 8) {
		h := strings.ToLower(m[1])
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

func looksLikeStructuralJunk(fileName string) bool {
	n := strings.ToLower(strings.Trim(fileName, " .-_"))
	if n == "" || n == "audio_ts" || n == "video_ts" {
		return true
	}
	if len(n) > 3 {
		return false
	}
	for _, r := range n {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func mixedChurn(s string) bool {
	switches := 0
	var prev kind
	for i, r := range s {
		k := kindOf(r)
		if i > 0 && k != prev {
			switches++
		}
		prev = k
	}
	return switches >= 6
}

type kind int

const (
	kindOther kind = iota
	kindLower
	kindUpper
	kindDigit
)

func kindOf(r rune) kind {
	switch {
	case unicode.IsLower(r):
		return kindLower
	case unicode.IsUpper(r):
		return kindUpper
	case unicode.IsDigit(r):
		return kindDigit
	default:
		return kindOther
	}
}
