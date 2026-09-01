package usenet

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mnightingale/rapidyenc"
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

// ---------------------------------------------------------------------------
// Fake NNTP server harness
// ---------------------------------------------------------------------------

// fakeArticle is one article the fake server can serve. When status is non-zero
// the server replies with that NNTP response code instead of a body (430 = not
// found, 451 = removed/DMCA).
type fakeArticle struct {
	body   []byte // yEnc-encoded article body, CRLF line endings, no trailing "."
	status int
}

// fakeNNTP is a minimal NNTP server speaking just enough of the protocol for
// Tensai75/nntp: greeting, MODE READER, AUTHINFO USER/PASS, BODY and QUIT.
type fakeNNTP struct {
	ln       net.Listener
	mu       sync.Mutex
	articles map[string]fakeArticle

	bodyCount atomic.Int64

	// gate, when non-nil, blocks every BODY response until it is closed. Each
	// blocked request first signals on blocked (best-effort, never blocking).
	gate    chan struct{}
	blocked chan struct{}
}

func newFakeNNTP(t *testing.T) *fakeNNTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeNNTP{ln: ln, articles: map[string]fakeArticle{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeNNTP) add(msgID string, body []byte) {
	f.mu.Lock()
	f.articles[msgID] = fakeArticle{body: body}
	f.mu.Unlock()
}

func (f *fakeNNTP) addStatus(msgID string, status int) {
	f.mu.Lock()
	f.articles[msgID] = fakeArticle{status: status}
	f.mu.Unlock()
}

func (f *fakeNNTP) cfg() ServerConfig { return f.cfgWith(2) }

func (f *fakeNNTP) cfgWith(maxConns int) ServerConfig {
	addr := f.ln.Addr().(*net.TCPAddr)
	return ServerConfig{Host: "127.0.0.1", Port: addr.Port, Username: "user", Password: "pass", MaxConns: maxConns}
}

// gateOn makes every subsequent BODY response block until releaseGate is called.
// Each blocked request signals once on f.blocked.
func (f *fakeNNTP) gateOn() {
	f.gate = make(chan struct{})
	f.blocked = make(chan struct{}, 16)
}

func (f *fakeNNTP) releaseGate() { close(f.gate) }

// serveAll registers every segment of p on this server.
func (f *fakeNNTP) serveAll(p testPayload) {
	for i, id := range p.msgIDs {
		f.add(id, p.parts[i])
	}
}

// serveOnly registers just the 1-based segment numbers listed.
func (f *fakeNNTP) serveOnly(p testPayload, numbers ...int) {
	for _, n := range numbers {
		f.add(p.msgIDs[n-1], p.parts[n-1])
	}
}

func (f *fakeNNTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	fmt.Fprint(w, "200 fake nntp ready\r\n")
	w.Flush()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)
		switch {
		case upper == "QUIT":
			fmt.Fprint(w, "205 closing connection\r\n")
			w.Flush()
			return
		case upper == "MODE READER":
			fmt.Fprint(w, "200 reader mode\r\n")
		case strings.HasPrefix(upper, "AUTHINFO USER"):
			fmt.Fprint(w, "381 password required\r\n")
		case strings.HasPrefix(upper, "AUTHINFO PASS"):
			fmt.Fprint(w, "281 authentication accepted\r\n")
		case strings.HasPrefix(upper, "BODY "):
			id := strings.Trim(strings.TrimSpace(line[len("BODY "):]), "<>")
			f.serveBody(w, id)
		default:
			fmt.Fprint(w, "500 unknown command\r\n")
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

func (f *fakeNNTP) serveBody(w *bufio.Writer, id string) {
	if f.gate != nil {
		select {
		case f.blocked <- struct{}{}:
		default:
		}
		<-f.gate
	}
	f.bodyCount.Add(1)
	f.mu.Lock()
	a, ok := f.articles[id]
	f.mu.Unlock()
	if !ok {
		fmt.Fprint(w, "430 no such article\r\n")
		return
	}
	if a.status != 0 {
		fmt.Fprintf(w, "%d article unavailable\r\n", a.status)
		return
	}
	fmt.Fprintf(w, "222 0 <%s> body follows\r\n", id)
	// NNTP dot-stuffing: a body line starting with "." gets an extra ".".
	for _, ln := range strings.SplitAfter(string(a.body), "\r\n") {
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, ".") {
			w.WriteString(".")
		}
		w.WriteString(ln)
	}
	fmt.Fprint(w, ".\r\n")
}

// yencPart yEnc-encodes one part of a file exactly as a real poster would.
func yencPart(t *testing.T, filename string, fileSize int64, part, total int, offset int64, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc, err := rapidyenc.NewEncoder(&buf, rapidyenc.Meta{
		FileName:   filename,
		FileSize:   fileSize,
		PartNumber: int64(part),
		TotalParts: int64(total),
		Offset:     offset,
		PartSize:   int64(len(data)),
	})
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}
	if _, err := enc.Write(data); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("encoder close: %v", err)
	}
	return buf.Bytes()
}

