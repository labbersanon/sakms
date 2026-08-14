package library

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"sync/atomic"
	"testing"
)

func TestClassifyPosterAspect(t *testing.T) {
	// ±5% of 2:3 (width/height = 2/3 ≈ 0.6667) is [0.6333, 0.7000].
	cases := []struct {
		name string
		w, h int
		want string
	}{
		{"exact 2:3", 200, 300, PosterAspectVertical},
		{"16:9", 1920, 1080, PosterAspectHorizontal},
		{"missing width", 0, 300, PosterAspectHorizontal},
		{"missing height", 200, 0, PosterAspectHorizontal},
		{"both zero", 0, 0, PosterAspectHorizontal},
		{"square", 1000, 1000, PosterAspectHorizontal},
		{"+5% boundary 210x300", 210, 300, PosterAspectVertical},
		{"just outside +5% 211x300", 211, 300, PosterAspectHorizontal},
		{"-5% boundary 190x300", 190, 300, PosterAspectVertical},
		{"just outside -5% 189x300", 189, 300, PosterAspectHorizontal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyPosterAspect(tc.w, tc.h)
			if got != tc.want {
				t.Fatalf("ClassifyPosterAspect(%d,%d) = %q, want %q", tc.w, tc.h, got, tc.want)
			}
		})
	}
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

func TestProbePosterAspect(t *testing.T) {
	t.Run("empty URL is horizontal", func(t *testing.T) {
		if got := ProbePosterAspect(context.Background(), ""); got != PosterAspectHorizontal {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("http URL is horizontal without fetch", func(t *testing.T) {
		var fetches atomic.Int32
		SetPosterFetchOverride(func(context.Context, string) ([]byte, error) {
			fetches.Add(1)
			return nil, nil
		})
		t.Cleanup(func() { SetPosterFetchOverride(nil) })
		if got := ProbePosterAspect(context.Background(), "http://example.com/p.jpg"); got != PosterAspectHorizontal {
			t.Fatalf("got %q", got)
		}
		if fetches.Load() != 0 {
			t.Fatalf("http URL must not fetch, got %d fetches", fetches.Load())
		}
	})
	t.Run("loopback https is rejected without fetch", func(t *testing.T) {
		var fetches atomic.Int32
		SetPosterFetchOverride(func(context.Context, string) ([]byte, error) {
			fetches.Add(1)
			return encodePNG(t, 200, 300), nil
		})
		t.Cleanup(func() { SetPosterFetchOverride(nil) })
		got := ProbePosterAspect(context.Background(), "https://127.0.0.1/portrait.png")
		if got != PosterAspectHorizontal {
			t.Fatalf("got %q, want horizontal", got)
		}
		if fetches.Load() != 0 {
			t.Fatalf("127.0.0.1 must not be fetched, got %d fetches", fetches.Load())
		}
	})

	portrait := encodePNG(t, 200, 300)
	landscape := encodePNG(t, 1920, 1080)
	SetPosterFetchOverride(func(_ context.Context, rawURL string) ([]byte, error) {
		switch rawURL {
		case "https://1.1.1.1/portrait.png":
			return portrait, nil
		case "https://1.1.1.1/landscape.png":
			return landscape, nil
		default:
			return nil, errNoPosterProxy
		}
	})
	t.Cleanup(func() { SetPosterFetchOverride(nil) })

	t.Run("2:3 png is vertical", func(t *testing.T) {
		got := ProbePosterAspect(context.Background(), "https://1.1.1.1/portrait.png")
		if got != PosterAspectVertical {
			t.Fatalf("got %q, want vertical", got)
		}
	})
	t.Run("16:9 png is horizontal", func(t *testing.T) {
		got := ProbePosterAspect(context.Background(), "https://1.1.1.1/landscape.png")
		if got != PosterAspectHorizontal {
			t.Fatalf("got %q, want horizontal", got)
		}
	})
	t.Run("fetch miss is horizontal", func(t *testing.T) {
		got := ProbePosterAspect(context.Background(), "https://1.1.1.1/missing.png")
		if got != PosterAspectHorizontal {
			t.Fatalf("got %q", got)
		}
	})
}

func TestParsePosterAspectFilter(t *testing.T) {
	got, err := ParsePosterAspectFilter("")
	if err != nil || got != "" {
		t.Fatalf("empty = %q err=%v", got, err)
	}
	got, err = ParsePosterAspectFilter(" vertical ")
	if err != nil || got != PosterAspectVertical {
		t.Fatalf("vertical = %q err=%v", got, err)
	}
	if _, err = ParsePosterAspectFilter("movie"); !errors.Is(err, ErrInvalidPosterAspect) {
		t.Fatalf("movie err=%v, want ErrInvalidPosterAspect", err)
	}
}

func TestMatchesPosterAspect(t *testing.T) {
	if !MatchesPosterAspect(PosterAspectVertical, "") {
		t.Fatal("empty want matches any class")
	}
	if !MatchesPosterAspect("", PosterAspectHorizontal) {
		t.Fatal("empty stored class is horizontal")
	}
	if MatchesPosterAspect(PosterAspectHorizontal, PosterAspectVertical) {
		t.Fatal("horizontal must not match vertical want")
	}
}
