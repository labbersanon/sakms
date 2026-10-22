package usenetsearch

import (
	"context"
	"fmt"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func completeFiles(n int) []fileAgg {
	out := make([]fileAgg, n)
	for i := range out {
		out[i] = fileAgg{PartM: 10, Seen: seenN(10)}
	}
	return out
}

func seenN(n int) map[int]bool {
	m := make(map[int]bool, n)
	for i := 1; i <= n; i++ {
		m[i] = true
	}
	return m
}

func TestComputeCompleteness_Table(t *testing.T) {
	cases := []struct {
		name   string
		files  []fileAgg
		fileM  int
		gate   int
		meets  bool
		reason string
	}{
		{"94.9 percent fails", []fileAgg{{PartM: 1000, Seen: seenN(949)}}, 0, 95, false, gateReasonBelow},
		{"95.0 percent passes", []fileAgg{{PartM: 1000, Seen: seenN(950)}}, 0, 95, true, gateReasonOK},
		{"100 percent passes", []fileAgg{{PartM: 10, Seen: seenN(10)}}, 0, 95, true, gateReasonOK},
		{"missing whole file via [k/N]", []fileAgg{
			{PartM: 10, Seen: seenN(10)}, {PartM: 10, Seen: seenN(10)},
		}, 3, 95, false, gateReasonBelow},
		{"file counter satisfied", []fileAgg{
			{PartM: 10, Seen: seenN(10)}, {PartM: 10, Seen: seenN(10)},
		}, 2, 95, true, gateReasonOK},
		{"meta files count as seen files only", []fileAgg{
			{PartM: 10, Seen: seenN(10)}, {Meta: true, PartM: 1, Seen: seenN(0)},
		}, 2, 95, true, gateReasonOK},
		{"undeclared part_m", []fileAgg{
			{PartM: 10, Seen: seenN(10)}, {PartM: 0, Seen: seenN(3)},
		}, 0, 95, false, gateReasonUndeclared},
		{"only meta", []fileAgg{{Meta: true, PartM: 1, Seen: seenN(1)}}, 0, 95, false, gateReasonNoPayload},
		{"out-of-range part ignored", []fileAgg{{PartM: 10, Seen: map[int]bool{1: true, 11: true, 12: true}}}, 0, 95, false, gateReasonBelow},
		{"lower gate admits 90", []fileAgg{{PartM: 100, Seen: seenN(90)}}, 0, 90, true, gateReasonOK},
		{"gate clamps to 80 floor", []fileAgg{{PartM: 100, Seen: seenN(80)}}, 0, 10, true, gateReasonOK},
		{"73 files one collection meets", completeFiles(73), 73, 95, true, gateReasonOK},
		{"one file of 73 is below gate", completeFiles(1), 73, 95, false, gateReasonBelow},
		{"implausible yEnc part_m undeclared", []fileAgg{{PartM: 967646, Seen: map[int]bool{730696: true}}}, 0, 95, false, gateReasonUndeclared},
		{"implausible fileM ignored", []fileAgg{{PartM: 10, Seen: seenN(10)}}, 967646, 95, true, gateReasonOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := computeCompleteness(tc.files, tc.fileM)
			if got := c.Meets(tc.gate); got != tc.meets {
				t.Fatalf("Meets=%v want %v (%+v)", got, tc.meets, c)
			}
			if got := c.Reason(tc.gate); got != tc.reason {
				t.Fatalf("Reason=%q want %q", got, tc.reason)
			}
		})
	}
}

func TestAddFilePayload_YencOncePerFile(t *testing.T) {
	agg := &fileAgg{Seen: map[int]bool{}}
	var payload int64
	for i := 0; i < 10; i++ {
		addFilePayload(&payload, agg, 1_000_000, 100)
	}
	if payload != 1_000_000 {
		t.Fatalf("payload=%d, want 1000000 (yEnc is the file size)", payload)
	}
}

