package library

import "testing"

// Asserts LocalSceneBox == "local" as a value — the stashboxdb package
// maintains a duplicate constant (reservedBoxLocal) to avoid an import cycle,
// and this test is the guard against drift between the two. If either drifts,
// both this test and the stashboxdb mirror test fail, flagging the discrepancy.
func TestLocalSceneBox_IsLiteral(t *testing.T) {
	if LocalSceneBox != "local" {
		t.Errorf("LocalSceneBox must be %q, got %q — stashboxdb.reservedBoxLocal mirrors this value", "local", LocalSceneBox)
	}
}

// LocalSceneID and LocalScenePHash must round-trip for any non-empty phash.
func TestLocalSceneID_RoundTrip(t *testing.T) {
	cases := []string{
		"abc123",
		"pdq256/5f:deadbeef",
		"0000000000000000",
	}
	for _, phash := range cases {
		id := LocalSceneID(phash)
		got, ok := LocalScenePHash(id)
		if !ok {
			t.Errorf("LocalScenePHash(%q) returned false; expected to strip the prefix", id)
		}
		if got != phash {
			t.Errorf("round-trip failed for %q: got %q", phash, got)
		}
	}
}

// LocalScenePHash must return false for a stash-box UUID (no phash: prefix).
func TestLocalScenePHash_ReturnsFalseForCatalogID(t *testing.T) {
	catalogIDs := []string{
		"550e8400-e29b-41d4-a716-446655440000", // real stash-box UUID shape
		"",                                      // empty
		"notaphash",
	}
	for _, id := range catalogIDs {
		if _, ok := LocalScenePHash(id); ok {
			t.Errorf("LocalScenePHash(%q) returned true; expected false for a non-local scene_id", id)
		}
	}
}

// IsLocalScene is an exact check on box, not a prefix-based one.
func TestIsLocalScene_ExactMatch(t *testing.T) {
	if !IsLocalScene("local") {
		t.Error("IsLocalScene(LocalSceneBox) must return true")
	}
	if !IsLocalScene(LocalSceneBox) {
		t.Error("IsLocalScene(LocalSceneBox) must return true")
	}

	notLocal := []string{"LOCAL", "Local", "stashdb", "tpdb", "fansdb", "local2", "", "phash:abc"}
	for _, box := range notLocal {
		if IsLocalScene(box) {
			t.Errorf("IsLocalScene(%q) returned true; must be an exact match on %q", box, LocalSceneBox)
		}
	}
}
