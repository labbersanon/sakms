package videophash

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
