package usenet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsOwnedStagingName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"nzb-0123456789abcdef", true},
		{"nzb-deadbeefcafebabe", true},
		{"nzb-1", true},
		{"nzb-42", true},
		{"nzb-ABCDEF0123456789", false}, // uppercase hex not minted
		{"nzb-0123456789abcde", false},  // 15 hex
		{"nzb-0123456789abcdef0", false},
		{"nzb-", false},
		{"queue", false},
		{"completed", false},
		{"../nzb-0123456789abcdef", false},
	}
	for _, tc := range cases {
		if got := IsOwnedStagingName(tc.name); got != tc.ok {
			t.Errorf("IsOwnedStagingName(%q)=%v want %v", tc.name, got, tc.ok)
		}
	}
}

func TestIsOwnedStagingPath_Containment(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "nzb-0123456789abcdef")
	if err := os.Mkdir(owned, 0o755); err != nil {
		t.Fatal(err)
	}
	if !IsOwnedStagingPath(root, owned) {
		t.Fatal("expected owned hex GID under root")
	}
	legacy := filepath.Join(root, "nzb-7")
	if err := os.Mkdir(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if !IsOwnedStagingPath(root, legacy) {
		t.Fatal("expected legacy counter GID under root")
	}
	other := filepath.Join(root, "not-ours")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsOwnedStagingPath(root, other) {
		t.Fatal("non-nzb name must not be owned")
	}
	writeOwnedMarker(other)
	if !IsOwnedStagingPath(root, other) {
		t.Fatal("marker should prove ownership for direct child")
	}
	nested := filepath.Join(owned, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsOwnedStagingPath(root, nested) {
		t.Fatal("nested path must not count as owned staging dir")
	}
	if IsOwnedStagingPath(root, root) {
		t.Fatal("staging root itself must not be owned-deletable")
	}
	outside := filepath.Join(t.TempDir(), "nzb-0123456789abcdef")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsOwnedStagingPath(root, outside) {
		t.Fatal("path outside staging root must not be owned")
	}
}

func TestRemoveOwnedStagingDir(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "nzb-aaaaaaaaaaaaaaaa")
	if err := os.Mkdir(owned, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owned, "a.rar"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnedStagingDir(root, owned); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned dir should be gone, err=%v", err)
	}

	foreignRoot := t.TempDir()
	foreign := filepath.Join(foreignRoot, "nzb-bbbbbbbbbbbbbbbb")
	if err := os.Mkdir(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnedStagingDir(root, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("foreign path must not be deleted when staging root differs")
	}
}
