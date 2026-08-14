package sectionlock

import (
	"reflect"
	"testing"
)

// SL-11: TabSections() equals the seven canonical sidebar ids.
//
// This is the Go half of the three-layer nav drift guard. The other two
// are in the frontend (NAV_ITEMS becomes an export, and a TS test
// set-compares it against LOCKABLE_TAB_SECTIONS). Adding a sidebar tab
// must fail at least one of the three — a new tab silently defaulting to
// unlocked is the fail-open case this exists to catch.
//
// The list is spelled out literally rather than derived from the package's
// own constants: a test that reads the same variable it is checking proves
// nothing.
func TestTabSectionsMatchesSidebar(t *testing.T) {
	want := []string{
		"dashboard",
		"discover",
		"library",
		"queue",
		"organize",
		"collections",
		"settings",
	}
	if got := TabSections(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TabSections() = %v, want %v\n"+
			"If a sidebar tab was added or removed, update sections.go AND "+
			"frontend/src/api/sectionLock.ts's LOCKABLE_TAB_SECTIONS together.", got, want)
	}
}

// AllSections is what the Settings panel renders checkboxes for: the tabs
// plus the cross-cutting adult-content pseudo-section.
func TestAllSectionsAddsAdultContent(t *testing.T) {
	all := AllSections()
	if len(all) != len(TabSections())+1 {
		t.Fatalf("AllSections() has %d entries, want %d", len(all), len(TabSections())+1)
	}
	if all[len(all)-1] != SectionAdultContent {
		t.Fatalf("AllSections() last entry = %q, want %q", all[len(all)-1], SectionAdultContent)
	}
}

// TabSections must hand out a copy — a caller sorting or truncating the
// result must not be able to corrupt the canonical list for everyone else.
func TestTabSectionsReturnsCopy(t *testing.T) {
	first := TabSections()
	first[0] = "mutated"
	if TabSections()[0] != SectionDashboard {
		t.Fatal("TabSections() leaked its backing array; a caller mutated the canonical list")
	}
}

func TestKnownSection(t *testing.T) {
	for _, id := range AllSections() {
		if !KnownSection(id) {
			t.Errorf("KnownSection(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"", "search", "downloads", "grabs", "adult"} {
		if KnownSection(id) {
			t.Errorf("KnownSection(%q) = true, want false", id)
		}
	}
}

func TestSetIntersects(t *testing.T) {
	locked := NewSet(SectionOrganize, SectionAdultContent)
	if !locked.Intersects(NewSet(SectionAdultContent)) {
		t.Error("expected {organize,adult-content} to intersect {adult-content}")
	}
	if locked.Intersects(NewSet(SectionQueue, SectionLibrary)) {
		t.Error("expected {organize,adult-content} not to intersect {queue,library}")
	}
	if locked.Intersects(NewSet()) {
		t.Error("expected no intersection with the empty set")
	}
}
