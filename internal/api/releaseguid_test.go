package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
)

// assertRememberedEnclosure checks the GUID cache rather than the JSON
// DownloadURL field — that field is json:"-" so HTTP decodes always see it empty.
func assertRememberedEnclosure(t *testing.T, guid, wantURL, wantProto string) {
	t.Helper()
	guid = strings.TrimSpace(guid)
	if guid == "" {
		t.Fatal("expected a non-empty guid for the remembered enclosure")
	}
	h, ok := grabReleaseCache.lookup(guid)
	if !ok {
		t.Fatalf("guid %q not in grabReleaseCache", guid)
	}
	if h.DownloadURL != wantURL {
		t.Errorf("cached DownloadURL = %q, want %q", h.DownloadURL, wantURL)
	}
	if wantProto != "" && h.Protocol != wantProto {
		t.Errorf("cached Protocol = %q, want %q", h.Protocol, wantProto)
	}
}

func TestReleaseGUIDCache_RememberAndLookup(t *testing.T) {
	c := newReleaseGUIDCache(time.Minute)
	guid := c.remember("g1", "https://idx/a.nzb", "usenet")
	if guid != "g1" {
		t.Fatalf("remember guid = %q, want g1", guid)
	}
	h, ok := c.lookup("g1")
	if !ok {
		t.Fatal("lookup missed a just-remembered guid")
	}
	if h.DownloadURL != "https://idx/a.nzb" || h.Protocol != "usenet" {
		t.Errorf("handle = %+v", h)
	}
}

func TestReleaseGUIDCache_EmptyGUIDDerivesOpaque(t *testing.T) {
	c := newReleaseGUIDCache(time.Minute)
	guid := c.remember("", "magnet:?xt=urn:btih:abc", "torrent")
	if guid == "" {
		t.Fatal("expected a derived guid")
	}
	h, ok := c.lookup(guid)
	if !ok || h.DownloadURL != "magnet:?xt=urn:btih:abc" {
		t.Errorf("derived lookup = %+v ok=%v", h, ok)
	}
}

func TestReleaseGUIDCache_Expired(t *testing.T) {
	c := newReleaseGUIDCache(time.Millisecond)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.remember("g-exp", "https://x/1", "torrent")
	c.now = func() time.Time { return now.Add(time.Second) }
	if _, ok := c.lookup("g-exp"); ok {
		t.Error("expected expired guid to miss")
	}
}

func TestResolveClientEnclosure_IgnoresClientURL(t *testing.T) {
	grabReleaseCache.remember("known", "https://idx/real.nzb", "usenet")
	t.Cleanup(func() {})

	req := apidto.AutoGrabRequest{
		GUID:             "known",
		DownloadURL:      "http://169.254.169.254/latest",
		DownloadProtocol: "torrent",
	}
	if err := resolveClientEnclosure(&req); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if req.DownloadURL != "https://idx/real.nzb" || req.DownloadProtocol != "usenet" {
		t.Errorf("got url=%q proto=%q", req.DownloadURL, req.DownloadProtocol)
	}
}

func TestResolveClientEnclosure_UnknownGUID(t *testing.T) {
	req := apidto.AutoGrabRequest{GUID: "never-seen", DownloadURL: "http://evil"}
	if err := resolveClientEnclosure(&req); err != errUnknownReleaseGUID {
		t.Fatalf("err = %v, want errUnknownReleaseGUID", err)
	}
}

func TestResolveClientEnclosure_NoGUIDLeavesInProcessURL(t *testing.T) {
	req := apidto.AutoGrabRequest{DownloadURL: "magnet:?from-feeder"}
	if err := resolveClientEnclosure(&req); err != nil {
		t.Fatal(err)
	}
	if req.DownloadURL != "magnet:?from-feeder" {
		t.Errorf("feeder URL cleared: %q", req.DownloadURL)
	}
}

func TestAutoGrabRequest_JSONIgnoresDownloadURL(t *testing.T) {
	var req apidto.AutoGrabRequest
	if err := json.Unmarshal([]byte(`{"title":"X","downloadUrl":"http://evil","guid":""}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.DownloadURL != "" {
		t.Errorf("downloadUrl leaked from JSON: %q", req.DownloadURL)
	}
}
