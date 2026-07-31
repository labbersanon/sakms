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
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// MaxBatchGrabItems bounds one POST /api/autograb-batch request. It counts
// SUBMITTED items — the flattened batch entries — NOT Discover cards: a
// season-expanded series contributes one item per selected season, so selecting
// 15 seasons of one show counts as 15 toward this cap. The cap bounds the number
// of *live acquisitions fired* (each item is its own Prowlarr search + potential
// download-client add), which is what matters for indexer/download load — not
// how many cards the operator clicked. It is deliberately far below apply-batch's
// 200: apply-batch commits already-searched local file operations, whereas each
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
func autoGrabBatchHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store) http.HandlerFunc {
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
				built, err := mode.Build(ctx, connStore, settingsStore, httpClient, dl, m)
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

	downloadClient, gid, _, err := dispatchToDownloadClient(ctx, sess, m, nzb, string(picked.Protocol), picked.DownloadURL, picked.Title)
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
