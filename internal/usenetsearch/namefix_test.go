package usenetsearch

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/usenet"
)

func par2Packet(typ string, body []byte) []byte {
	length := par2HeaderLen + len(body)
	b := append([]byte{}, par2Magic...)
	b = binary.LittleEndian.AppendUint64(b, uint64(length))
	b = append(b, make([]byte, 32)...) // packet md5 + recovery set id (unchecked)
	t := make([]byte, 16)
	copy(t, typ)
	b = append(b, t...)
	return append(b, body...)
}

func par2FileDescPacket(name string) []byte {
	body := make([]byte, 48)
	body = binary.LittleEndian.AppendUint64(body, 1234)
	nb := []byte(name)
	for len(nb)%4 != 0 {
		nb = append(nb, 0)
	}
	return par2Packet("PAR 2.0\x00FileDesc", append(body, nb...))
}

func TestPar2FileNames(t *testing.T) {
	var data []byte
	data = append(data, par2Packet("PAR 2.0\x00Main", make([]byte, 12))...)
	data = append(data, par2FileDescPacket("Some.Movie.2021.1080p.BluRay.x264-GRP.part01.rar")...)
	data = append(data, par2FileDescPacket("Some.Movie.2021.1080p.BluRay.x264-GRP.part02.rar")...)
	got := par2FileNames(data)
	if len(got) != 2 || got[0] != "Some.Movie.2021.1080p.BluRay.x264-GRP.part01.rar" {
		t.Fatalf("names = %v", got)
	}
	// Truncated mid-packet: the complete first name survives, no panic.
	got = par2FileNames(data[:len(data)-20])
	if len(got) < 1 {
		t.Fatalf("truncated names = %v", got)
	}
	for _, bad := range [][]byte{nil, []byte("PAR2\x00PKT"), append([]byte("PAR2\x00PKT"), make([]byte, 60)...), []byte("garbage")} {
		_ = par2FileNames(bad)
	}
}

func TestNfoSceneNames(t *testing.T) {
	nfo := []byte("  RELEASE INFO\n  Name.....: Another.Movie.2020.1080p.WEB-DL.x264-GRP\n  Size: 4GB\n")
	got := nfoSceneNames(nfo)
	if len(got) == 0 || got[0] != "Another.Movie.2020.1080p.WEB-DL.x264-GRP" {
		t.Fatalf("nfo names = %v", got)
	}
}

func rar4Fixture(member string) []byte {
	b := []byte("Rar!\x1a\x07\x00")
	b = append(b, 0, 0, 0x73, 0, 0, 13, 0, 0, 0, 0, 0, 0, 0) // main header, size 13
	hdr := make([]byte, 32)
	hdr[2] = 0x74
	binary.LittleEndian.PutUint16(hdr[5:], uint16(32+len(member)))
	binary.LittleEndian.PutUint16(hdr[26:], uint16(len(member)))
	return append(append(b, hdr...), member...)
}

func TestRarMemberNameFixtures(t *testing.T) {
	got := usenet.ArchiveMemberNames(rar4Fixture("Some.Show.S01E02.720p.HDTV.x264-GRP.mkv"))
	if len(got) != 1 || got[0] != "Some.Show.S01E02.720p.HDTV.x264-GRP.mkv" {
		t.Fatalf("rar4 = %v", got)
	}
	rar5 := append([]byte("Rar!\x1a\x07\x01\x00"), []byte("\x00\x00Some.Show.S01E02.720p.HDTV.x264-GRP.mkv\x00")...)
	got = usenet.ArchiveMemberNames(rar5)
	if len(got) != 1 {
		t.Fatalf("rar5 = %v", got)
	}
	cands := candidatesFromSample(usenet.BodySample{Data: rar4Fixture("Some.Show.S01E02.720p.HDTV.x264-GRP.mkv")}, false)
	if len(cands) != 1 || cands[0].source != sourceRar || normalizeRecovered(cands[0].name) != "Some.Show.S01E02.720p.HDTV.x264-GRP" {
		t.Fatalf("cands = %+v", cands)
	}
}

func TestObfSkeletonGroupsVolumes(t *testing.T) {
	const h = "0123456789abcdef0123456789abcdef"
	subs := []string{
		fmt.Sprintf(`[01/46] - "%s.part01.rar" yEnc (1/80)`, h),
		fmt.Sprintf(`[02/46] - "%s.part02.rar" yEnc (37/80)`, h),
		fmt.Sprintf(`[45/46] - "%s.part45.rar" yEnc (1/12)`, h),
		fmt.Sprintf(`[46/46] - "%s.vol00+01.par2" yEnc (1/1)`, h),
		fmt.Sprintf(`"%s.par2" yEnc (1/1)`, h),
		fmt.Sprintf(`"%s.r05" yEnc (2/9)`, h),
	}
	want := ObfKey("g", "p@x", subs[0])
	for _, s := range subs {
		if got := ObfKey("g", "p@x", s); got != want {
			t.Fatalf("key(%q) = %s, want %s", s, got, want)
		}
	}
	if !strings.HasPrefix(want, "obf:") || len(want) != len("obf:")+16 {
		t.Fatalf("key = %q", want)
	}
	if ObfKey("g", "other@x", subs[0]) == want || ObfKey("g2", "p@x", subs[0]) == want {
		t.Fatal("poster/group must separate sets")
	}
	if hashCore(obfSkeleton(subs[0])) != h {
		t.Fatalf("hashCore = %q", hashCore(obfSkeleton(subs[0])))
	}
	for _, rnd := range []string{"dFkQ3xZp9aL2mNq8xW", "A1b2C3d4E5f6G7h8I9"} {
		if !looksObfuscated(rnd) {
			t.Fatalf("%q should look obfuscated", rnd)
		}
	}
	for _, ok := range []string{"TheMatrixReloaded2003", "Some.Movie.2021.1080p.WEB-DL", "short"} {
		if looksObfuscated(ok) {
			t.Fatalf("%q must not look obfuscated", ok)
		}
	}
}

func TestObfSkeleton_LeadHashTokenGroupsRandomFilenames(t *testing.T) {
	const tok = "ah63jka93jf0jh26ahjas558"
	a := fmt.Sprintf(`%s - [01/22] - "random01.part01.rar" yEnc (1/40)`, tok)
	b := fmt.Sprintf(`%s - [22/22] - "other.vol00+01.par2" yEnc (1/1)`, tok)
	c := `t2EI3CdWdF0hi5b8L9tkx[08/52] - "aaa.rar" yEnc (1/9)`
	d := `t2EI3CdWdF0hi5b8L9tkx[52/52] - "zzz.par2" yEnc (1/1)`
	if ObfKey("g", "p@x", a) != ObfKey("g", "p@x", b) {
		t.Fatalf("dash-hash split: %s vs %s", ObfKey("g", "p@x", a), ObfKey("g", "p@x", b))
	}
	if ObfKey("g", "p@x", c) != ObfKey("g", "p@x", d) {
		t.Fatalf("glued-hash split: %s vs %s", ObfKey("g", "p@x", c), ObfKey("g", "p@x", d))
	}
	if ObfKey("g", "p@x", a) == ObfKey("g", "p@x", c) {
		t.Fatal("distinct tokens must not share a key")
	}
	ps := ParseSubject(a)
	if !ps.Obfuscated {
		t.Fatalf("lead hash should be obfuscated, got %+v", ps)
	}
}

func TestStageObfuscated_EmptySkeletonDoesNotStage(t *testing.T) {
	c := &Crawler{NameFixEnabled: true}
	rows := c.buildHeaders("alt.binaries.bluray", []usenet.MessageOverview{
		{Number: 1, Subject: `[01/10] - ".part01.rar" yEnc (1/20)`, From: "p@x", MessageID: "a@x", Bytes: 100},
		{Number: 2, Subject: `[02/10] - ".part02.rar" yEnc (1/20)`, From: "p@x", MessageID: "b@x", Bytes: 100},
	})
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	for i, h := range rows {
		if isObfKey(h.ReleaseName) {
			t.Fatalf("row %d empty skeleton staged as %q", i, h.ReleaseName)
		}
	}
}

