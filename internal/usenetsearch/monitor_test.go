package usenetsearch

import (
	"context"
	"errors"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/usenet"
)

type stubHeaderSource struct {
	stat map[string]bool // msgid → found
	busy bool
}

func (s *stubHeaderSource) Capabilities(context.Context) ([]string, error) {
	return nil, nil
}
func (s *stubHeaderSource) GroupRange(context.Context, string) (usenet.GroupRange, error) {
	return usenet.GroupRange{}, nil
}
func (s *stubHeaderSource) Overview(context.Context, string, int, int) ([]usenet.MessageOverview, error) {
	return nil, nil
}
func (s *stubHeaderSource) ListActive(context.Context, string) ([]string, error) {
	return nil, nil
}
func (s *stubHeaderSource) BodyHead(context.Context, string, int) (usenet.BodySample, error) {
	return usenet.BodySample{}, usenet.ErrBusy
}
func (s *stubHeaderSource) StatArticle(_ context.Context, msgID string) (bool, error) {
	if s.busy {
		return false, usenet.ErrBusy
	}
	found, ok := s.stat[msgID]
	if !ok {
		return false, errors.New("unexpected msgid")
	}
	return found, nil
}

// Claude 2026-10-15: stub for the all-pool StatArticleAll (LivenessOnce now
// uses it instead of StatArticle). present=true -> StatPresent, false ->
// StatMissing; busy mirrors the single-pool stub's ErrBusy/StatInconclusive.
func (s *stubHeaderSource) StatArticleAll(_ context.Context, msgID string) (usenet.StatVerdict, error) {
	if s.busy {
		return usenet.StatInconclusive, usenet.ErrBusy
	}
	found, ok := s.stat[msgID]
	if !ok {
		return usenet.StatInconclusive, errors.New("unexpected msgid")
	}
	if found {
		return usenet.StatPresent, nil
	}
	return usenet.StatMissing, nil
}

func TestLivenessOnce_DeletesOnlyMissing(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	rows := []HeaderRow{
		{Group: "g", MsgNum: 1, MsgID: "alive@x", Subject: "a", ReleaseName: "A", PartN: 1, PartM: 1, Filename: "a.mkv", Bytes: 1, PostedAt: 1},
		{Group: "g", MsgNum: 2, MsgID: "gone@x", Subject: "b", ReleaseName: "B", PartN: 1, PartM: 1, Filename: "b.mkv", Bytes: 1, PostedAt: 2},
	}
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	src := &stubHeaderSource{stat: map[string]bool{"alive@x": true, "gone@x": false}}
	n, _, err := LivenessOnce(ctx, src, idx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted=%d want 1", n)
	}
	count, err := idx.CountHeaders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d want 1", count)
	}
}

func TestLivenessOnce_BusyYields(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	if err := idx.Upsert(ctx, []HeaderRow{{
		Group: "g", MsgNum: 1, MsgID: "a@x", Subject: "a", ReleaseName: "A",
		PartN: 1, PartM: 1, Filename: "a.mkv", Bytes: 1, PostedAt: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	src := &stubHeaderSource{busy: true, stat: map[string]bool{}}
	n, _, err := LivenessOnce(ctx, src, idx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("deleted=%d want 0 on busy", n)
	}
	count, _ := idx.CountHeaders(ctx)
	if count != 1 {
		t.Fatalf("row should remain, count=%d", count)
	}
}
