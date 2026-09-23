package api

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

func TestCollectWatchPaths_IncludesKidsRoots(t *testing.T) {
	_, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/media/Movies"); err != nil {
		t.Fatal(err)
	}
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, "/media/Series"); err != nil {
		t.Fatal(err)
	}
	kidsMovies, _ := mode.Movies.KidsRootPathKey()
	kidsSeries, _ := mode.Series.KidsRootPathKey()
	if err := settingsStore.Set(ctx, kidsMovies, "/media/Movies (Kids)"); err != nil {
		t.Fatal(err)
	}
	if err := settingsStore.Set(ctx, kidsSeries, "/media/Series (Kids)"); err != nil {
		t.Fatal(err)
	}
	got := collectWatchPaths(ctx, settingsStore)
	byKey := map[string]string{}
	for _, wp := range got {
		byKey[wp.Key] = wp.Path
	}
	if byKey["movies"] != "/media/Movies" || byKey["movies_kids"] != "/media/Movies (Kids)" {
		t.Fatalf("movies roots = %v", byKey)
	}
	if byKey["series"] != "/media/Series" || byKey["series_kids"] != "/media/Series (Kids)" {
		t.Fatalf("series roots = %v", byKey)
	}
}

func TestCollectWatchPaths_SkipsDuplicateKidsPath(t *testing.T) {
	_, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/media/Movies"); err != nil {
		t.Fatal(err)
	}
	kidsMovies, _ := mode.Movies.KidsRootPathKey()
	if err := settingsStore.Set(ctx, kidsMovies, "/media/Movies"); err != nil {
		t.Fatal(err)
	}
	got := collectWatchPaths(ctx, settingsStore)
	if len(got) != 1 || got[0].Key != "movies" {
		t.Fatalf("got %#v", got)
	}
}

func TestCollectWatchPaths_EmptySettings(t *testing.T) {
	_, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	if got := collectWatchPaths(context.Background(), settingsStore); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
