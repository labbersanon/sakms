// Command gendto regenerates internal/apidto/ts/dto.gen.ts from
// internal/apidto's Go source. Run it after any change to internal/apidto,
// then commit the resulting .ts file — internal/apidto/gen's TestNoDrift
// (part of `go test ./...`) fails the build if the committed file and a
// fresh regeneration ever disagree.
//
// Usage:
//
//	go run ./cmd/gendto
//
// This binary is NOT part of the sakms server (cmd/sakms never imports
// internal/apidto/gen), so tygo and its dependencies never link into the
// production image.
package main

import (
	"log"
	"path/filepath"
	"runtime"

	"github.com/labbersanon/sakms/internal/apidto/gen"
)

func main() {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		log.Fatal("gendto: could not resolve source file path")
	}
	// Resolve the output path relative to this file's location (cmd/gendto),
	// not the caller's working directory, so `go run ./cmd/gendto` works
	// the same from anywhere in the repo.
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	outPath := filepath.Join(repoRoot, "internal", "apidto", "ts", "dto.gen.ts")

	if err := gen.Generate(outPath); err != nil {
		log.Fatalf("gendto: %v", err)
	}
	log.Printf("gendto: wrote %s", outPath)
}
