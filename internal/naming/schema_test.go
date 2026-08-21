package naming

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestTMDBIDFromPath(t *testing.T) {
	cases := []struct {
		path string
		want int
	}{
		{"/media/Movies/Indiana Jones and the Last Crusade (1989) [tmdbid-89]/Indiana Jones and the Last Crusade (1989) [tmdbid-89] - 1080p H264.mp4", 89},
		{"Some Movie (2020) [tmdbid-42].mkv", 42},
		{"/media/Series/Some Show (2019) [tmdbid-555]/Season 01/Some Show S01E01.mkv", 555},
		{"/media/Movies/Some Movie (2020)/Some Movie (2020).mkv", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := TMDBIDFromPath(tc.path); got != tc.want {
			t.Errorf("TMDBIDFromPath(%q)=%d, want %d", tc.path, got, tc.want)
		}
	}
}

func TestMatchesMovieSchema(t *testing.T) {
	t.Run("conformant Jellyfin folder+file matches", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "Some Movie (2020) [tmdbid-42]")
		writeFile(t, folder, "Some Movie (2020) [tmdbid-42].mkv")
		if !MatchesMovieSchema(folder, Jellyfin) {
			t.Error("expected a conformant Jellyfin folder to match")
		}
	})

	t.Run("conformant Legacy folder+file matches", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "Some Movie (2020)")
		writeFile(t, folder, "Some Movie (2020).mkv")
		if !MatchesMovieSchema(folder, Legacy) {
			t.Error("expected a conformant Legacy folder to match")
		}
	})

	t.Run("a bare loose file never matches, even with a conformant-looking name", func(t *testing.T) {
		root := t.TempDir()
		path := writeFile(t, root, "Some Movie (2020) [tmdbid-42].mkv")
		if MatchesMovieSchema(path, Jellyfin) {
			t.Error("expected a bare file (no wrapping folder) to never match")
		}
	})

	t.Run("mismatched file name inside a conformant-looking folder does not match", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "Some Movie (2020) [tmdbid-42]")
		writeFile(t, folder, "Some.Movie.2020.1080p.BluRay-GROUP.mkv")
		if MatchesMovieSchema(folder, Jellyfin) {
			t.Error("expected a folder/file name mismatch to not match")
		}
	})

	t.Run("missing tmdbid tag under Jellyfin does not match", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "Some Movie (2020)")
		writeFile(t, folder, "Some Movie (2020).mkv")
		if MatchesMovieSchema(folder, Jellyfin) {
			t.Error("expected a Legacy-shaped folder to not match under the Jellyfin preset")
		}
	})

	t.Run("Jellyfin-shaped folder does not match under Legacy", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "Some Movie (2020) [tmdbid-42]")
		writeFile(t, folder, "Some Movie (2020) [tmdbid-42].mkv")
		if MatchesMovieSchema(folder, Legacy) {
			t.Error("expected a Jellyfin-shaped folder to not match under the Legacy preset")
		}
	})

	t.Run("nonexistent path does not match", func(t *testing.T) {
		if MatchesMovieSchema(filepath.Join(t.TempDir(), "nope"), Jellyfin) {
			t.Error("expected a nonexistent path to not match")
		}
	})
}

func TestMatchesSeriesSchema(t *testing.T) {
	t.Run("conformant Jellyfin episode matches", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show (2019) [tmdbid-555]", "Season 01"), "Some Show S01E01.mkv")
		if !MatchesSeriesSchema(videoPath, Jellyfin) {
			t.Error("expected a conformant Jellyfin episode to match")
		}
	})

	t.Run("conformant Legacy episode matches", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show", "Season 01"), "Some Show - S01E01.mkv")
		if !MatchesSeriesSchema(videoPath, Legacy) {
			t.Error("expected a conformant Legacy episode to match")
		}
	})

	t.Run("dash-separated file does not match under Jellyfin", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show (2019) [tmdbid-555]", "Season 01"), "Some Show - S01E01.mkv")
		if MatchesSeriesSchema(videoPath, Jellyfin) {
			t.Error("expected a dash-separated (Legacy-shaped) file to not match under Jellyfin")
		}
	})

	t.Run("space-separated file does not match under Legacy", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show", "Season 01"), "Some Show S01E01.mkv")
		if MatchesSeriesSchema(videoPath, Legacy) {
			t.Error("expected a space-separated (Jellyfin-shaped) file to not match under Legacy")
		}
	})

	t.Run("missing tmdbid tag on series folder does not match under Jellyfin", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show", "Season 01"), "Some Show S01E01.mkv")
		if MatchesSeriesSchema(videoPath, Jellyfin) {
			t.Error("expected a bare-title series folder to not match under Jellyfin")
		}
	})

	t.Run("wrong season folder shape does not match", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show", "S01"), "Some Show - S01E01.mkv")
		if MatchesSeriesSchema(videoPath, Legacy) {
			t.Error("expected an abbreviated season folder name to not match")
		}
	})

	t.Run("a scene-release-named episode does not match either preset", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, root, "Some.Show.S01E01.1080p.WEB-DL.x264-GROUP.mkv")
		if MatchesSeriesSchema(videoPath, Jellyfin) || MatchesSeriesSchema(videoPath, Legacy) {
			t.Error("expected a loose scene-release file to not match either preset")
		}
	})

	// A logical-episode-split file's range-shaped name (EpisodeRangeFileName's
	// own output) must be recognized as conformant — otherwise it would be
	// endlessly re-proposed on every Scan despite already being correctly
	// organized (the real bug this coverage guards against).
	t.Run("a Jellyfin range-shaped episode matches", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show (2019) [tmdbid-555]", "Season 01"), "Some Show S01E01-E02.mkv")
		if !MatchesSeriesSchema(videoPath, Jellyfin) {
			t.Error("expected a conformant Jellyfin range-shaped episode to match")
		}
	})

	t.Run("a Legacy range-shaped episode matches", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show", "Season 01"), "Some Show - S01E01-E02.mkv")
		if !MatchesSeriesSchema(videoPath, Legacy) {
			t.Error("expected a conformant Legacy range-shaped episode to match")
		}
	})
}

func TestMatchesAdultSchema(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"AdultFileName output matches", "/media/Adult/Brazzers - Scene Title (2021-03-04) [phash-abc123].mp4", true},
		{"tag with no studio/date still matches", "/media/Adult/Scene Title [phash-abc123].mkv", true},
		{"a raw scene-release name does not match", "/media/Adult/some.scene.1080p.XXX-GROUP.mp4", false},
		{"a movie tmdbid tag is not a phash tag", "/media/Adult/Some Movie (2020) [tmdbid-42].mkv", false},
		{"an empty phash tag is malformed and does not match", "/media/Adult/Scene Title [phash-].mp4", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatchesAdultSchema(c.path); got != c.want {
				t.Errorf("MatchesAdultSchema(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}
