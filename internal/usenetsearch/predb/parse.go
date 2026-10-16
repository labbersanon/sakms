package predb

import (
	"regexp"
	"strconv"
	"strings"
)

// Announce is one parsed PreDB channel line.
type Announce struct {
	Kind   string // NEW, NUKE, UNNUKE, MODNUKE, DEL, UNDEL
	Entry  Entry
	Reason string
}

var (
	reKind    = regexp.MustCompile(`(?i)^(NEW|PRE|NUKE|UNNUKE|MODNUKE|DEL|UNDEL)\b\s*:?\s*(.*)$`)
	reBracket = regexp.MustCompile(`\[([^\]]*)\]`)
	reIRCFmt  = regexp.MustCompile("\x03\\d{0,2}(?:,\\d{1,2})?|[\x02\x0f\x1d\x1f\x16]")
	reSize    = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(kb|mb|gb|tb)\b`)
	reFiles   = regexp.MustCompile(`(?i)\b(\d+)\s*(?:x|f|files?)\b`)
	reRelease = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._()+\-]*-[A-Za-z0-9_]+$`)
)

// ParseAnnounce parses an NNTmux-style PreDB announce line:
//
//	NEW: [CATEGORY] Release.Name-GRP [optional size / file-count brackets]
//	NUKE: Release.Name-GRP [reason]
//
// IRC colour/format codes are stripped first. Lines that do not carry a
// scene-style release token return ok=false.
func ParseAnnounce(line string) (Announce, bool) {
	line = strings.TrimSpace(reIRCFmt.ReplaceAllString(line, ""))
	m := reKind.FindStringSubmatch(line)
	if m == nil {
		return Announce{}, false
	}
	a := Announce{Kind: strings.ToUpper(m[1])}
	if a.Kind == "PRE" {
		a.Kind = "NEW"
	}
	rest := m[2]
	brackets := reBracket.FindAllStringSubmatch(rest, -1)
	plain := strings.Fields(reBracket.ReplaceAllString(rest, " "))
	for _, f := range plain {
		f = strings.Trim(f, "|:,")
		if len(f) >= 8 && reRelease.MatchString(f) && strings.ContainsAny(f, "._") {
			a.Entry.Title = f
			break
		}
	}
	if a.Entry.Title == "" {
		return Announce{}, false
	}
	for _, b := range brackets {
		txt := strings.TrimSpace(b[1])
		switch {
		case txt == "":
		case reSize.MatchString(txt) || reFiles.MatchString(txt):
			if sm := reSize.FindStringSubmatch(txt); sm != nil && a.Entry.SizeBytes == 0 {
				a.Entry.SizeBytes = sizeBytes(sm[1], sm[2])
			}
			if fm := reFiles.FindStringSubmatch(txt); fm != nil && a.Entry.Files == 0 {
				a.Entry.Files, _ = strconv.Atoi(fm[1])
			}
		case a.Entry.Category == "" && (a.Kind == "NEW"):
			a.Entry.Category = txt
		default:
			if a.Reason == "" {
				a.Reason = txt
			}
		}
	}
	a.Entry.Nuked = a.Kind == "NUKE" || a.Kind == "MODNUKE"
	return a, true
}

func sizeBytes(num, unit string) int64 {
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"kb": 1 << 10, "mb": 1 << 20, "gb": 1 << 30, "tb": 1 << 40}[strings.ToLower(unit)]
	return int64(v * mult)
}
