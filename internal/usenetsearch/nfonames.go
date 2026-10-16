package usenetsearch

import (
	"regexp"
	"strings"
)

// sceneNameRe matches dotted scene names ("Title.Words.2021.1080p.WEB.x264-GRP"):
// at least two dots and a hyphen (group tail), no spaces.
var sceneNameRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9_()-]*(?:\.[A-Za-z0-9_()-]+){2,}`)

// nfoSceneNames extracts scene-name candidates from an NFO head, in order of
// appearance. Callers still validate each (NFO-only names need a PreDB hit).
func nfoSceneNames(data []byte) []string {
	if len(data) > 64<<10 {
		data = data[:64<<10]
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range sceneNameRe.FindAllString(string(data), -1) {
		m = strings.Trim(m, ".-")
		if !strings.Contains(m, "-") {
			continue
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
		if len(out) >= 5 {
			break
		}
	}
	return out
}
