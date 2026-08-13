package phash

import "testing"

func TestParseHWAccels_CudaPreferredOverVaapi(t *testing.T) {
	got := parseHWAccels("Hardware acceleration methods:\ncuda\nvaapi\n")
	if got != "cuda" {
		t.Errorf("got %q, want %q", got, "cuda")
	}
}

func TestParseHWAccels_VaapiAloneReturnsVaapi(t *testing.T) {
	got := parseHWAccels("Hardware acceleration methods:\nvaapi\n")
	if got != "vaapi" {
		t.Errorf("got %q, want %q", got, "vaapi")
	}
}

func TestParseHWAccels_UnknownAccelReturnsEmpty(t *testing.T) {
	got := parseHWAccels("Hardware acceleration methods:\ndxva2\nvideotoolbox\n")
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestParseHWAccels_EmptyOutputReturnsEmpty(t *testing.T) {
	got := parseHWAccels("")
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestSelectHWAccelForCodec_AllowsStableHardwareDecodeCodecs(t *testing.T) {
	for _, codec := range []string{"h264", "hevc", "h265"} {
		if got := selectHWAccelForCodec("cuda", codec); got != "cuda" {
			t.Errorf("codec %q: got %q, want cuda", codec, got)
		}
	}
}

func TestSelectHWAccelForCodec_RoutesAV1ToCPU(t *testing.T) {
	if got := selectHWAccelForCodec("cuda", "av1"); got != "" {
		t.Errorf("AV1 must route CPU after CUDA timeout failures, got %q", got)
	}
}

func TestSelectHWAccelForCodec_NoDetectedHWAccelStaysCPU(t *testing.T) {
	if got := selectHWAccelForCodec("", "h264"); got != "" {
		t.Errorf("no detected hwaccel should stay CPU, got %q", got)
	}
}
