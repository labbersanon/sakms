package identify

import "testing"

func TestParseCatalogSceneURL(t *testing.T) {
	tests := []struct {
		url     string
		box     string
		id      string
		isMovie bool
		wantOK  bool
	}{
		{
			url:    "https://stashdb.org/scenes/a29768db-b3cd-4a71-a75e-4294373207bb",
			box:    "stashdb",
			id:     "a29768db-b3cd-4a71-a75e-4294373207bb",
			wantOK: true,
		},
		{
			url:    "stashdb.org/scenes/a29768db-b3cd-4a71-a75e-4294373207bb",
			box:    "stashdb",
			id:     "a29768db-b3cd-4a71-a75e-4294373207bb",
			wantOK: true,
		},
		{
			url:    "https://fansdb.cc/scenes/b29768db-b3cd-4a71-a75e-4294373207bb",
			box:    "fansdb",
			id:     "b29768db-b3cd-4a71-a75e-4294373207bb",
			wantOK: true,
		},
		{
			url:    "https://theporndb.net/scenes/evilangel-test-scene",
			box:    "tpdb",
			id:     "evilangel-test-scene",
			wantOK: true,
		},
		{
			url:     "https://theporndb.net/movies/some-movie-slug",
			box:     "tpdb",
			id:      "some-movie-slug",
			isMovie: true,
			wantOK:  true,
		},
		{url: "https://example.com/scenes/foo", wantOK: false},
		{url: "not a url", wantOK: false},
	}
	for _, tc := range tests {
		box, id, isMovie, ok := ParseCatalogSceneURL(tc.url)
		if ok != tc.wantOK {
			t.Errorf("ParseCatalogSceneURL(%q) ok = %v, want %v", tc.url, ok, tc.wantOK)
			continue
		}
		if !tc.wantOK {
			continue
		}
		if box != tc.box || id != tc.id || isMovie != tc.isMovie {
			t.Errorf("ParseCatalogSceneURL(%q) = %q %q movie=%v, want %q %q movie=%v",
				tc.url, box, id, isMovie, tc.box, tc.id, tc.isMovie)
		}
	}
}