func TestParsesAsRelease(t *testing.T) {
	for name, want := range map[string]bool{
		"Some.Movie.2021.1080p.BluRay.x264-GRP": true,
		"Some.Show.S01E02.720p.HDTV.x264-GRP":   true,
		"Some.Movie.2021":                       false, // no resolution/source
		"Some.Movie.1080p.BluRay.x264-GRP":      false, // no year/episode
		"0123456789abcdef0123456789abcdef":      false,
	} {
		if got := parsesAsRelease(name); got != want {
			t.Errorf("parsesAsRelease(%q) = %v, want %v", name, got, want)
		}
	}
}

type fakePreDB struct {
	hashes     map[string]PreDBHit
	names      map[string]PreDBHit
	filenames  map[string]PreDBHit
	requestIDs map[int64]PreDBHit
}

func (f *fakePreDB) ByHash(_ context.Context, h string) (PreDBHit, bool) {
	v, ok := f.hashes[h]
	return v, ok
}
func (f *fakePreDB) ByName(_ context.Context, n string) (PreDBHit, bool) {
	v, ok := f.names[n]
	return v, ok
}
func (f *fakePreDB) ByFilename(_ context.Context, n string) (PreDBHit, bool) {
	v, ok := f.filenames[strings.ToLower(n)]
	return v, ok
}
func (f *fakePreDB) ByRequestID(_ context.Context, id int64, _ string) (PreDBHit, bool) {
	v, ok := f.requestIDs[id]
	return v, ok
}

const nfGroup = "alt.binaries.movies"

// obfuscatedSet builds files x 2 OVER rows of one hash-obfuscated RAR set plus
// a small .par2 (first segment msgid "<prefix>par2"). ~131 MB payload.
func obfuscatedSet(prefix, hash string, startNum, files int) []usenet.MessageOverview {
	var out []usenet.MessageOverview
	n := startNum
	for f := 1; f <= files; f++ {
		for p := 1; p <= 2; p++ {
			out = append(out, usenet.MessageOverview{
				Number:    n,
				Subject:   fmt.Sprintf(`[%02d/%d] - "%s.part%02d.rar" yEnc (%d/2)`, f, files+1, hash, f, p),
				From:      "poster@obf",
				MessageID: fmt.Sprintf("%s%d_%d@t", prefix, f, p),
				Bytes:     2_000_000,
				DateUnix:  1700000000,
			})
			n++
		}
	}
	out = append(out, usenet.MessageOverview{
		Number:    n,
		Subject:   fmt.Sprintf(`[%02d/%d] - "%s.par2" yEnc (1/1)`, files+1, files+1, hash),
		From:      "poster@obf",
		MessageID: prefix + "par2",
		Bytes:     40_000,
		DateUnix:  1700000000,
	})
	return out
}

type nfFixture struct {
	idx   *Index
	src   *crawlStub
	crawl *Crawler
	nf    *NameFixer
}

func newNFFixture(t *testing.T, rows []usenet.MessageOverview, admission *PolicySnapshot) *nfFixture {
	t.Helper()
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	src := &crawlStub{gr: usenet.GroupRange{Low: 1, High: len(rows)}, rows: rows, bodies: map[string]usenet.BodySample{}}
	c := &Crawler{Src: src, Index: idx, Groups: []string{nfGroup}, NameFixEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} }}
	if admission != nil {
		c.AdmissionEnabled = true
		c.Admitter = newAdmitter(&fakePolicy{snap: admission})
	}
	budget := &bodyBudget{}
	budget.setCapMB(0)
	nf := &NameFixer{Src: src, Index: idx, Crawler: c, Groups: []string{nfGroup}, Budget: budget}
	return &nfFixture{idx: idx, src: src, crawl: c, nf: nf}
}

func (f *nfFixture) crawlAndRecompute(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.crawl.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.idx.RecomputeDirty(ctx, 500); err != nil {
		t.Fatal(err)
	}
}

func (f *nfFixture) release(t *testing.T) (key, state, display string, attempts int) {
	t.Helper()
	err := f.idx.db.QueryRow(`SELECT release_key, state, display_name, name_attempts FROM usenet_nntp_releases LIMIT 1`).Scan(&key, &state, &display, &attempts)
	if err != nil {
		t.Fatalf("release row: %v", err)
	}
	return
}

const nfHash = "0123456789abcdef0123456789abcdef"

func par2Body(names ...string) usenet.BodySample {
	var data []byte
	for _, n := range names {
		data = append(data, par2FileDescPacket(n)...)
	}
	return usenet.BodySample{Data: data, YencName: nfHash + ".par2", WireBytes: int64(len(data))}
}

func TestNameFix_StagedThenRenamedFromPar2ThenSearchable(t *testing.T) {
	rows := obfuscatedSet("o", nfHash, 1, 45)
	f := newNFFixture(t, rows, nil)
	ctx := context.Background()
	f.crawlAndRecompute(t)

	key, state, display, _ := f.release(t)
	if !isObfKey(key) || state != statePendingName || display != "" {
		t.Fatalf("staged release = %q %q %q", key, state, display)
	}
	if cs, _ := f.idx.SearchReleases(ctx, Query{}); len(cs) != 0 {
		t.Fatalf("staged set must not be searchable: %v", cs)
	}

	f.src.bodies["opar2"] = par2Body("Some.Movie.2021.1080p.BluRay.x264-GRP.part01.rar", "Some.Movie.2021.1080p.BluRay.x264-GRP.part02.rar")
	res, err := f.nf.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed != 1 || res.BySource[sourcePar2] != 1 || res.BodyFetches != 1 {
		t.Fatalf("result = %+v", res)
	}
	var searchable bool
	if err := f.idx.db.QueryRow(`SELECT searchable FROM usenet_nntp_releases`).Scan(&searchable); err != nil {
		t.Fatal(err)
	}
	if !searchable {
		t.Fatal("renamed 100% set must be searchable without waiting for RecomputeDirty")
	}
	if _, err := f.idx.RecomputeDirty(ctx, 500); err != nil {
		t.Fatal(err)
	}
	cs, err := f.idx.SearchReleases(ctx, Query{Terms: []string{"Some.Movie"}})
	if err != nil || len(cs) != 1 {
		t.Fatalf("search = %v err=%v", cs, err)
	}
	if cs[0].Name != "Some.Movie.2021.1080p.BluRay.x264-GRP" {
		t.Fatalf("name = %q", cs[0].Name)
	}
	nzb, err := ToNZB(cs[0])
	if err != nil {
		t.Fatal(err)
	}
	segs := 0
	for _, nzf := range nzb.Files {
		segs += len(nzf.Segs)
	}
	if segs != len(rows) {
		t.Fatalf("nzb segments = %d, want %d (all set segments)", segs, len(rows))
	}
	// The locator (seed msgid -> staged key) still resolves to the renamed set.
	c, err := f.idx.LoadBySeed(ctx, nfGroup, cs[0].SeedMsgID)
	if err != nil || c.Name != cs[0].Name {
		t.Fatalf("locator round-trip = %q err=%v", c.Name, err)
	}
}

