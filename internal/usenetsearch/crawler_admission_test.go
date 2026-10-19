package usenetsearch

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/usenet"
)

// crawlStub is a HeaderSource stub driving crawlGroup/CrawlOnce end to end:
// a fixed GroupRange + Overview rowset, plus a StatArticleAll table keyed by
// msgid (usenet.StatVerdict, or an explicit error for an ErrBusy case).
// statCalls records every msgid actually STATed, in call order, so tests can
// assert "STATed once" across chunk/pass boundaries.
type crawlStub struct {
	gr        usenet.GroupRange
	rows      []usenet.MessageOverview
	stat      map[string]usenet.StatVerdict
	statErr   map[string]error
	statCalls []string
	// bodies/bodyErr drive BodyHead (namefix tests); bodyCalls records msgids.
	bodies    map[string]usenet.BodySample
	bodyErr   error
	bodyCalls []string
}

func (c *crawlStub) BodyHead(_ context.Context, msgID string, maxBytes int) (usenet.BodySample, error) {
	c.bodyCalls = append(c.bodyCalls, msgID)
	if c.bodyErr != nil {
		return usenet.BodySample{}, c.bodyErr
	}
	s, ok := c.bodies[msgID]
	if !ok {
		return usenet.BodySample{}, usenet.ErrArticleNotFound
	}
	if maxBytes > 0 && len(s.Data) > maxBytes {
		s.Data = s.Data[:maxBytes]
	}
	return s, nil
}

func (c *crawlStub) Capabilities(context.Context) ([]string, error) { return nil, nil }
func (c *crawlStub) GroupRange(context.Context, string) (usenet.GroupRange, error) {
	return c.gr, nil
}
func (c *crawlStub) Overview(_ context.Context, _ string, from, to int) ([]usenet.MessageOverview, error) {
	var out []usenet.MessageOverview
	for _, r := range c.rows {
		if r.Number >= from && r.Number <= to {
			out = append(out, r)
		}
	}
	return out, nil
}
func (c *crawlStub) ListActive(context.Context, string) ([]string, error) { return nil, nil }
func (c *crawlStub) StatArticle(context.Context, string) (bool, error)    { return false, nil }
func (c *crawlStub) StatArticleAll(_ context.Context, msgID string) (usenet.StatVerdict, error) {
	c.statCalls = append(c.statCalls, msgID)
	if err, ok := c.statErr[msgID]; ok {
		return usenet.StatInconclusive, err
	}
	if v, ok := c.stat[msgID]; ok {
		return v, nil
	}
	return usenet.StatPresent, nil
}

type fakePolicy struct{ snap *PolicySnapshot }

func (f *fakePolicy) Snapshot(context.Context) (*PolicySnapshot, error) { return f.snap, nil }

// releaseRows builds n payload-row MessageOverviews for one release name,
// one distinct file per row ("partN.rar") so pickSample's (Filename, PartN,
// MsgNum) sort is simply alphabetical by file — matches TestParseSubject_
// Scene's own subject convention so ParseSubject extracts a consistent
// ReleaseName across every row.
func releaseRows(startNum int, releaseName, msgPrefix string, n int) []usenet.MessageOverview {
	lower := strings.ToLower(releaseName)
	out := make([]usenet.MessageOverview, 0, n)
	for i := 1; i <= n; i++ {
		subj := fmt.Sprintf(`%s - "%s.part%d.rar" yEnc (1/1)`, releaseName, lower, i)
		out = append(out, usenet.MessageOverview{
			Number:    startNum + i - 1,
			Subject:   subj,
			From:      "poster@test",
			MessageID: fmt.Sprintf("%s%d@test", msgPrefix, i),
			Bytes:     1000,
			DateUnix:  1700000000,
		})
	}
	return out
}

func movieModeStd(tiers []quality.Tier, minRes, maxRes int) Standard {
	return Standard{Mode: "movies", Policy: "mode", Tiers: tiers, MinRes: minRes, MaxRes: maxRes}
}

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)
	fn()
	return buf.String()
}

