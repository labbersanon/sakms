package usenetsearch

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ParsedSubject is the usable subset of an NNTP subject line.
type ParsedSubject struct {
	Name       string
	PartN      int
	PartM      int
	Filename   string
	YencSize   int64
	Obfuscated bool
	Meta       bool // par2 / nfo / sfv
	OK         bool
	// FileN/FileM carry the "[k/N]" file counter some posters prefix (0 when absent).
	FileN int
	FileM int
}

var (
	rePartNM  = regexp.MustCompile(`(?i)\(\s*(\d+)\s*/\s*(\d+)\s*\)`)
	reFileNM  = regexp.MustCompile(`\[\s*(\d+)\s*/\s*(\d+)\s*\]`)
	reYencSz  = regexp.MustCompile(`(?i)yEnc\s*(?:\(.*?\)\s*)?(?:\[?\s*)?(\d+)\s*(?:bytes?)?`)
	reFileTok = regexp.MustCompile(`(?i)([\w.\-]+\.(?:rar|r\d{2}|part\d+\.rar|par2|nfo|sfv|mkv|mp4|avi|zip|7z))`)
	reHexBlob = regexp.MustCompile(`(?i)^[a-f0-9]{16,}$`)
)

// nntpUTF8 replaces invalid UTF-8 (latin1/CP1252 NNTP headers) with U+FFFD.
func nntpUTF8(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}

