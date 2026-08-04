package identify

// Claude 2026-08-04: new file — the dynamic stash-box cascade (Stage 5 Wave 3,
// plans .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md §Wave 3 and
// .omc/plans/ralplan-adult-identify-configurable-databases.md §2.3).
// Reason: this package had the literal []string{"stashdb","fansdb"} written
// out at nine call sites, so adding a third database meant editing all nine.
// The order now comes from one snapshot (Identifier.StashBoxes) taken per
// Scan, and the three helpers below are the ONLY places that decide iteration
// order — a new call site must use one of them rather than write a literal.
// Troubleshooting: if identification suddenly consults the wrong database or
// skips one, look at legacyCascade's fallback first — an Identifier built
// WITHOUT StashBoxes (every hand-built one in tests, and any caller that
// predates Wave 3) silently gets the old two-database order.
// Related files: internal/mode/mode.go (buildIdentifier, the one producer),
// internal/stashboxdb/store.go (where the order is persisted)

import "strings"

// DatabaseRef is one configured stash-box database in cascade order — the
// dynamic replacement for the hardcoded name literals. It carries only what
// ORDERING and GATING need; the client itself is looked up by Name in
// BoxSearcher.stashBoxes / GiveBack.Boxes, which stay name-keyed maps.
//
// TPDB is deliberately NOT a DatabaseRef. It is a different protocol on the
// search side (REST, internal/tpdbrest) and is appended to every cascade
// explicitly as the literal "tpdb", exactly as it was before this change.
type DatabaseRef struct {
	Name string
	// FansiteOnly gates FUZZY TEXT search only. An exact lookup (scene UUID,
	// fingerprint) is never gated by it — an exact id either matches or it
	// does not, so there is no false-positive risk to protect against. This
	// reproduces, by flag, what the old code did by hardcoding the name
	// "fansdb" into the text-search paths and nowhere else.
	FansiteOnly bool
}

// legacyCascade is the pre-Stage-5 hardcoded order, used verbatim when an
// Identifier carries no snapshot. It is what makes AC15 hold for callers that
// never learned about the registry: a zero-value Identifier.StashBoxes must
// behave EXACTLY as the code did before Wave 3, not as "no databases
// configured" (which would silently disable identification everywhere).
var legacyCascade = []DatabaseRef{
	{Name: "stashdb", FansiteOnly: false},
	{Name: "fansdb", FansiteOnly: true},
}

// cascade returns the ordered databases for this Identifier, falling back to
// the legacy pair when no snapshot was injected.
func (id *Identifier) cascade() []DatabaseRef {
	if len(id.StashBoxes) == 0 {
		return legacyCascade
	}
	return id.StashBoxes
}

// exactBoxes returns every configured database in cascade order, ungated —
// for lookups that are exact by construction (scene UUID, fingerprint) and so
// carry no false-match risk from a fansite catalog.
func (id *Identifier) exactBoxes() []string {
	refs := id.cascade()
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Name)
	}
	return out
}

// textBoxes returns the databases to consult for FUZZY TEXT search, applying
// the fansite gate: a FansiteOnly database is consulted only when one of texts
// carries a fansite hint (see IsFansiteHinted).
//
// On a default install this returns ["stashdb"] unhinted and
// ["stashdb","fansdb"] hinted — byte-identical to the two literals it
// replaced in searchInternalDBs and verifyStudio (AC15).
func (id *Identifier) textBoxes(texts ...string) []string {
	hinted := IsFansiteHinted(texts...)
	refs := id.cascade()
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.FansiteOnly && !hinted {
			continue
		}
		out = append(out, ref.Name)
	}
	return out
}

// uuidBoxes returns the databases for a direct scene-UUID lookup: cascade
// order, ungated, except that a database whose NAME appears in the filename or
// its parent folder is promoted to the front. The promotion is the generic
// form of the old `strings.Contains(stem, "fansdb")` flip — a file named
// "...[fansdb-<uuid>]..." almost certainly carries that database's id, so
// asking it first saves a round trip against every other one.
//
// Divergence from the old literal, accepted deliberately (ralplan §2.3): when
// a filename mentions BOTH database names, the old code always put "fansdb"
// first; this puts them in cascade order. Both still get queried and the first
// non-nil result wins, so the outcome differs only in which round trip is
// spent first, and only for a filename naming two databases at once.
func (id *Identifier) uuidBoxes(stem, parentName string) []string {
	refs := id.cascade()
	haystack := strings.ToLower(stem + "\x00" + parentName)

	promoted := make([]string, 0, len(refs))
	rest := make([]string, 0, len(refs))
	for _, ref := range refs {
		if name := strings.ToLower(ref.Name); name != "" && strings.Contains(haystack, name) {
			promoted = append(promoted, ref.Name)
			continue
		}
		rest = append(rest, ref.Name)
	}
	return append(promoted, rest...)
}

// fingerprintBoxes is exactBoxes plus TPDB's stash-box-protocol GraphQL
// endpoint last — the fixed lookup order restored from the prior CLI, now with
// its stash-box half configurable. A stale comment in that CLI once claimed a
// 4th "TPDB-REST" fingerprint stage; it never existed and is deliberately not
// restored here.
func (id *Identifier) fingerprintBoxes() []string {
	return append(id.exactBoxes(), "tpdb")
}
