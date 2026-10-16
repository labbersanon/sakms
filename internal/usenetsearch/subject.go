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
	// FileN/FileM carry the file counter some posters prefix (0 when absent).
	FileN int
	FileM int
}

var (
	rePartNM = regexp.MustCompile(`(?i)\(\s*(\d+)\s*/\s*(\d+)\s*\)`)
	reFileNM = regexp.MustCompile(`\[\s*(\d+)\s*/\s*(\d+)\s*\]`)
	// Claude 2026-10-16: NNTmux getFileCount shapes ([01/74], (72/93), 1 of 50, 1-50).
	// Reason: named ReleaseName used to keep [k/N] / (k/N) in the stem, so a
	//   73-file episode became 73 keys at ~1% complete.
	// Troubleshooting: Decker [48/73] vs [42/73] still split → stem still
	//   contains the file token; Bhool (72/93) in parens is this regex, not reFileNM.
	// Review if: PAR2 file lists replace [k/N] as the file counter.
	// Related files: .omc/plans/nntp-collection-grouping.md
	reFileCount = regexp.MustCompile(`(?i)(?:^|[\[(\s])(\d{1,5})(?:/|[\s_]of[\s_]|-)(\d{1,5})(?:[\])\s:]|$)`)
	reYencSz    = regexp.MustCompile(`(?i)yEnc\s*(?:\(.*?\)\s*)?(?:\[?\s*)?(\d+)\s*(?:bytes?)?`)
	reFileTok   = regexp.MustCompile(`(?i)([\w.\-]+\.(?:rar|r\d{2}|part\d+\.rar|par2|nfo|sfv|mkv|mp4|avi|zip|7z))`)
	reHexBlob   = regexp.MustCompile(`(?i)^[a-f0-9]{16,}$`)
	// Claude 2026-10-16: small NNTmux collection-regex subset (not the 4.8k table).
	// Reason: [#a.b.teevee] / [Group] wrappers and leading hash tokens are how
	//   NNTmux clusters files whose quoted names differ; generic stem alone
	//   keeps the wrapper or hashes per-file random names.
	// Troubleshooting: teevee stems still start with [#a.b. → reGroupTag;
	//   ah63jk…[01/22] vs [22/22] split → leadCollectionToken.
	// Review if: a second group-specific pattern is needed beyond these three.
	reGroupTag     = regexp.MustCompile(`(?i)^\[(?:#?a\.b\.[a-z0-9.+\-]+|[A-Za-z][^\[\]]{0,40})\]\s*`)
	reLeadHashDash = regexp.MustCompile(`^([A-Za-z0-9]{16,})\s*-\s*[\[(]\d{1,5}/\d{1,5}`)
	reLeadHashGlue = regexp.MustCompile(`^([A-Za-z0-9]{16,})\[\d{1,5}/\d{1,5}\]`)
)

// nntpUTF8 replaces invalid UTF-8 (latin1/CP1252 NNTP headers) with U+FFFD.
func nntpUTF8(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}

// ParseSubject extracts release identity from an overview subject.
func ParseSubject(subj string) ParsedSubject {
	return parseSubjectWith(subj, extraPatternsLive())
}

func parseSubjectWith(subj string, extra []compiledExtra) ParsedSubject {
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
	out.PartN, out.PartM = yencParts(s)
	out.FileN, out.FileM = fileCount(s, out.PartN, out.PartM)
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
	if tok := leadCollectionTokenWith(s, extra); tok != "" && tokenLooksObfuscated(tok) {
		out.Obfuscated = true
		return out
	}
	name := collectionStemWith(s, out.Filename, extra)
	if reHexBlob.MatchString(name) || (len(name) >= 20 && mostlyHex(name)) {
		out.Obfuscated = true
		return out
	}
	if name == "" || len(name) < 4 {
		return out
	}
	out.Name = name
	out.OK = !out.Obfuscated && (out.PartM > 0 || out.Filename != "" || strings.Contains(name, ".") || strings.Contains(name, "-"))
	return out
}

// collectionStem is the named grouping key: the subject with yEnc, file/part
// counters, and the quoted filename removed. [k/N] is FileN/FileM, not identity.
func collectionStem(subj, filename string) string {
	return collectionStemWith(subj, filename, extraPatternsLive())
}

func collectionStemWith(subj, filename string, extra []compiledExtra) string {
	s := subj
	if i := strings.Index(strings.ToLower(s), "yenc"); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = reGroupTag.ReplaceAllString(s, "")
	s = applyStripPrefix(s, extra)
	if i := strings.Index(s, `"`); i >= 0 {
		if j := strings.LastIndex(s, `"`); j > i {
			s = strings.TrimSpace(s[:i] + " " + s[j+1:])
		}
	}
	s = rePartNM.ReplaceAllString(s, " ")
	s = reFileNM.ReplaceAllString(s, " ")
	s = reFileCount.ReplaceAllString(s, " ")
	if filename != "" {
		if i := strings.Index(strings.ToLower(s), strings.ToLower(filename)); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
	}
	s = reObfSuffix.ReplaceAllString(s, "")
	s = strings.TrimRight(s, "]")
	s = strings.Trim(s, "-_ [")
	return collapseWS(s)
}