func TestNameFix_Par2FileDescHashHitsPreDB(t *testing.T) {
	const title = "FileDesc.Movie.2021.1080p.BluRay.x264-GRP"
	sum := md5.Sum([]byte(title))
	h := hex.EncodeToString(sum[:])
	f := newNFFixture(t, obfuscatedSet("fd", nfHash, 1, 1), nil)
	f.nf.PreDB = &fakePreDB{hashes: map[string]PreDBHit{h: {Title: title}}}
	f.crawlAndRecompute(t)
	f.src.bodies["fdpar2"] = par2Body(h + ".part01.rar")
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 || res.BySource[sourcePreDB] != 1 || res.BodyFetches != 1 {
		t.Fatalf("PAR2 FileDesc hash must PreDB-rename: %+v", res)
	}
	if _, _, display, _ := f.release(t); display != title {
		t.Fatalf("display = %q", display)
	}
}

func TestNameFix_Md5SubjectResolvesWithZeroBodyFetches(t *testing.T) {
	const title = "Another.Movie.2020.1080p.WEB-DL.x264-GRP"
	sum := md5.Sum([]byte(title))
	h := hex.EncodeToString(sum[:])
	f := newNFFixture(t, obfuscatedSet("m", h, 1, 45), nil)
	f.nf.PreDB = &fakePreDB{hashes: map[string]PreDBHit{h: {Title: title}}}
	f.crawlAndRecompute(t)
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 || res.BodyFetches != 0 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("result = %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	if _, _, display, _ := f.release(t); display != title {
		t.Fatalf("display = %q", display)
	}
}

func TestNameFix_GateRejectedNameDeletesSet(t *testing.T) {
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.High}, 2160, 0)}}
	f := newNFFixture(t, obfuscatedSet("r", nfHash, 1, 45), snap)
	f.crawlAndRecompute(t)
	f.src.bodies["rpar2"] = par2Body("Some.Movie.2021.1080p.BluRay.x264-GRP.part01.rar")
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.RejectGate != 1 || res.Renamed != 0 {
		t.Fatalf("result = %+v", res)
	}
	var n int
	f.idx.db.QueryRow(`SELECT (SELECT COUNT(*) FROM usenet_nntp_headers)+(SELECT COUNT(*) FROM usenet_nntp_releases)`).Scan(&n)
	if n != 0 {
		t.Fatalf("rows left after gate reject = %d", n)
	}
	// The rejected set does not re-stage when its rows are crawled again.
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_watermarks SET high=0`); err != nil {
		t.Fatal(err)
	}
	f.crawlAndRecompute(t)
	f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases`).Scan(&n)
	if n != 0 {
		t.Fatalf("rejected set re-staged: %d", n)
	}
}

func TestNameFix_MissingStatDeletesSet(t *testing.T) {
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.High, quality.Medium}, 0, 0)}}
	f := newNFFixture(t, obfuscatedSet("s", nfHash, 1, 45), snap)
	f.crawlAndRecompute(t)
	f.src.bodies["spar2"] = par2Body("Some.Movie.2021.1080p.BluRay.x264-GRP.part01.rar")
	for _, id := range pickSampleIDs(t, f) {
		if f.src.stat == nil {
			f.src.stat = map[string]usenet.StatVerdict{}
		}
		f.src.stat[id] = usenet.StatMissing
	}
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.RejectStat != 1 || res.Renamed != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func pickSampleIDs(t *testing.T, f *nfFixture) []string {
	t.Helper()
	key, _, _, _ := f.release(t)
	rows, err := f.idx.SetRows(context.Background(), nfGroup, key)
	if err != nil {
		t.Fatal(err)
	}
	return pickSample(rows)
}

