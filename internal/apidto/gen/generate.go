// Package gen wraps tygo (github.com/gzuidhof/tygo), the source-parsing
// Go→TypeScript codegen tool chosen for internal/apidto (see
// .omc/plans/frontend-redesign-seerr.md Stage 0 and internal/apidto's
// package doc / README.md for the full rationale). This package is
// deliberately NOT imported by cmd/sakms — tygo and its dependencies
// (golang.org/x/tools/go/packages, a real Go source-file parser) never link
// into the production binary. It's imported only by cmd/gendto (the local
// dev/CI regeneration entry point) and by this package's own drift test.
package gen

import (
	"github.com/gzuidhof/tygo/tygo"
)

// SourcePackage is the Go import path tygo parses. Pointed at ONLY
// internal/apidto — never internal/api or any other internal/domain
// package — so generation only ever sees the curated, exported DTO set,
// never leaks an internal handler's unexported request struct or a raw
// domain type (see internal/apidto's package doc for why that boundary
// matters).
const SourcePackage = "github.com/labbersanon/sakms/internal/apidto"

// Generate runs tygo against SourcePackage and writes the resulting
// TypeScript to outputPath (a full file path, e.g. ending in ".ts").
// Generation is fully deterministic (verified empirically: two runs against
// identical Go source produce byte-identical output — tygo has no
// timestamp, random ordering, or similar non-determinism), which is what
// lets TestNoDrift byte-compare a fresh run against the committed file.
func Generate(outputPath string) error {
	cfg := &tygo.Config{
		Packages: []*tygo.PackageConfig{
			{
				Path:       SourcePackage,
				OutputPath: outputPath,
				// PreserveComments defaults to "default" (package + type +
				// field doc comments all carried into the .ts) — load-bearing
				// here, since ConnectionUpsertRequest.APIKey's three-state
				// mapping rule rides into the generated output as a JSDoc
				// comment on the field itself, not just in this repo's Go
				// source or README.
			},
		},
	}
	return tygo.New(cfg).Generate()
}
