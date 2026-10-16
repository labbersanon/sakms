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
