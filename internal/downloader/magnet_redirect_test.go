package downloader

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// Claude 2026-08-11: cover Prowlarr-style HTTP→magnet redirects.
// Reason: Grab was 502ing with unsupported protocol scheme "magnet".
// Troubleshooting: reproduces the production 301 Location: magnet path.
// Review if: fetchMetainfoOrMagnet gains Content-Type magnet body support.

func TestFetchMetainfoOrMagnet_MagnetRedirect(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:7C0E0CE539B2BB41E61F3709940630C702BB4C0F&dn=Example&tr=udp%3a%2f%2fuploads.gamecoast.net%3a6969%2fannounce"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, magnet, http.StatusMovedPermanently)
	}))
	t.Cleanup(srv.Close)

	m := &Manager{httpClient: srv.Client()}
	mi, got, err := m.fetchMetainfoOrMagnet(context.Background(), srv.URL+"/download")
	if err != nil {
		t.Fatalf("fetchMetainfoOrMagnet: %v", err)
	}
	if mi != nil {
		t.Fatalf("expected nil metainfo on magnet redirect, got %+v", mi)
	}
	if got != magnet {
		t.Fatalf("magnet = %q, want %q", got, magnet)
	}
}

func TestFetchMetainfoOrMagnet_HTTPRedirectToTorrentBody(t *testing.T) {
	miBuilt := &metainfo.MetaInfo{Announce: "http://x"}
	info := metainfo.Info{
		Name:        "test",
		PieceLength: 16384,
		Files:       []metainfo.FileInfo{{Length: 0, Path: []string{"test"}}},
	}
	// One all-zero piece hash (20 bytes) — enough for metainfo.Load to succeed.
	info.Pieces = make([]byte, 20)
	var err error
	miBuilt.InfoBytes, err = bencode.Marshal(info)
	if err != nil {
		t.Fatalf("marshal info: %v", err)
	}
	var torrentBuf bytes.Buffer
	if err := miBuilt.Write(&torrentBuf); err != nil {
		t.Fatalf("write torrent: %v", err)
	}
	torrentBody := torrentBuf.Bytes()

	mux := http.NewServeMux()
	mux.HandleFunc("/file.torrent", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(torrentBody)
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/file.torrent", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	m := &Manager{httpClient: srv.Client()}
	mi, magnet, err := m.fetchMetainfoOrMagnet(context.Background(), srv.URL+"/download")
	if err != nil {
		t.Fatalf("fetchMetainfoOrMagnet: %v", err)
	}
	if magnet != "" {
		t.Fatalf("unexpected magnet %q", magnet)
	}
	if mi == nil {
		t.Fatal("expected metainfo from torrent body")
	}
	got, err := mi.UnmarshalInfo()
	if err != nil {
		t.Fatalf("UnmarshalInfo: %v", err)
	}
	if got.Name != "test" {
		t.Fatalf("info.Name = %q, want test", got.Name)
	}
}

func TestFetchMetainfoOrMagnet_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	m := &Manager{httpClient: srv.Client()}
	_, _, err := m.fetchMetainfoOrMagnet(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want mention of 404", err)
	}
}

func TestMagnetLocation(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusMovedPermanently,
		Header:     http.Header{"Location": []string{"  magnet:?xt=urn:btih:abc  "}},
	}
	if got := magnetLocation(resp); got != "magnet:?xt=urn:btih:abc" {
		t.Fatalf("magnetLocation = %q", got)
	}
	resp.StatusCode = http.StatusOK
	if got := magnetLocation(resp); got != "" {
		t.Fatalf("non-redirect Location should be ignored, got %q", got)
	}
}
