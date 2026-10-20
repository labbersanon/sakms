package usenetsearch

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/settings"
)

func TestRewriteHeaderPage_MergesPerFileKeys(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.hdtv.x264"
	err = idx.Upsert(ctx, []HeaderRow{
		{
			Group: g, MsgNum: 1, MsgID: "a@x",
			Subject: `[01/73] R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR - "ep.part01.rar" yEnc (1/10)`,
			From:    "p@x", PostedAt: 1, Bytes: 100,
			ReleaseName: "01/73] R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR",
			PartN:       1, PartM: 10, Filename: "ep.part01.rar", FileN: 1, FileM: 73,
		},
		{
			Group: g, MsgNum: 2, MsgID: "b@x",
			Subject: `[48/73] R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR - "ep.part48.rar" yEnc (4/10)`,
			From:    "p@x", PostedAt: 1, Bytes: 100,
			ReleaseName: "48/73] R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR",
			PartN:       4, PartM: 10, Filename: "ep.part48.rar", FileN: 48, FileM: 73,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, changed, _, _, err := idx.rewriteHeaderPage(ctx, "", "", 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || changed != 2 {
		t.Fatalf("n=%d changed=%d", n, changed)
	}
	if err := idx.rebuildReleasesFromHeaders(ctx); err != nil {
		t.Fatal(err)
	}
	var keys, rows int
	if err := sqlDB.QueryRow(`SELECT COUNT(DISTINCT release_name), COUNT(*) FROM usenet_nntp_headers WHERE group_name=?`, g).Scan(&keys, &rows); err != nil {
		t.Fatal(err)
	}
	if keys != 1 || rows != 2 {
		t.Fatalf("keys=%d rows=%d, want 1 key over 2 rows", keys, rows)
	}
	var releases int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases WHERE group_name=?`, g).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if releases != 1 {
		t.Fatalf("releases=%d, want 1", releases)
	}
}

func TestRewriteHeaderPage_IdentQuotedEpisodeSplitsPack(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.multimedia.anime.highspeed"
	const pack = "WOR-HAZ Chivalry of a Failed Knight S01 BD HEVC 1080p FLAC"
	const ep9 = "WOR-HAZ Chivalry of a Failed Knight S01E09 BD HEVC 1080p FLAC"
	const ep10 = "WOR-HAZ Chivalry of a Failed Knight S01E10 BD HEVC 1080p FLAC"
	err = idx.Upsert(ctx, []HeaderRow{
		{
			Group: g, MsgNum: 1, MsgID: "e9@x",
			Subject: `[9/12] - "` + ep9 + `.mkv" yEnc (1/10)`,
			From:    "p@x", PostedAt: 1, Bytes: 100,
			ReleaseName: pack, PartN: 1, PartM: 10, Filename: ep9 + ".mkv", FileN: 9, FileM: 12,
		},
		{
			Group: g, MsgNum: 2, MsgID: "e10@x",
			Subject: `[10/12] - "` + ep10 + `.mkv" yEnc (1/10)`,
			From:    "p@x", PostedAt: 1, Bytes: 100,
			ReleaseName: pack, PartN: 1, PartM: 10, Filename: ep10 + ".mkv", FileN: 10, FileM: 12,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, changed, _, _, err := idx.rewriteHeaderPage(ctx, "", "", 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || changed != 2 {
		t.Fatalf("n=%d changed=%d", n, changed)
	}
	if err := idx.rebuildReleasesFromHeaders(ctx); err != nil {
		t.Fatal(err)
	}
	var keys, packLeft int
	if err := sqlDB.QueryRow(`SELECT COUNT(DISTINCT release_name), COUNT(*) FILTER (WHERE release_name=?) FROM usenet_nntp_headers WHERE group_name=?`, pack, g).Scan(&keys, &packLeft); err != nil {
		t.Fatal(err)
	}
	if keys != 2 || packLeft != 0 {
		t.Fatalf("keys=%d pack_left=%d, want 2 episode keys and no pack stem", keys, packLeft)
	}
	var named int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_releases WHERE group_name=? AND release_key IN (?,?)`, g, ep9, ep10).Scan(&named); err != nil {
		t.Fatal(err)
	}
	if named != 2 {
		t.Fatalf("episode releases=%d, want 2", named)
	}
}

func TestRebuildReleasesFromHeaders_KeepsRecoveredName(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	const key = "obf:cccccccccccccccc"
	const title = "Recovered.Movie.2021.1080p.WEB-DL.x264-GRP"
	if err := idx.Upsert(ctx, fileRows(g, key, "h.rar", "h", 0, 10, 10, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE usenet_nntp_releases SET display_name=?, name_source='ybegin', state='named' WHERE release_key=?`, title, key); err != nil {
		t.Fatal(err)
	}
	if err := idx.rebuildReleasesFromHeaders(ctx); err != nil {
		t.Fatal(err)
	}
	var display, source string
	if err := sqlDB.QueryRow(`SELECT display_name, name_source FROM usenet_nntp_releases WHERE release_key=?`, key).Scan(&display, &source); err != nil {
		t.Fatal(err)
	}
	if display != title || source != "ybegin" {
		t.Fatalf("display=%q source=%s, want recovered ybegin kept", display, source)
	}
}

func TestRunCollectionRewrite_IdempotentFlag(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	s := NewService(nil, sqlDB)
	s.idx = idx
	s.cfg.NameFixEnabled = true
	ctx := context.Background()
	s.runCollectionRewrite(ctx)
	st := settings.New(sqlDB)
	done, err := st.GetBool(ctx, KeyCollectionRewriteDone, false)
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	s.runCollectionRewrite(ctx) // no-op
}
