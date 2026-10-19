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
	// Claude 2026-10-18: #PreNNTmux / nZEDb tagged bot lines (TT/CT/SZ/FL).
	// Reason: TmuxPREBot does not use NEW: [section] Title-GRP; the channel
	//   topic documents DT/TT/SC/CT/RQ/SZ/FL/FN tags. Live keepalive is
	//   INFO: [ts …] with no TT and must not parse.
	// Troubleshooting: parsed=0 while PRIVMSG traffic exists → line has no
	//   [TT:] and no NEW:/PRE: prefix.
	// Review if: TmuxPREBot changes its tag letters.
	reTaggedTT   = regexp.MustCompile(`(?i)\[TT:\s*([^\]]+)\]`)
	reTaggedCT   = regexp.MustCompile(`(?i)\[CT:\s*([^\]]+)\]`)
	reTaggedSZ   = regexp.MustCompile(`(?i)\[SZ:\s*([^\]]+)\]`)
	reTaggedFL   = regexp.MustCompile(`(?i)\[FL:\s*([^\]]+)\]`)
	reTaggedFN   = regexp.MustCompile(`(?i)\[FN:\s*([^\]]+)\]`)
	reTaggedRQ   = regexp.MustCompile(`(?i)\[RQ:\s*([^\]]+)\]`)
	reTaggedNuke = regexp.MustCompile(`(?i)\[((?:UN|MOD|RE|OLD)?NUKE(?:D)?):\s*([^\]]*)\]`)
)

// ParseAnnounce parses an NNTmux-style PreDB announce line:
//
//	NEW: [CATEGORY] Release.Name-GRP [optional size / file-count brackets]
//	NUKE: Release.Name-GRP [reason]
//
// or a tagged nZEDb/TmuxPREBot line:
//
//	[DT: …] [TT: Release.Name-GRP] [SC: …] [CT: TV] [SZ: 800MB] [FL: 12] [FN: file.rar]
//
// IRC colour/format codes are stripped first. Lines that do not carry a
// scene-style release token return ok=false.
func ParseAnnounce(line string) (Announce, bool) {
	line = strings.TrimSpace(reIRCFmt.ReplaceAllString(line, ""))
	if a, ok := parseTaggedAnnounce(line); ok {
		return a, true
	}
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

func taggedNA(line string, re *regexp.Regexp) string {
	m := re.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	s := strings.TrimSpace(m[1])
	if s == "" || strings.EqualFold(s, "N/A") {
		return ""
	}
	return s
}

func parseTaggedAnnounce(line string) (Announce, bool) {
	title := taggedNA(line, reTaggedTT)
	if title == "" {
		return Announce{}, false
	}
	a := Announce{Kind: "NEW", Entry: Entry{Title: title}}
	if cat := taggedNA(line, reTaggedCT); cat != "" {
		a.Entry.Category = cat
	}
	if sz := reTaggedSZ.FindStringSubmatch(line); sz != nil {
		raw := strings.TrimSpace(sz[1])
		if sm := reSize.FindStringSubmatch(raw); sm != nil {
			a.Entry.SizeBytes = sizeBytes(sm[1], sm[2])
		}
	}
	if fl := reTaggedFL.FindStringSubmatch(line); fl != nil {
		raw := strings.TrimSpace(fl[1])
		if n, err := strconv.Atoi(raw); err == nil {
			a.Entry.Files = n
		} else if fm := reFiles.FindStringSubmatch(raw); fm != nil {
			a.Entry.Files, _ = strconv.Atoi(fm[1])
		}
	}
	if fn := taggedNA(line, reTaggedFN); fn != "" {
		a.Entry.Filename = fn
	}
	if rq := taggedNA(line, reTaggedRQ); rq != "" {
		if n, err := strconv.ParseInt(rq, 10, 64); err == nil && n > 0 {
			a.Entry.RequestID = n
		}
	}
	if nk := reTaggedNuke.FindStringSubmatch(line); nk != nil {
		tag := strings.ToUpper(strings.TrimSpace(nk[1]))
		switch {
		case strings.HasPrefix(tag, "UN"):
			a.Kind = "UNNUKE"
		case strings.HasPrefix(tag, "MOD"):
			a.Kind = "MODNUKE"
		default:
			a.Kind = "NUKE"
		}
		a.Reason = strings.TrimSpace(nk[2])
		a.Entry.Nuked = a.Kind == "NUKE" || a.Kind == "MODNUKE"
	}
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
