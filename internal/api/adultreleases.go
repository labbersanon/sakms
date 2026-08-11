package api

// Claude 2026-08-11: Adult release cache resolver — adultSceneKey, adultIdentityWeak,
// resolveAdultReleases, pickPersistedAdultEnclosure.
// Reason: "search once, persist, reuse" — cache-first candidate sourcing separates
// candidate sourcing from candidate gating (§5.1 of the adult-release-persistence plan).
// Troubleshooting: if DetailPopup always searches Prowlarr, check HIT/MISS log lines;
// a perpetual MISS means no rows are linked to the derived scene key.
// Review if: a non-Adult mode ever needs release persistence.

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/settings"
)

// adultConsumer decides the confidence-gate posture for resolveAdultReleases.
// The ONLY human-reviewed consumer in the codebase is DetailPopup's picker grid;
// every RunAutoGrab-driven path and the bulk batch are unattended (spec §Goal.5).
type adultConsumer int

const (
	// adultConsumerPicker is DetailPopup — no title gate, ever.
	// discover_availability.go already runs FilterReleases for the grid.
	adultConsumerPicker adultConsumer = iota
	// adultConsumerUnattended is batch + every RunAutoGrab trigger.
	// resolveAdultReleases applies a deterministic title match before returning.
	adultConsumerUnattended
)

// adultResolution is what resolveAdultReleases returns.
type adultResolution struct {
	Releases  []prowlarr.Release
	FromCache bool // true ⇒ ZERO Prowlarr calls were made
}

// adultSceneKey derives a stable, lookup-safe key for the given scene.
// Prefers box:sceneId when both are present (catalog-sourced cards); falls
// back to "title:<normalized>" for Show-More-sourced and legacy items that
// carry no catalog id, via normalizeAdultQuery so the key tolerates the same
// punctuation variance the Prowlarr query already accepts.
// Accepted limitation: two genuinely distinct scenes with the same normalized
// title share a fallback key — the same collision class dedupeAdultShowMoreItems
// already accepts. Catalog-sourced scenes never hit the fallback.
func adultSceneKey(box, sceneID, title string) string {
	if box != "" && sceneID != "" {
		return box + ":" + sceneID
	}
	return "title:" + strings.ToLower(normalizeAdultQuery(title))
}

// adultIdentityWeak reports whether a release's identity signals are too thin
// for SILENT unattended dispatch. Weak iff:
//
//	(a) studio is empty AND performers is empty (nothing to check against), OR
//	(b) neither the studio nor ANY performer name appears in the release's
//	    own title at titleSimilarityFloor.
//
// "appears in" uses identify.TitleSimilarity — the same deterministic check
// FilterReleases' fast path uses. What changed is the CONSEQUENCE: this NEVER
// drops a release. It only routes an unattended dispatch to staged-for-approval
// instead of the download client.
func adultIdentityWeak(studio string, performers []string, releaseTitle string) bool {
	if studio == "" && len(performers) == 0 {
		return true
	}
	if studio != "" {
		if identify.TitleSimilarity(studio, releaseTitle) >= titleSimilarityFloor ||
			singleWordTitleMatches(studio, releaseTitle) {
			return false
		}
	}
	for _, p := range performers {
		if p == "" {
			continue
		}
		if identify.TitleSimilarity(p, releaseTitle) >= titleSimilarityFloor ||
			singleWordTitleMatches(p, releaseTitle) {
			return false
		}
	}
	return true
}

