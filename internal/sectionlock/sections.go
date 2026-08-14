// Package sectionlock implements the configurable section PIN lock: a
// single operator-chosen PIN that gates whichever sidebar sections (and/or
// Adult content everywhere) the operator has checked.
//
// The anti-RBAC invariant this package exists to preserve: ONE PIN, ONE
// unlock ticket, and the ticket unlocks EVERY locked section at once. It
// carries no identity and no per-section scope — it is a boolean "the
// operator entered the PIN recently." Unlocking `discover` therefore also
// unlocks `adult-content`. With a single shared secret any other design is
// security theatre and would be a de-facto role layer, which is exactly
// what CLAUDE.md's single-operator model rules out.
//
// Threat model (it licenses three choices that would be wrong in a
// hardened system): someone with LAN and/or physical access to the
// operator's own devices — a household member, a shared screen, a guest on
// the network — casually browsing into a locked section. NOT a determined
// attacker with host access, and not anyone who can read /proc or the
// SQLite file. That is why the brute-force lockout lives in process memory
// and clears on restart, why SAKMS_SECTION_LOCK_DISABLE=1 is a full
// disarm, and why the PIN may travel as a plaintext header.
//
// Layering (this package is the mechanism; the wiring lives elsewhere):
//
//	Layer 1 — auth.Middleware classifies r.URL.Path and gates. PRIMARY.
//	Layer 2 — mode.Build calls RequireMode for row/body-addressed Adult.
//	Layer 3 — handlers Build does not reach call RequireAdult.
//
// Layers 2 and 3 read ONLY the Decision this package puts in the request
// context (see context.go). There is deliberately no second decrypt/verify
// path anywhere in the codebase.
package sectionlock

// Canonical section identifiers. These are the seven live sidebar hrefs
// (frontend/src/screens/AppShell.tsx's NAV_ITEMS, minus the leading "/")
// plus the mode-scoped pseudo-section adult-content, which is not a tab at
// all — it cuts across every tab that can show Adult material.
//
// Go is authoritative for this list. The frontend keeps its own copy in
// frontend/src/api/sectionLock.ts and a frontend test set-compares it
// against NAV_ITEMS, so adding a sidebar tab fails that test; TestTabSections
// here is the Go-side half of the same drift guard.
const (
	SectionDashboard   = "dashboard"
	SectionDiscover    = "discover"
	SectionLibrary     = "library"
	SectionQueue       = "queue"
	SectionOrganize    = "organize"
	SectionCollections = "collections"
	SectionSettings    = "settings"

	// SectionAdultContent gates Adult material wherever it appears,
	// independent of which tab it appears under. A route can belong to a
	// tab section AND to this one — /api/modes/adult/rename/scan is
	// {organize, adult-content} — in which case it is gated when EITHER is
	// locked.
	SectionAdultContent = "adult-content"
)

// tabSections is the sidebar-derived half of the canonical list, in
// sidebar order.
var tabSections = []string{
	SectionDashboard,
	SectionDiscover,
	SectionLibrary,
	SectionQueue,
	SectionOrganize,
	SectionCollections,
	SectionSettings,
}

// TabSections returns the lockable sidebar sections, in sidebar order. The
// returned slice is a copy — callers may sort or filter it freely.
func TabSections() []string {
	out := make([]string, len(tabSections))
	copy(out, tabSections)
	return out
}

// AllSections returns every lockable section id: the sidebar tabs plus
// adult-content. This is the set the Settings panel renders a checkbox for.
func AllSections() []string {
	return append(TabSections(), SectionAdultContent)
}

// KnownSection reports whether id is one this build recognises.
//
// It is a WRITE-side validator, not a filter applied to stored data — the
// distinction is load-bearing. PUT /api/section-lock/sections calls it to
// reject an id no version of this app ever emitted (a typo or a miscasing,
// which would otherwise be stored and lock nothing while returning 204).
// Store.LockedSections deliberately does NOT call it: an unknown id already in
// storage is preserved, so a temporarily-removed tab re-locks if it returns,
// and it gates nothing meanwhile because classification only ever emits
// canonical ids.
func KnownSection(id string) bool {
	for _, s := range AllSections() {
		if s == id {
			return true
		}
	}
	return false
}

// Set is an unordered collection of section ids. Classification returns a
// set rather than a single id because one route can belong to several
// sections at once (§4.1).
type Set map[string]struct{}

// NewSet builds a Set from ids.
func NewSet(ids ...string) Set {
	s := make(Set, len(ids))
	for _, id := range ids {
		s[id] = struct{}{}
	}
	return s
}

// Add inserts id.
func (s Set) Add(id string) { s[id] = struct{}{} }

// Has reports whether id is a member.
func (s Set) Has(id string) bool {
	_, ok := s[id]
	return ok
}

// Len returns the member count.
func (s Set) Len() int { return len(s) }

// Sorted returns the members in lexical order — for deterministic JSON,
// log lines, and test comparisons.
func (s Set) Sorted() []string {
	out := make([]string, 0, len(s))
	for id := range s {
		out = append(out, id)
	}
	sortStrings(out)
	return out
}

// Intersects reports whether s and other share any member.
func (s Set) Intersects(other Set) bool {
	// Iterate the smaller side.
	if len(other) < len(s) {
		s, other = other, s
	}
	for id := range s {
		if other.Has(id) {
			return true
		}
	}
	return false
}

// sortStrings is a tiny insertion sort — the slices here are at most nine
// elements, and this keeps sections.go free of an import purely for
// deterministic ordering.
func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
