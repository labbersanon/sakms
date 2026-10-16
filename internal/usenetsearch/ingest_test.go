package usenetsearch

import (
	"context"
	"testing"
	"unicode/utf8"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/usenet"
)

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