// testPayload is a deterministic multi-segment file split into segCount parts.
type testPayload struct {
	filename string
	full     []byte
	msgIDs   []string
	parts    [][]byte // yEnc-encoded body per segment
	nzbXML   string
}

func makePayload(t *testing.T, segCount, partSize int) testPayload {
	t.Helper()
	p := testPayload{filename: "release.bin"}
	p.full = make([]byte, segCount*partSize)
	for i := range p.full {
		p.full[i] = byte(i % 251)
	}
	var segs strings.Builder
	for i := 0; i < segCount; i++ {
		msgID := fmt.Sprintf("seg%d@test", i+1)
		off := int64(i * partSize)
		p.msgIDs = append(p.msgIDs, msgID)
		p.parts = append(p.parts, yencPart(t, p.filename, int64(len(p.full)), i+1, segCount, off, p.full[off:off+int64(partSize)]))
		fmt.Fprintf(&segs, `      <segment bytes="%d" number="%d">%s</segment>`+"\n", partSize, i+1, msgID)
	}
	p.nzbXML = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p@example.com" date="0" subject="release.bin (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
` + segs.String() + `    </segments>
  </file>
</nzb>`
	return p
}

// nzbServer serves p's NZB XML over HTTP.
func nzbServer(t *testing.T, p testPayload) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-nzb")
		w.Write([]byte(p.nzbXML))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// waitTerminal polls until the download leaves "active", or fails the test.
func waitTerminal(t *testing.T, m *Manager, gid string) Download {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		d, _ := m.FindByGID(gid)
		if d != nil && d.Status != "active" {
			return *d
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("download %s never reached a terminal status", gid)
	return Download{}
}

// TestFetchSegment_DecodesRealWireFormat is a REGRESSION TEST, not scaffolding.
//
// Before articleWireReader existed, fetchSegment could not decode a single
// article: nntp.Conn.Body() canonicalises CRLF to bare LF and swallows the
// terminating "." line, while rapidyenc's decoder splits on literal "\r\n" and
// needs "\r\n.\r\n" to know the article ended — so every fetch failed with
// "yEnc decode: unexpected EOF" and no usenet download could ever complete.
// This test drives the real fetch/assemble path against a real NNTP wire stream
// carrying real rapidyenc-encoded articles. Keep it.
//
// It also covers the legacy single-server Config.Server path staying supported.
func TestFetchSegment_DecodesRealWireFormat(t *testing.T) {
	p := makePayload(t, 3, 4096)
	srvA := newFakeNNTP(t)
	srvA.serveAll(p)
	nzb := nzbServer(t, p)

	staging := t.TempDir()
	m := New(Config{Server: srvA.cfg(), StagingDir: staging, HTTPClient: nzb.Client()})
	if got := len(m.currentPools()); got != 1 {
		t.Fatalf("legacy Config.Server should produce exactly 1 pool, got %d", got)
	}

	gid, err := m.AddNZB(context.Background(), nzb.URL, "Test Release")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	d := waitTerminal(t, m, gid)
	if d.Status != "complete" {
		t.Fatalf("status = %q, err = %q", d.Status, d.ErrorMessage)
	}
	got, err := os.ReadFile(filepath.Join(staging, gid, p.filename))
	if err != nil {
		t.Fatalf("read assembled file: %v", err)
	}
	if !bytes.Equal(got, p.full) {
		t.Fatalf("assembled file mismatch: got %d bytes, want %d", len(got), len(p.full))
	}
}

// ---------------------------------------------------------------------------
// Multi-pool retrieval, error classification, runtime reconfiguration
// ---------------------------------------------------------------------------

func TestNew_ServersPluralAndLegacySingular(t *testing.T) {
	a := newFakeNNTP(t)
	b := newFakeNNTP(t)

	// Servers wins when both are set.
	m := New(Config{Server: a.cfg(), Servers: []ServerConfig{a.cfg(), b.cfg()}})
	if got := len(m.currentPools()); got != 2 {
		t.Errorf("Servers: got %d pools, want 2", got)
	}
	// Legacy singular is promoted when Servers is empty.
	m = New(Config{Server: a.cfg()})
	if got := len(m.currentPools()); got != 1 {
		t.Errorf("legacy Server: got %d pools, want 1", got)
	}
	// A zero-value Config is valid and yields no pools — the Manager can be
	// constructed unconditionally at boot and configured later.
	m = New(Config{})
	if got := len(m.currentPools()); got != 0 {
		t.Errorf("empty Config: got %d pools, want 0", got)
	}
	if cap(m.currentSemaphore()) < 1 {
		t.Error("semaphore capacity must be clamped to at least 1 with zero servers")
	}

	// HasSubscriptions is the out-of-package pre-flight guard: with the Manager
	// now constructed unconditionally at boot, a nil check no longer answers
	// "is usenet configured?".
	if m.HasSubscriptions() {
		t.Error("HasSubscriptions must be false with zero servers")
	}
	m.SetSubscriptions([]ServerConfig{a.cfg()})
	if !m.HasSubscriptions() {
		t.Error("HasSubscriptions must be true after SetSubscriptions adds one")
	}
	m.SetSubscriptions(nil)
	if m.HasSubscriptions() {
		t.Error("HasSubscriptions must be false again after all subscriptions are removed")
	}
}

func TestConcurrencyBudget_SumsAndClamps(t *testing.T) {
	cases := []struct {
		name string
		in   []ServerConfig
		want int
	}{
		{"no servers clamps to 1", nil, 1},
		{"single explicit", []ServerConfig{{MaxConns: 7}}, 7},
		{"zero substitutes the default", []ServerConfig{{MaxConns: 0}}, defaultMaxConnsPerServer},
		{"negative substitutes the default", []ServerConfig{{MaxConns: -3}}, defaultMaxConnsPerServer},
		{"sum across servers", []ServerConfig{{MaxConns: 5}, {MaxConns: 3}}, 8},
		{"sum mixes explicit and defaulted", []ServerConfig{{MaxConns: 5}, {MaxConns: 0}}, 5 + defaultMaxConnsPerServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := concurrencyBudget(newPools(tc.in)); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDownload_MaxConnsZero_StillDownloads is the M5 regression: a subscription
// saved without an explicit max_conns contributes 0, and errgroup.SetLimit(0)
// blocks every Go() call forever — a silent hang, not an error. The
// defaultMaxConnsPerServer substitution must keep the download working.
func TestDownload_MaxConnsZero_StillDownloads(t *testing.T) {
	p := makePayload(t, 4, 2048)
	srv := newFakeNNTP(t)
	srv.serveAll(p)
	nzb := nzbServer(t, p)

	staging := t.TempDir()
	m := New(Config{Servers: []ServerConfig{srv.cfgWith(0)}, StagingDir: staging, HTTPClient: nzb.Client()})
	if got := concurrencyBudget(m.currentPools()); got != defaultMaxConnsPerServer {
		t.Fatalf("budget for a MaxConns=0 subscription: got %d, want %d", got, defaultMaxConnsPerServer)
	}

	gid, err := m.AddNZB(context.Background(), nzb.URL, "Zero MaxConns")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	d := waitTerminal(t, m, gid)
	if d.Status != "complete" {
		t.Fatalf("status = %q, err = %q", d.Status, d.ErrorMessage)
	}
	assertAssembled(t, staging, gid, p)
}

// TestFetchSegmentAny_FallsBackAcrossPools is the per-segment fallback AC:
// server A holds segments 1 and 3, server B holds only segment 2, and the
// assembled file is still byte-for-byte correct.
func TestFetchSegmentAny_FallsBackAcrossPools(t *testing.T) {
	p := makePayload(t, 3, 4096)
	srvA := newFakeNNTP(t)
	srvA.serveOnly(p, 1, 3)
	srvB := newFakeNNTP(t)
	srvB.serveOnly(p, 2)
	nzb := nzbServer(t, p)

	staging := t.TempDir()
	m := New(Config{
		Servers:    []ServerConfig{srvA.cfg(), srvB.cfg()},
		StagingDir: staging,
		HTTPClient: nzb.Client(),
	})

	gid, err := m.AddNZB(context.Background(), nzb.URL, "Split Retention")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	d := waitTerminal(t, m, gid)
	if d.Status != "complete" {
		t.Fatalf("status = %q, err = %q", d.Status, d.ErrorMessage)
	}
	assertAssembled(t, staging, gid, p)
	if d.Err != nil {
		t.Errorf("a successful download must carry no Err, got %v", d.Err)
	}
	// Segment 2 must have been tried on A (430) before falling through to B.
	if srvA.bodyCount.Load() != 3 {
		t.Errorf("server A BODY count: got %d, want 3 (segments 1, 2, 3 all attempted)", srvA.bodyCount.Load())
	}
	if srvB.bodyCount.Load() != 1 {
		t.Errorf("server B BODY count: got %d, want 1 (only the segment A lacks)", srvB.bodyCount.Load())
	}
}

// TestDownload_EveryPool430_IsArticleNotFound proves the retryable
// classification survives the wrapping done by assembleFile and downloadAll.
func TestDownload_EveryPool430_IsArticleNotFound(t *testing.T) {
	p := makePayload(t, 2, 1024)
	srvA := newFakeNNTP(t) // knows nothing -> 430
	srvB := newFakeNNTP(t) // knows nothing -> 430
	nzb := nzbServer(t, p)

	m := New(Config{
		Servers:    []ServerConfig{srvA.cfg(), srvB.cfg()},
		StagingDir: t.TempDir(),
		HTTPClient: nzb.Client(),
	})
	gid, err := m.AddNZB(context.Background(), nzb.URL, "Missing Everywhere")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	d := waitTerminal(t, m, gid)
	if d.Status != "error" {
		t.Fatalf("status = %q, want error", d.Status)
	}
	if !errors.Is(d.Err, ErrArticleNotFound) {
		t.Errorf("Download.Err = %v, want errors.Is ErrArticleNotFound", d.Err)
	}
	if errors.Is(d.Err, ErrArticleRemoved) {
		t.Error("a 430-everywhere failure must NOT classify as the permanent ErrArticleRemoved")
	}
	if d.ErrorMessage == "" {
		t.Error("ErrorMessage must still be populated for the UI")
	}
}

// TestDownload_451_IsArticleRemoved is the bug this task exists to fix: a
// permanent DMCA takedown must be distinguishable from a transient failure, so
// a retry ticker never retries it forever.
func TestDownload_451_IsArticleRemoved(t *testing.T) {
	p := makePayload(t, 2, 1024)
	srv := newFakeNNTP(t)
	srv.addStatus(p.msgIDs[0], 451)
	srv.add(p.msgIDs[1], p.parts[1])
	nzb := nzbServer(t, p)

	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: t.TempDir(), HTTPClient: nzb.Client()})
	gid, err := m.AddNZB(context.Background(), nzb.URL, "Taken Down")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	d := waitTerminal(t, m, gid)
	if d.Status != "error" {
		t.Fatalf("status = %q, want error", d.Status)
	}
	if !errors.Is(d.Err, ErrArticleRemoved) {
		t.Errorf("Download.Err = %v, want errors.Is ErrArticleRemoved", d.Err)
	}
	if errors.Is(d.Err, ErrArticleNotFound) {
		t.Error("a 451 must NOT classify as the retryable ErrArticleNotFound")
	}
}

// TestFetchSegmentAny_ErrorPrecedence pins the classification ladder directly.
func TestFetchSegmentAny_ErrorPrecedence(t *testing.T) {
	p := makePayload(t, 1, 512)
	id := p.msgIDs[0]

	t.Run("no subscriptions", func(t *testing.T) {
		m := New(Config{})
		if _, err := m.fetchSegmentAny(id); !errors.Is(err, ErrNoSubscriptions) {
			t.Errorf("got %v, want ErrNoSubscriptions", err)
		}
	})

	t.Run("430 then 451 is permanent", func(t *testing.T) {
		a := newFakeNNTP(t) // 430
		b := newFakeNNTP(t)
		b.addStatus(id, 451)
		m := New(Config{Servers: []ServerConfig{a.cfg(), b.cfg()}})
		_, err := m.fetchSegmentAny(id)
		if !errors.Is(err, ErrArticleRemoved) {
			t.Errorf("got %v, want ErrArticleRemoved", err)
		}
	})

	// The downstream half of this claim — that an unclassified error really is
	// retried rather than permanently failed — is not observable from this
	// package (classifyDownloadState lives in internal/api, which imports this
	// one). It is asserted in internal/api's
	// TestClassifyDownloadStateTransientFailureIsRetryable; the two together
	// are the guard. All this subtest can prove is the half it owns: a dial
	// failure must not be MISCLASSIFIED as the permanent ErrArticleRemoved,
	// which is the only error api treats as terminal.
	t.Run("unreachable server does not become a permanent failure", func(t *testing.T) {
		a := newFakeNNTP(t)
		a.addStatus(id, 451)
		dead := ServerConfig{Host: "127.0.0.1", Port: deadPort(t), MaxConns: 1}
		m := New(Config{Servers: []ServerConfig{a.cfg(), dead}})
		_, err := m.fetchSegmentAny(id)
		if err == nil {
			t.Fatal("expected an error")
		}
		if errors.Is(err, ErrArticleRemoved) {
			t.Errorf("a dial failure must NOT be classified as the permanent ErrArticleRemoved, got %v", err)
		}
		if errors.Is(err, ErrArticleNotFound) {
			t.Errorf("a dial failure must stay unclassified, not claim every subscription answered 430, got %v", err)
		}
	})

	t.Run("a reachable server that has it wins over an earlier 430", func(t *testing.T) {
		a := newFakeNNTP(t) // 430
		b := newFakeNNTP(t)
		b.add(id, p.parts[0])
		m := New(Config{Servers: []ServerConfig{a.cfg(), b.cfg()}})
		res, err := m.fetchSegmentAny(id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if int64(len(res.data)) != 512 {
			t.Errorf("decoded %d bytes, want 512", len(res.data))
		}
	})
}

// TestSetSubscriptions_SwapWhileIdle covers the plain reconfiguration case:
// unchanged pools are reused, retired pools are closed, and downloads work
// against the new set without a restart.
func TestSetSubscriptions_SwapWhileIdle(t *testing.T) {
	p := makePayload(t, 2, 2048)
	srvA := newFakeNNTP(t)
	srvA.serveAll(p)
	srvB := newFakeNNTP(t)
	srvB.serveAll(p)
	nzb := nzbServer(t, p)

	staging := t.TempDir()
	m := New(Config{Servers: []ServerConfig{srvA.cfg()}, StagingDir: staging, HTTPClient: nzb.Client()})
	poolA := m.currentPools()[0]

	// Re-applying the identical config must reuse the pool, not churn it.
	m.SetSubscriptions([]ServerConfig{srvA.cfg()})
	if got := m.currentPools()[0]; got != poolA {
		t.Error("an unchanged subscription must reuse its existing pool")
	}

	// Swap A out for B.
	m.SetSubscriptions([]ServerConfig{srvB.cfg()})
	pools := m.currentPools()
	if len(pools) != 1 || pools[0] == poolA {
		t.Fatalf("pool set was not swapped: %d pools, same-as-A=%v", len(pools), len(pools) == 1 && pools[0] == poolA)
	}
	if pools[0].cfg.Port != srvB.cfg().Port {
		t.Errorf("active pool points at port %d, want %d", pools[0].cfg.Port, srvB.cfg().Port)
	}
	if _, err := poolA.get(); err == nil {
		t.Error("the retired pool must be closed and refuse new connections")
	}
	// Double-close must be safe.
	poolA.close()

	// Growing the set must grow the concurrency budget.
	m.SetSubscriptions([]ServerConfig{srvA.cfg(), srvB.cfg()})
	if got, want := cap(m.currentSemaphore()), srvA.cfg().MaxConns+srvB.cfg().MaxConns; got != want {
		t.Errorf("semaphore capacity after growing the set: got %d, want %d", got, want)
	}

	// And the engine still downloads afterwards.
	gid, err := m.AddNZB(context.Background(), nzb.URL, "After Swap")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	if d := waitTerminal(t, m, gid); d.Status != "complete" {
		t.Fatalf("status = %q, err = %q", d.Status, d.ErrorMessage)
	}
	assertAssembled(t, staging, gid, p)

	// Dropping to zero subscriptions is legal and fails cleanly rather than hanging.
	m.SetSubscriptions(nil)
	if _, err := m.fetchSegmentAny(p.msgIDs[0]); !errors.Is(err, ErrNoSubscriptions) {
		t.Errorf("with no subscriptions: got %v, want ErrNoSubscriptions", err)
	}
}

// TestSetSubscriptions_SwapDuringActiveDownload is the concurrency case the plan
// flagged: retire a pool while a segment fetch still holds one of its
// connections, with the progress-poll loop running at the same time. Run under
// -race, this asserts the swap is free of data races and use-after-close, and
// that the in-flight download still reaches a terminal state.
func TestSetSubscriptions_SwapDuringActiveDownload(t *testing.T) {
	p := makePayload(t, 5, 2048)
	srvA := newFakeNNTP(t)
	srvA.gateOn()
	srvA.serveAll(p)
	srvB := newFakeNNTP(t)
	srvB.serveAll(p)
	nzb := nzbServer(t, p)

	staging := t.TempDir()
	m := New(Config{Servers: []ServerConfig{srvA.cfg()}, StagingDir: staging, HTTPClient: nzb.Client()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Start(ctx) // concurrent snapshot/fanout loop + the shutdown close() path
	sub, unsub := m.Subscribe()
	defer unsub()
	go func() {
		for range sub {
		}
	}()

	gid, err := m.AddNZB(ctx, nzb.URL, "Swap Mid-Flight")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}

	// Wait until a BODY is actually in flight against server A, so the swap
	// below retires a pool that a fetch is currently holding a connection from.
	select {
	case <-srvA.blocked:
	case <-time.After(20 * time.Second):
		t.Fatal("no BODY request ever reached server A")
	}

	m.SetSubscriptions([]ServerConfig{srvB.cfg()})
	srvA.releaseGate() // let the in-flight fetch complete against the now-closed pool

	d := waitTerminal(t, m, gid)
	// The assertion is "reached a terminal state without racing or panicking".
	// Success is the expected path (server B holds every segment), so report the
	// error rather than tolerating it silently.
	if d.Status != "complete" {
		t.Fatalf("status = %q, err = %q", d.Status, d.ErrorMessage)
	}
	assertAssembled(t, staging, gid, p)

	// Prove the swap actually landed mid-download rather than after it: server A
	// served only the one gated segment, and every remaining segment came from
	// the server that replaced it.
	if got := srvA.bodyCount.Load(); got != 1 {
		t.Errorf("server A BODY count: got %d, want 1 (only the gated in-flight segment)", got)
	}
	if got := srvB.bodyCount.Load(); got != int64(len(p.msgIDs)-1) {
		t.Errorf("server B BODY count: got %d, want %d (every segment after the swap)", got, len(p.msgIDs)-1)
	}

	// The retired pool must not have parked its returned connection in an
	// abandoned idle channel.
	if _, err := m.currentPools()[0].get(); err != nil {
		t.Errorf("the surviving pool must still be usable: %v", err)
	}

	cancel()                          // exercise Start's shutdown close-all path
	time.Sleep(50 * time.Millisecond) // let Start observe the cancellation
}

// assertAssembled checks the assembled file matches the payload byte-for-byte.
func assertAssembled(t *testing.T, staging, gid string, p testPayload) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(staging, gid, p.filename))
	if err != nil {
		t.Fatalf("read assembled file: %v", err)
	}
	if !bytes.Equal(got, p.full) {
		t.Fatalf("assembled file mismatch: got %d bytes, want %d", len(got), len(p.full))
	}
}

// TestPool_PutAfterClose_TerminatesConnection is the leak guard for the
// SetSubscriptions swap: close() cannot touch a connection an in-flight fetch is
// holding (it is not in the idle channel), so that connection comes back via
// put() after the pool is already retired. It must be terminated, not parked in
// an idle channel nobody will ever drain again.
func TestPool_PutAfterClose_TerminatesConnection(t *testing.T) {
	srv := newFakeNNTP(t)
	p := newPool(srv.cfg())

	conn, err := p.get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	p.close() // retires the pool while conn is still checked out
	p.close() // double-close must be safe

	p.put(conn, true)
	if n := len(p.idle); n != 0 {
		t.Errorf("a connection returned to a closed pool must not be parked; idle holds %d", n)
	}
	if _, err := p.get(); err == nil {
		t.Error("a closed pool must refuse to hand out connections")
	}
}

// TestPool_HardCapsLiveConnections is the NZBGet ServerX.Connections regression:
// with MaxConns=1, a second get must wait for put rather than dialing a second
// socket (the pre-fix behavior that blew past provider limits).
func TestPool_HardCapsLiveConnections(t *testing.T) {
	srv := newFakeNNTP(t)
	cfg := srv.cfg()
	cfg.MaxConns = 1
	p := newPool(cfg)

	first, err := p.get()
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if got := len(p.live); got != 1 {
		t.Fatalf("live tokens after first dial: got %d, want 1", got)
	}

	type getResult struct {
		err error
	}
	done := make(chan getResult, 1)
	go func() {
		c, err := p.get()
		if err != nil {
			done <- getResult{err: err}
			return
		}
		p.put(c, true)
		done <- getResult{}
	}()

	select {
	case <-done:
		t.Fatal("second get returned while MaxConns=1 connection was still checked out — pool dialed past the hard cap")
	case <-time.After(100 * time.Millisecond):
		// Expected: blocked until put.
	}

	p.put(first, true)

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("second get after put: %v", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second get stayed blocked after put returned the only connection")
	}
}

// deadPort returns a port that is guaranteed to refuse connections: bind it,
// read the assigned number, then release it. Avoids assuming anything about the
// host (e.g. that nothing listens on port 1).
func deadPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

var opaqueNZBGID = regexp.MustCompile(`^nzb-[0-9a-f]{16}$`)

// Regression for the restart collision: a process-local nzb-1, nzb-2, … counter
// reset on every container restart and reused the GID of an in-flight grab.
func TestAllocateStaging_OpaqueAndUnique(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	g1, d1, err := m.allocateStaging()
	if err != nil {
		t.Fatalf("first allocateStaging: %v", err)
	}
	g2, d2, err := m.allocateStaging()
	if err != nil {
		t.Fatalf("second allocateStaging: %v", err)
	}
	if g1 == g2 {
		t.Fatalf("two allocations minted the same gid %q", g1)
	}
	for gid, dir := range map[string]string{g1: d1, g2: d2} {
		if !opaqueNZBGID.MatchString(gid) {
			t.Errorf("gid %q: want nzb- + 16 hex chars", gid)
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Errorf("staging dir %s: %v", dir, err)
		}
	}
}

func TestAddNZB_ReturnsOpaqueGID(t *testing.T) {
	p := makePayload(t, 1, 1024)
	nzb := nzbServer(t, p)
	m := New(Config{StagingDir: t.TempDir(), HTTPClient: nzb.Client()})
	gid, err := m.AddNZB(context.Background(), nzb.URL, "Opaque GID")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	if !opaqueNZBGID.MatchString(gid) {
		t.Errorf("AddNZB gid %q: want nzb- + 16 hex chars", gid)
	}
	m.Cancel(gid)
}
