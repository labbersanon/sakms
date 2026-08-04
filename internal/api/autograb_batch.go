package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// MaxBatchGrabItems bounds one POST /api/autograb-batch request. It counts
// SUBMITTED items — the flattened batch entries the frontend builds — NOT
// Discover cards: a flattened item is a whole season OR a single episode, so
// a season-expanded series contributes one item per selected season, and an
// episode-level selection (season-episode-picker-redesign, 2026-08-02)
// contributes one item per selected episode — 15 episodes of one show counts
// as 15 toward this cap, exactly as 15 seasons would. (AMENDED 2026-08-02:
// this comment previously read "a season-expanded series contributes one item
// per selected season" because no episode-level bulk selection existed yet.
// THE GUARD ITSELF IS UNCHANGED — len(req.Items) already counted flattened
// items correctly, and apidto.AutoGrabRequest already carried EpisodeNumber;
// only what the frontend can put in that slice grew.) The cap bounds the
// number of *live acquisitions fired* (each item is its own Prowlarr search +
// potential download-client add), which is what matters for indexer/download
// load — not how many cards the operator clicked, and an episode grab costs
// exactly what a season grab costs, so nothing about this granularity argues
// for a different cap value. It is deliberately far below apply-batch's 200:
// apply-batch commits already-searched local file operations, whereas each
// item here fires a live indexer search.
const MaxBatchGrabItems = 20

