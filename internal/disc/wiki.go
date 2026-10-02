package disc

// Claude 2026-10-02: Wikipedia wikitext TOC for anthology DVDs.
// Reason: IFO/ffmpeg has duration and order, not cartoon names. OVID and
//   DVDID do not name shorts. Golden Collection pages list Disc N tables
//   in disc order (titles 2–16 on Vol 5 D1).
// Troubleshooting: empty TOC — heading must be "Disc N"; title is cell 2
//   of the first wikitable in that section (not From the Vaults).
// Review if: a disc fingerprint DB starts returning per-title names.

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	wikiDiscHead = regexp.MustCompile(`(?i)^disc\s+(\d+)\b`)
	wikiRowSplit = regexp.MustCompile(`\n\|-`)
)

// ParseDiscTOC returns cartoon titles from the Disc N wikitable, in row order.
func ParseDiscTOC(wikitext string, discN int) []string {
	if discN < 1 {
		discN = 1
	}
	section := wikiDiscSection(wikitext, discN)
	if section == "" {
		section = wikitext
	}
	table := firstWikiTable(section)
	if table == "" {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, row := range wikiRowSplit.Split(table, -1) {
		cells := wikiRowCells(row)
		if len(cells) < 2 {
			continue
		}
		if _, err := strconv.Atoi(cells[0]); err != nil {
			continue
		}
		title := cells[1]
		if title == "" || seen[strings.ToLower(title)] {
			continue
		}
		seen[strings.ToLower(title)] = true
		names = append(names, title)
	}
	return names
}

func wikiDiscSection(wikitext string, discN int) string {
	type head struct {
		start, end, level, n int
	}
	var heads []head
	offset := 0
	for _, line := range strings.Split(wikitext, "\n") {
		level, title, ok := wikiHeading(line)
		if ok {
			n := 0
			if m := wikiDiscHead.FindStringSubmatch(title); len(m) == 2 {
				n, _ = strconv.Atoi(m[1])
			}
			heads = append(heads, head{start: offset, end: offset + len(line), level: level, n: n})
		}
		offset += len(line) + 1
	}
	for i, h := range heads {
		if h.n != discN {
			continue
		}
		end := len(wikitext)
		for _, nxt := range heads[i+1:] {
			if nxt.level <= h.level {
				end = nxt.start
				break
			}
		}
		if h.end >= end {
			return ""
		}
		return wikitext[h.end:end]
	}
	return ""
}

func wikiHeading(line string) (int, string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "==") {
		return 0, "", false
	}
	open := 0
	for open < len(line) && line[open] == '=' {
		open++
	}
	close := 0
	for close < len(line) && line[len(line)-1-close] == '=' {
		close++
	}
	if open < 2 || close < 2 {
		return 0, "", false
	}
	n := open
	if close < n {
		n = close
	}
	if n >= len(line)-n {
		return 0, "", false
	}
	title := strings.TrimSpace(line[n : len(line)-n])
	if title == "" {
		return 0, "", false
	}
	return n, title, true
}

func firstWikiTable(s string) string {
	i := strings.Index(s, "{|")
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], "\n|}")
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}

func wikiRowCells(row string) []string {
	var cells []string
	for _, line := range strings.Split(row, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "{|" || strings.HasPrefix(line, "{|") {
			continue
		}
		if strings.HasPrefix(line, "!") {
			continue
		}
		if !strings.HasPrefix(line, "|") {
			continue
		}
		if line == "|-" || strings.HasPrefix(line, "|+") || line == "|}" {
			continue
		}
		inner := strings.TrimSpace(strings.TrimPrefix(line, "|"))
		if inner == "" {
			continue
		}
		title := wikiCellTitle(inner)
		if title == "" {
			continue
		}
		cells = append(cells, title)
	}
	return cells
}

func wikiCellTitle(cell string) string {
	cell = stripWikiTemplates(cell)
	cell = strings.ReplaceAll(cell, "'''", "")
	cell = strings.ReplaceAll(cell, "''", "")
	if i := strings.Index(cell, "[["); i >= 0 {
		rest := cell[i+2:]
		j := strings.Index(rest, "]]")
		if j >= 0 {
			inner := rest[:j]
			if k := strings.LastIndex(inner, "|"); k >= 0 {
				inner = inner[k+1:]
			}
			return strings.TrimSpace(inner)
		}
	}
	cell = strings.TrimSpace(cell)
	if cell == "" || strings.EqualFold(cell, "n/a") {
		return ""
	}
	if _, err := strconv.Atoi(cell); err == nil {
		return cell
	}
	if strings.EqualFold(cell, "lt") || strings.EqualFold(cell, "mm") {
		return ""
	}
	return cell
}

func stripWikiTemplates(s string) string {
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			return s
		}
		depth := 0
		end := -1
		for p := i; p < len(s)-1; p++ {
			if s[p] == '{' && s[p+1] == '{' {
				depth++
				p++
				continue
			}
			if s[p] == '}' && s[p+1] == '}' {
				depth--
				p++
				if depth == 0 {
					end = p + 1
					break
				}
			}
		}
		if end < 0 {
			return s[:i]
		}
		s = s[:i] + s[end:]
	}
}
