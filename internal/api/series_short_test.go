package api

import "testing"

func TestPreferMovieForSeriesYear(t *testing.T) {
	// One Good Turn: library 1931, movie 1931, TV show 2012.
	if !preferMovieForSeriesYear(true, 2012, true, 1931, 1931) {
		t.Fatal("expected the movie when only its year matches")
	}
	// A real series whose premiere matches stays on TV.
	if preferMovieForSeriesYear(true, 2012, true, 1931, 2012) {
		t.Fatal("expected the TV show when its year matches")
	}
	// No TV record: the movie is the only catalog entry.
	if !preferMovieForSeriesYear(false, 0, true, 1936, 1936) {
		t.Fatal("expected the movie when TV is missing")
	}
	if preferMovieForSeriesYear(true, 2012, false, 0, 1931) {
		t.Fatal("movie must exist")
	}
}

func TestPosterURLIsImage(t *testing.T) {
	if posterURLIsImage("https://www.themoviedb.org/movie/48903-one-good-turn/images/posters") {
		t.Fatal("gallery page")
	}
	if posterURLIsImage("https://commons.wikimedia.org/wiki/File:L%26H_Lucky_Dog_1919.jpg") {
		t.Fatal("wiki file page")
	}
	if !posterURLIsImage("https://image.tmdb.org/t/p/w342/HHDx94rrlVD3C3PBZmrOPJYJQZ.jpg") {
		t.Fatal("tmdb image")
	}
	if !posterURLIsImage("/api/posters/local?itemId=190") {
		t.Fatal("local poster")
	}
}
