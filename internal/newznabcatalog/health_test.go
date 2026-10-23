package newznabcatalog

import "testing"

func TestIndexerHealthFromCursors_EmptyIsWaiting(t *testing.T) {
	got := IndexerHealthFromCursors(nil)
	if len(got) != 2 || got[0].Indexer != "newznab" || got[1].Indexer != "newznab2" {
		t.Fatalf("slots=%+v", got)
	}
	if got[0].Status != HealthUnset || got[0].Detail != "waiting" {
		t.Fatalf("newznab=%+v", got[0])
	}
}

func TestIndexerHealthFromCursors_WorstStatusWins(t *testing.T) {
	got := IndexerHealthFromCursors([]Cursor{
		{Indexer: "newznab", Kind: KindMovies, Complete: false, UpdatedAt: 10},
		{Indexer: "newznab", Kind: KindAdult, LastError: "rate limited", UpdatedAt: 20},
		{Indexer: "newznab2", Kind: KindMovies, Complete: true, LastError: "request limit reached", UpdatedAt: 30},
	})
	if got[0].Status != HealthRateLimited || got[0].UpdatedAt != 20 {
		t.Fatalf("newznab=%+v", got[0])
	}
	if got[1].Status != HealthQuota || got[1].Detail != "request limit reached" {
		t.Fatalf("newznab2=%+v", got[1])
	}
}

func TestIndexerHealthFromCursors_CrawlingThenFinished(t *testing.T) {
	got := IndexerHealthFromCursors([]Cursor{
		{Indexer: "newznab", Kind: KindMovies, Complete: true, UpdatedAt: 1},
		{Indexer: "newznab", Kind: KindSeries, Complete: false, UpdatedAt: 2},
		{Indexer: "newznab2", Kind: KindMovies, Complete: true, UpdatedAt: 3},
		{Indexer: "newznab2", Kind: KindSeries, Complete: true, UpdatedAt: 4},
		{Indexer: "newznab2", Kind: KindAdult, Complete: true, UpdatedAt: 5},
	})
	if got[0].Status != HealthCrawling || got[0].Detail != "series" {
		t.Fatalf("crawling=%+v", got[0])
	}
	if got[1].Status != HealthOK || got[1].Detail != "finished" {
		t.Fatalf("ok=%+v", got[1])
	}
}

func TestIndexerHealthFromCursors_StripsAPIKey(t *testing.T) {
	got := IndexerHealthFromCursors([]Cursor{
		{Indexer: "newznab", Kind: KindMovies, LastError: "get https://api.example/api?apikey=secret", UpdatedAt: 1},
	})
	if got[0].Status != HealthError || got[0].Detail != "indexer error" {
		t.Fatalf("got=%+v", got[0])
	}
}
