package main

// scanAdapter is the ONE production implementation of scanschedule.Scanner.
//
// DELIBERATE EXCEPTION (same register as internal/recheck's package doc and
// internal/scanschedule's): the scanschedule.Scanner interface it satisfies is
// not an abstraction for polymorphism — it exists solely as a compile-time
// safety boundary so internal/scanschedule can NEVER reach an Apply-family call
// (see that package's doc). This adapter is the single place that residual risk
// lives, because it legitimately imports dedup/rename/purge to call their
// ScanLibrary* functions AND proposals to persist via ReplacePending — exactly
// the surface where an Apply call COULD be wired in by mistake. It is therefore
// deliberately isolated in this one small file (not inline among main.go's many
// other imports) so the static allowlist test in
// internal/scanschedule/allowlist_test.go can scan it in full and fail on any
// reference to a mutating dedup/rename/purge/proposals symbol other than the
// Scan*-family functions and ReplacePending. Keep this file's dedup/rename/
// purge/proposals usage to exactly that: Scan and ReplacePending, nothing else.
// If you find yourself reaching for Apply, Repick, Dismiss, a fingerprint- or
// draft-submit, or any other mutator here, stop — that is precisely the bug
// this whole design exists to make impossible.

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/labbersanon/sakms/internal/api"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/nodes"
	"github.com/labbersanon/sakms/internal/parseentity"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/pruning"
	"github.com/labbersanon/sakms/internal/purge"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/scanschedule"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/vmaf"
)

// scanAdapter holds exactly the collaborators the manual scan handlers use, so
// a scheduled Scan is byte-for-byte the same propose-phase as a manual one.
// prober/phashHasher/videoHasher are stored as their CONCRETE types (never the
// dedup.Prober/dedup.PHasher/rename.PHasher interface names) so this file never
// references a dedup/rename symbol outside the Scan*-family — the interfaces are
// satisfied structurally at each Scan call.
type scanAdapter struct {
	httpClient    *http.Client
	connStore     *connections.Store
	scStore       *serviceconn.Store
	settingsStore *settings.Store
	propStore     *proposals.Store
	libStore      *library.Store
	// Claude 2026-08-03: added pruningStore (B4, plan §3.3).
	// Reason: ScanPurge loads the mode's enabled pruning rules and passes them
	// to the same purge.Scan* functions the manual handler calls, so a
	// scheduled Purge scan proposes exactly what a manual one does.
	// Troubleshooting: internal/pruning is NOT in allowlist_test.go's
	// restrictedPkgs (dedup/rename/purge/proposals only), so calling
	// ListEnabledForMode from this file is intentionally outside that scan's
	// scope. Do not "helpfully" move this load into internal/purge as a new
	// exported function — that is exactly what the allowlist would reject.
	pruningStore *pruning.Store
	prober       *mediainfo.Prober
	phashHasher  *nodes.Dispatcher // dedup's video perceptual hasher (all modes)
	videoHasher  *nodes.Dispatcher // rename Adult's StashDB-compatible hasher
	entityStore  parseentity.EntityStore
}

// newScanAdapter wires the scheduler's Scanner from the same stores main.go
// already constructed for NewMux.
func newScanAdapter(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, pruningStore *pruning.Store, prober *mediainfo.Prober, phashHasher, videoHasher *nodes.Dispatcher, entityStore parseentity.EntityStore) *scanAdapter {
	return &scanAdapter{
		httpClient:    httpClient,
		connStore:     connStore,
		scStore:       scStore,
		settingsStore: settingsStore,
		propStore:     propStore,
		libStore:      libStore,
		pruningStore:  pruningStore,
		prober:        prober,
		phashHasher:   phashHasher,
		videoHasher:   videoHasher,
		entityStore:   entityStore,
	}
}

// Compile-time proof the adapter satisfies the Scan-only contract.
var _ scanschedule.Scanner = (*scanAdapter)(nil)