func TestCrawlOnce_SkipsLanguageNamedGroup(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	const group = "alt.binaries.dvd.german"
	rows := releaseRows(1, "Some.Movie.2020.GERMAN.1080p.WEB-DL.x264-GROUP", "de", 3)
	src := &crawlStub{gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows}
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, LanguageFilter: true}
	if _, err := c.CrawlOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	wm, err := idx.Watermark(context.Background(), group)
	if err != nil || wm != 0 {
		t.Fatalf("watermark=%d err=%v, want 0 (group not crawled)", wm, err)
	}
}

func TestCrawlGroup_LanguageFilterSkipsGermanTitle(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const group = "alt.binaries.movies"
	rows := releaseRows(1, "Some.Movie.2020.GERMAN.1080p.WEB-DL.x264-GROUP", "de", 3)
	src := &crawlStub{gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 0, 0)}}
	c := &Crawler{
		Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true,
		GroupModesFn:   func(string) []string { return []string{"movies"} },
		Admitter:       newAdmitter(&fakePolicy{snap: snap}),
		LanguageFilter: true,
	}
	logs := captureLog(t, func() {
		if _, err := c.CrawlOnce(ctx); err != nil {
			t.Fatal(err)
		}
	})
	n, err := idx.CountHeaders(ctx)
	if err != nil || n != 0 {
		t.Fatalf("count=%d err=%v, want 0", n, err)
	}
	if len(src.statCalls) != 0 {
		t.Fatalf("statCalls=%v, want none (language skip before STAT)", src.statCalls)
	}
	if !strings.Contains(logs, "skip_lang=1") {
		t.Fatalf("logs missing skip_lang=1:\n%s", logs)
	}
}

func TestCrawlGroup_NonMatchingReleaseNeverUpserted(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()

	const group = "alt.binaries.movies"
	rows := releaseRows(1, "Rejected.Movie.2001.720p.WEB-DL.x264-GROUP", "rej", 3)
	src := &crawlStub{gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 1080, 0)}}
	admit := newAdmitter(&fakePolicy{snap: snap})

	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} }, Admitter: admit}
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}

	n, err := idx.CountHeaders(ctx)
	if err != nil || n != 0 {
		t.Fatalf("count=%d err=%v, want 0 (720p fails movies MinRes=1080)", n, err)
	}
	if len(src.statCalls) != 0 {
		t.Fatalf("statCalls=%v, want none (gate reject happens before STAT)", src.statCalls)
	}
	wm, err := idx.Watermark(ctx, group)
	if err != nil || wm != src.gr.High {
		t.Fatalf("watermark=%d err=%v, want %d (always advances)", wm, err, src.gr.High)
	}
}

func TestCrawlGroup_TwoOfThreeMissingSkipsAndLogs(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()

	const group = "alt.binaries.movies"
	rows := releaseRows(1, "Admitted.Movie.2020.1080p.WEB-DL.x264-GROUP", "m2of3", 5)
	src := &crawlStub{
		gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows,
		stat: map[string]usenet.StatVerdict{
			"m2of31@test": usenet.StatMissing, // sample picks rows 1,3,5
			"m2of33@test": usenet.StatMissing,
			"m2of35@test": usenet.StatPresent,
		},
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 0, 0)}}
	admit := newAdmitter(&fakePolicy{snap: snap})
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} }, Admitter: admit}

	logs := captureLog(t, func() {
		if _, err := c.CrawlOnce(ctx); err != nil {
			t.Fatal(err)
		}
	})

	n, err := idx.CountHeaders(ctx)
	if err != nil || n != 0 {
		t.Fatalf("count=%d err=%v, want 0 (2-of-3 missing skips)", n, err)
	}
	if !strings.Contains(logs, "usenetsearch: admit-skip-stat") || !strings.Contains(logs, "stat=missing") {
		t.Fatalf("logs missing admit-skip-stat/stat=missing:\n%s", logs)
	}
}