// autoGrabBatchHandler is Discover's bounded multi-select auto-grab — a
// deliberate, user-approved exception to the single-grab "one release per click,
// never a batch" invariant (see the AutoGrabBatch* DTO doc comments and
// autograb.go's autoGrabHandler, whose single-item invariant and tests are
// unchanged). It mirrors applyBatchHandler's SKELETON only (decode →
// empty/cap guard → per-mode-cached sequential loop → always-200 with per-item
// results) but runs the SINGLE autograb pipeline (autoGrabSearch →
// buildAutoGrabCandidates → autograb.Select → dispatch+record, via
// grabOneBatchItem) inside the loop, not apply-batch's Apply path. It fires no
// player notify and no webhook: an auto-grab notifies downstream players at
// IMPORT time (checkImportHandler), never at grab time — same as the single
// endpoint, so apply-batch's changesByMode/NotifyPlayers machinery is
// deliberately absent.
//
// Load-bearing safety property (hard blocker #1): the loop is SEQUENTIAL. At
// most one Prowlarr search is ever in flight across the whole batch — never a
// goroutine fan-out — so a bulk grab can never recreate the "hundreds of
// concurrent indexer queries" pattern that got the old per-card Discover
// availability badge permanently banned. A batch larger than MaxBatchGrabItems
// is rejected BEFORE the loop (hard blocker #2), so no search fires at all for
// an over-cap request (sessions are built lazily inside the loop).
//
// Skip-and-continue: one item's failure (unconfigured service, unknown mode,
// search error, ...) is recorded as that item's Error and the loop moves on —
// the batch never aborts, and the response is always 200. Partial-batch
// durability (known, accepted property): each qualified item is persisted the
// instant it grabs (grabsStore.Create per item, same as the single path), so if
// the server dies mid-batch, already-grabbed items survive in /grabs and
// /downloads; only the client's in-progress view of the run is lost. No rollback
// of a partially-completed batch is attempted or wanted — consistent with
// apply-batch's per-item commit model.
func autoGrabBatchHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var req apidto.AutoGrabBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// Both guards fire BEFORE any session build or Prowlarr search — an empty
		// or over-cap batch must never touch an indexer (hard blocker #2). Empty
		// mirrors applyBatchHandler's empty-batch 400.
		if len(req.Items) == 0 {
			http.Error(w, "items must not be empty", http.StatusBadRequest)
			return
		}
		if len(req.Items) > MaxBatchGrabItems {
			http.Error(w, fmt.Sprintf("too many items: %d exceeds the %d-item batch cap", len(req.Items), MaxBatchGrabItems), http.StatusBadRequest)
			return
		}
		if err := rejectSeasonEpisodeOverlap(req.Items); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// One session per distinct mode (apply-batch's cache pattern). Built with
		// dl (NOT nil, unlike apply-batch): the grab path dispatches to the
		// download client, whose torrent branch hard-fails on a nil Downloader.
		// mode.Build also validates the mode string (unknown → error), which
		// becomes this item's per-item Error rather than a silent misroute.
		sessions := make(map[mode.Mode]*mode.Session)
		results := make([]apidto.AutoGrabBatchResult, 0, len(req.Items))

		var grabbed, fellBack, alreadyGrabbing, errored int
		for i, item := range req.Items {
			m := mode.Mode(item.Mode)
			label := strings.TrimSpace(item.Request.Title)
			res := apidto.AutoGrabBatchResult{Index: i, Mode: item.Mode, Label: label}

			fail := func(msg string) {
				res.Error = msg
				results = append(results, res)
				errored++
				log.Printf("autoGrabBatch: item=%d mode=%q title=%q outcome=error err=%q", i, item.Mode, label, msg)
			}

			if label == "" {
				fail("title is required")
				continue
			}

			sess, ok := sessions[m]
			if !ok {
				built, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, m)
				if err != nil {
					fail(err.Error())
					continue
				}
				sessions[m] = built
				sess = built
			}

			// The same per-item preflight the single endpoint enforces, converted
			// to per-item errors (never an abort). Both guards are RELAXED for a
			// direct-grab item (one carrying its own enclosure URL): it dispatches
			// straight to the download client and needs neither Prowlarr nor TMDB,
			// so a Prowlarr-less install can grab feed items in bulk exactly as it
			// can singly (Low finding — single/bulk parity). autoGrabSearch
			// dereferences sess.Prowlarr.Search unguarded, so a nil-Prowlarr
			// SEARCH item without the first guard would still panic — but
			// grabOneBatchItem takes the direct path before autoGrabSearch for a
			// DownloadURL-bearing item.
			directGrab := strings.TrimSpace(item.Request.DownloadURL) != ""
			if sess.Prowlarr == nil && !directGrab {
				fail("prowlarr isn't configured yet — add it in Settings first")
				continue
			}
			if m != mode.Adult && sess.TMDB == nil && !directGrab {
				fail("tmdb isn't configured yet — add it in Settings first")
				continue
			}

			grab, fallback, itemAlreadyGrabbing, candidates, message, err := grabOneBatchItem(ctx, sess, m, settingsStore, nzb, grabsStore, item.Request)
			switch {
			case err != nil:
				fail(err.Error())
			case fallback:
				res.Fallback = true
				res.Candidates = candidates
				res.Message = message
				results = append(results, res)
				fellBack++
				log.Printf("autoGrabBatch: item=%d mode=%q title=%q outcome=fallback candidates=%d", i, item.Mode, label, len(candidates))
			case itemAlreadyGrabbing:
				// Distinct from Grabbed: the idempotency guard found an in-flight
				// grab for this exact download, so no new row was recorded. Not
				// counted as a fresh grab (three-state-honesty: never fold one
				// outcome into another).
				res.AlreadyGrabbing = true
				res.Grab = grab
				res.Message = message
				results = append(results, res)
				alreadyGrabbing++
				log.Printf("autoGrabBatch: item=%d mode=%q title=%q outcome=already_grabbing", i, item.Mode, label)
			default:
				res.Grabbed = true
				res.Grab = grab
				res.Message = message
				results = append(results, res)
				grabbed++
				log.Printf("autoGrabBatch: item=%d mode=%q title=%q outcome=grabbed indexer=%q", i, item.Mode, label, grab.Indexer)
			}
		}

		log.Printf("autoGrabBatch summary: requested=%d grabbed=%d fell_back=%d already_grabbing=%d errored=%d", len(req.Items), grabbed, fellBack, alreadyGrabbing, errored)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apidto.AutoGrabBatchResponse{Results: results})
	}
}