// rootFolder reads m's configured library root path, or "" when unset (a mode
// with no root folder configured is simply skipped by the callers, same as the
// watch-folder scanner does).
func (a *scanAdapter) rootFolder(ctx context.Context, m mode.Mode) (string, error) {
	key, ok := api.LibraryRootFolderKey(m)
	if !ok {
		return "", nil
	}
	path, err := a.settingsStore.Get(ctx, key)
	if errors.Is(err, settings.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return path, nil
}

// ScanRename runs the Rename propose-phase for m and replaces its Rename queue —
// identical to renameScanHandler / scanFromWatcher, minus the HTTP shell. Never
// Applies.
func (a *scanAdapter) ScanRename(ctx context.Context, m mode.Mode) error {
	sess, err := mode.Build(ctx, a.connStore, a.scStore, a.settingsStore, a.httpClient, nil, m)
	if err != nil {
		return err
	}
	if sess.Identify != nil {
		sess.Identify.EntityStore = a.entityStore
	}

	rootPath, err := a.rootFolder(ctx, m)
	if err != nil {
		return err
	}
	if rootPath == "" {
		return nil // no root folder configured for this mode — nothing to scan
	}

	var found []proposals.Proposal
	switch m {
	case mode.Movies:
		preset, pErr := api.ResolveNamingPreset(ctx, a.settingsStore, m)
		if pErr != nil {
			return pErr
		}
		matchCfg, tErr := api.ResolveMatchConfig(ctx, a.settingsStore, m)
		if tErr != nil {
			return tErr
		}
		found, err = rename.ScanLibrary(ctx, sess, a.libStore, rootPath, preset, matchCfg, a.prober)
	case mode.Series:
		preset, pErr := api.ResolveNamingPreset(ctx, a.settingsStore, m)
		if pErr != nil {
			return pErr
		}
		matchCfg, tErr := api.ResolveMatchConfig(ctx, a.settingsStore, m)
		if tErr != nil {
			return tErr
		}
		found, err = rename.ScanLibrarySeries(ctx, sess, a.libStore, rootPath, preset, matchCfg, a.prober)
	case mode.Adult:
		// Claude 2026-08-12: upgrade local scenes before scanning (D6).
		// Reason: UpgradeLocalAdultScenes must run before ScanLibraryAdult so
		//   upgraded files appear in the known map and are not re-proposed.
		//   Allowlisted in internal/scanschedule/allowlist_test.go — it is a
		//   pre-scan identity upgrade, not a proposal Apply. Soft-fail.
		// Review if: UpgradeLocalAdultScenes is folded into ScanLibraryAdult.
		if _, upgradeErr := rename.UpgradeLocalAdultScenes(ctx, sess, a.libStore, a.prober); upgradeErr != nil {
			log.Printf("scan: upgrade local adult scenes: %v", upgradeErr)
		}
		found, err = rename.ScanLibraryAdult(ctx, sess, a.libStore, a.videoHasher, a.prober, rootPath, "")
	default:
		return nil
	}
	if err != nil {
		return err
	}

	_, err = a.propStore.ReplacePending(ctx, m, proposals.Rename, found)
	return err
}

// ScanPurge runs the Purge propose-phase for m and replaces its Purge queue —
// identical to purgeScanHandler, minus the HTTP shell. Movies/Series read the
// tracked library directly. Adult builds an Identify session only to backfill
// catalog tags onto library_scene_tags (fill-if-empty) before matching; it
// still never Applies.
func (a *scanAdapter) ScanPurge(ctx context.Context, m mode.Mode) error {
	var rules []pruning.Rule
	// `var err error` at FUNCTION scope, deliberately OUTSIDE the if below.
	// Writing `rules, err := ...` inside the block instead re-declares rules in
	// the block's own scope, leaving the outer rules permanently nil — every
	// scheduled Purge scan would then run with an empty rule set and propose
	// nothing. It compiles, it is silent, and no test covers it.
	var err error
	if a.pruningStore != nil {
		rules, err = a.pruningStore.ListEnabledForMode(ctx, m)
		if err != nil {
			return err
		}
	}

	var found []proposals.Proposal
	switch m {
	case mode.Movies:
		found, err = purge.ScanLibrary(ctx, a.libStore, rules)
	case mode.Series:
		found, err = purge.ScanLibrarySeries(ctx, a.libStore, rules)
	case mode.Adult:
		var catalog purge.CatalogTagSource
		sess, berr := mode.Build(ctx, a.connStore, a.scStore, a.settingsStore, a.httpClient, nil, mode.Adult)
		if berr != nil {
			log.Printf("scan: adult identify for purge tags: %v", berr)
		} else if sess != nil {
			catalog = api.NewAdultCatalogTags(sess.Identify)
		}
		found, err = purge.ScanLibraryAdult(ctx, a.libStore, rules, "", catalog)
	default:
		return nil
	}
	if err != nil {
		return err
	}

	_, err = a.propStore.ReplacePending(ctx, m, proposals.Purge, found)
	return err
}

// ScanDedup runs the Dedup propose-phase for m and replaces its Dedup queue —
// identical to dedupScanHandler's background scan, minus the HTTP shell and the
// SSE progress stream (a scheduled cycle passes nil onProgress; the scan
// functions nil-guard it). The Hub concurrency guard is applied by the caller
// (scanschedule.runDedupCycle), not here. Never Applies.
//
// When eagerVMAF is true, after the scan is persisted it eagerly computes VMAF
// scores for each group's non-reference candidates against the group's largest
// on-disk file (star topology, vmaf.ReferenceIndex), so the on-demand view
// path serves a warm cache.
// This eager fan-out runs here (rather than in the scheduler) because it needs
// the in-memory groups the scan just found, which this method's error-only
// return does not surface to the caller. A consequence: the caller's Dedup Hub
// guard (scanschedule.runDedupCycle) stays held across the eager compute too —
// intended, see that function's doc. Eager VMAF touches only the vmaf_scores
// cache, never proposals.
func (a *scanAdapter) ScanDedup(ctx context.Context, m mode.Mode, eagerVMAF bool) error {
	sess, err := mode.Build(ctx, a.connStore, a.scStore, a.settingsStore, a.httpClient, nil, m)
	if err != nil {
		return err
	}

	threshold, err := api.ResolvePHashThreshold(ctx, a.settingsStore, m)
	if err != nil {
		return err
	}

	rootPath, err := a.rootFolder(ctx, m)
	if err != nil {
		return err
	}
	if rootPath == "" {
		return nil // no root folder configured for this mode — nothing to scan
	}
	if m == mode.Adult && sess.Identify == nil {
		// Adult Dedup requires the identify pipeline; unconfigured means there
		// is nothing to scan (the manual handler surfaces this as a 400, but a
		// background cycle simply skips this mode rather than failing).
		return nil
	}

	var found []proposals.Proposal
	switch m {
	case mode.Movies:
		found, err = dedup.ScanLibraryPHash(ctx, sess, a.libStore, rootPath, a.prober, a.phashHasher, threshold, nil)
	case mode.Series:
		found, err = dedup.ScanLibrarySeriesPHash(ctx, sess, a.libStore, rootPath, a.prober, a.phashHasher, threshold, nil)
	case mode.Adult:
		found, err = dedup.ScanLibraryAdult(ctx, sess, a.libStore, rootPath, a.prober, a.phashHasher, threshold, nil, "")
	default:
		return nil
	}
	if err != nil {
		return err
	}

	if _, err := a.propStore.ReplacePending(ctx, m, proposals.Dedup, found); err != nil {
		return err
	}

	if eagerVMAF {
		a.eagerVMAF(ctx, found)
	}
	return nil
}

// eagerVMAF computes and caches a VMAF score for every non-reference candidate
// in every group against that group's VMAF reference — the largest on-disk
// file (internal/vmaf.ReferenceIndex), not the Keep/Apply Winner.
// Star topology matches the on-demand view path. It is best-effort and
// skip-and-continue: a group with no usable reference is skipped, and a
// single pair's compute/cache failure (or a cancelled context) is logged and
// stepped over so one bad file never blocks the rest of the cycle (plan AC7).
// Concurrency is bounded by internal/vmaf's shared semaphore inside
// api.EagerComputeAndCacheVMAF, not here.
func (a *scanAdapter) eagerVMAF(ctx context.Context, groups []proposals.Proposal) {
	for _, g := range groups {
		if ctx.Err() != nil {
			return
		}
		// Claude 2026-08-17: VMAF reference is largest file, not Winner.
		// Reason: keep protocol (place.QualityKey) stays independent of VMAF
		// comparison (deep-interview-dedup-vmaf-schedule).
		// Troubleshooting: if Dedup badges miss the scheduled cache, the UI is
		// still sending Keep-primary as referenceIndex.
		// Review if: VMAF reference is changed back to Winner.
		refIdx := vmaf.ReferenceIndex(g.Candidates)
		if refIdx < 0 || refIdx >= len(g.Candidates) {
			log.Printf("scanschedule: eager vmaf: group %q has no VMAF reference — skipping", g.SourceName)
			continue
		}
		reference := g.Candidates[refIdx].Path
		if reference == "" {
			log.Printf("scanschedule: eager vmaf: group %q has no VMAF reference — skipping", g.SourceName)
			continue
		}
		for i, c := range g.Candidates {
			if ctx.Err() != nil {
				return
			}
			if i == refIdx || c.Path == reference {
				continue // the VMAF reference is never scored against itself
			}
			if err := api.EagerComputeAndCacheVMAF(ctx, a.libStore, c.Path, reference); err != nil {
				log.Printf("scanschedule: eager vmaf %s vs %s: %v", c.Path, reference, err)
				continue
			}
		}
	}
}
