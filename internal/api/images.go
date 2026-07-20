package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/imageproxy"
)

// imageProxyHandler streams poster/thumbnail art from an allowlisted external
// image host (TMDB for Movies/Series, TPDB for Adult) through the Go backend,
// so the browser never hot-links those hosts directly — the requirement that
// keeps operator browsing off TMDB/TPDB and plays cleanly with the internal-
// only Traefik/CrowdSec middleware (plan Decision #7). It is registered on
// NewMux, so it inherits the same session/API-key protection as every other
// route; an <img src="/api/images/proxy?url=..."> carries the operator's
// session cookie exactly like any other same-origin request.
//
// The upstream URL is passed as a ?url= query param (URL-encoded), not a path
// segment: the upstream is a full absolute URL with its own scheme, host,
// path, and query string, so carrying it as one opaque, percent-encoded value
// is cleaner and less error-prone than reconstructing it from path segments
// (and preserves upstream sizing query params like TPDB/imgix's ?w=&h=).
//
// The imageproxy.Proxy is built once here (constructor runs at mux-build time),
// so its in-memory LRU cache is a process-lifetime singleton shared across
// every request — a poster fetched during one grid render is not re-fetched
// from TMDB/TPDB on the next.
func imageProxyHandler(httpClient *http.Client) http.HandlerFunc {
	proxy := imageproxy.New(httpClient)
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("url")
		if raw == "" {
			http.Error(w, "url query parameter is required", http.StatusBadRequest)
			return
		}

		img, err := proxy.Fetch(r.Context(), raw)
		if err != nil {
			// A bad/off-allowlist URL is the operator's request error (400);
			// an allowlisted host that failed to serve is an upstream/gateway
			// error (502), same split adultDiscoverHandler uses.
			if errors.Is(err, imageproxy.ErrInvalidURL) || errors.Is(err, imageproxy.ErrHostNotAllowed) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", img.ContentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(img.Body)))
		// Poster/thumbnail art is effectively immutable per URL (TMDB/TPDB
		// bake a content hash / stable path into it), so let the browser cache
		// it too and spare even the proxy round-trip on a re-render.
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Write(img.Body)
	}
}
