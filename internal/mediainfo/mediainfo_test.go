package mediainfo

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fakeProber(out []byte, err error) *Prober {
	return &Prober{
		run: func(ctx context.Context, path string) ([]byte, error) {
			return out, err
		},
		timeout: 5 * time.Second,
	}
}

func TestProbe_ParsesFields(t *testing.T) {
	raw := []byte(`{"streams":[{"codec_name":"av1","width":1920,"height":1080,"bit_rate":"4416482"}]}`)
	p := fakeProber(raw, nil)

	got, err := p.Probe(context.Background(), "/fake/path.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CodecName != "av1" || got.Width != 1920 || got.Height != 1080 || got.BitRate != 4416482 {
		t.Errorf("unexpected probe result: %+v", got)
	}
}

func TestProbe_ParsesDuration(t *testing.T) {
	raw := []byte(`{"streams":[{"codec_name":"h264","width":1920,"height":1080,"bit_rate":"4416482"}],"format":{"duration":"1800.48"}}`)
	p := fakeProber(raw, nil)

	got, err := p.Probe(context.Background(), "/fake/path.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Duration != 1800.48 {
		t.Errorf("expected duration 1800.48 from the format section, got %v", got.Duration)
	}
}

func TestProbe_MissingFormatDurationDefaultsToZero(t *testing.T) {
	// The existing streams-only fixture shape (no format section at all) must
	// still parse cleanly, yielding Duration == 0 rather than an error.
	raw := []byte(`{"streams":[{"codec_name":"av1","width":1920,"height":1080,"bit_rate":"4416482"}]}`)
	p := fakeProber(raw, nil)

	got, err := p.Probe(context.Background(), "/fake/path.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Duration != 0 {
		t.Errorf("expected 0 duration when no format section is present, got %v", got.Duration)
	}
}

func TestProbe_MissingBitRateDefaultsToZero(t *testing.T) {
	raw := []byte(`{"streams":[{"codec_name":"h264","width":1280,"height":720,"bit_rate":""}]}`)
	p := fakeProber(raw, nil)

	got, err := p.Probe(context.Background(), "/fake/path.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.BitRate != 0 {
		t.Errorf("expected 0 bitrate fallback, got %d", got.BitRate)
	}
}

func TestProbe_ParsesFormatTags(t *testing.T) {
	raw := []byte(`{
		"streams":[{"codec_name":"h264","width":1920,"height":1080,"bit_rate":"1000"}],
		"format":{"duration":"7200.0","tags":{"title":"The Matrix","date":"1999-03-31","IMDB":"tt0133093","tmdb":"603"}}
	}`)
	p := fakeProber(raw, nil)
	got, err := p.Probe(context.Background(), "/fake.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tags.Title != "The Matrix" || got.Tags.Year != 1999 || got.Tags.IMDBID != "tt0133093" || got.Tags.TMDBID != 603 {
		t.Fatalf("tags = %+v", got.Tags)
	}
}

func TestParseFormatTags_TitleYearOnly(t *testing.T) {
	tags := ParseFormatTags(map[string]string{"TITLE": "Copacabana (1947)", "YEAR": "1947"})
	if tags.Title != "Copacabana (1947)" || tags.Year != 1947 {
		t.Fatalf("%+v", tags)
	}
}

func TestProbe_NoVideoStreamErrors(t *testing.T) {
	raw := []byte(`{"streams":[]}`)
	p := fakeProber(raw, nil)

	_, err := p.Probe(context.Background(), "/fake/path.mp4")
	if err == nil {
		t.Error("expected an error for a file with no video stream")
	}
}

func TestProbe_RunnerErrorPropagates(t *testing.T) {
	p := fakeProber(nil, errors.New("ffprobe: no such file"))

	_, err := p.Probe(context.Background(), "/fake/path.mp4")
	if err == nil {
		t.Error("expected the runner's error to propagate")
	}
}

func TestProbe_MalformedJSONErrors(t *testing.T) {
	p := fakeProber([]byte("not json"), nil)

	_, err := p.Probe(context.Background(), "/fake/path.mp4")
	if err == nil {
		t.Error("expected an error for malformed JSON output")
	}
}
