package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// --- pure display logic ----------------------------------------------------

func TestBuildKeyRows(t *testing.T) {
	catalog := []string{"movies_library_root_folder", "series_library_root_folder", "adult_library_root_folder"}
	authored := []authoredMapping{
		{Key: "movies_library_root_folder", NodePath: "/mnt/movies"},
		{Key: "series_library_root_folder", NodePath: ""}, // blank = skip, treated as unset
	}
	got := buildKeyRows(catalog, authored)
	want := []keyRow{
		{Key: "movies_library_root_folder", NodePath: "/mnt/movies", Mapped: true},
		{Key: "series_library_root_folder", NodePath: "", Mapped: false},
		{Key: "adult_library_root_folder", NodePath: "", Mapped: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildKeyRows = %+v, want %+v", got, want)
	}
}

func TestBuildKeyRows_PreservesCatalogOrderAndIgnoresUnknownAuthored(t *testing.T) {
	catalog := []string{"b_key", "a_key"}
	authored := []authoredMapping{
		{Key: "a_key", NodePath: "/a"},
		{Key: "ghost_key", NodePath: "/ghost"}, // not in catalog → not rendered
	}
	got := buildKeyRows(catalog, authored)
	want := []keyRow{
		{Key: "b_key", NodePath: "", Mapped: false},
		{Key: "a_key", NodePath: "/a", Mapped: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildKeyRows = %+v, want %+v", got, want)
	}
}

func TestKeyRowTitle(t *testing.T) {
	if got := keyRowTitle(keyRow{Key: "k", NodePath: "/x", Mapped: true}); got != "k  →  /x" {
		t.Errorf("mapped title = %q", got)
	}
	if got := keyRowTitle(keyRow{Key: "k", Mapped: false}); got != "k  →  not set" {
		t.Errorf("unset title = %q", got)
	}
}

func TestSetItemTitle(t *testing.T) {
	if got := setItemTitle(true); got != "Change folder…" {
		t.Errorf("mapped = %q", got)
	}
	if got := setItemTitle(false); got != "Set folder…" {
		t.Errorf("unset = %q", got)
	}
}

func TestPathMappingGateOpen(t *testing.T) {
	if pathMappingGateOpen(0) {
		t.Error("gate should be CLOSED with zero media roots")
	}
	if !pathMappingGateOpen(1) {
		t.Error("gate should be OPEN with one media root")
	}
	if !pathMappingGateOpen(3) {
		t.Error("gate should be OPEN with three media roots")
	}
}

func TestPathPushWarningLine(t *testing.T) {
	// Empty error → hidden (the daemon clears lastPushError on a successful echo,
	// so the line disappears on its own).
	if text, show := pathPushWarningLine(""); show || text != "" {
		t.Errorf("empty error: got (%q, %v), want (\"\", false)", text, show)
	}
	text, show := pathPushWarningLine(`push for "movies_library_root_folder" failed: status 422`)
	if !show {
		t.Fatal("non-empty error should show the warning line")
	}
	if want := `⚠ Path mapping: last push failed — push for "movies_library_root_folder" failed: status 422 (re-pick a folder to retry)`; text != want {
		t.Errorf("warning text = %q, want %q", text, want)
	}
}

// --- control client round-trip over a real unix socket ---------------------

func TestPathMapControlClient_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "control.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /pathmap", func(w http.ResponseWriter, r *http.Request) {
		writePathMap(w, http.StatusOK, pathMapView{
			AuthoredPaths:   []authoredMapping{{Key: "movies_library_root_folder", NodePath: "/mnt/movies"}},
			LibraryPathKeys: []string{"movies_library_root_folder", "series_library_root_folder"},
			LastPushError:   "",
		})
	})
	mux.HandleFunc("POST /pathmap/set", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key       string `json:"key"`
			LocalPath string `json:"localPath"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.LocalPath == "/" {
			writePathMap(w, http.StatusBadRequest, pathMapView{Error: "path is too shallow"})
			return
		}
		// set echo omits the catalog, like the daemon's writePathMapState.
		writePathMap(w, http.StatusOK, pathMapView{
			AuthoredPaths: []authoredMapping{{Key: req.Key, NodePath: req.LocalPath}},
		})
	})
	mux.HandleFunc("POST /pathmap/clear", func(w http.ResponseWriter, r *http.Request) {
		writePathMap(w, http.StatusOK, pathMapView{AuthoredPaths: nil})
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })

	client := newControlClient(socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	view, err := client.getPathMap(ctx)
	if err != nil {
		t.Fatalf("getPathMap: %v", err)
	}
	if !reflect.DeepEqual(view.LibraryPathKeys, []string{"movies_library_root_folder", "series_library_root_folder"}) {
		t.Errorf("catalog = %v", view.LibraryPathKeys)
	}
	if len(view.AuthoredPaths) != 1 || view.AuthoredPaths[0].NodePath != "/mnt/movies" {
		t.Errorf("authored = %+v", view.AuthoredPaths)
	}

	view, err = client.setPathMap(ctx, "movies_library_root_folder", "/mnt/movies")
	if err != nil {
		t.Fatalf("setPathMap: %v", err)
	}
	if len(view.AuthoredPaths) != 1 || view.AuthoredPaths[0].Key != "movies_library_root_folder" {
		t.Errorf("set echo authored = %+v", view.AuthoredPaths)
	}

	if _, err = client.clearPathMap(ctx, "movies_library_root_folder"); err != nil {
		t.Fatalf("clearPathMap: %v", err)
	}

	// A daemon-side rejection (400 with an error body) surfaces as an error.
	_, err = client.setPathMap(ctx, "movies_library_root_folder", "/")
	if err == nil || err.Error() != "path is too shallow" {
		t.Fatalf("setPathMap(/) error = %v, want \"path is too shallow\"", err)
	}
}

func writePathMap(w http.ResponseWriter, status int, v pathMapView) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