func TestCrawlGroup_OneOfThreeMissingUpsertsPartial(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()

	const group = "alt.binaries.movies"
	rows := releaseRows(1, "Admitted.Movie.2020.1080p.WEB-DL.x264-GROUP", "m1of3", 5)
	src := &crawlStub{
		gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows,
		stat: map[string]usenet.StatVerdict{
			"m1of31@test": usenet.StatMissing,
			"m1of33@test": usenet.StatPresent,
			"m1of35@test": usenet.StatPresent,
		},
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 0, 0)}}
	admit := newAdmitter(&fakePolicy{snap: snap})
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} }, Admitter: admit}

	logs := captureLog(t, func() {
		if _, err := c.CrawlOnce(ctx); err != nil {
			t.Fatal(err)
		}
	})

	n, err := idx.CountHeaders(ctx)
	if err != nil || n != 5 {
		t.Fatalf("count=%d err=%v, want 5 (1-of-3 missing still upserts)", n, err)
	}
	if !strings.Contains(logs, "usenetsearch: admit ") || !strings.Contains(logs, "stat=partial") {
		t.Fatalf("logs missing admit/stat=partial:\n%s", logs)
	}
}

func TestCrawlGroup_BusyStatDefersAndUpserts(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()

	const group = "alt.binaries.movies"
	rows := releaseRows(1, "Admitted.Movie.2020.1080p.WEB-DL.x264-GROUP", "busy", 5)
	src := &crawlStub{
		gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows,
		statErr: map[string]error{"busy1@test": usenet.ErrBusy},
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 0, 0)}}
	admit := newAdmitter(&fakePolicy{snap: snap})
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} }, Admitter: admit}

	logs := captureLog(t, func() {
		if _, err := c.CrawlOnce(ctx); err != nil {
			t.Fatal(err)
		}
	})

	n, err := idx.CountHeaders(ctx)
	if err != nil || n != 5 {
		t.Fatalf("count=%d err=%v, want 5 (busy defers, still upserts)", n, err)
	}
	if !strings.Contains(logs, "stat=deferred") {
		t.Fatalf("logs missing stat=deferred:\n%s", logs)
	}
}

func TestCrawlGroup_CrossChunkReleaseStatsOnce(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()

	const group = "alt.binaries.movies"
	name := "Admitted.Movie.2020.1080p.WEB-DL.x264-GROUP"
	chunk1 := releaseRows(1, name, "cc", 3) // rows 1-3
	chunk2 := releaseRows(4, name, "cc", 2) // rows 4-5, continuation
	// Re-number chunk2's filenames/msgids as part4/part5 so they don't
	// collide with chunk1's part1-3 and the release stays logically "the
	// same" across both chunks (same ReleaseName, same group).
	for i := range chunk2 {
		chunk2[i].MessageID = fmt.Sprintf("cc%d@test", i+4)
		chunk2[i].Subject = fmt.Sprintf(`%s - "%s.part%d.rar" yEnc (1/1)`, name, strings.ToLower(name), i+4)
	}
	src := &crawlStub{
		stat: map[string]usenet.StatVerdict{"cc1@test": usenet.StatPresent, "cc2@test": usenet.StatPresent, "cc3@test": usenet.StatPresent},
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 0, 0)}}
	admit := newAdmitter(&fakePolicy{snap: snap})
	groupModesFn := func(string) []string { return []string{"movies"} }
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true, GroupModesFn: groupModesFn, Admitter: admit}

	kept1, err := c.admitChunk(ctx, group, 1, 3, toHeaderRows(group, chunk1))
	if err != nil {
		t.Fatal(err)
	}
	if len(kept1) != 3 {
		t.Fatalf("chunk1 kept=%d, want 3", len(kept1))
	}
	if err := idx.Upsert(ctx, kept1); err != nil {
		t.Fatal(err)
	}
	if len(src.statCalls) != 3 {
		t.Fatalf("statCalls after chunk1=%v, want 3 (first/median/last of 3)", src.statCalls)
	}

	kept2, err := c.admitChunk(ctx, group, 4, 5, toHeaderRows(group, chunk2))
	if err != nil {
		t.Fatal(err)
	}
	if len(kept2) != 2 {
		t.Fatalf("chunk2 kept=%d, want 2 (existing shortcut keeps the continuation rows)", len(kept2))
	}
	if len(src.statCalls) != 3 {
		t.Fatalf("statCalls after chunk2=%v, want still 3 (existing shortcut, no re-STAT)", src.statCalls)
	}
}

