package library

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

func TestSetSeriesTMDBID_RepairsZero(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// Upsert with a real id then force tmdb_id=0 to simulate Ancient Aliens row.
	ser, err := s.UpsertSeries(ctx, Series{TMDBID: 900001, Title: "Ancient Aliens", Year: 2009, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE library_series SET tmdb_id = 0 WHERE id = ?`, ser.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesTMDBID(ctx, ser.ID, 32608, 101501); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSeriesByTMDBID(ctx, 32608)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ser.ID || got.TVDBID != 101501 || got.Title != "Ancient Aliens" {
		t.Fatalf("got %+v", got)
	}
}

func TestSetSeriesTMDBID_Conflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, err := s.UpsertSeries(ctx, Series{TMDBID: 1, Title: "A", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.UpsertSeries(ctx, Series{TMDBID: 2, Title: "B", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	_ = a
	if err := s.SetSeriesTMDBID(ctx, b.ID, 1, 0); err == nil {
		t.Fatal("expected conflict error")
	}
}

func TestSetMovieTMDBID_RepairsNegative(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: -99, Title: "Synthetic", Year: 1947,
		FilePath: "/m.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMovieTMDBID(ctx, item.ID, 11449, "Jo Jo Dancer", 1986); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetByTMDBID(ctx, mode.Movies, 11449)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != item.ID || got.Title != "Jo Jo Dancer" || got.Year != 1986 {
		t.Fatalf("%+v", got)
	}
}

