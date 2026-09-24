package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
)

func TestPutSeriesEpisodeProgress_MarksWatchedAtNinetyPercent(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Show", Year: 2020, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	ep, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"})
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"positionSeconds": 91, "durationSeconds": 100})
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/api/modes/series/tracked/%d/episodes/%d/progress", srv.URL, series.ID, ep.ID), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
	listed, err := libStore.ListEpisodeProgressForSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !listed[ep.ID].Watched || listed[ep.ID].PositionSeconds != 91 {
		t.Fatalf("expected watched at 91/100, got %+v", listed[ep.ID])
	}
}

func TestPutSeriesEpisodeProgress_RejectsSiblingSeries(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	a, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "A", Year: 2020, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 2, Title: "B", Year: 2021, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	epB, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: b.ID, SeasonNumber: 1, EpisodeNumber: 1})
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"positionSeconds": 10, "durationSeconds": 100})
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/api/modes/series/tracked/%d/episodes/%d/progress", srv.URL, a.ID, epB.ID), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	listed, err := libStore.ListEpisodeProgressForSeries(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("sibling write leaked: %+v", listed)
	}
}