func toHeaderRows(group string, overviews []usenet.MessageOverview) []HeaderRow {
	return (&Crawler{}).buildHeaders(group, overviews)
}

func TestCrawlGroup_WatermarkAdvancesEvenWhenEverythingDropped(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()

	const group = "alt.binaries.movies"
	rows := releaseRows(1, "Rejected.Movie.2001.480p.WEB-DL.x264-GROUP", "wm", 2)
	src := &crawlStub{gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 1080, 0)}}
	admit := newAdmitter(&fakePolicy{snap: snap})
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} }, Admitter: admit}
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	wm, err := idx.Watermark(ctx, group)
	if err != nil || wm != src.gr.High {
		t.Fatalf("watermark=%d err=%v, want %d", wm, err, src.gr.High)
	}
	n, _ := idx.CountHeaders(ctx)
	if n != 0 {
		t.Fatalf("count=%d, want 0", n)
	}
}

func TestCrawlGroup_GradesUnchangedAfterStatSkip(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()

	const group = "alt.binaries.movies"
	rows := releaseRows(1, "Admitted.Movie.2020.1080p.WEB-DL.x264-GROUP", "grd", 3)
	src := &crawlStub{
		gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows,
		stat: map[string]usenet.StatVerdict{"grd1@test": usenet.StatMissing, "grd2@test": usenet.StatMissing, "grd3@test": usenet.StatMissing},
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 0, 0)}}
	admit := newAdmitter(&fakePolicy{snap: snap})

	before, err := idx.GetGrade(ctx, "movies", group)
	if err != nil {
		t.Fatal(err)
	}
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} }, Admitter: admit}
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := idx.GetGrade(ctx, "movies", group)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("grade changed from %d to %d after a STAT skip; crawl must never call DemoteForFailure/NoteSuccess", before, after)
	}
}

func TestCrawlGroup_AdmissionDisabledReproducesLegacyUpsertSet(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()

	const group = "alt.binaries.movies"
	var rows []usenet.MessageOverview
	rows = append(rows, releaseRows(1, "Rejected.Movie.2001.720p.WEB-DL.x264-GROUP", "legA", 2)...)
	rows = append(rows, releaseRows(3, "Admitted.Movie.2020.1080p.WEB-DL.x264-GROUP", "legB", 2)...)
	src := &crawlStub{
		gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows,
		stat: map[string]usenet.StatVerdict{"legB1@test": usenet.StatMissing, "legB2@test": usenet.StatMissing},
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 1080, 0)}}
	admit := newAdmitter(&fakePolicy{snap: snap})

	// AdmissionEnabled=false: every row from Overview upserts regardless of
	// quality/STAT, exactly like the pre-admission crawler.
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: false,
		GroupModesFn: func(string) []string { return []string{"movies"} }, Admitter: admit}
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := idx.CountHeaders(ctx)
	if err != nil || n != int64(len(rows)) {
		t.Fatalf("count=%d err=%v, want %d (admission disabled upserts everything)", n, err, len(rows))
	}
	if len(src.statCalls) != 0 {
		t.Fatalf("statCalls=%v, want none when admission is disabled", src.statCalls)
	}
}