func TestNameFix_BodyBudgetStopsPassWithoutAttempts(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("b", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	// Unresolvable first sample (600 kB, no name) exhausts most of a 1 MiB cap;
	// the next pick (a 2 MB payload article) no longer fits.
	f.src.bodies["bpar2"] = usenet.BodySample{Data: []byte("nothing"), WireBytes: 600_000}
	f.nf.Budget.capBytes = 1 << 20
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.BudgetStop || res.BodyBytes > 1<<20 || res.BodyBytes != 600_000 || res.Unresolved != 0 {
		t.Fatalf("result = %+v", res)
	}
	if _, state, _, attempts := f.release(t); state != statePendingName || attempts != 0 {
		t.Fatalf("state=%s attempts=%d, want pending/0 (budget stop charges nothing)", state, attempts)
	}
}

func TestNameFix_BodyArticleTimeoutContinuesToNextPick(t *testing.T) {
	nfo := usenet.MessageOverview{
		Number: 1, Subject: fmt.Sprintf(`[%02d/%d] - "%s.nfo" yEnc (1/1)`, 1, 2, nfHash),
		From: "poster@obf", MessageID: "tnfo", Bytes: 10_000, DateUnix: 1700000000,
	}
	rows := append([]usenet.MessageOverview{nfo}, obfuscatedSet("t", nfHash, 2, 1)...)
	f := newNFFixture(t, rows, nil)
	f.crawlAndRecompute(t)
	f.src.bodyFailFirst = 1
	f.src.bodies["tnfo"] = usenet.BodySample{Data: []byte("Release info"), WireBytes: 1000}
	f.src.bodies["tpar2"] = par2Body("Some.Movie.2021.1080p.BluRay.x264-GRP.part01.rar")
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed != 1 || res.BySource[sourcePar2] != 1 || res.BodyFetches < 2 {
		t.Fatalf("hung first BODY must not abort the phase: %+v calls=%v", res, f.src.bodyCalls)
	}
	if _, _, display, _ := f.release(t); display != "Some.Movie.2021.1080p.BluRay.x264-GRP" {
		t.Fatalf("display = %q", display)
	}
}

func TestPickBodyTargets_PrefersSmallPar2(t *testing.T) {
	rows := []HeaderRow{
		{MsgID: "huge@t", Filename: "hash.part01.rar", Bytes: 2_000_000, FileN: 1},
		{MsgID: "par2@t", Filename: "hash.par2", Bytes: 20_000, IsMeta: true},
	}
	picks := pickBodyTargets(rows)
	if len(picks) == 0 || picks[0].MsgID != "par2@t" {
		t.Fatalf("picks = %+v, want small par2 first", picks)
	}
}

func TestNameFix_BusyStopsWithoutAttempts(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("y", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	f.src.bodyErr = usenet.ErrBusy
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Busy || res.Unresolved != 0 {
		t.Fatalf("result = %+v", res)
	}
	if _, _, _, attempts := f.release(t); attempts != 0 {
		t.Fatalf("attempts = %d", attempts)
	}
}

func TestNameFix_UnresolvedBacksOffThenKeepsCompleteYield(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("u", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	ctx := context.Background()
	f.src.bodies["upar2"] = usenet.BodySample{Data: []byte("nothing"), WireBytes: 1000}
	for i := 1; i <= 3; i++ {
		if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET next_name_at=0`); err != nil {
			t.Fatal(err)
		}
		res, err := f.nf.Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 && res.Unresolved == 0 && res.Finalized == 0 {
			t.Fatalf("pass %d did nothing: %+v", i, res)
		}
	}
	res, _ := f.nf.Run(ctx)
	if res.Sets != 0 {
		t.Fatalf("set retried before backoff elapsed: %+v", res)
	}
	var n int
	f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases`).Scan(&n)
	if n != 1 {
		t.Fatalf("complete yield set deleted after 3 failed attempts: %d", n)
	}
}

func TestDuePendingNames_SkipsExhaustedKeptSets(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("u", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	ctx := context.Background()
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET name_attempts=?, next_name_at=0, dirty=FALSE`, maxNameAttempts); err != nil {
		t.Fatal(err)
	}
	sets, err := f.idx.DuePendingNames(ctx, []string{nfGroup}, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 0 {
		t.Fatalf("due=%d, want 0 (exhausted kept yield is shelved)", len(sets))
	}
	used, err := f.idx.StagedBytes(ctx, nfGroup)
	if err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Fatalf("staged_bytes=%d, want 0 (exhausted sets do not fill the unnamed cap)", used)
	}
}

func TestSweepUnnamed_StillDropsFirehoseAfterAttempts(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("u", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	ctx := context.Background()
	const fire = "alt.binaries.boneless"
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET group_name=?`, fire); err != nil {
		t.Fatal(err)
	}
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_headers SET group_name=?`, fire); err != nil {
		t.Fatal(err)
	}
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET name_attempts=?`, maxNameAttempts); err != nil {
		t.Fatal(err)
	}
	n, err := f.idx.SweepUnnamed(ctx, DefaultUnnamedTTLDays*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("swept=%d, want 1 boneless complete set", n)
	}
}

func TestNameFix_SmallSetFinalizedWithoutBody(t *testing.T) {
	rows := obfuscatedSet("z", nfHash, 1, 45)
	rows = rows[:len(rows)-1] // no PAR2: junk under 100 MiB is dropped
	for i := range rows {
		rows[i].Bytes = 1000 // ~60 kB payload, far under 100 MiB
	}
	f := newNFFixture(t, rows, nil)
	f.crawlAndRecompute(t)
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Finalized != 1 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("result = %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
}

func TestNameFix_SmallPar2SetTriesBody(t *testing.T) {
	rows := obfuscatedSet("z", nfHash, 1, 45)
	for i := range rows {
		rows[i].Bytes = 1000
	}
	f := newNFFixture(t, rows, nil)
	f.crawlAndRecompute(t)
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Finalized != 0 || len(f.src.bodyCalls) == 0 {
		t.Fatalf("small PAR2 set must BODY, not finalize: %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
}

func TestNameFix_TTLFinalizesOldSets(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("t", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	const fire = "alt.binaries.boneless"
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET group_name=?`, fire); err != nil {
		t.Fatal(err)
	}
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_headers SET group_name=?`, fire); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour).Unix()
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET ingested_at=?`, old); err != nil {
		t.Fatal(err)
	}
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Finalized != 1 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

// padPendingOverCap inserts dummy headers on group's pending sets so
// StagedBytes exceeds unnamedCapBytes(maxGB, pct).
func padPendingOverCap(t *testing.T, idx *Index, group string, maxGB, pct int) {
	t.Helper()
	ctx := context.Background()
	used, err := idx.StagedBytes(ctx, group)
	if err != nil {
		t.Fatal(err)
	}
	cap := unnamedCapBytes(maxGB, pct)
	if used >= cap {
		return
	}
	need := (cap-used)/approxHeaderRowBytes + 1
	_, err = idx.db.Exec(`
INSERT INTO usenet_nntp_headers (group_name, msg_num, msgid, subject, from_addr, posted_at, bytes,
  release_name, part_n, part_m, filename, yenc_size, obfuscated, is_meta, ingested_at, file_n, file_m)
SELECT r.group_name, 2000000000 + g, 'pad-' || g || '@t', 'pad', 'poster@obf', 1700000000, 1,
  r.release_key, 1, 1, 'pad.rar', 0, TRUE, FALSE, 1700000000, 1, 1
FROM usenet_nntp_releases r
CROSS JOIN generate_series(1, ?) AS g
WHERE r.group_name=? AND r.state='pending_name' AND r.name_attempts < ?`,
		need, group, maxNameAttempts)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStagedBytes_HeaderOccupancyNotPayload(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("a", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	ctx := context.Background()
	var headers int64
	if err := f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_headers`).Scan(&headers); err != nil {
		t.Fatal(err)
	}
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET payload_bytes=?`, int64(20)<<30); err != nil {
		t.Fatal(err)
	}
	used, err := f.idx.StagedBytes(ctx, nfGroup)
	if err != nil {
		t.Fatal(err)
	}
	want := headers * approxHeaderRowBytes
	if used != want {
		t.Fatalf("StagedBytes=%d, want %d (headers×%d); payload must not fill the unnamed cap", used, want, approxHeaderRowBytes)
	}
}

func TestStaging_CapDropsNewSetsButKeepsExisting(t *testing.T) {
	rowsA := obfuscatedSet("a", nfHash, 1, 45)
	f := newNFFixture(t, rowsA, nil)
	f.crawl.MaxGB = 1
	f.crawl.UnnamedMaxPct = 1
	f.crawlAndRecompute(t)
	padPendingOverCap(t, f.idx, nfGroup, 1, 1)
	const h2 = "fedcba9876543210fedcba9876543210"
	rowsB := obfuscatedSet("c", h2, len(rowsA)+1, 45)
	// New rows for the already-staged set A plus a brand-new set B.
	extra := usenet.MessageOverview{
		Number: len(rowsA) + len(rowsB) + 1, Subject: fmt.Sprintf(`[46/46] - "%s.part46.rar" yEnc (1/1)`, nfHash),
		From: "poster@obf", MessageID: "a46@t", Bytes: 1000, DateUnix: 1700000000,
	}
	f.src.rows = append(append(f.src.rows, rowsB...), extra)
	f.src.gr.High = len(f.src.rows)
	logs := captureLog(t, func() {
		f.crawl.AdmissionEnabled = true
		f.crawl.Admitter = newAdmitter(&fakePolicy{snap: &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd(nil, 0, 0)}}})
		if _, err := f.crawl.CrawlOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	var sets int
	f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases`).Scan(&sets)
	if sets != 1 {
		t.Fatalf("sets = %d, want 1 (new set B dropped over the cap)", sets)
	}
	var n int
	f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_headers WHERE msgid='a46@t'`).Scan(&n)
	if n != 1 {
		t.Fatal("row of an already-staged set must still be kept")
	}
	if !strings.Contains(logs, "skip_obf_cap=") || strings.Contains(logs, "skip_obf_cap=0") {
		t.Fatalf("log missing skip_obf_cap>0:\n%s", logs)
	}
}

func TestStaging_CapIsPerGroup(t *testing.T) {
	rowsA := obfuscatedSet("a", nfHash, 1, 45)
	f := newNFFixture(t, rowsA, nil)
	f.crawl.MaxGB = 1
	f.crawl.UnnamedMaxPct = 1
	f.crawlAndRecompute(t)
	padPendingOverCap(t, f.idx, nfGroup, 1, 1)
	const other = "alt.binaries.moovee"
	const h2 = "fedcba9876543210fedcba9876543210"
	rowsB := obfuscatedSet("c", h2, 1, 5)
	f.src.rows = rowsB
	f.src.gr = usenet.GroupRange{Low: 1, High: len(rowsB)}
	f.crawl.Groups = []string{other}
	if _, err := f.crawl.CrawlOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases WHERE group_name=?`, other).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("moovee sets=%d, want 1 (peer group's cap must not block)", n)
	}
}

func TestStaging_Step5NoiseNotStaged(t *testing.T) {
	rows := obfuscatedSet("b", nfHash, 1, 5)
	f := newNFFixture(t, rows, nil)
	f.crawl.Groups = []string{"alt.binaries.boneless"}
	f.nf.Groups = []string{"alt.binaries.boneless"}
	f.crawlAndRecompute(t)
	var n int
	if err := f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("releases=%d, want 0 (step-5 noise must not fill unnamed budget)", n)
	}
}

func TestCrawl_EmptyYieldStopsGroup(t *testing.T) {
	var rows []usenet.MessageOverview
	for i := 1; i <= overviewChunk*3; i++ {
		rows = append(rows, usenet.MessageOverview{
			Number: i, Subject: "nope", From: "x@y", MessageID: fmt.Sprintf("n%d@t", i),
			Bytes: 100, DateUnix: 1700000000,
		})
	}
	f := newNFFixture(t, rows, &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd(nil, 0, 0)}})
	f.crawl.MaxChunksPerGroup = 3
	f.crawl.MaxChunksPerPass = 3
	if _, err := f.crawl.CrawlOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	wm, err := f.idx.Watermark(context.Background(), nfGroup)
	if err != nil {
		t.Fatal(err)
	}
	if wm != overviewChunk {
		t.Fatalf("watermark=%d, want %d (one empty chunk, not three)", wm, overviewChunk)
	}
}