// leadCollectionToken is the NNTmux hash-token capture: a 16+ char token
// immediately before [k/N] or (k/N), optionally glued (t2EI3…[08/52]).
func leadCollectionToken(subj string) string {
	return leadCollectionTokenWith(subj, extraPatternsLive())
}

func leadCollectionTokenWith(subj string, extra []compiledExtra) string {
	s := strings.TrimSpace(subj)
	for _, e := range extra {
		if extraKind(e.Kind) != extraLeadToken {
			continue
		}
		if m := e.re.FindStringSubmatch(s); len(m) >= 2 && m[1] != "" {
			return m[1]
		}
	}
	if m := reLeadHashGlue.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	if m := reLeadHashDash.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	return ""
}

func applyStripPrefix(s string, extra []compiledExtra) string {
	for _, e := range extra {
		if extraKind(e.Kind) == extraStripPrefix {
			s = e.re.ReplaceAllString(s, "")
		}
	}
	return strings.TrimSpace(s)
}

func tokenLooksObfuscated(tok string) bool {
	return looksObfuscated(tok) || mostlyHex(tok)
}

// yencParts returns the trailing yEnc (n/m) segment counter. Prefers a match
// after the yEnc keyword so an earlier (72/93) file counter is not stolen.
func yencParts(s string) (n, m int) {
	region := s
	if i := strings.Index(strings.ToLower(s), "yenc"); i >= 0 {
		region = s[i:]
	}
	all := rePartNM.FindAllStringSubmatch(region, -1)
	if len(all) == 0 && region != s {
		all = rePartNM.FindAllStringSubmatch(s, -1)
	}
	if len(all) == 0 {
		return 0, 0
	}
	last := all[len(all)-1]
	n, _ = strconv.Atoi(last[1])
	m, _ = strconv.Atoi(last[2])
	return n, m
}

// fileCount prefers the last [k/N], else the last NNTmux-shaped counter that
// is not the yEnc part pair.
func fileCount(s string, partN, partM int) (n, m int) {
	if all := reFileNM.FindAllStringSubmatch(s, -1); len(all) > 0 {
		last := all[len(all)-1]
		n, _ = strconv.Atoi(last[1])
		m, _ = strconv.Atoi(last[2])
		return n, m
	}
	yencAt := strings.Index(strings.ToLower(s), "yenc")
	all := reFileCount.FindAllStringSubmatchIndex(s, -1)
	for i := len(all) - 1; i >= 0; i-- {
		loc := all[i]
		fn, _ := strconv.Atoi(s[loc[2]:loc[3]])
		fm, _ := strconv.Atoi(s[loc[4]:loc[5]])
		if yencAt >= 0 && loc[0] >= yencAt && fn == partN && fm == partM {
			continue
		}
		return fn, fm
	}
	return 0, 0
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

// minObfSkeleton is the shortest skeleton that may be hashed into ObfKey.
// Shorter values (including empty after suffix strip) would collapse every
// obfuscated post from one poster in a group into a single mega-bucket.
const minObfSkeleton = 8

// obfSkeleton returns the grouping skeleton of an overview subject (case kept
// so looksObfuscated can see case churn; ObfKey lowercases before hashing).
func obfSkeleton(subj string) string {
	return obfSkeletonWith(subj, extraPatternsLive())
}

func obfSkeletonWith(subj string, extra []compiledExtra) string {
	if tok := leadCollectionTokenWith(subj, extra); tok != "" {
		return tok
	}
	s := subj
	if i := strings.Index(strings.ToLower(s), "yenc"); i > 0 {
		s = s[:i]
	}
	s = applyStripPrefix(s, extra)
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
	return obfKeyWith(group, from, subj, extraPatternsLive())
}

func obfKeyWith(group, from, subj string, extra []compiledExtra) string {
	// Claude 2026-10-16: totalFiles is NOT mixed into this hash.
	// Reason: plan 2A wanted sha1(name+totalFiles) like NNTmux collections, but
	//   sakms hashes each header independently. PAR2/NFO subjects often omit
	//   [k/N], so FileM=0 vs FileM=46 would split one upload. FileM still lives
	//   on the header for completeness. Mega-buckets are prevented by refusing
	//   skeletons shorter than minObfSkeleton (see stageObfuscated).
	// Troubleshooting: two same-poster hashes colliding → skeleton identical;
	//   empty-skeleton bluray 100k-seg set → stageObfuscated must no-op.
	// Review if: a collection row caches totalFiles so two same-token uploads
	//   with different file counts stay separate.
	sum := sha256.Sum256([]byte(group + "\x00" + from + "\x00" + strings.ToLower(obfSkeletonWith(subj, extra))))
	return obfKeyPrefix + hex.EncodeToString(sum[:8])
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