// rejectSeasonEpisodeOverlap refuses a batch that contains BOTH a whole-season
// item and a single-episode item for that same season of that same title.
// Dispatching the pair grabs a season pack plus one episode already inside it —
// two genuinely different releases, so activeGrabForGID's download-client GID
// dedup cannot see the conflict, and the result is a duplicate on disk. That is
// a direct hit on the mission's "no duplicates" bar.
//
// Claude 2026-08-02: DEFENSE IN DEPTH ONLY — this is not the primary
// enforcement and must not be mistaken for it. Mainstream.tsx's
// SeriesSeasonSelect.toggle already makes the combination unconstructible
// through the UI (selecting a season clears that season's episode keys and vice
// versa, against the shared selection store rather than card-local state). This
// guard exists because that check is CLIENT-side, and POST /api/autograb-batch
// is reachable from the X-Api-Key out-of-process surface by any authenticated
// caller, which never runs it.
// Reason: rejects the whole batch rather than skipping the offending items —
// the operator asked for a specific set, and silently grabbing a subset of it
// is the kind of partial success this codebase's three-state-honesty convention
// exists to prevent. Fires BEFORE the loop, like the empty and cap guards, so a
// rejected batch touches no indexer.
// Troubleshooting: Phase-4 review FIX 6 (security-reviewer, defense in depth).
// Review if: whole-series selection ships, which would need its own rule here.
//
// TMDBID <= 0 is skipped deliberately: a direct-grab item (an Adult feed
// enclosure) carries no TMDB id, so several unrelated ones would all collide on
// (mode, 0, 0) and produce a false 400 on exactly the Prowlarr-less install the
// handler goes out of its way to keep working. A Series episode always carries
// a real id — SeriesSeasonSelect's targetFor sets it — so nothing this guard
// targets is lost.
func rejectSeasonEpisodeOverlap(items []apidto.AutoGrabBatchItem) error {
	type seasonKey struct {
		mode   string
		tmdbID int
		season int
	}
	wholeSeason := make(map[seasonKey]bool)
	episodeOf := make(map[seasonKey]int)
	for _, it := range items {
		if it.Request.TMDBID <= 0 {
			continue
		}
		k := seasonKey{mode: it.Mode, tmdbID: it.Request.TMDBID, season: it.Request.SeasonNumber}
		switch {
		case it.Request.EpisodeNumber > 0:
			episodeOf[k] = it.Request.EpisodeNumber
		case it.Request.SeasonSpecified:
			wholeSeason[k] = true
		}
	}
	for k := range wholeSeason {
		if ep, ok := episodeOf[k]; ok {
			return fmt.Errorf(
				"batch contains both the whole season and episode %d of %s season %d (tmdbId %d) — grabbing a season pack alongside an episode inside it lands a duplicate on disk; pick one or the other",
				ep, k.mode, k.season, k.tmdbID)
		}
	}
	return nil
}