func TestCrawlGroup_NilAdmitterIsLegacyPath(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()

	const group = "alt.binaries.movies"
	rows := releaseRows(1, "Rejected.Movie.2001.720p.WEB-DL.x264-GROUP", "nilA", 2)
	src := &crawlStub{gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows}
	// AdmissionEnabled=true but Admitter is nil (defensive): must still
	// behave like the legacy path, not panic.
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true}
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := idx.CountHeaders(ctx)
	if err != nil || n != int64(len(rows)) {
		t.Fatalf("count=%d err=%v, want %d", n, err, len(rows))
	}
}

func TestPruneGroupNonMatching_DropsHashTokenNames(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const group = "alt.binaries.moovee"
	const keep = "Admitted.Movie.2020.1080p.WEB-DL.x264-GROUP"
	const junk = "Njabzsard2xrgxGUQIT"
	if err := idx.Upsert(ctx, []HeaderRow{
		{Group: group, MsgNum: 1, MsgID: "keep@x", Subject: "x", ReleaseName: keep, PartN: 1, PartM: 1, Filename: "a.mkv", Bytes: 1, PostedAt: 1},
		{Group: group, MsgNum: 2, MsgID: "junk@x", Subject: "x", ReleaseName: junk, PartN: 1, PartM: 1, Filename: "b.rar", Bytes: 1, PostedAt: 1},
	}); err != nil {
		t.Fatal(err)
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 0, 0)}}
	n, err := pruneGroupNonMatching(ctx, idx, group, []string{"movies"}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned=%d, want 1 (hash token only)", n)
	}
	var left string
	if err := idx.db.QueryRowContext(ctx, `SELECT release_name FROM usenet_nntp_headers`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != keep {
		t.Fatalf("left=%q, want scene name kept", left)
	}
}

func TestCrawlGroup_HashTokenCollectionStagedNotNamed(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const group = "alt.binaries.moovee"
	const tok = "Njabzsard2xrgxGUQIT"
	rows := []usenet.MessageOverview{
		{Number: 1, Subject: tok + ` [3/10] "p4v0O75-2qNMayAm3QhP_iIbuaK6.par2" yEnc (1/2)`, From: "p@t", MessageID: "a@t", Bytes: 1000, DateUnix: 1700000000},
		{Number: 2, Subject: tok + ` [7/10] "zzzzzzzzzzzzzzzzzzzz.part01.rar" yEnc (1/50)`, From: "p@t", MessageID: "b@t", Bytes: 1000, DateUnix: 1700000000},
	}
	src := &crawlStub{gr: usenet.GroupRange{Low: 1, High: 2}, rows: rows}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 0, 0)}}
	c := &Crawler{
		Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true, NameFixEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} },
		Admitter:     newAdmitter(&fakePolicy{snap: snap}),
	}
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var named int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_releases WHERE release_key=?`, tok).Scan(&named); err != nil {
		t.Fatal(err)
	}
	if named != 0 {
		t.Fatalf("named hash-token rows=%d, want 0 (must stage, not admit)", named)
	}
	var staged int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_headers WHERE obfuscated=TRUE AND release_name LIKE 'obf:%'`).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if staged != 2 {
		t.Fatalf("staged headers=%d, want 2", staged)
	}
}

func TestPruneGroupNonMatching(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const group = "alt.binaries.movies"

	if err := idx.Upsert(ctx, []HeaderRow{
		{Group: group, MsgNum: 1, MsgID: "keep@x", Subject: "x", ReleaseName: "Admitted.Movie.2020.1080p.WEB-DL.x264-GROUP", PartN: 1, PartM: 1, Filename: "a.mkv", Bytes: 1, PostedAt: 1},
		{Group: group, MsgNum: 2, MsgID: "drop@x", Subject: "x", ReleaseName: "Rejected.Movie.2001.720p.WEB-DL.x264-GROUP", PartN: 1, PartM: 1, Filename: "b.mkv", Bytes: 1, PostedAt: 1},
	}); err != nil {
		t.Fatal(err)
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 1080, 0)}}
	n, err := pruneGroupNonMatching(ctx, idx, group, []string{"movies"}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned=%d, want 1", n)
	}
	count, err := idx.CountHeaders(ctx)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v, want 1 (only the matching release remains)", count, err)
	}
}

