package identify

// Claude 2026-08-04: new file — the AC15 gate for Stage 5 Wave 3 (plan
// .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md, acceptance criterion 15:
// "Identification behaviour is byte-identical on a default install").
// Reason: Wave 3 replaced nine hardcoded []string{"stashdb","fansdb"} literals
// with a snapshot-driven cascade. An off-by-one in priority ordering would
// silently degrade identification quality with no error and no failing test —
// this file is the regression net that makes that loud instead of silent.
// Troubleshooting: if TestCascade_AC15_DefaultInstallOrderIsUnchanged fails,
// STOP. Do not adjust its expectations to match the code; the expectations ARE
// the pre-Stage-5 behaviour, copied from the literals that were removed.

import (
	"reflect"
	"testing"
)

// defaultInstallSnapshot is what migration 0061 seeds, in the order and with
// the flags stashboxdb.Store.List reports them. Written out literally rather
// than read from the store so this test states the expected contract in one
// place instead of asserting the code against itself.
var defaultInstallSnapshot = []DatabaseRef{
	{Name: "stashdb", FansiteOnly: false},
	{Name: "fansdb", FansiteOnly: true},
}

// TestCascade_AC15_DefaultInstallOrderIsUnchanged is the hard gate. Each case
// names the pre-Stage-5 literal it is preserving, so a future reader can
// verify the expectation against git history rather than trusting this file.
func TestCascade_AC15_DefaultInstallOrderIsUnchanged(t *testing.T) {
	id := &Identifier{StashBoxes: defaultInstallSnapshot}

	t.Run("uuid lookup, unhinted", func(t *testing.T) {
		// was: []string{"stashdb", "fansdb"} (identify.go tryUUIDLookup)
		assertOrder(t, id.uuidBoxes("Studio.Scene.1080p", "Some Folder"),
			[]string{"stashdb", "fansdb"})
	})

	t.Run("uuid lookup, fansdb named in the filename", func(t *testing.T) {
		// was: the []string{"fansdb", "stashdb"} flip on a literal "fansdb"
		// substring in stem or parentName.
		assertOrder(t, id.uuidBoxes("scene-fansdb-1234", "Folder"),
			[]string{"fansdb", "stashdb"})
		assertOrder(t, id.uuidBoxes("scene-1234", "fansdb-imports"),
			[]string{"fansdb", "stashdb"})
	})

	t.Run("text search, unhinted", func(t *testing.T) {
		// was: []string{"stashdb"} (searchInternalDBs / verifyStudio /
		// verifyOnePerformer all built the same list).
		assertOrder(t, id.textBoxes("Studio.Scene.1080p", "Some Studio"),
			[]string{"stashdb"})
	})

	t.Run("text search, fansite-hinted", func(t *testing.T) {
		// was: []string{"stashdb"} + append("fansdb") under IsFansiteHinted.
		assertOrder(t, id.textBoxes("clip.onlyfans.2024", ""),
			[]string{"stashdb", "fansdb"})
	})

	t.Run("ungated art and gender lookups", func(t *testing.T) {
		// was: []string{"stashdb", "fansdb"} (StudioImage, PerformerImage,
		// PerformerGender, reSearchAfterGrounding).
		assertOrder(t, id.exactBoxes(), []string{"stashdb", "fansdb"})
	})

	t.Run("fingerprint cascade", func(t *testing.T) {
		// was: fingerprintCascadeOrder = []string{"stashdb","fansdb","tpdb"}
		assertOrder(t, id.fingerprintBoxes(), []string{"stashdb", "fansdb", "tpdb"})
	})
}

