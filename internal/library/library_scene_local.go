package library

import "strings"

// LocalSceneBox is the reserved library_scenes.box value for a scene that has
// a file and an operator-confirmed name but no catalog identity yet (a
// "Reviewed" scene). The local identity scheme is described in full in the
// implementation plan (.omc/plans/autopilot-impl-adult-rename-review-alts.md §2).
//
// RESERVATION: internal/stashboxdb reserves this name explicitly so an
// operator who tries to create a stash-box database literally named "local"
// gets ErrNameReserved rather than the confusing ErrNameHaunted. See
// stashboxdb/store.go — the reservation there uses a package-local duplicate
// of this string to avoid an import cycle (stashboxdb → library → mode →
// stashboxdb); the two must stay in sync. A test in stashboxdb/store_test.go
// and library_scene_local_test.go both assert the string values are equal.
const LocalSceneBox = "local"

const localScenePHashPrefix = "phash:"

// LocalSceneID builds the scene_id for a local scene from its perceptual hash.
// The result is "phash:<hash>". The prefix is self-describing and leaves room
// for future non-phash local key kinds (sha256:, size-dur:) without ambiguity.
func LocalSceneID(phash string) string {
	return localScenePHashPrefix + phash
}

// IsLocalScene reports whether box is the local box name. Exact match, not
// prefix-based — there is exactly one reserved value and prefix matching would
// make "localized" or "locally-sourced" collide.
func IsLocalScene(box string) bool {
	return box == LocalSceneBox
}

// LocalScenePHash strips the "phash:" prefix from sceneID and returns the
// underlying hash and true. Returns ("", false) for any id that does not have
// the prefix — i.e. a real catalog scene_id such as a stash-box UUID.
func LocalScenePHash(sceneID string) (string, bool) {
	after, ok := strings.CutPrefix(sceneID, localScenePHashPrefix)
	if !ok || after == "" {
		return "", false
	}
	return after, true
}