func TestService_PruneNonMatchingOnce_RunsOncePerGroup(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const group = "alt.binaries.movies"

	if err := idx.Upsert(ctx, []HeaderRow{
		{Group: group, MsgNum: 1, MsgID: "drop@x", Subject: "x", ReleaseName: "Rejected.Movie.2001.720p.WEB-DL.x264-GROUP", PartN: 1, PartM: 1, Filename: "b.mkv", Bytes: 1, PostedAt: 1},
	}); err != nil {
		t.Fatal(err)
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 1080, 0)}}
	admit := newAdmitter(&fakePolicy{snap: snap})
	s := &Service{idx: idx}
	cfg := Config{Enabled: true, Movies: true, MoviesGroups: []string{group}, AdmissionEnabled: true}

	s.pruneNonMatchingOnce(ctx, []string{group}, cfg, admit, idx)
	count, err := idx.CountHeaders(ctx)
	if err != nil || count != 0 {
		t.Fatalf("count=%d err=%v, want 0 after first prune", count, err)
	}

	// Re-insert the same rejected release — the second call must NOT
	// re-prune it (one-shot per process, per group).
	if err := idx.Upsert(ctx, []HeaderRow{
		{Group: group, MsgNum: 1, MsgID: "drop@x", Subject: "x", ReleaseName: "Rejected.Movie.2001.720p.WEB-DL.x264-GROUP", PartN: 1, PartM: 1, Filename: "b.mkv", Bytes: 1, PostedAt: 1},
	}); err != nil {
		t.Fatal(err)
	}
	s.pruneNonMatchingOnce(ctx, []string{group}, cfg, admit, idx)
	count, err = idx.CountHeaders(ctx)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v, want 1 (second call is a no-op, already pruned this group)", count, err)
	}
}

func overCallsByGroup(calls []overCall) map[string]int {
	out := map[string]int{}
	for _, c := range calls {
		out[c.group]++
	}
	return out
}

const (
	firehose = "alt.binaries.bloaf"
	core     = "alt.binaries.moovee"
)

