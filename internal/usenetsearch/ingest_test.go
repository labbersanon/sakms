package usenetsearch

import (
	"context"
	"testing"
	"unicode/utf8"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/usenet"
)

func TestIngestRange_DropsSkippedKeys(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	const skipped = "Stale.Partial.2020"
	const fresh = "Fresh.Movie.2020.1080p.WEB-DL.x264-GROUP"
	if _, err := sqlDB.ExecContext(ctx, `
INSERT INTO usenet_nntp_skips (group_name, release_key, reason, first_msg, last_msg, skipped_at)
VALUES (?, ?, 'incomplete', 101, 150, 1)`, g, skipped); err != nil {
		t.Fatal(err)
	}
	var rows []usenet.MessageOverview
	rows = append(rows, releaseRows(101, skipped, "old", 5)...)
	rows = append(rows, releaseRows(201, fresh, "new", 5)...)
	src := &crawlStub{gr: usenet.GroupRange{Low: 101, High: 205}, rows: rows}
	c := &Crawler{Src: src, Index: idx, Groups: []string{g}}
	if _, err := c.ingestRange(ctx, ingestOpts{Dir: dirForward}, g, 101, 205); err != nil {
		t.Fatal(err)
	}
	if c.curSkipStub != 5 {
		t.Fatalf("curSkipStub=%d, want 5", c.curSkipStub)
	}
	var oldN, newN int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_headers WHERE release_name=?`, skipped).Scan(&oldN); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_headers WHERE release_name=?`, fresh).Scan(&newN); err != nil {
		t.Fatal(err)
	}
	if oldN != 0 || newN != 5 {
		t.Fatalf("headers skipped=%d fresh=%d, want 0/5", oldN, newN)
	}
}

func TestBuildHeaders_SanitizesLatin1(t *testing.T) {
	subj := "Foo.Bar.2024" + string([]byte{0xe4, 0x6e, 0x64}) + " yEnc (1/2)"
	from := "poster" + string([]byte{0xfc}) + "@x"
	hs := (&Crawler{}).buildHeaders("g", []usenet.MessageOverview{{
		Number: 1, Subject: subj, From: from, MessageID: "<a@x>", Bytes: 100,
	}})
	if len(hs) != 1 {
		t.Fatalf("headers=%d", len(hs))
	}
	if !utf8.ValidString(hs[0].Subject) || !utf8.ValidString(hs[0].From) || !utf8.ValidString(hs[0].MsgID) {
		t.Fatalf("invalid utf8 subject=%q from=%q msgid=%q", hs[0].Subject, hs[0].From, hs[0].MsgID)
	}
}

func TestUpsert_Latin1Subject(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	hs := (&Crawler{}).buildHeaders("g", []usenet.MessageOverview{{
		Number:    1,
		Subject:   "Foo.Bar.2024" + string([]byte{0xe4}) + " yEnc (1/2)",
		From:      "p" + string([]byte{0xfc}) + "@x",
		MessageID: "<a@x>",
		Bytes:     1000,
	}})
	if err := idx.Upsert(context.Background(), hs); err != nil {
		t.Fatal(err)
	}
}