func TestAddFilePayload_NoYencSumsScaledBytes(t *testing.T) {
	agg := &fileAgg{Seen: map[int]bool{}}
	var payload, raw int64 = 0, 2000
	addFilePayload(&payload, agg, 0, raw)
	addFilePayload(&payload, agg, 0, raw)
	scaled := int64(float64(raw) / 1.37)
	if payload != 2*scaled {
		t.Fatalf("payload=%d, want %d", payload, 2*scaled)
	}
}

func TestClampMinCompletenessPct(t *testing.T) {
	for in, want := range map[int]int{0: 95, -3: 95, 10: 80, 80: 80, 95: 95, 100: 100, 150: 100} {
		if got := ClampMinCompletenessPct(in); got != want {
			t.Errorf("clamp(%d)=%d want %d", in, got, want)
		}
	}
}

// fileRows builds seenParts of m payload rows for one file of a release.
func fileRows(group, release, filename, idPrefix string, firstMsg, m, seenParts int, fileM int) []HeaderRow {
	var rows []HeaderRow
	for i := 1; i <= seenParts; i++ {
		rows = append(rows, HeaderRow{
			Group: group, MsgNum: firstMsg + i, MsgID: fmt.Sprintf("%s%d@t", idPrefix, i),
			Subject: fmt.Sprintf(`%s "%s" yEnc (%d/%d)`, release, filename, i, m), From: "p@t",
			PostedAt: 1700000000, Bytes: 1000, ReleaseName: release, PartN: i, PartM: m,
			Filename: filename, FileM: fileM,
		})
	}
	return rows
}

