package playback

import "testing"

func TestDirectPlayContainer(t *testing.T) {
	for _, p := range []string{"a.mp4", "a.m4v", "a.webm", "a.MOV"} {
		if !DirectPlayContainer(p) {
			t.Errorf("%s: want Direct Play", p)
		}
	}
	for _, p := range []string{"a.mkv", "a.avi", "a.wmv", "a.ts", "a.m2ts", "a.iso"} {
		if DirectPlayContainer(p) {
			t.Errorf("%s: must not Direct Play", p)
		}
	}
}

func TestPlayableInApp(t *testing.T) {
	if !PlayableInApp("/lib/show.mkv") || !PlayableInApp("/lib/x.avi") {
		t.Fatal("library videos should be playable")
	}
	if PlayableInApp("/lib/disc.iso") || PlayableInApp("/lib/notes.txt") {
		t.Fatal("iso/non-video must not be playable")
	}
}

func TestVideoURL(t *testing.T) {
	mp4 := "/api/modes/movies/tracked/10/video?fileId=3"
	if got := VideoURL(mp4, "/m/a.mp4"); got != mp4 {
		t.Fatalf("mp4 url = %q", got)
	}
	want := "/api/modes/movies/tracked/10/video/hls/index.m3u8?fileId=3"
	if got := VideoURL(mp4, "/m/a.mkv"); got != want {
		t.Fatalf("mkv url = %q, want %q", got, want)
	}
	browse := "/api/organize/browse/video?path=%2Fmedia%2Fa.avi"
	wantB := "/api/organize/browse/video/hls/index.m3u8?path=%2Fmedia%2Fa.avi"
	if got := VideoURL(browse, "/media/a.avi"); got != wantB {
		t.Fatalf("browse = %q, want %q", got, wantB)
	}
}

func TestDecide(t *testing.T) {
	tests := []struct {
		v, a string
		want Decision
	}{
		{"h264", "aac", Remux},
		{"avc1", "mp4a", Remux},
		{"h264", "", Remux},
		{"av1", "opus", Remux},
		{"h264", "ac3", DirectStream},
		{"h264", "dts", DirectStream},
		{"h264", "truehd", DirectStream},
		{"hevc", "aac", Transcode},
		{"hevc", "ac3", Transcode},
		{"mpeg4", "mp3", Transcode},
		{"mpeg2video", "ac3", Transcode},
		{"vc1", "wmav2", Transcode},
	}
	for _, tc := range tests {
		if got := Decide(tc.v, tc.a); got != tc.want {
			t.Errorf("Decide(%q,%q)=%s want %s", tc.v, tc.a, got, tc.want)
		}
	}
}
