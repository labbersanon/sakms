package identify

import (
	"net/url"
	"strings"
)

// ParseCatalogSceneURL recognizes stash-box and TPDB scene/movie page URLs.
// Returns box ("stashdb"|"fansdb"|"tpdb"), the catalog id or slug, and whether
// the path is a TPDB movie (vs scene). Non-catalog URLs return ok=false.
func ParseCatalogSceneURL(raw string) (box, sceneID string, isMovie bool, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", "", false, false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "stashdb.org", "www.stashdb.org":
		box = "stashdb"
	case "fansdb.cc", "www.fansdb.cc":
		box = "fansdb"
	case "theporndb.net", "www.theporndb.net":
		box = "tpdb"
	default:
		return "", "", false, false
	}

	path := strings.Trim(u.EscapedPath(), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", "", false, false
	}
	switch parts[0] {
	case "scenes":
		id := strings.TrimSpace(parts[1])
		if id == "" {
			return "", "", false, false
		}
		if box == "tpdb" {
			return box, id, false, true
		}
		if _, uuidOK := ExtractUUID(id); uuidOK {
			return box, id, false, true
		}
		return "", "", false, false
	case "movies":
		if box != "tpdb" {
			return "", "", false, false
		}
		id := strings.TrimSpace(parts[1])
		if id == "" {
			return "", "", false, false
		}
		return box, id, true, true
	default:
		return "", "", false, false
	}
}