func TestStaging_FirehoseWithoutNamedDropsNewSets(t *testing.T) {
	rows := obfuscatedSet("b", nfHash, 1, 5)
	f := newNFFixture(t, rows, nil)
	f.crawl.Groups = []string{"alt.binaries.bloaf"}
	f.nf.Groups = []string{"alt.binaries.bloaf"}
	f.crawlAndRecompute(t)
	var n int
	if err := f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("releases=%d, want 0 (bloaf must not fill the unnamed budget)", n)
	}
}

func TestStaging_YieldGroupEmptyIndexStages(t *testing.T) {
	rows := obfuscatedSet("a", nfHash, 1, 5)
	f := newNFFixture(t, rows, nil)
	const g = "alt.binaries.multimedia.anime.highspeed"
	f.crawl.Groups = []string{g}
	f.nf.Groups = []string{g}
	f.crawlAndRecompute(t)
	var n int
	if err := f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases WHERE group_name=?`, g).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("highspeed sets=%d, want 1 (yield group must stage with no named release)", n)
	}
}

// With namefix off the crawl keeps today's behavior: nothing is staged (no
// obf-keyed headers, no pending_name release rows); rows are parsed exactly as
// ParseSubject always did.
func TestNameFixOff_ObfuscatedRowsNotStaged(t *testing.T) {
	rows := obfuscatedSet("g", nfHash, 1, 5)
	f := newNFFixture(t, rows, nil)
	f.crawl.NameFixEnabled = false
	f.crawlAndRecompute(t)
	var n int
	f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases WHERE state='pending_name'`).Scan(&n)
	if n != 0 {
		t.Fatalf("pending_name rows = %d, want 0 with namefix off", n)
	}
	f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_headers WHERE release_name LIKE 'obf:%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("obf-keyed headers = %d, want 0 with namefix off", n)
	}
	f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_headers`).Scan(&n)
	if n != len(rows) {
		t.Fatalf("headers = %d, want %d (legacy upserts every row)", n, len(rows))
	}
}

func TestCrawl_ImplausibleYencNotStaged(t *testing.T) {
	rows := []usenet.MessageOverview{{
		Number: 1, Subject: `"UrHfhOEuTZTAP6SWX2HhDYJ6N" yEnc (730696/967646)`,
		From: "p@x", MessageID: "j@t", Bytes: 500000, DateUnix: 1700000000,
	}}
	f := newNFFixture(t, rows, nil)
	f.crawlAndRecompute(t)
	var n int
	f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases WHERE state='pending_name'`).Scan(&n)
	if n != 0 {
		t.Fatalf("pending_name=%d, want 0 (unique-ID yEnc is not a set)", n)
	}
}

func TestSweepImplausibleYenc_RemovesPending(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.hdtv.x264"
	if err := idx.Upsert(ctx, []HeaderRow{{
		Group: g, MsgNum: 1, MsgID: "j@t", Subject: `"UrHfhOEuTZTAP6SWX2HhDYJ6N" yEnc (730696/967646)`,
		From: "p@x", PostedAt: 1, Bytes: 1000, ReleaseName: "obf:deadbeefdeadbeef",
		PartN: 730696, PartM: 967646,
	}}); err != nil {
		t.Fatal(err)
	}
	n, err := idx.SweepImplausibleYenc(ctx, 10)
	if err != nil || n != 1 {
		t.Fatalf("removed=%d err=%v, want 1", n, err)
	}
	var left int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("releases left=%d, want 0", left)
	}
}

func TestDuePendingNames_StagingCoresFirst(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const core = "alt.binaries.moovee"
	const tail = "alt.binaries.boneless"
	rows := append(
		fileRows(core, "obf:cccccccccccccccc", "c.rar", "c", 0, 10, 10, 0),
		fileRows(tail, "obf:tttttttttttttttt", "t.rar", "t", 100, 10, 10, 0)...,
	)
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`UPDATE usenet_nntp_releases SET dirty=FALSE, next_name_at=0`); err != nil {
		t.Fatal(err)
	}
	sets, err := idx.DuePendingNames(ctx, []string{tail, core}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || sets[0].Group != core {
		t.Fatalf("got %+v, want staging core %s first", sets, core)
	}
}

func TestDuePendingNames_SkipsBonelessWhenAnimeDue(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const yield = "alt.binaries.anime"
	const firehose = "alt.binaries.boneless"
	rows := append(
		fileRows(yield, "obf:aaaaaaaaaaaaaaaa", "a.rar", "a", 0, 10, 10, 0),
		fileRows(firehose, "obf:bbbbbbbbbbbbbbbb", "b.rar", "b", 100, 10, 10, 0)...,
	)
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`UPDATE usenet_nntp_releases SET dirty=FALSE, next_name_at=0, ingested_at=CASE WHEN group_name=? THEN 1 ELSE 99 END`, yield); err != nil {
		t.Fatal(err)
	}
	sets, err := idx.DuePendingNames(ctx, []string{firehose, yield}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || sets[0].Group != yield {
		t.Fatalf("got %+v, want only yield group %s (boneless must not fill leftover slots)", sets, yield)
	}
}