// TestCascade_NoSnapshotFallsBackToLegacy covers the other half of AC15: an
// Identifier that was never given a snapshot (every hand-built one in this
// package's tests, and any caller not yet taught about the registry) must
// behave as it did before Wave 3 — NOT as "zero databases configured", which
// would silently disable identification everywhere.
func TestCascade_NoSnapshotFallsBackToLegacy(t *testing.T) {
	id := &Identifier{}

	assertOrder(t, id.exactBoxes(), []string{"stashdb", "fansdb"})
	assertOrder(t, id.textBoxes("plain.file", ""), []string{"stashdb"})
	assertOrder(t, id.textBoxes("clip.onlyfans", ""), []string{"stashdb", "fansdb"})
	assertOrder(t, id.uuidBoxes("plain.file", ""), []string{"stashdb", "fansdb"})
	assertOrder(t, id.fingerprintBoxes(), []string{"stashdb", "fansdb", "tpdb"})
}

// TestCascade_ThreeDatabases is the N=3 case: a third database, an operator
// reorder, and a second fansite-only row must all be reflected everywhere.
func TestCascade_ThreeDatabases(t *testing.T) {
	id := &Identifier{StashBoxes: []DatabaseRef{
		{Name: "pmvstash", FansiteOnly: false},
		{Name: "fansdb", FansiteOnly: true},
		{Name: "stashdb", FansiteOnly: false},
	}}

	assertOrder(t, id.exactBoxes(), []string{"pmvstash", "fansdb", "stashdb"})
	assertOrder(t, id.textBoxes("plain.file", ""), []string{"pmvstash", "stashdb"})
	assertOrder(t, id.textBoxes("clip.onlyfans", ""), []string{"pmvstash", "fansdb", "stashdb"})
	assertOrder(t, id.fingerprintBoxes(), []string{"pmvstash", "fansdb", "stashdb", "tpdb"})

	// Promotion respects cascade order among the promoted set, and leaves the
	// rest in cascade order behind it.
	assertOrder(t, id.uuidBoxes("scene-stashdb-99", ""),
		[]string{"stashdb", "pmvstash", "fansdb"})
}

// TestCascade_SingleDatabase is the N=1 case, including the degenerate one
// where the only database is fansite-only: an unhinted text search then has
// nothing to consult, and must return an empty list rather than falling back
// to a name that is not configured.
func TestCascade_SingleDatabase(t *testing.T) {
	only := &Identifier{StashBoxes: []DatabaseRef{{Name: "stashdb"}}}
	assertOrder(t, only.exactBoxes(), []string{"stashdb"})
	assertOrder(t, only.textBoxes("plain.file", ""), []string{"stashdb"})
	assertOrder(t, only.fingerprintBoxes(), []string{"stashdb", "tpdb"})

	fansiteOnly := &Identifier{StashBoxes: []DatabaseRef{{Name: "fansdb", FansiteOnly: true}}}
	assertOrder(t, fansiteOnly.textBoxes("plain.file", ""), []string{})
	assertOrder(t, fansiteOnly.textBoxes("clip.onlyfans", ""), []string{"fansdb"})
	// The fansite gate must never leak into the exact paths.
	assertOrder(t, fansiteOnly.exactBoxes(), []string{"fansdb"})
	assertOrder(t, fansiteOnly.uuidBoxes("plain.file", ""), []string{"fansdb"})
}

// TestGiveBack_DraftOrder covers giveback.go's fallback walk: it prefers TPDB,
// then the first CONFIGURED database in cascade order — and with no Order set
// it still resolves "stashdb", preserving the old literal.
func TestGiveBack_DraftOrder(t *testing.T) {
	if got := (&GiveBack{}).draftOrder(); !reflect.DeepEqual(got, []string{"stashdb"}) {
		t.Errorf("empty Order = %v, want the legacy [stashdb]", got)
	}
	g := &GiveBack{Order: []string{"pmvstash", "stashdb"}}
	if got := g.draftOrder(); !reflect.DeepEqual(got, []string{"pmvstash", "stashdb"}) {
		t.Errorf("draftOrder = %v", got)
	}
}

func assertOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cascade order = %v, want %v", got, want)
	}
}
