package usenet

import (
	"net/http"
	"testing"
)

// -- ParseURL --

func TestParseURL_PlainNNTP_DefaultPort(t *testing.T) {
	cfg, err := ParseURL("nntp://news.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "news.example.com" {
		t.Errorf("host: got %q, want %q", cfg.Host, "news.example.com")
	}
	if cfg.Port != 119 {
		t.Errorf("port: got %d, want 119", cfg.Port)
	}
	if cfg.TLS {
		t.Error("TLS: got true, want false for nntp://")
	}
}

func TestParseURL_TLS_DefaultPort(t *testing.T) {
	cfg, err := ParseURL("nntps://secure.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "secure.example.com" {
		t.Errorf("host: got %q, want %q", cfg.Host, "secure.example.com")
	}
	if cfg.Port != 563 {
		t.Errorf("port: got %d, want 563", cfg.Port)
	}
	if !cfg.TLS {
		t.Error("TLS: got false, want true for nntps://")
	}
}

func TestParseURL_ExplicitPort_Plain(t *testing.T) {
	cfg, err := ParseURL("nntp://news.example.com:8119")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8119 {
		t.Errorf("port: got %d, want 8119", cfg.Port)
	}
	if cfg.TLS {
		t.Error("TLS should remain false when scheme is nntp")
	}
}

func TestParseURL_ExplicitPort_TLS(t *testing.T) {
	cfg, err := ParseURL("nntps://secure.example.com:443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 443 {
		t.Errorf("port: got %d, want 443", cfg.Port)
	}
	if !cfg.TLS {
		t.Error("TLS should remain true when scheme is nntps")
	}
}

func TestParseURL_WrongScheme(t *testing.T) {
	_, err := ParseURL("https://news.example.com")
	if err == nil {
		t.Fatal("expected error for https scheme, got nil")
	}
}

func TestParseURL_NoScheme(t *testing.T) {
	_, err := ParseURL("news.example.com")
	if err == nil {
		t.Fatal("expected error for URL with no scheme, got nil")
	}
}

// -- ParseNZB --

const singleFileNZB = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <head>
    <meta type="title">Some.Movie.2023.1080p.BluRay-GROUP</meta>
    <meta type="category">Movie</meta>
  </head>
  <file poster="poster@example.com" date="1700000000" subject="Some.Movie.2023.1080p.BluRay-GROUP.rar (1/2)">
    <groups>
      <group>alt.binaries.movies</group>
    </groups>
    <segments>
      <segment bytes="75000000" number="1">abc123@example.com</segment>
      <segment bytes="50000000" number="2">def456@example.com</segment>
    </segments>
  </file>
