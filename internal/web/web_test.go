package web

import (
	"bytes"
	"io/fs"
	"net/http/httptest"
	"testing"
)

// A stale cached shell that still calls a since-removed API route is a real
// incident (see setCacheControl's doc comment) -- these tests lock in the
// cache-control split that prevents it: index.html/unhashed files must always
// revalidate, content-hashed assets/* can cache forever.
//
// The hashed-asset test globs for a real assets/* file at run time instead of
// hardcoding today's build hash -- a hardcoded filename would silently break
// on the next frontend content change (Vite regenerates the hash, the literal
// path stops existing, existsAsFile falls through to the no-store branch, and
// the assertion fails for the wrong reason).
func TestHandler_CacheControl_HashedAsset(t *testing.T) {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	matches, err := fs.Glob(sub, "assets/*")
	if err != nil || len(matches) == 0 {
		t.Fatalf("no files found under assets/ (err=%v)", err)
	}

	h := Handler()
	req := httptest.NewRequest("GET", "/"+matches[0], nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want immutable long-lived cache", got)
	}
}

func TestHandler_CacheControl_IndexHTML(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/index.html", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestHandler_CacheControl_Root(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestBuiltIndex_UsesRootRelativeAssets(t *testing.T) {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	body, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if bytes.Contains(body, []byte(`"./assets/`)) || bytes.Contains(body, []byte(`'./assets/`)) {
		t.Fatalf("index.html uses relative asset URLs; nested SPA routes resolve them under the route path")
	}
	if !bytes.Contains(body, []byte(`"/assets/`)) && !bytes.Contains(body, []byte(`'/assets/`)) {
		t.Fatalf("index.html does not reference root-relative Vite assets")
	}
}

func TestHandler_CacheControl_UnhashedStaticFile(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/logo.svg", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// SPA fallback: an unknown path (client-side route) falls back to index.html
// and must carry the same no-store guarantee as a direct index.html request.
func TestHandler_CacheControl_SPAFallback(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/discover", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