func TestSearch_StrictGateVisibility(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"

	var rows []HeaderRow
	rows = append(rows, fileRows(g, "Full.Movie.2020.1080p", "full.mkv", "full", 0, 100, 100, 0)...)
	rows = append(rows, fileRows(g, "Edge.Movie.2020.1080p", "edge.mkv", "edge", 1000, 100, 95, 0)...)
	rows = append(rows, fileRows(g, "Short.Movie.2020.1080p", "short.mkv", "short", 2000, 100, 94, 0)...)
	rows = append(rows, fileRows(g, "Undecl.Movie.2020.1080p", "undecl.mkv", "undecl", 3000, 0, 5, 0)...)
	rows = append(rows, fileRows(g, "Show.Name.S01E02.720p.HDTV", "show.mkv", "show", 4000, 100, 90, 0)...)
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// Before recompute nothing is searchable (dirty, searchable=FALSE).
	cs, err := idx.SearchReleases(ctx, Query{Groups: []string{g}, Terms: []string{"Movie"}, Limit: 10})
	if err != nil || len(cs) != 0 {
		t.Fatalf("pre-recompute got %d err=%v, want 0", len(cs), err)
	}
	st, err := idx.RecomputeDirty(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if st.Releases != 5 || st.Searchable != 4 || st.Undeclared != 1 || st.Backlog != 0 {
		t.Fatalf("stats=%+v (94%% Short + 90%% SxxExx identified; undeclared stays off)", st)
	}
	cs, err = idx.SearchReleases(ctx, Query{Groups: []string{g}, Terms: []string{"Movie"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Candidate{}
	for _, c := range cs {
		got[c.Name] = c
	}
	if len(got) != 3 || got["Full.Movie.2020.1080p"].Name == "" || got["Edge.Movie.2020.1080p"].Name == "" || got["Short.Movie.2020.1080p"].Name == "" {
		t.Fatalf("searchable=%v want Full+Edge+Short", got)
	}
	if got["Short.Movie.2020.1080p"].Complete {
		t.Fatal("94% scene title is identifiable but not grab-complete")
	}
	if !got["Edge.Movie.2020.1080p"].Complete || got["Edge.Movie.2020.1080p"].CompletenessPct != 95 {
		t.Fatalf("edge=%+v", got["Edge.Movie.2020.1080p"])
	}
	ep, err := idx.SearchReleases(ctx, Query{Groups: []string{g}, Terms: []string{"Show.Name"}, Limit: 10})
	if err != nil || len(ep) != 1 || ep[0].Complete {
		t.Fatalf("90%% episode should be identified not grab-complete: %+v err=%v", ep, err)
	}

	// The strict gate follows min_completeness_pct: 94% is searchable at 90.
	idx.SetMinCompletenessPct(90)
	if err := idx.markDirty(ctx, []releaseKey{{g, "Short.Movie.2020.1080p"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 100); err != nil {
		t.Fatal(err)
	}
	cs, _ = idx.SearchReleases(ctx, Query{Groups: []string{g}, Terms: []string{"Short"}, Limit: 10})
	if len(cs) != 1 {
		t.Fatalf("short at gate 90: got %d", len(cs))
	}
}

func TestSearch_LocatorRoundTripUnchangedByRename(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	rows := fileRows(g, "Round.Trip.2021.720p", "rt.mkv", "rt", 0, 4, 4, 0)
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	cs, err := idx.SearchReleases(ctx, Query{Groups: []string{g}, Terms: []string{"Round"}, Limit: 5})
	if err != nil || len(cs) != 1 {
		t.Fatalf("cs=%d err=%v", len(cs), err)
	}
	loc := EncodeLocator(g, cs[0].SeedMsgID, cs[0].Name, 4, 4000)
	p, ok, err := DecodeLocator(loc)
	if err != nil || !ok {
		t.Fatalf("decode: %v %v", ok, err)
	}
	c, err := idx.LoadBySeed(ctx, p.Group, p.SeedMsgID)
	if err != nil || c.Name != cs[0].Name || c.SegmentCount != 4 {
		t.Fatalf("LoadBySeed=%+v err=%v", c, err)
	}
}

func TestDeleteGroupIndex_CascadesReleaseTables(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.cascade"
	if err := idx.Upsert(ctx, fileRows(g, "Cascade.Rel.2020", "c.mkv", "c", 0, 2, 2, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO usenet_nntp_missed_ranges (group_name, lo, hi) VALUES (?,1,5)`, g); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.DeleteGroupIndex(ctx, g); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"usenet_nntp_releases", "usenet_nntp_missed_ranges", "usenet_nntp_headers"} {
		var n int
		if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tbl+` WHERE group_name=?`, g).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s rows=%d err=%v, want 0", tbl, n, err)
		}
	}
}

func TestLivenessDelete_MarksReleaseDirtyAndOrphansDrop(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.live"
	if err := idx.Upsert(ctx, fileRows(g, "Live.Rel.2020", "l.mkv", "l", 0, 10, 10, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if n, err := idx.DeleteByMsgIDs(ctx, []string{"l1@t"}); err != nil || n != 1 {
		t.Fatalf("delete n=%d err=%v", n, err)
	}
	var dirty bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT dirty FROM usenet_nntp_releases WHERE group_name=?`, g).Scan(&dirty); err != nil || !dirty {
		t.Fatalf("dirty=%v err=%v, want true", dirty, err)
	}
	st, err := idx.RecomputeDirty(ctx, 10)
	if err != nil || st.Searchable != 0 || st.BelowGate != 1 {
		t.Fatalf("9/10 must fall below the 95%% gate: %+v err=%v", st, err)
	}
	ids := make([]string, 0, 9)
	for i := 2; i <= 10; i++ {
		ids = append(ids, fmt.Sprintf("l%d@t", i))
	}
	if _, err := idx.DeleteByMsgIDs(ctx, ids); err != nil {
		t.Fatal(err)
	}
	st, err = idx.RecomputeDirty(ctx, 10)
	if err != nil || st.Orphans != 1 {
		t.Fatalf("orphans=%+v err=%v", st, err)
	}
}
