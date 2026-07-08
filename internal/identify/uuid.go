package identify

import "regexp"

var uuidRe = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// ExtractUUID finds a stash-box scene UUID embedded in a filename or folder
// name (e.g. a folder named "... - StashDB [a29768db-b3cd-4a71-a75e-4294373207bb]").
// Returns ("", false) if none found.
func ExtractUUID(s string) (string, bool) {
	m := uuidRe.FindString(s)
	return m, m != ""
}