// resolveAdultReleases is the single funnel for "what candidates exist for this
// Adult scene". Cache-first: if the scene has at least ONE persisted release
// still inside its own protocol window, those are returned and Prowlarr is NOT
// called. Otherwise one live Prowlarr search runs, its RAW result is persisted
// and linked to the scene, and the raw list is returned.
//
// A nil store, or any store error, degrades to the live path — persistence is
// never allowed to block a grab.
func resolveAdultReleases(ctx context.Context, sess *mode.Session, store *adultnewest.ReleaseStore, consumer adultConsumer, req apidto.AutoGrabRequest) (adultResolution, error) {
	key := adultSceneKey(req.Box, req.SceneID, req.Title)

	if store != nil {
		fresh, err := store.FreshReleasesForScene(ctx, key, time.Now())
		if err != nil {
			log.Printf("adult release cache: store error for scene=%q — falling back to live search: %v", key, err)
		} else if len(fresh) > 0 {
			out := filterForConsumer(fresh, consumer, req.Title)
			// Claude 2026-08-11: empty-after-filter is a MISS, not a HIT.
			// Reason: a Show More page can leave only title-mismatched links, or
			// the unattended title gate can empty the set — returning FromCache
			// with zero candidates would freeze an empty grid/dispatch for the
			// whole protocol window with no force-refresh (code-review HIGH/MEDIUM).
			// Troubleshooting: HIT with candidates=0 after filter means a bug; we
			// fall through to live search instead.
			// Review if: DetailPopup gains an explicit force-refresh that can
			// invalidate links without a live miss path.
			if len(out) > 0 {
				log.Printf("adult release cache: HIT scene=%q candidates=%d (after consumer filter: %d) — no Prowlarr search", key, len(fresh), len(out))
				return adultResolution{Releases: out, FromCache: true}, nil
			}
			log.Printf("adult release cache: STALE-HIT scene=%q raw=%d usable=0 — falling through to live search", key, len(fresh))
		}
	}

	// Cache miss: one live Prowlarr title-only search.
	query := normalizeAdultQuery(strings.TrimSpace(req.Title))
	releases, err := sess.Prowlarr.Search(ctx, query, []int{adultAutoGrabCategory})
	if err != nil {
		return adultResolution{}, err
	}

	// Persist the raw result, best-effort.
	if store != nil {
		if perr := store.PersistReleases(ctx, query, releases, []string{key}); perr != nil {
			log.Printf("adult release cache: persisting raw releases for scene=%q (non-fatal): %v", key, perr)
		}
	}

	out := filterForConsumer(releases, consumer, req.Title)
	log.Printf("adult release cache: MISS scene=%q — live Prowlarr search returned %d raw releases (after consumer filter: %d)", key, len(releases), len(out))
	return adultResolution{Releases: out, FromCache: false}, nil
}

// filterForConsumer applies the consumer-appropriate title gate.
// adultConsumerPicker returns the raw list unchanged — discover_availability.go
// already runs FilterReleases for the grid. adultConsumerUnattended applies the
// same deterministic predicate FilterReleases uses (amendment A1 / critique C-1:
// restores the title-match safety net aiConfirmAdultReleases used to provide on
// the unattended path, without AI and without dropping releases for the picker).
func filterForConsumer(releases []prowlarr.Release, consumer adultConsumer, targetTitle string) []prowlarr.Release {
	if consumer == adultConsumerPicker {
		return releases
	}
	out := make([]prowlarr.Release, 0, len(releases))
	for _, rel := range releases {
		if identify.TitleSimilarity(targetTitle, rel.Title) >= titleSimilarityFloor ||
			singleWordTitleMatches(targetTitle, rel.Title) {
			out = append(out, rel)
		}
	}
	return out
}

// pickPersistedAdultEnclosure resolves the ONE persisted, still-fresh candidate
// an unattended Adult grab should dispatch, or ok=false when the scene has no
// fresh persisted candidate after title filtering (caller then falls through to
// the normal search path).
//
// It picks with the SAME scorer every other grab path uses — buildAutoGrabCandidates
// + autograb.Select at the mode's configured tier and minSeedersFor(mode.Adult) —
// so "served from cache" never means "skipped the quality floor".
//
// Amendment A6: identity weakness is deliberately NOT handled here — callers run
// adultIdentityWeak on the returned release and route it to staging.
func pickPersistedAdultEnclosure(ctx context.Context, store *adultnewest.ReleaseStore, settingsStore *settings.Store, req apidto.AutoGrabRequest) (rel prowlarr.Release, ok bool) {
	if store == nil {
		return prowlarr.Release{}, false
	}
	key := adultSceneKey(req.Box, req.SceneID, req.Title)
	fresh, err := store.FreshReleasesForScene(ctx, key, time.Now())
	if err != nil {
		log.Printf("pickPersistedAdultEnclosure: store error for scene=%q: %v", key, err)
		return prowlarr.Release{}, false
	}

	// Title-filter before scoring: the persisted list is raw (locked decision 2A),
	// so a title-mismatching release must not win the scorer just because it
	// happens to be the highest bitrate.
	filtered := filterForConsumer(fresh, adultConsumerUnattended, req.Title)
	if len(filtered) == 0 {
		return prowlarr.Release{}, false
	}

	cands := buildAutoGrabCandidates(filtered, float64(req.DurationSeconds), false)
	sel := autograb.Select(cands, autoGrabTier(ctx, settingsStore, mode.Adult), minSeedersFor(mode.Adult))
	if sel.Fallback {
		return prowlarr.Release{}, false
	}
	return filtered[sel.PickIndex], true
}
