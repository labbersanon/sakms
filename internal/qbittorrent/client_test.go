package qbittorrent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServer wires a mux that fakes qBittorrent's login + one other
// endpoint, so every test only has to supply the endpoint-specific handler.
func newTestServer(t *testing.T, loginOK bool, extra func(mux *http.ServeMux)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing login form: %v", err)
		}
		if r.FormValue("username") != "wade" || r.FormValue("password") != "hunter2" {
			t.Errorf("unexpected login credentials: %+v", r.Form)
		}
		if !loginOK {
			w.Write([]byte("Fails."))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
		w.Write([]byte("Ok."))
	})
	if extra != nil {
		extra(mux)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestAdd_SendsURLAndCategoryWithSessionCookie(t *testing.T) {
	var gotCookie, gotBody string
	srv := newTestServer(t, true, func(mux *http.ServeMux) {
		mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
			gotCookie = r.Header.Get("Cookie")
			body, _ := io.ReadAll(r.Body)
			gotBody = string(body)
			w.Write([]byte("Ok."))
		})
	})
	c := New(Config{BaseURL: srv.URL, Username: "wade", Password: "hunter2"}, srv.Client())

	if err := c.Add(context.Background(), "magnet:?xt=urn:btih:abc123", "movies"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCookie != "SID=test-sid" {
		t.Errorf("expected session cookie to be forwarded, got %q", gotCookie)
	}
	// url.Values.Encode() sorts keys alphabetically — "category" before "urls".
	if gotBody != "category=movies&urls=magnet%3A%3Fxt%3Durn%3Abtih%3Aabc123" {
		t.Errorf("unexpected add request body: %q", gotBody)
	}
}

func TestAdd_LoginFailureIsAnError(t *testing.T) {
	srv := newTestServer(t, false, nil)
	c := New(Config{BaseURL: srv.URL, Username: "wade", Password: "hunter2"}, srv.Client())

	if err := c.Add(context.Background(), "magnet:?xt=urn:btih:abc123", ""); err == nil {
		t.Fatal("expected an error for rejected login")
	}
}

func TestAdd_RejectedByQbittorrentIsAnError(t *testing.T) {
	srv := newTestServer(t, true, func(mux *http.ServeMux) {
		mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("Fails."))
		})
	})
	c := New(Config{BaseURL: srv.URL, Username: "wade", Password: "hunter2"}, srv.Client())

	if err := c.Add(context.Background(), "not-a-real-magnet", ""); err == nil {
		t.Fatal("expected an error when qbittorrent rejects the torrent")
	}
}

func TestStatus_ParsesTorrentInfo(t *testing.T) {
	srv := newTestServer(t, true, func(mux *http.ServeMux) {
		mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("hashes") != "abc123" {
				t.Errorf("unexpected hashes query param: %s", r.URL.Query().Get("hashes"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"hash":"abc123","name":"Some.Movie.2023","state":"downloading","progress":0.42}]`))
		})
	})
	c := New(Config{BaseURL: srv.URL, Username: "wade", Password: "hunter2"}, srv.Client())

	status, err := c.Status(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.State != "downloading" || status.Progress != 0.42 {
		t.Errorf("unexpected status: %+v", status)
	}
}

func TestStatus_NoMatchingTorrentIsAnError(t *testing.T) {
	srv := newTestServer(t, true, func(mux *http.ServeMux) {
		mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		})
	})
	c := New(Config{BaseURL: srv.URL, Username: "wade", Password: "hunter2"}, srv.Client())

	if _, err := c.Status(context.Background(), "does-not-exist"); err == nil {
		t.Fatal("expected an error when qbittorrent reports no matching torrent")
	}
}

func TestHashFromMagnet(t *testing.T) {
	hash, ok := HashFromMagnet("magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12&dn=Some+Movie")
	if !ok {
		t.Fatal("expected ok=true for a valid magnet URI")
	}
	if hash != "abcdef1234567890abcdef1234567890abcdef12" {
		t.Errorf("expected a lowercased hash, got %q", hash)
	}
}

func TestHashFromMagnet_NonMagnetURLIsNotOK(t *testing.T) {
	_, ok := HashFromMagnet("https://indexer.example/download/1.torrent")
	if ok {
		t.Error("expected ok=false for a plain .torrent download URL")
	}
}
