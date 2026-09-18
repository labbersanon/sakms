package usenetsearch

import (
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
}

var (
	rePartNM  = regexp.MustCompile(`(?i)\(\s*(\d+)\s*/\s*(\d+)\s*\)`)
	reYencSz  = regexp.MustCompile(`(?i)yEnc\s*(?:\(.*?\)\s*)?(?:\[?\s*)?(\d+)\s*(?:bytes?)?`)
	reFileTok = regexp.MustCompile(`(?i)([\w.\-]+\.(?:rar|r\d{2}|part\d+\.rar|par2|nfo|sfv|mkv|mp4|avi|zip|7z))`)
	reHexBlob = regexp.MustCompile(`(?i)^[a-f0-9]{16,}$`)
)

// ParseSubject extracts release identity from an overview subject.
func ParseSubject(subj string) ParsedSubject {
	s := strings.TrimSpace(subj)
	if s == "" {
		return ParsedSubject{}
	}
	out := ParsedSubject{}
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
