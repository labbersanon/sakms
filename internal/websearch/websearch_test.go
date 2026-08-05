package websearch

import (
	"context"
	"errors"
	"testing"
)

type stubClient struct {
	res []Result
	err error
}

func (s stubClient) Search(context.Context, string, int) ([]Result, error) { return s.res, s.err }
func (s stubClient) Ping(context.Context) error                           { return s.err }

func TestFailover_PrimaryEmptyUsesFallback(t *testing.T) {
	f := &Failover{Clients: []Client{
		stubClient{err: errors.New("402")},
		stubClient{res: []Result{{Title: "ok", URL: "http://x"}}},
	}}
	got, err := f.Search(context.Background(), "q", 5)
	if err != nil || len(got) != 1 || got[0].Title != "ok" {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestFailover_AllFailSoftEmpty(t *testing.T) {
	f := &Failover{Clients: []Client{
		stubClient{err: errors.New("a")},
		stubClient{res: nil},
	}}
	got, err := f.Search(context.Background(), "q", 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v err=%v", got, err)
	}
}
