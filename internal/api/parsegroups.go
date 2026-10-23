package api

import "strings"

// parseGroupList splits a newline/comma-separated list. Used by blocked
// release groups, unpack passwords, and preferred grab languages.
func parseGroupList(raw string) []string {
	raw = strings.ReplaceAll(raw, ",", "\n")
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		g := strings.TrimSpace(line)
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	return out
}