// grabOneBatchItem runs the SAME pipeline as autoGrabHandler for one batch item
// — autoGrabSearch → buildAutoGrabCandidates → autograb.Select, then either
// dispatch+record the single top qualifier or return the ranked fallback pick
// list. It shares every building block with the single endpoint (nothing is
// re-implemented here), returning a three-state outcome — a grab, a fallback
// candidate list, or an error — instead of writing HTTP, so the batch loop can
// record it per item. Like the single handler, exactly one release is grabbed
// per successful item: this is still a one-release grab, run once per selected
// item.
//
// A DownloadURL-bearing item (an Adult feed enclosure) takes the direct-grab
// path first — shared with the single handler via grabDirectEnclosure — before
// autoGrabSearch (which dereferences sess.Prowlarr), so it neither searches nor
// requires Prowlarr (C1/D4). For every OTHER item the precondition still holds:
// callers must have confirmed sess.Prowlarr (and, for non-Adult, sess.TMDB) is
// non-nil.
func grabOneBatchItem(ctx context.Context, sess *mode.Session, m mode.Mode, settingsStore *settings.Store, nzb *usenet.Manager, grabsStore *grabs.Store, req apidto.AutoGrabRequest) (grab *apidto.Grab, fallback bool, alreadyGrabbing bool, candidates []apidto.AutoGrabCandidate, message string, err error) {
	// Direct-grab (C1/D4): dispatch the item's own enclosure straight to the
	// download client, identical to the single handler's path — no Prowlarr.
	if strings.TrimSpace(req.DownloadURL) != "" {
		dto, already, _, err := grabDirectEnclosure(ctx, sess, m, settingsStore, nzb, grabsStore, req)
		if err != nil {
			return nil, false, false, nil, "", err
		}
		if already {
			return dto, false, true, nil, "already grabbing this release", nil
		}
		return dto, false, false, nil, "grabbed " + req.Title, nil
	}

	releases, runtimeSeconds, err := autoGrabSearch(ctx, sess, m, req)
	if err != nil {
		return nil, false, false, nil, "", err
	}

	// Claude 2026-08-03: apply FilterSeasonScope on the batch path too.
	// Reason: grabOneBatchItem reimplements score-and-dispatch rather than
	// delegating to RunAutoGrab, so wiring the filter only into RunAutoGrab
	// left Discover bulk select (BulkBar → autoGrabBatch) unfiltered — the
	// same UI surface that can pick Season 4 across many cards. Architect
	// review rejected the first pass for exactly this gap.
	// Troubleshooting: if a bulk Series grab still dispatches a wrong-season
	// release, check this call before assuming the brace wire format failed.
	// Review if: grabOneBatchItem is refactored to delegate to RunAutoGrab
	// (then this copy becomes dead and should be removed with that change).
	if m == mode.Series {
		releases = FilterSeasonScope(releases, req.SeasonNumber, req.EpisodeNumber, req.SeasonSpecified)
	}

	neutralizeSeasonPacks := m == mode.Series && runtimeSeconds > 0
	cands := buildAutoGrabCandidates(releases, runtimeSeconds, neutralizeSeasonPacks)
	sel := autograb.Select(cands, autoGrabTier(ctx, settingsStore, m), minSeedersFor(m))

	// Fallback: nothing cleared the floor → hand back the ranked pick list, no
	// grab attempted (never "grab the least-bad option").
	if sel.Fallback {
		return nil, true, false, rankedAutoGrabCandidates(sel, releases), "nothing cleared the quality floor automatically — pick one below", nil
	}

	rootFolder, err := autoGrabRootFolder(ctx, settingsStore, m)
	if err != nil {
		return nil, false, false, nil, "", err
	}
	picked := releases[sel.PickIndex]

	downloadClient, gid, _, err := dispatchToDownloadClient(ctx, settingsStore, sess, m, nzb, string(picked.Protocol), picked.DownloadURL, picked.Title)
	if err != nil {
		return nil, false, false, nil, "", err
	}

	// Idempotency guard: a repeat grab of the same release comes back with the
	// SAME gid (the download client dedupes by infohash) — report the in-flight
	// grab as its own distinct outcome instead of recording a duplicate row.
	if existing, dup, _, err := activeGrabForGID(ctx, grabsStore, m, gid); dup || err != nil {
		if err != nil {
			return nil, false, false, nil, "", err
		}
		return existing, false, true, nil, "already grabbing this release", nil
	}

	created, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: m, Title: req.Title, TMDBID: req.TMDBID,
		SeasonNumber: req.SeasonNumber, EpisodeNumber: req.EpisodeNumber, SeasonSpecified: req.SeasonSpecified,
		Indexer: picked.Indexer, Protocol: string(picked.Protocol),
		DownloadClient: downloadClient, RootFolderPath: rootFolder,
	})
	if err != nil {
		return nil, false, false, nil, "", err
	}
	if gid != "" {
		if err := grabsStore.SetDownloadGID(ctx, created.ID, gid); err != nil {
			return nil, false, false, nil, "", err
		}
		created.DownloadGID = gid
	}

	dto := toDTOGrab(created)
	return &dto, false, false, nil, "auto-grabbed " + picked.Title, nil
}
