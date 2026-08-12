package identify

import (
	"net/url"
	"strings"
)

// Claude 2026-08-12: accept schemeless pasted catalog URLs.
// Reason: SearchTakeover routes known catalog domains without requiring
// "https://", so the deterministic catalog lookup must parse the same input.
// Troubleshooting: "stashdb.org/scenes/<uuid>" should not fall through to the
// AI-required arbitrary URL path.
// Review if: the frontend stops accepting schemeless catalog domains.
func parseCatalogURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err == nil && u.Host != "" {
		return u, nil
	}
	if !strings.Contains(trimmed, "://") {
		return url.Parse("https://" + trimmed)
	}
	return u, err
}

// ParseCatalogSceneURL recognizes stash-box and TPDB scene/movie page URLs.
// Returns box ("stashdb"|"fansdb"|"tpdb"), the catalog id or slug, and whether
// the path is a TPDB movie (vs scene). Non-catalog URLs return ok=false.
func ParseCatalogSceneURL(raw string) (box, sceneID string, isMovie bool, ok bool) {
	u, err := parseCatalogURL(raw)
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
