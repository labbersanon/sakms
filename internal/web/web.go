// Package web serves SAK's frontend — a single dependency-free static
// page (no build step, no framework) embedded directly into the binary, so
// there's nothing extra to deploy alongside it.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var embedded embed.FS

// Handler serves the embedded static assets at the root of whatever path
// it's mounted on (typically "/") — index.html for "/", and everything else
// (app.js, style.css) at its own name. There's no client-side routing to
// fall back for: the page is a single document that switches views with
// plain JS, so a normal static file server is all that's needed.
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err) // static is embedded at build time — this can only fail if the embed itself is broken
	}
	return http.FileServer(http.FS(sub))
}
