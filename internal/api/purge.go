package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/pruning"
	"github.com/labbersanon/sakms/internal/purge"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

func adultPurgeScanContext(parent context.Context) context.Context {
	// WithoutCancel: a proxy/client abort must not cancel ReplacePending after
	// tags were already written (live 499 left the queue empty).
	return context.WithoutCancel(parent)
}

// identifyCatalogTags adapts *identify.Identifier to purge.CatalogTagSource
// so Adult Clean-up can fill-if-empty library_scene_tags from the catalog.
type identifyCatalogTags struct {
	id *identify.Identifier
}

// NewAdultCatalogTags wraps id for purge.ScanLibraryAdult. Nil id returns nil
// (skip backfill) so tests and a missing Adult identify config stay fail-closed.
func NewAdultCatalogTags(id *identify.Identifier) purge.CatalogTagSource {
	if id == nil {
		return nil
	}
	return identifyCatalogTags{id: id}
}

func (t identifyCatalogTags) TagsByPHash(ctx context.Context, phashes []string) (map[string]purge.CatalogTagHit, error) {
	matches, err := t.id.LookupFingerprints(ctx, phashes)
	if err != nil {
		return nil, err
	}
	out := make(map[string]purge.CatalogTagHit, len(matches))
	for ph, m := range matches {
		if m == nil {
			continue
		}
		out[ph] = purge.CatalogTagHit{Box: m.Box, SceneID: m.SceneID, Tags: library.SplitCatalogTags(m.Tags)}
	}
	return out, nil
}

func (t identifyCatalogTags) TagsByID(ctx context.Context, box, sceneID string) ([]string, error) {
	if t.id.Boxes == nil {
		return nil, nil
	}
	// Claude 2026-08-14: do NOT Wait on Identify.Throttle here.
	// Reason: Adult Clean-up backfill is one FindScene per untagged scene.
	//   Throttle is 1s/host (identify's interactive cascade). 250 leftovers
	//   took ~5 min; VPS nginx proxy_read_timeout is 300s, so the scan 504'd
	//   and Traefik canceled the request (499) before ReplacePending.
	// Troubleshooting: POST /api/modes/adult/purge/scan returned nginx 504
	//   HTML in the Clean-up page; 126/250 scenes were tagged, queue empty.
	// Review if: backfill is a batched GraphQL lookup or runs off the request.
	res, err := t.id.Boxes.ResolveCatalogRef(ctx, box, sceneID, false)
	if err != nil || res == nil {
		return nil, err
	}
	return library.SplitCatalogTags(res.Tags), nil
}

func adultPurgeCatalogTags(ctx context.Context, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) purge.CatalogTagSource {
	if connStore == nil {
		return nil
	}
	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Adult)
	if err != nil || sess == nil {
		return nil
	}
	return NewAdultCatalogTags(sess.Identify)
}

// purgeScanHandler runs the Purge workflow's propose-phase for {mode}: loads
// that mode's enabled pruning rules, evaluates every tracked item against
// them, and replaces the live Purge queue with whatever matched.
// Every mode dispatches to a library-backed sibling now (libStore, no *arr
// app involved): Movies/Series to purge.ScanLibrary/ScanLibrarySeries, Adult
// to purge.ScanLibraryAdult (Whisparr eliminated, Stage 4).
//
// Claude 2026-08-11: the global per-mode tag allowlist load is gone; rules are
// now the single matching mechanism, with tags a fourth AND'd per-rule
// condition (plan
// .omc/plans/autopilot-impl-purge-rules-consolidation-cleanup-rename.md §5.1).
// Reason: the rules are loaded HERE and passed down as a parameter so
// cmd/sakms/scanadapter.go's scheduled ScanPurge runs the byte-identical
// propose logic without needing a new exported purge symbol (which
// internal/scanschedule's allowlist test would reject — see internal/purge's
// own header comment).
// Troubleshooting: pruningStore MAY BE NIL in tests that don't exercise rules;
// a nil store means "no rules," not a panic — and with the allowlist retired,
// no rules now means no proposals at all.
// Review if: Purge ever gains a second matching mechanism alongside rules.
// Claude 2026-08-14: Adult scan builds Identify to backfill catalog tags.
// Reason: library_scene_tags was empty for every identified scene; tags-only
//
//	Clean-up rules scanned 0 hits. Build failure is skip-backfill, not 502.
//
// Review if: grab-import always persists tags and a one-shot backfill has run.
func purgeScanHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, pruningStore *pruning.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		aspect, aerr := parseAdultAspectQuery(r)
		if aerr != nil {
			http.Error(w, aerr.Error(), http.StatusBadRequest)
			return
		}

		var rules []pruning.Rule
		var err error
		if pruningStore != nil {
			rules, err = pruningStore.ListEnabledForMode(ctx, m)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		var found []proposals.Proposal
		switch m {
		case mode.Movies:
			found, err = purge.ScanLibrary(ctx, libStore, rules)
		case mode.Series:
			found, err = purge.ScanLibrarySeries(ctx, libStore, rules)
		case mode.Adult:
			ctx = adultPurgeScanContext(ctx)
			found, err = purge.ScanLibraryAdult(ctx, libStore, rules, aspect, adultPurgeCatalogTags(ctx, httpClient, connStore, scStore, settingsStore))
		default:
			http.Error(w, fmt.Sprintf("unknown mode %q", m), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		saved, err := propStore.ReplacePending(ctx, m, proposals.Purge, found)
		if err != nil {
			if errors.Is(err, proposals.ErrReplaceDeferred) {
				http.Error(w, "scan deferred: an apply is currently in flight — retry after the apply completes", http.StatusServiceUnavailable)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(saved)
	}
}

// Claude 2026-08-14: previous purgeScanHandler (no scStore, no catalog tag
// backfill) is replaced by the signature above. Commented out rather than
// deleted.
// Reason: Adult Clean-up tags-only rules scanned 0 hits because
//   library_scene_tags was never filled from the catalog.
// Review if: grab-import always persists tags and a one-shot backfill has run.
//
// func purgeScanHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, pruningStore *pruning.Store) http.HandlerFunc {
// 	...
// }