</nzb>`

func TestParseNZB_ValidSingleFile(t *testing.T) {
	nzb, err := ParseNZB([]byte(singleFileNZB))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nzb.Meta) != 2 {
		t.Errorf("meta: got %d entries, want 2", len(nzb.Meta))
	}
	if nzb.Meta[0].Type != "title" || nzb.Meta[0].Value != "Some.Movie.2023.1080p.BluRay-GROUP" {
		t.Errorf("meta[0]: got %+v", nzb.Meta[0])
	}
	if len(nzb.Files) != 1 {
		t.Fatalf("files: got %d, want 1", len(nzb.Files))
	}
	f := nzb.Files[0]
	if len(f.Groups) != 1 || f.Groups[0] != "alt.binaries.movies" {
		t.Errorf("groups: got %v", f.Groups)
	}
	if len(f.Segs) != 2 {
		t.Fatalf("segments: got %d, want 2", len(f.Segs))
	}
	if f.Segs[0].Number != 1 || f.Segs[0].Bytes != 75000000 || f.Segs[0].MsgID != "abc123@example.com" {
		t.Errorf("seg[0]: got %+v", f.Segs[0])
	}
	if f.Segs[1].Number != 2 || f.Segs[1].Bytes != 50000000 {
		t.Errorf("seg[1]: got %+v", f.Segs[1])
	}
}

func TestParseNZB_MultipleFiles(t *testing.T) {
	data := []byte(`<?xml version="1.0"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p" date="0" subject="pack.rar (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="1000" number="1">msg1@test</segment></segments>
  </file>
  <file poster="p" date="0" subject="pack.r00 (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="1000" number="1">msg2@test</segment></segments>
  </file>
</nzb>`)
	nzb, err := ParseNZB(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nzb.Files) != 2 {
		t.Errorf("files: got %d, want 2", len(nzb.Files))
	}
}

func TestParseNZB_NoMetaNoFiles(t *testing.T) {
	data := []byte(`<?xml version="1.0"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`)
	nzb, err := ParseNZB(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nzb.Meta) != 0 || len(nzb.Files) != 0 {
		t.Errorf("expected empty NZB, got %+v", nzb)
	}
}

func TestParseNZB_InvalidXML(t *testing.T) {
	_, err := ParseNZB([]byte("<not valid xml"))
	if err == nil {
		t.Fatal("expected error for malformed XML, got nil")
	}
}

func TestParseNZB_EmptyInput(t *testing.T) {
	_, err := ParseNZB([]byte{})
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

// -- sanitizeName --

func TestSanitizeName_ForwardSlash(t *testing.T) {
	if got := sanitizeName("foo/bar/baz"); got != "foo_bar_baz" {
		t.Errorf("got %q, want %q", got, "foo_bar_baz")
	}
}

func TestSanitizeName_Backslash(t *testing.T) {
	if got := sanitizeName(`a\b\c`); got != "a_b_c" {
		t.Errorf("got %q, want %q", got, "a_b_c")
	}
}

func TestSanitizeName_NullByte(t *testing.T) {
	if got := sanitizeName("abc\x00def"); got != "abc_def" {
		t.Errorf("got %q, want %q", got, "abc_def")
	}
}

func TestSanitizeName_AllThreeSeparators(t *testing.T) {
	if got := sanitizeName("/\\\x00"); got != "___" {
		t.Errorf("got %q, want %q", got, "___")
	}
}

func TestSanitizeName_NoSpecialChars(t *testing.T) {
	in := "Some.Movie.2023.1080p.BluRay-GROUP"
	if got := sanitizeName(in); got != in {
		t.Errorf("expected identity, got %q", got)
	}
}

func TestSanitizeName_Empty(t *testing.T) {
	if got := sanitizeName(""); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// -- parseDNZBHeaders --

func TestParseDNZBHeaders_AllPresent(t *testing.T) {
	h := make(http.Header)
	h.Set("X-DNZB-Name", "Some.Movie.2023-GROUP")
	h.Set("X-DNZB-RCode", "200")
	h.Set("X-DNZB-Failure", "")
	d := parseDNZBHeaders(h)
	if d.Name != "Some.Movie.2023-GROUP" {
		t.Errorf("Name: got %q", d.Name)
	}
	if d.RCode != 200 {
		t.Errorf("RCode: got %d, want 200", d.RCode)
	}
	if d.Failure != "" {
		t.Errorf("Failure: got %q, want empty", d.Failure)
	}
}

func TestParseDNZBHeaders_Failure(t *testing.T) {
	h := make(http.Header)
	h.Set("X-DNZB-RCode", "400")
	h.Set("X-DNZB-Failure", "NZB not found on server")
	d := parseDNZBHeaders(h)
	if d.RCode != 400 {
		t.Errorf("RCode: got %d, want 400", d.RCode)
	}
	if d.Failure != "NZB not found on server" {
		t.Errorf("Failure: got %q", d.Failure)
	}
}

func TestParseDNZBHeaders_EmptyHeaders(t *testing.T) {
	d := parseDNZBHeaders(http.Header{})
	if d.Name != "" || d.RCode != 0 || d.Failure != "" {
		t.Errorf("expected zero-value DNZBHeaders, got %+v", d)
	}
}

func TestParseDNZBHeaders_InvalidRCode(t *testing.T) {
	h := make(http.Header)
	h.Set("X-DNZB-RCode", "not-a-number")
	d := parseDNZBHeaders(h)
	// strconv.Atoi failure leaves RCode at zero (the _, _ pattern in parseDNZBHeaders).
	if d.RCode != 0 {
		t.Errorf("RCode: got %d, want 0 for non-numeric input", d.RCode)
	}
}
