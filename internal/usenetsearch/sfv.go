package usenetsearch

import (
	"regexp"
	"strings"
)

// Claude 2026-10-20: SFV inner names + CRC32 (nZEDb/srrdb rename path).
// Reason: obfuscated hash.par2 sets often still ship a tiny .sfv whose
//   payload filename is the scene name and whose CRC is srrdb archive-crc.
// Troubleshooting: BODY cands=0 on an .sfv article → this parser skipped
//   comments or a Windows path. Review if: CRC32 is hex with 0x prefix.
// Related files: namefix.go candidatesFromSample, srrdb.go

type sfvEntry struct {
	Name string
	CRC  string // 8 uppercase hex
}

var reSFVLine = regexp.MustCompile(`(?i)^(?:\./)?(?:.*[/\\])?([^;\s][^/\\]*\S)\s+([0-9a-f]{8})\s*$`)

func sfvEntries(data []byte) []sfvEntry {
	if len(data) == 0 || len(data) > 64<<10 || !looksSFVText(data) {
		return nil
	}
	var out []sfvEntry
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		m := reSFVLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := strings.TrimSpace(m[1])
		crc := strings.ToUpper(m[2])
		if name == "" || crc == "00000000" {
			continue
		}
		key := strings.ToLower(name) + "\x00" + crc
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, sfvEntry{Name: name, CRC: crc})
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func sfvCRCs(data []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range preferredSFVEntries(sfvEntries(data)) {
		if seen[e.CRC] {
			continue
		}
		seen[e.CRC] = true
		out = append(out, e.CRC)
	}
	return out
}

func looksSFVText(data []byte) bool {
	ctrl := 0
	for _, b := range data {
		if b == 0 {
			return false
		}
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			ctrl++
		}
	}
	return ctrl*8 < len(data)+1
}

func preferredSFVEntries(in []sfvEntry) []sfvEntry {
	if len(in) <= 1 {
		return in
	}
	var payload, rest []sfvEntry
	for _, e := range in {
		low := strings.ToLower(e.Name)
		switch {
		case strings.HasSuffix(low, ".nfo"), strings.HasSuffix(low, ".sfv"),
			strings.HasSuffix(low, ".srr"), strings.HasSuffix(low, ".jpg"),
			strings.HasSuffix(low, ".jpeg"), strings.HasSuffix(low, ".png"),
			strings.HasSuffix(low, ".txt"):
			rest = append(rest, e)
		default:
			payload = append(payload, e)
		}
	}
	return append(payload, rest...)
}
