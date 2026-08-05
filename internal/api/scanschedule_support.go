package api

import (
	"context"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/settings"
)

// This file exposes the minimal seam cmd/sakms/scanadapter.go needs to build a
// Scan cycle identically to the manual scan handlers, WITHOUT duplicating the
// per-mode settings-resolution logic (naming preset, phash/confidence
// thresholds, root-folder key) into package main. Duplicating the phash
// scale-gate in particular would risk silent drift that changes Dedup grouping,
// so the single source of truth (library.go's resolve* helpers) is reused via
// these thin exported wrappers instead. Kept in its own file (not handler.go /
// vmaf.go) so it never touches those shared, concurrently-edited files.

// ResolveNamingPreset is the exported wrapper over resolveNamingPreset, for the
// scan scheduler's adapter (Rename cycles need the active preset).
func ResolveNamingPreset(ctx context.Context, settingsStore *settings.Store, m mode.Mode) (naming.Preset, error) {
	return resolveNamingPreset(ctx, settingsStore, m)
}

// ResolveMatchConfig is the exported wrapper over resolveMatchConfig, for the
// scan scheduler's adapter (Rename cycles).
func ResolveMatchConfig(ctx context.Context, settingsStore *settings.Store, m mode.Mode) (rename.MatchConfig, error) {
	return resolveMatchConfig(ctx, settingsStore, m)
}

// ResolvePHashThreshold is the exported wrapper over resolvePHashThreshold, for
// the scan scheduler's adapter (Dedup cycles). Reusing it (rather than
// reimplementing the scale-tagged version gate in package main) is what keeps a
// scheduled Dedup scan grouping files exactly like a manual one.
func ResolvePHashThreshold(ctx context.Context, settingsStore *settings.Store, m mode.Mode) (int, error) {
	return resolvePHashThreshold(ctx, settingsStore, m)
}

// LibraryRootFolderKey is the exported wrapper over libraryRootFolderKey, so the
// adapter reads a mode's configured root-folder path from the same settings key
// the handlers use. ok is false for a mode with no root-folder concept.
func LibraryRootFolderKey(m mode.Mode) (key string, ok bool) {
	return libraryRootFolderKey(m)
}

// EagerComputeAndCacheVMAF computes and caches one candidate-vs-reference VMAF
// score for a scheduled Dedup cycle's eager fan-out (plan AC6). It is the
// eager-path counterpart to vmafHandler's on-demand computeAndCacheVMAF, sharing
// the SAME package-level compute seam (vmafCompute → vmaf.Compute) so combined
// on-demand + eager concurrency is bounded by internal/vmaf's single shared
// semaphore (plan AC7), and the SAME identity-stamp helper
// (library.VMAFFileIdentity) so an eagerly-cached score reads back as a warm
// cache hit on the next on-demand view instead of a stale miss.
//
// It checks the cache first (a valid cached score is a no-op — eager runs are
// idempotent and cheap on re-scan) and returns any compute/stat/store error to
// the caller, which logs-and-continues to the next pair (skip-and-continue,
// AC7) so one pathological pair never blocks the cycle.
func EagerComputeAndCacheVMAF(ctx context.Context, libStore *library.Store, candidatePath, referencePath string) error {
	if _, ok, err := libStore.GetValidVMAFScore(ctx, candidatePath, referencePath); err != nil {
		return err
	} else if ok {
		return nil // already cached and still valid — nothing to do
	}

	score, err := vmafCompute(ctx, candidatePath, referencePath)
	if err != nil {
		return err
	}
	size, mtime, err := library.VMAFFileIdentity(candidatePath)
	if err != nil {
		return err
	}
	return libStore.UpsertVMAFScore(ctx, library.VMAFScore{
		CandidatePath:      candidatePath,
		CandidateFileSize:  size,
		CandidateFileMTime: mtime,
		ReferencePath:      referencePath,
		Score:              score,
		ComputedAt:         time.Now().UTC().Format(time.RFC3339Nano),
	})
}
