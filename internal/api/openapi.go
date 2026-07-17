package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var openapiYAML []byte

// OpenapiHandler serves the embedded OpenAPI 3.1 spec at GET /api/openapi.yaml.
// No auth required — the spec describes public API structure, not private data.
func OpenapiHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(openapiYAML)
	}
}