// ParseSubject extracts release identity from an overview subject.
func ParseSubject(subj string) ParsedSubject {
	// Claude 2026-10-16: sanitize before ToLower+slice.
	// Reason: ToLower expands each invalid byte to U+FFFD (3 bytes); slicing
	//   the original with that index panics ([:80] with length 78 in prod).
	// Troubleshooting: crawl loop panic in ParseSubject → latin1 subject before yEnc.
	// Review if: Overview() already returns valid UTF-8.
	s := strings.TrimSpace(nntpUTF8(subj))
	if s == "" {
		return ParsedSubject{}
	}
	out := ParsedSubject{}
	if m := reFileNM.FindStringSubmatch(s); len(m) == 3 {
		out.FileN, _ = strconv.Atoi(m[1])
		out.FileM, _ = strconv.Atoi(m[2])
	}
	core := s
	if i := strings.Index(strings.ToLower(core), "yenc"); i > 0 {
		core = strings.TrimSpace(core[:i])
	}
	core = strings.Trim(core, "[]() \"'")
	if reHexBlob.MatchString(core) || (len(core) >= 20 && mostlyHex(core)) {
		out.Obfuscated = true
		return out
	}
	if m := rePartNM.FindStringSubmatch(s); len(m) == 3 {
		out.PartN, _ = strconv.Atoi(m[1])
		out.PartM, _ = strconv.Atoi(m[2])
	}
	if m := reYencSz.FindStringSubmatch(s); len(m) == 2 {
		out.YencSize, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if m := reFileTok.FindStringSubmatch(s); len(m) == 2 {
		out.Filename = m[1]
		ext := strings.ToLower(filepath.Ext(out.Filename))
		base := strings.ToLower(out.Filename)
		if ext == ".par2" || ext == ".nfo" || ext == ".sfv" || strings.Contains(base, ".par2") {
			out.Meta = true
		}
	}
	name := core
	if out.Filename != "" {
		if i := strings.Index(strings.ToLower(name), strings.ToLower(out.Filename)); i > 0 {
			name = strings.TrimSpace(name[:i])
		}
	}
	name = strings.Trim(name, "[]()-_ \"'")
	name = collapseWS(name)
	if name == "" || len(name) < 4 {
		return out
	}
	out.Name = name
	out.OK = !out.Obfuscated && (out.PartM > 0 || out.Filename != "" || strings.Contains(name, ".") || strings.Contains(name, "-"))
	return out
}

// Claude 2026-10-15: obfuscated-set grouping skeleton (plan D3 step 1).
// Reason: obfuscated posts have no release name, so volumes of one upload are
//   grouped by (group, poster, subject skeleton): the quoted filename with the
//   segment counter "(n/m)", file counter "[k/N]", and volume/extension
//   suffixes (.partNN.rar, .rNN, .7z.NNN, .volNN+NN.par2, .par2) removed.
// Troubleshooting: one upload splitting into many staged sets → compare the
//   skeletons of its subjects; per-file random names are not clustered (v1).
// Review if: posters start varying the hash base between volumes.

var reObfSuffix = regexp.MustCompile(`(?i)(?:\.vol\d+\+\d+\.par2|\.part\d+\.rar|\.r\d{2,3}|\.7z\.\d{3}|\.z\d{2}|\.par2|\.rar|\.zip|\.7z|\.nfo|\.sfv|\.mkv|\.mp4|\.avi|\.nzb)+$`)

// obfSkeleton returns the grouping skeleton of an overview subject (case kept
// so looksObfuscated can see case churn; ObfKey lowercases before hashing).
func obfSkeleton(subj string) string {
	s := subj
	if i := strings.Index(strings.ToLower(s), "yenc"); i > 0 {
		s = s[:i]
	}
	s = rePartNM.ReplaceAllString(s, "")
	s = reFileNM.ReplaceAllString(s, "")
	if i := strings.Index(s, `"`); i >= 0 {
		if j := strings.LastIndex(s, `"`); j > i {
			s = s[i+1 : j]
		}
	}
	s = strings.Trim(s, "[]() -_\"'")
	s = reObfSuffix.ReplaceAllString(s, "")
	return collapseWS(s)
}

// ObfKey returns the staged-set key ("obf:<16-hex>") for one subject.
func ObfKey(group, from, subj string) string {
	sum := sha256.Sum256([]byte(group + "\x00" + from + "\x00" + strings.ToLower(obfSkeleton(subj))))
	return obfKeyPrefix + hex.EncodeToString(sum[:8])
}

// subjectCounters returns the (n/m) segment counter that ParseSubject skips
// for obfuscated subjects.
func subjectCounters(subj string) (partN, partM int) {
	if m := rePartNM.FindStringSubmatch(subj); len(m) == 3 {
		partN, _ = strconv.Atoi(m[1])
		partM, _ = strconv.Atoi(m[2])
	}
	return partN, partM
}

// hashCore returns the skeleton when it is a bare md5 (32) or sha1 (40) hex
// string — the form hash-obfuscating posters use for md5/sha1(title).
func hashCore(skeleton string) string {
	if (len(skeleton) == 32 || len(skeleton) == 40) && reHexBlob.MatchString(skeleton) {
		return strings.ToLower(skeleton)
	}
	return ""
}

// looksObfuscated flags a separator-free, long, case/digit-churning skeleton
// (random alphanumerics) that ParseSubject's hex check misses. Conservative:
// readable release names carry dots/dashes/spaces or few case/digit switches.
func looksObfuscated(skeleton string) bool {
	if len(skeleton) < 16 || strings.ContainsAny(skeleton, ".-_ ") {
		return false
	}
	switches := 0
	prev := rune(0)
	for _, r := range skeleton {
		// Upper→lower is an ordinary capitalised word ("Matrix"), not churn.
		if prev != 0 && charClass(prev) != charClass(r) && !(charClass(prev) == 3 && charClass(r) == 2) {
			switches++
		}
		prev = r
	}
	return switches*4 >= len(skeleton)
}

func charClass(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return 1
	case r >= 'a' && r <= 'z':
		return 2
	case r >= 'A' && r <= 'Z':
		return 3
	default:
		return 0
	}
}

func mostlyHex(s string) bool {
	hex := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			hex++
		}
	}
	return float64(hex)/float64(len(s)) >= 0.85
}

func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// IsMetaSubject reports PAR2/NFO/SFV-like subjects (aligned with usenet.isMetaSubject intent).
func IsMetaSubject(subj, filename string) bool {
	low := strings.ToLower(subj + " " + filename)
	return strings.Contains(low, ".par2") || strings.Contains(low, ".nfo") || strings.Contains(low, ".sfv")
}