// newFirehoseFixture returns an index and a stub where firehose is far from
// HIGH and core is one chunk from caught up.
func newFirehoseFixture(t *testing.T) (*Index, *bfStub) {
	t.Helper()
	idx, err := OpenIndex(dbtest.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	src := &bfStub{
		rows: map[string][]usenet.MessageOverview{
			firehose: releaseRows(1, "Junk.Movie.2001.480p.WEB-DL.x264-GROUP", "j", 5),
			core:     releaseRows(1, "Good.Movie.2020.1080p.WEB-DL.x264-GROUP", "g", 3),
		},
		gr: map[string]usenet.GroupRange{
			firehose: {Low: 1, High: 10000},
			core:     {Low: 1, High: 3},
		},
	}
	return idx, src
}

func TestCrawlOnce_FirehoseGroupDoesNotStarveTheRest(t *testing.T) {
	idx, src := newFirehoseFixture(t)
	ctx := context.Background()
	c := &Crawler{
		Src: src, Index: idx, Groups: []string{firehose, core},
		MaxChunksPerGroup: 1, MaxChunksPerPass: 2,
	}
	res, err := c.CrawlOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced {
		t.Fatal("expected Advanced")
	}
	if res.NextStart != 1 {
		t.Fatalf("NextStart=%d, want 1", res.NextStart)
	}
	wmFire, err := idx.Watermark(ctx, firehose)
	if err != nil || wmFire != overviewChunk {
		t.Fatalf("bloaf watermark=%d err=%v, want %d (one chunk, not High)", wmFire, err, overviewChunk)
	}
	wmCore, err := idx.Watermark(ctx, core)
	if err != nil || wmCore != 3 {
		t.Fatalf("moovee watermark=%d err=%v, want 3 (visited in the same pass)", wmCore, err)
	}
	by := overCallsByGroup(src.calls)
	if by[firehose] != 1 || by[core] != 1 {
		t.Fatalf("OVER calls by group=%v, want bloaf=1 moovee=1", by)
	}
}

func TestCrawlOnce_RoundRobinStartsPastFirehose(t *testing.T) {
	idx, src := newFirehoseFixture(t)
	ctx := context.Background()
	c := &Crawler{
		Src: src, Index: idx, Groups: []string{firehose, core},
		CrawlStart: 1, MaxChunksPerGroup: 1, MaxChunksPerPass: 1,
	}
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	by := overCallsByGroup(src.calls)
	if by[core] != 1 {
		t.Fatalf("OVER calls by group=%v, want moovee first when CrawlStart=1", by)
	}
	if by[firehose] != 0 {
		t.Fatalf("bloaf OVERed in a 1-chunk pass that started at moovee: %v", by)
	}
	wmCore, err := idx.Watermark(ctx, core)
	if err != nil || wmCore != 3 {
		t.Fatalf("moovee watermark=%d err=%v, want 3", wmCore, err)
	}
	wmFire, err := idx.Watermark(ctx, firehose)
	if err != nil || wmFire != 0 {
		t.Fatalf("bloaf watermark=%d err=%v, want 0 (not visited)", wmFire, err)
	}
}

func TestCrawlOnce_CoresGetMajorityOfPassBudget(t *testing.T) {
	idx, src := newFirehoseFixture(t)
	const other = "alt.binaries.amazing"
	src.rows[other] = releaseRows(1, "Junk.Movie.2001.480p.WEB-DL.x264-GROUP", "a", 5)
	src.gr[other] = usenet.GroupRange{Low: 1, High: 10000}
	ctx := context.Background()
	c := &Crawler{
		Src: src, Index: idx, Groups: []string{firehose, other, core},
		MaxChunksPerGroup: 1, MaxChunksPerPass: 3,
	}
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	by := overCallsByGroup(src.calls)
	if by[core] < 1 {
		t.Fatalf("OVER calls by group=%v, want moovee visited this pass", by)
	}
}

func TestForwardLag_FirstCrawlWindowAndCaughtUp(t *testing.T) {
	gr := usenet.GroupRange{Low: 1, High: 1_000_000}
	if g := forwardLag(0, gr); g != overviewChunk*50 {
		t.Fatalf("wm=0 lag=%d, want first-crawl window %d", g, overviewChunk*50)
	}
	if g := forwardLag(1_000_000, gr); g != 0 {
		t.Fatalf("caught-up lag=%d, want 0", g)
	}
	if g := forwardLag(900_000, gr); g != 100_000 {
		t.Fatalf("100k-behind lag=%d, want 100000", g)
	}
	if g := forwardLag(950_000, gr); g != coreCatchUpLag {
		t.Fatalf("threshold lag=%d, want %d", g, coreCatchUpLag)
	}
}

func TestSplitCoreRest_StagingCoresOnly(t *testing.T) {
	cores, rest := splitCoreRest([]string{"alt.binaries.tv", "alt.binaries.moovee", "alt.binaries.bloaf"})
	if len(cores) != 1 || cores[0] != "alt.binaries.moovee" {
		t.Fatalf("cores=%v, want moovee only (namefix still fills staging first)", cores)
	}
	if len(rest) != 2 {
		t.Fatalf("rest=%v, want tv+bloaf", rest)
	}
}

func TestSplitYieldRest_YieldGroupsAreCores(t *testing.T) {
	cores, rest := splitYieldRest([]string{
		"alt.binaries.tv",
		"alt.binaries.bloaf",
		"alt.binaries.anime",
		"alt.binaries.dvd",
		"alt.binaries.moovee",
	})
	got := map[string]bool{}
	for _, g := range cores {
		got[g] = true
	}
	for _, g := range []string{"alt.binaries.tv", "alt.binaries.anime", "alt.binaries.moovee"} {
		if !got[g] {
			t.Fatalf("cores=%v missing yield group %s", cores, g)
		}
	}
	if got["alt.binaries.bloaf"] || got["alt.binaries.dvd"] {
		t.Fatalf("firehose in cores: %v", cores)
	}
	if len(rest) != 2 {
		t.Fatalf("rest=%v, want bloaf+dvd", rest)
	}
}

func TestCrawlOnce_SkipsListTailWhileYieldGroupsLag(t *testing.T) {
	idx, err := OpenIndex(dbtest.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	src := &bfStub{
		rows: map[string][]usenet.MessageOverview{
			firehose: releaseRows(1, "Junk.Movie.2001.480p.WEB-DL.x264-GROUP", "j", 5),
			core:     releaseRows(100001, "Good.Movie.2020.1080p.WEB-DL.x264-GROUP", "g", 3),
		},
		gr: map[string]usenet.GroupRange{
			firehose: {Low: 1, High: 10000},
			core:     {Low: 1, High: 200_000},
		},
	}
	ctx := context.Background()
	if err := idx.SetWatermark(ctx, core, 100_000); err != nil {
		t.Fatal(err)
	}
	c := &Crawler{
		Src: src, Index: idx, Groups: []string{firehose, core},
		MaxChunksPerGroup: 1, MaxChunksPerPass: 3,
	}
	res, err := c.CrawlOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced {
		t.Fatal("expected Advanced")
	}
	if !c.SkipListTail {
		t.Fatal("expected SkipListTail while moovee is 100k behind HIGH")
	}
	c.BackfillEnabled = true
	if !c.shouldBackfill() {
		t.Fatal("yield-core backfill must still run while SkipListTail")
	}
	if c.backfillWanted(ctx, firehose) {
		t.Fatal("bloaf must not backfill while SkipListTail")
	}
	if !c.backfillWanted(ctx, core) {
		t.Fatal("moovee must still backfill while SkipListTail")
	}
	by := overCallsByGroup(src.calls)
	if by[firehose] != 0 {
		t.Fatalf("bloaf OVERed while yield group lagging: %v", by)
	}
	if by[core] < 1 {
		t.Fatalf("moovee not OVERed: %v", by)
	}
}

func TestCrawlOnce_ListTailRunsWhenYieldCaughtUp(t *testing.T) {
	idx, src := newFirehoseFixture(t)
	ctx := context.Background()
	c := &Crawler{
		Src: src, Index: idx, Groups: []string{firehose, core},
		MaxChunksPerGroup: 1, MaxChunksPerPass: 3,
	}
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if c.SkipListTail {
		t.Fatal("did not expect SkipListTail when moovee lag is 3")
	}
	c.BackfillEnabled = true
	if !c.shouldBackfill() {
		t.Fatal("backfill should run when yield groups are caught up")
	}
	by := overCallsByGroup(src.calls)
	if by[firehose] < 1 {
		t.Fatalf("bloaf should get rest budget when cores caught up: %v", by)
	}
	if by[core] < 1 {
		t.Fatalf("moovee should still run: %v", by)
	}
}

func TestCrawler_SkipListTailFiltersFirehoseBackfill(t *testing.T) {
	c := &Crawler{BackfillEnabled: true, SkipListTail: true}
	if !c.shouldBackfill() {
		t.Fatal("backfill stays enabled; yield cores still walk history")
	}
	if c.backfillWanted(context.Background(), firehose) {
		t.Fatal("bloaf backfill skipped while catching up")
	}
	if !c.backfillWanted(context.Background(), core) {
		t.Fatal("moovee backfill runs while catching up")
	}
}