func TestGroupHasNamedRelease_IgnoresSubjectNamed(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.boneless"
	if err := idx.Upsert(ctx, fileRows(g, "Some.Dump.2020.1080p", "d.mkv", "d", 0, 10, 10, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	ok, err := idx.GroupHasNamedRelease(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("subject-named rows must not unlock staging/backfill investment")
	}
	if _, err := sqlDB.Exec(`UPDATE usenet_nntp_releases SET name_source='par2', searchable=TRUE`); err != nil {
		t.Fatal(err)
	}
	ok, err = idx.GroupHasNamedRelease(ctx, g)
	if err != nil || !ok {
		t.Fatalf("par2 searchable should unlock investment: ok=%v err=%v", ok, err)
	}
}

func TestHashTokens_StripsArchiveSuffix(t *testing.T) {
	const h = "a3de4a915e25ce1f8c0e7b315e6e0257"
	got := hashTokens(h + ".7z.001")
	if len(got) != 1 || got[0] != h {
		t.Fatalf("hashTokens = %v", got)
	}
	if hashCore(h+".7z.001") != "" {
		t.Fatal("hashCore must not accept a suffixed filename")
	}
	const sha = "0123456789abcdef0123456789abcdef01234567"
	got = hashTokens(`"` + sha + `.part01.rar"`)
	if len(got) != 1 || got[0] != sha {
		t.Fatalf("sha1 token = %v", got)
	}
}

func leadTokenHashSet(prefix, token, hash string, startNum, files int) []usenet.MessageOverview {
	var out []usenet.MessageOverview
	n := startNum
	for f := 1; f <= files; f++ {
		fn := fmt.Sprintf("%s.part%02d.rar", hash, f)
		for p := 1; p <= 2; p++ {
			out = append(out, usenet.MessageOverview{
				Number:    n,
				Subject:   fmt.Sprintf(`%s - [%02d/%d] - "%s" yEnc (%d/2)`, token, f, files+1, fn, p),
				From:      "poster@obf",
				MessageID: fmt.Sprintf("%s%d_%d@t", prefix, f, p),
				Bytes:     2_000_000,
				DateUnix:  1700000000,
			})
			n++
		}
	}
	out = append(out, usenet.MessageOverview{
		Number:    n,
		Subject:   fmt.Sprintf(`%s - [%02d/%d] - "%s.par2" yEnc (1/1)`, token, files+1, files+1, hash),
		From:      "poster@obf",
		MessageID: prefix + "par2",
		Bytes:     40_000,
		DateUnix:  1700000000,
	})
	return out
}

func TestNameFix_FilenameHashResolvesFromPreDB(t *testing.T) {
	const title = "Lead.Token.2021.1080p.WEB-DL.x264-GRP"
	const token = "ah63jka93jf0jh26ahjas558"
	sum := md5.Sum([]byte(title))
	h := hex.EncodeToString(sum[:])
	rows := leadTokenHashSet("lh", token, h, 1, 45)
	if hashCore(obfSkeleton(rows[0].Subject)) == h {
		t.Fatal("setup: grouping skeleton must be the lead token, not the filename hash")
	}
	f := newNFFixture(t, rows, nil)
	f.nf.PreDB = &fakePreDB{hashes: map[string]PreDBHit{h: {Title: title}}}
	f.crawlAndRecompute(t)
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 || res.BodyFetches != 0 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("result = %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	if _, _, display, _ := f.release(t); display != title {
		t.Fatalf("display = %q", display)
	}
}

func TestNameFix_PreDBFilenameResolvesRandomArchiveName(t *testing.T) {
	const title = "Named.From.File.2020.1080p.BluRay.x264-GRP"
	const token = "KLoSR1zOTNoaex3x"
	rows := leadTokenHashSet("fn", token, "v1AAqzDH11LfEAAN9yuzTJ", 1, 45)
	f := newNFFixture(t, rows, nil)
	f.nf.PreDB = &fakePreDB{filenames: map[string]PreDBHit{
		"v1aaqzdh11lfeaan9yuztj.part01.rar": {Title: title},
	}}
	f.crawlAndRecompute(t)
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("result = %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
}

func TestNameFix_RequestIDResolvesFromPreDB(t *testing.T) {
	const title = "Grey.S21E03.720p.HDTV.x264-GRP"
	const id int64 = 23051509
	rows := requestIDSet("rq", id, 1, 45)
	f := newNFFixture(t, rows, nil)
	f.nf.PreDB = &fakePreDB{requestIDs: map[int64]PreDBHit{id: {Title: title}}}
	f.crawlAndRecompute(t)
	key, state, display, _ := f.release(t)
	if !isObfKey(key) || state != statePendingName || display != "" {
		t.Fatalf("staged = %q %q %q", key, state, display)
	}
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 || res.BodyFetches != 0 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("result = %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	if _, _, display, _ := f.release(t); display != title {
		t.Fatalf("display = %q", display)
	}
}

func requestIDSet(prefix string, id int64, startNum, files int) []usenet.MessageOverview {
	var out []usenet.MessageOverview
	n := startNum
	for f := 1; f <= files; f++ {
		for p := 1; p <= 2; p++ {
			out = append(out, usenet.MessageOverview{
				Number: n,
				Subject: fmt.Sprintf(`[%d]-[FULL]-[#a.b.teevee@EFNet]-[ ]-[%02d/%d] - "%s.part%02d.rar" yEnc (%d/2)`,
					id, f, files+1, prefix, f, p),
				From:      "poster@req",
				MessageID: fmt.Sprintf("%s%d_%d@t", prefix, f, p),
				Bytes:     2_000_000,
				DateUnix:  1700000000,
			})
			n++
		}
	}
	out = append(out, usenet.MessageOverview{
		Number: n,
		Subject: fmt.Sprintf(`[%d]-[FULL]-[#a.b.teevee@EFNet]-[ ]-[%02d/%d] - "%s.par2" yEnc (1/1)`,
			id, files+1, files+1, prefix),
		From:      "poster@req",
		MessageID: prefix + "par2",
		Bytes:     40_000,
		DateUnix:  1700000000,
	})
	return out
}

func dropPayloadParts(t *testing.T, idx *Index, n int) {
	t.Helper()
	if n <= 0 {
		return
	}
	res, err := idx.db.Exec(`
DELETE FROM usenet_nntp_headers
WHERE msgid IN (
  SELECT msgid FROM usenet_nntp_headers
  WHERE is_meta=FALSE AND part_n=2
  ORDER BY msg_num
  LIMIT ?
)`, n)
	if err != nil {
		t.Fatal(err)
	}
	deleted, _ := res.RowsAffected()
	if deleted != int64(n) {
		t.Fatalf("deleted payload parts=%d, want %d", deleted, n)
	}
	var key string
	if err := idx.db.QueryRow(`SELECT release_key FROM usenet_nntp_releases LIMIT 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if err := idx.markDirty(context.Background(), []releaseKey{{nfGroup, key}}); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
}

func TestDuePendingPreDB_IncludesBelowGatePending(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("bg", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	dropPayloadParts(t, f.idx, 9) // 81/90 = 90%
	ctx := context.Background()
	due, err := f.idx.DuePendingNames(ctx, []string{nfGroup}, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("DuePendingNames=%d, want 0 (BODY stays at 95%%)", len(due))
	}
	sets, err := f.idx.DuePendingPreDB(ctx, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || !sets[0].PreDBOnly {
		t.Fatalf("DuePendingPreDB=%+v, want one PreDB-only 90%% set", sets)
	}
}

func TestNameFix_PreDBBelowGateSearchableNotGrabReady(t *testing.T) {
	const title = "Below.Gate.2021.1080p.WEB-DL.x264-GRP"
	sum := md5.Sum([]byte(title))
	h := hex.EncodeToString(sum[:])
	f := newNFFixture(t, obfuscatedSet("bg", h, 1, 45), nil)
	f.nf.PreDB = &fakePreDB{hashes: map[string]PreDBHit{h: {Title: title}}}
	f.crawlAndRecompute(t)
	dropPayloadParts(t, f.idx, 9)
	ctx := context.Background()
	res, err := f.nf.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 || res.BodyFetches != 0 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("result=%+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	var searchable bool
	var source string
	var bp int
	if err := f.idx.db.QueryRow(`SELECT searchable, name_source, completeness_bp FROM usenet_nntp_releases`).Scan(&searchable, &source, &bp); err != nil {
		t.Fatal(err)
	}
	if !searchable || source != sourcePreDB || bp < spanRepairFloorBP || bp >= f.idx.MinCompletenessPct()*100 {
		t.Fatalf("searchable=%v source=%s bp=%d", searchable, source, bp)
	}
	cs, err := f.idx.SearchReleases(ctx, Query{Terms: []string{"Below.Gate"}})
	if err != nil || len(cs) != 1 || cs[0].Complete || cs[0].Name != title {
		t.Fatalf("search=%+v err=%v", cs, err)
	}
	if _, err := ToNZB(cs[0]); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("ToNZB err=%v, want ErrIncomplete", err)
	}
	grab, err := f.idx.SearchReleases(ctx, Query{Terms: []string{"Below.Gate"}, GrabReady: true})
	if err != nil || len(grab) != 0 {
		t.Fatalf("GrabReady search=%+v err=%v, want empty", grab, err)
	}
}

func TestRenameRelease_Par2BelowGateIsIdentified(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("p2", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	dropPayloadParts(t, f.idx, 9)
	ctx := context.Background()
	key, _, _, _ := f.release(t)
	if err := f.idx.RenameRelease(ctx, nfGroup, key, "Guessed.Movie.2021.1080p.BluRay.x264-GRP", sourcePar2, confMedium); err != nil {
		t.Fatal(err)
	}
	var searchable bool
	if err := f.idx.db.QueryRow(`SELECT searchable FROM usenet_nntp_releases`).Scan(&searchable); err != nil {
		t.Fatal(err)
	}
	if !searchable {
		t.Fatal("PAR2-named below-gate set must be identifiable")
	}
	cs, err := f.idx.SearchReleases(ctx, Query{Terms: []string{"Guessed.Movie"}})
	if err != nil || len(cs) != 1 || cs[0].Complete {
		t.Fatalf("search=%+v err=%v, want identified not grab-complete", cs, err)
	}
	grab, err := f.idx.SearchReleases(ctx, Query{Terms: []string{"Guessed.Movie"}, GrabReady: true})
	if err != nil || len(grab) != 0 {
		t.Fatalf("GrabReady=%+v err=%v", grab, err)
	}
}

func TestDuePendingPreDB_IncludesBoneless(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("bo", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	ctx := context.Background()
	const fire = "alt.binaries.boneless"
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET group_name=?`, fire); err != nil {
		t.Fatal(err)
	}
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_headers SET group_name=?`, fire); err != nil {
		t.Fatal(err)
	}
	due, err := f.idx.DuePendingNames(ctx, []string{fire, nfGroup}, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("DuePendingNames boneless=%d, want 0 (BODY stays yield-only)", len(due))
	}
	sets, err := f.idx.DuePendingPreDB(ctx, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || sets[0].Group != fire || !sets[0].PreDBOnly {
		t.Fatalf("DuePendingPreDB=%+v, want boneless PreDB-only", sets)
	}
}

func TestNameFix_ExhaustedYieldPreDBRetryNoBody(t *testing.T) {
	const title = "Later.Irc.2022.1080p.WEB-DL.x264-GRP"
	sum := md5.Sum([]byte(title))
	h := hex.EncodeToString(sum[:])
	f := newNFFixture(t, obfuscatedSet("ex", h, 1, 45), nil)
	f.crawlAndRecompute(t)
	ctx := context.Background()
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET name_attempts=?, next_name_at=0, dirty=FALSE`, maxNameAttempts); err != nil {
		t.Fatal(err)
	}
	res, err := f.nf.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed != 0 || res.BodyFetches != 0 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("miss must not BODY: %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	if res.Sets != 1 {
		t.Fatalf("exhausted yield should still be tried: %+v", res)
	}
	f.nf.PreDB = &fakePreDB{hashes: map[string]PreDBHit{h: {Title: title}}}
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET next_name_at=0`); err != nil {
		t.Fatal(err)
	}
	res, err = f.nf.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("later PreDB hit = %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	if _, _, display, _ := f.release(t); display != title {
		t.Fatalf("display = %q", display)
	}
}

func TestDuePendingIdentBody_IncludesBelowGatePar2(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("ib", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	dropPayloadParts(t, f.idx, 9)
	ctx := context.Background()
	due, err := f.idx.DuePendingNames(ctx, []string{nfGroup}, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("DuePendingNames=%d, want 0 (95%% BODY gate unchanged)", len(due))
	}
	ident, err := f.idx.DuePendingIdentBody(ctx, []string{nfGroup}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ident) != 1 || ident[0].PreDBOnly || !ident[0].HasPar2 {
		t.Fatalf("DuePendingIdentBody=%+v, want one BODY-capable 90%% PAR2 set", ident)
	}
}

func TestNameFix_Par2BelowGateBODYSearchable(t *testing.T) {
	const title = "Guessed.Movie.2021.1080p.BluRay.x264-GRP"
	f := newNFFixture(t, obfuscatedSet("p2b", nfHash, 1, 45), nil)
	f.crawlAndRecompute(t)
	dropPayloadParts(t, f.idx, 9)
	f.src.bodies["p2bpar2"] = par2Body(title + ".part01.rar")
	ctx := context.Background()
	res, err := f.nf.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed != 1 || res.BySource[sourcePar2] != 1 || res.BodyFetches == 0 {
		t.Fatalf("result=%+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	var searchable bool
	var bp int
	if err := f.idx.db.QueryRow(`SELECT searchable, completeness_bp FROM usenet_nntp_releases`).Scan(&searchable, &bp); err != nil {
		t.Fatal(err)
	}
	if !searchable || bp < spanRepairFloorBP || bp >= f.idx.MinCompletenessPct()*100 {
		t.Fatalf("searchable=%v bp=%d", searchable, bp)
	}
	cs, err := f.idx.SearchReleases(ctx, Query{Terms: []string{"Guessed.Movie"}})
	if err != nil || len(cs) != 1 || cs[0].Complete || cs[0].Name != title {
		t.Fatalf("search=%+v err=%v", cs, err)
	}
	grab, err := f.idx.SearchReleases(ctx, Query{Terms: []string{"Guessed.Movie"}, GrabReady: true})
	if err != nil || len(grab) != 0 {
		t.Fatalf("GrabReady=%+v err=%v", grab, err)
	}
}

func TestNameFix_SkipBodyStillHitsPreDB(t *testing.T) {
	const title = "Quiet.Movie.2022.1080p.WEB-DL.x264-GRP"
	sum := md5.Sum([]byte(title))
	h := hex.EncodeToString(sum[:])
	f := newNFFixture(t, obfuscatedSet("qb", h, 1, 45), nil)
	f.nf.PreDB = &fakePreDB{hashes: map[string]PreDBHit{h: {Title: title}}}
	f.nf.SkipBody = true
	f.src.bodies["qbpar2"] = par2Body("Would.Body.2021.1080p.BluRay.x264-GRP.part01.rar")
	f.crawlAndRecompute(t)
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 || len(f.src.bodyCalls) != 0 {
		t.Fatalf("SkipBody must PreDB without BODY: %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	if _, _, display, _ := f.release(t); display != title {
		t.Fatalf("display=%q", display)
	}
}

func TestNameFix_PreDBOnlyBeforeSlowBODY(t *testing.T) {
	const title = "Boneless.Hit.2022.1080p.WEB-DL.x264-GRP"
	sum := md5.Sum([]byte(title))
	h := hex.EncodeToString(sum[:])
	mv := obfuscatedSet("mv", nfHash, 1, 45)
	bo := obfuscatedSet("bo", h, len(mv)+1, 45)
	f := newNFFixture(t, append(mv, bo...), nil)
	f.nf.PreDB = &fakePreDB{hashes: map[string]PreDBHit{h: {Title: title}}}
	f.nf.PassBudget = 50 * time.Millisecond
	f.src.bodyDelay = 200 * time.Millisecond
	f.src.bodies["mvpar2"] = par2Body(nfHash + ".part01.rar")
	f.crawlAndRecompute(t)
	boKey := ObfKey(nfGroup, "poster@obf", bo[0].Subject)
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_releases SET group_name=? WHERE release_key=?`, "alt.binaries.boneless", boKey); err != nil {
		t.Fatal(err)
	}
	if _, err := f.idx.db.Exec(`UPDATE usenet_nntp_headers SET group_name=? WHERE release_name=?`, "alt.binaries.boneless", boKey); err != nil {
		t.Fatal(err)
	}
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.HashHits != 1 || res.Renamed != 1 {
		t.Fatalf("PreDB-only must run before slow BODY: %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	var display, src string
	if err := f.idx.db.QueryRow(`SELECT display_name, name_source FROM usenet_nntp_releases WHERE release_key=?`, boKey).Scan(&display, &src); err != nil {
		t.Fatal(err)
	}
	if display != title || src != sourcePreDB {
		t.Fatalf("boneless display=%q source=%s", display, src)
	}
}

func TestNameFix_QuotedFansubTitleFromSubject(t *testing.T) {
	const title = "[UQW] Black Clover - Ep170 [BDRip 1080p HEVC-YUV420P10 FLAC]"
	rows := []usenet.MessageOverview{
		{Number: 1, Subject: `[01/2] - "` + title + `.mkv" yEnc (1/2)`, From: "poster@obf", MessageID: "qc1@t", Bytes: 80_000_000, DateUnix: 1700000000},
		{Number: 2, Subject: `[01/2] - "` + title + `.mkv" yEnc (2/2)`, From: "poster@obf", MessageID: "qc2@t", Bytes: 80_000_000, DateUnix: 1700000000},
		{Number: 3, Subject: `[02/2] - "` + title + `.par2" yEnc (1/1)`, From: "poster@obf", MessageID: "qcpar2", Bytes: 40_000, DateUnix: 1700000000},
	}
	f := newNFFixture(t, rows, nil)
	f.crawlAndRecompute(t)
	var display string
	var searchable bool
	if err := f.idx.db.QueryRow(`SELECT display_name, searchable FROM usenet_nntp_releases WHERE display_name<>'' LIMIT 1`).Scan(&display, &searchable); err != nil {
		t.Fatal(err)
	}
	if display != title || !searchable {
		t.Fatalf("quoted ident must be searchable at ingest: display=%q searchable=%v", display, searchable)
	}
}

func TestNameFix_QuotedFansubTitleSurvivesStrictAdmission(t *testing.T) {
	const title = "[UQW] Black Clover - Ep170 [BDRip 1080p HEVC-YUV420P10 FLAC]"
	rows := []usenet.MessageOverview{
		{Number: 1, Subject: `[01/2] - "` + title + `.mkv" yEnc (1/2)`, From: "poster@obf", MessageID: "qa1@t", Bytes: 80_000_000, DateUnix: 1700000000},
		{Number: 2, Subject: `[01/2] - "` + title + `.mkv" yEnc (2/2)`, From: "poster@obf", MessageID: "qa2@t", Bytes: 80_000_000, DateUnix: 1700000000},
		{Number: 3, Subject: `[02/2] - "` + title + `.par2" yEnc (1/1)`, From: "poster@obf", MessageID: "qapar2", Bytes: 40_000, DateUnix: 1700000000},
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.High}, 2160, 0)}}
	f := newNFFixture(t, rows, snap)
	f.crawlAndRecompute(t)
	var n int
	if err := f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases WHERE display_name=? AND searchable`, title).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("spaced ident quoted title must ingest under a 2160p gate, got %d", n)
	}
}

func TestNameFix_QuotedHashFilenameDoesNotRename(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("qh", "uzmv2D5wxtEsPRJRjBN30g", 1, 45), nil)
	f.crawlAndRecompute(t)
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed != 0 {
		t.Fatalf("hash quoted filename must stay unnamed: %+v", res)
	}
}

func TestNameFix_QuotedTitleWithoutResSearchableAtIngest(t *testing.T) {
	const title = "MilfAF - London River"
	var rows []usenet.MessageOverview
	for i := 1; i <= 5; i++ {
		rows = append(rows, usenet.MessageOverview{
			Number: i, Subject: fmt.Sprintf(`"%s.mkv" yEnc (%d/5)`, title, i),
			From: "poster@obf", MessageID: fmt.Sprintf("lr%d@t", i), Bytes: 80_000_000, DateUnix: 1700000000,
		})
	}
	f := newNFFixture(t, rows, nil)
	f.crawlAndRecompute(t)
	var n int
	if err := f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases WHERE display_name=? AND searchable`, title).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("quoted title without resolution must be searchable at 100%%, got %d", n)
	}
}

func TestNameFix_HeaderRecoversUnderscore1080pPending(t *testing.T) {
	const title = "[Fancutfags]_Puella_Magi_Madoka_Magica_-_03_[BD-Fancut][h264-1080p_FLAC][245C3BA1]"
	subj := `[01/2] - "` + title + `.mkv" yEnc (1/2)`
	key := ObfKey(nfGroup, "poster@obf", subj)
	var rows []HeaderRow
	for i := 1; i <= 2; i++ {
		rows = append(rows, HeaderRow{
			Group: nfGroup, MsgNum: i, MsgID: fmt.Sprintf("md%d@t", i), Subject: fmt.Sprintf(`[01/2] - "%s.mkv" yEnc (%d/2)`, title, i),
			From: "poster@obf", PostedAt: 1700000000, Bytes: 80_000_000,
			ReleaseName: key, PartN: i, PartM: 2, Filename: title + ".mkv",
			Obfuscated: true, FileN: 1, FileM: 2, YencSize: 80_000_000,
		})
	}
	rows = append(rows, HeaderRow{
		Group: nfGroup, MsgNum: 3, MsgID: "mdpar2@t", Subject: `[02/2] - "` + title + `.par2" yEnc (1/1)`,
		From: "poster@obf", PostedAt: 1700000000, Bytes: 40_000,
		ReleaseName: key, PartN: 1, PartM: 1, Filename: title + ".par2",
		Obfuscated: true, IsMeta: true, FileN: 2, FileM: 2,
	})
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	due, err := idx.DuePendingHeaderNames(ctx, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("DuePendingHeaderNames=%d, want underscore 1080p pending", len(due))
	}
	if _, err := idx.db.Exec(`UPDATE usenet_nntp_releases SET next_name_at=?`, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	due, err = idx.DuePendingHeaderNames(ctx, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("DuePendingHeaderNames after PreDB backoff=%d, want 1", len(due))
	}
	budget := &bodyBudget{}
	budget.setCapMB(0)
	nf := &NameFixer{Src: &crawlStub{bodies: map[string]usenet.BodySample{}}, Index: idx, Groups: []string{nfGroup}, Budget: budget}
	res, err := nf.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed != 1 {
		t.Fatalf("header must rename underscore 1080p without BODY: %+v", res)
	}
	var searchable bool
	var display string
	if err := sqlDB.QueryRow(`SELECT display_name, searchable FROM usenet_nntp_releases`).Scan(&display, &searchable); err != nil {
		t.Fatal(err)
	}
	if display != title || !searchable {
		t.Fatalf("display=%q searchable=%v", display, searchable)
	}
}

func TestNameFix_QuotedTitleFromStubFilenameBeatsBodyQueue(t *testing.T) {
	const title = "[UQW] Black Clover - Ep170 [BDRip 1080p HEVC-YUV420P10 FLAC]"
	quoted := []usenet.MessageOverview{
		{Number: 1, Subject: `[01/2] - "` + title + `.mkv" yEnc (1/2)`, From: "poster@obf", MessageID: "qs1@t", Bytes: 80_000_000, DateUnix: 1700000000},
		{Number: 2, Subject: `[01/2] - "` + title + `.mkv" yEnc (2/2)`, From: "poster@obf", MessageID: "qs2@t", Bytes: 80_000_000, DateUnix: 1700000000},
		{Number: 3, Subject: `[02/2] - "` + title + `.par2" yEnc (1/1)`, From: "poster@obf", MessageID: "qspar2", Bytes: 40_000, DateUnix: 1700000000},
	}
	hash := obfuscatedSet("qb", "uzmv2D5wxtEsPRJRjBN30g", 10, 5)
	f := newNFFixture(t, append(quoted, hash...), nil)
	f.crawlAndRecompute(t)
	var named, pending int
	if err := f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases WHERE display_name=? AND searchable`, title).Scan(&named); err != nil {
		t.Fatal(err)
	}
	if err := f.idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases WHERE state='pending_name'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if named != 1 || pending != 1 {
		t.Fatalf("quoted ident searchable=%d hash pending=%d, want 1/1 (no BODY wait)", named, pending)
	}
}

func TestNameFix_HashFileDescShelvesBody(t *testing.T) {
	f := newNFFixture(t, obfuscatedSet("hf", nfHash, 1, 45), nil)
	f.src.bodies["hfpar2"] = par2Body(nfHash + ".part01.rar")
	f.crawlAndRecompute(t)
	res, err := f.nf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed != 0 || res.Unresolved != 1 || res.BodyFetches != 1 || len(f.src.bodyCalls) != 1 {
		t.Fatalf("hash FileDesc must BODY once: %+v bodyCalls=%v", res, f.src.bodyCalls)
	}
	var attempts int
	if err := f.idx.db.QueryRow(`SELECT name_attempts FROM usenet_nntp_releases`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != maxNameAttempts {
		t.Fatalf("attempts=%d, want shelved at %d", attempts, maxNameAttempts)
	}
}
