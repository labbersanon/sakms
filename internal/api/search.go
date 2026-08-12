package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/webhooks"
)

// categoriesForSearch restricts a search to m's Newznab category — the
// 2000-range for Movies, the 5000-range for TV, the 6000-range (XXX) for
// Adult (adultAutoGrabCategory, defined in autograb.go and shared here
// rather than a second local constant). Covers both single episodes and
// season packs — Newznab doesn't split those into separate categories.
//
// FIXED 2026-07-15: this previously had no mode.Adult case at all and fell
// through to the Movies default (2000) — so the manual Search screen was
// silently searching Adult under the Movies category the whole time. Found
// while investigating a real "Adult posters/downloads broken" report;
// unrelated to that report's root causes (see internal/imageproxy and the
// detail-popup's discoverAvailabilityHandler, which already used the correct
// category via the separate adultAutoGrabCategory constant — this bug was
// scoped to the manual Search screen only).
func categoriesForSearch(m mode.Mode) []int {
	switch m {
	case mode.Series:
		return []int{5000}
	case mode.Adult:
		return []int{adultAutoGrabCategory}
	default: // Movies
		return []int{2000}
	}
}

// searchHandler queries Prowlarr for {mode} and scores every result against
// that mode's configured quality-prefs (tier + max resolution, defaulting
// to quality.Default/no cap when unset) — a read-only proxy+transform,
// nothing staged or persisted.
//
// UNLESS usenet_autograb_enabled is on. Then this endpoint stops being
// read-only: it hands its already-fetched, already-deduped releases to
// runToggleGatedSearch, which scores them and either dispatches the winner or
// parks a pending_retry row, and answers with an OUTCOME
// (apidto.SearchAutoGrabOutcome) instead of a pick-a-release list — there is
// nothing left to pick once the toggle has picked. This route is the one place
// a Usenet search happens BEFORE a pick is made, which is why the gated
// TriggerRequest hooks here and not in grabHandler (which already holds the
// operator's chosen release) or in dispatchToDownloadClient (which RunAutoGrab
// itself calls, so a hook there would recurse).
//
// Movies and Series only: GET /api/modes/adult/search is served by the
// concrete-path adultSearchHandler, which reaches the SAME shared
// runToggleGatedSearch rather than holding a second copy of this branch.
func searchHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store, whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()
		query := r.URL.Query().Get("q")
		if query == "" {
			http.Error(w, "q query parameter is required", http.StatusBadRequest)
			return
		}

		// Read the toggle once, first. It only chooses a RESPONSE SHAPE here;
		// the gate itself is enforced inside RunAutoGrab (same constant, so the
		// two reads cannot drift).
		autoGrab, err := settingsStore.GetBool(ctx, usenetAutoGrabEnabledKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// dl, not nil: the toggle-ON branch dispatches, and
		// dispatchToDownloadClient rejects a torrent dispatch on a nil
		// Session.Downloader.
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.Prowlarr == nil {
			http.Error(w, "prowlarr isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		releases, err := sess.Prowlarr.Search(ctx, query, categoriesForSearch(m))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		// Collapse duplicate releases (the same release cross-posted to multiple
		// indexers) BEFORE scoring, keeping the highest-seeder survivor of each
		// group. Applies to all modes — the handler is mode-agnostic. Title here
		// is the raw pre-scoring release string, so it carries resolution/source/
		// codec tokens and normalizes safely without collapsing quality variants.
		//
		// normalizeAdultQuery's name is a holdover from its original Adult-only
		// use (internal/api/autograb.go) — reused here deliberately, not by
		// accident, since it already does exactly what dedup needs (strip
		// punctuation, preserve alphanumeric tokens). A future Adult-motivated
		// tweak to it would also change dedup behavior here for Movies/Series.
		releases = dedupeReleases(releases, func(rel prowlarr.Release) releaseDedupKey {
			return releaseDedupKey{
				downloadURL:     rel.DownloadURL,
				normalizedTitle: normalizeAdultQuery(rel.Title),
				seeders:         rel.Seeders,
			}
		})

		// Toggle ON: the scorer runs here instead of the pick list being
		// rendered. Everything below this point (release.ScoreCandidate, the
		// sort, the []SearchReleaseResult encode) is skipped — there is nothing
		// left to render when the toggle has already picked.
		if autoGrab {
			outcome, status, err := runToggleGatedSearch(ctx,
				AutoGrabDeps{SettingsStore: settingsStore, NZB: nzb, GrabsStore: grabsStore, Webhooks: whStore},
				sess, searchGrabIdentity{Mode: m, Title: query}, releases)
			if err != nil {
				http.Error(w, err.Error(), status)
				return
			}
			writeJSON(w, outcome)
			return
		}

		prefs, err := searchQualityProfile(ctx, settingsStore, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		now := time.Now()
		out := make([]apidto.SearchReleaseResult, len(releases))
		for i, rel := range releases {
			info := release.Parse(rel.Title)
			out[i] = apidto.SearchReleaseResult{
				GUID: rel.GUID, Title: rel.Title, Indexer: rel.Indexer,
				Protocol: string(rel.Protocol), Size: rel.Size, Seeders: rel.Seeders,
				DownloadURL: rel.DownloadURL, PublishDate: rel.PublishDate,
				Score: release.ScoreCandidate(release.Candidate{
					Info: info, Protocol: string(rel.Protocol), Seeders: rel.Seeders,
					PublishDate: rel.PublishDate, IndexerFlags: rel.IndexerFlags,
				}, prefs, now),
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// searchQualityProfile loads {mode}'s quality-prefs setting (see
// getQualityPrefsHandler/putQualityPrefsHandler) and maps it to the
// release.Profile Search scores against, defaulting to quality.Default/no
// resolution cap when unset.
func searchQualityProfile(ctx context.Context, settingsStore *settings.Store, m mode.Mode) (release.Profile, error) {
	tierStr, err := settingsStore.Get(ctx, qualityTierKey(m))
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return release.Profile{}, err
	}
	tier := quality.Tier(tierStr)
	if tierStr == "" {
		tier = quality.Default
	}

	maxResStr, err := settingsStore.Get(ctx, maxResolutionKey(m))
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return release.Profile{}, err
	}
	maxRes := 0
	if maxResStr != "" {
		maxRes, _ = strconv.Atoi(maxResStr)
	}

	return quality.ProfileFor(tier, maxRes), nil
}

// searchGrabIdentity is the mode-agnostic identity of the thing being searched
// for — exactly the fields FindPendingRetry's dedup key needs, no more.
//
// TMDBID is 0 at this hook in EVERY mode: both Search routes carry only ?q=.
// That is not a defect of Adult specifically — it means excludes.Key (the same
// normalization requestKey uses) degrades to the lowercased title uniformly, so
// rows created here are title-keyed in all three modes. The general
// (mode, tmdb_id, season, season_specified, episode) key still governs the
// operator and retry paths, which do carry a TMDB id.
type searchGrabIdentity struct {
	Mode            mode.Mode
	Title           string
	TMDBID          int
	Season, Episode int
	SeasonSpecified bool
}

// runToggleGatedSearch is the ONE implementation of the toggle-ON search
// branch, called from searchHandler (Movies/Series) and adultSearchHandler
// (Adult). Neither handler holds a copy of it: one shared function was the
// explicit design decision over two duplicated branches.
//
// It DELEGATES to RunAutoGrab; it does not re-implement it. The toggle gate,
// the autograb.Select call, the dispatchToDownloadClient dispatch and the
// pending_retry write all stay inside RunAutoGrab — anything else would make
// this a SECOND gated scoring-and-dispatch path and falsify the "a future
// trigger can invoke the shared mechanism without duplicating the
// scoring/toggle-gating logic" guarantee. What is genuinely shared here is
// identity construction, root-folder resolution, the AutoGrabRequest build and
// the outcome-DTO mapping.
//
// releases is the caller's already-deduped Prowlarr result list. It returns the
// body the caller encodes plus the HTTP status to surface on error.
func runToggleGatedSearch(ctx context.Context, deps AutoGrabDeps, sess *mode.Session,
	id searchGrabIdentity, releases []prowlarr.Release) (apidto.SearchAutoGrabOutcome, int, error) {

	rootFolder, err := autoGrabRootFolder(ctx, deps.SettingsStore, id.Mode)
	if err != nil {
		return apidto.SearchAutoGrabOutcome{}, http.StatusBadRequest, err
	}

	// Never hand RunAutoGrab a nil slice: nil means "run your own search", and
	// its internal search is TMDB-id-driven while this route carries only ?q=.
	// A search that legitimately returned nothing must reach Selection.Fallback
	// and become a pending_retry row, not an internal search that cannot work.
	if releases == nil {
		releases = []prowlarr.Release{}
	}

	// RuntimeSeconds is 0 in every mode at this hook — runtime comes from TMDB
	// (MovieDetails.Runtime, seriesEpisodeRuntimeSeconds) or from an Adult
	// entity's DurationSeconds, and a query-only Search request carries none of
	// them.
	//
	// CONSEQUENCE, verified and reported rather than papered over: this hook
	// currently can NEVER auto-grab. autograb.GradeCandidate short-circuits on
	// `SizeBytes <= 0 || RuntimeSeconds <= 0` and returns not-qualified BEFORE
	// reaching any other check — including the Lossless remux/bluray bypass, so
	// not even a remux qualifies here. Every toggle-ON search therefore takes
	// Selection.Fallback and parks a pending_retry row, which the 24h retry
	// cycle re-searches. That is the ungradeable machinery behaving exactly as
	// designed, and it is precisely the state the retry pipeline exists to make
	// observable — it is NOT a bug to patch with a title->TMDB resolution step
	// invented here. If auto-grab-on-first-search is wanted, that is a separate,
	// deliberate decision about where runtime comes from.
	//
	// (The plan's §2.3.1 predicted "only a Lossless-source release can
	// auto-grab" on this path. That prediction is wrong for the reason above;
	// the direction — expect retries, not grabs — is right, and stronger.)
	out, err := RunAutoGrab(ctx, deps, sess, AutoGrabRequest{
		Mode: id.Mode, Title: id.Title, TMDBID: id.TMDBID,
		Season: id.Season, Episode: id.Episode, SeasonSpecified: id.SeasonSpecified,
		RootFolderPath: rootFolder, Trigger: TriggerRequest,
		Releases: releases,
	})
	if err != nil {
		return apidto.SearchAutoGrabOutcome{}, out.Status, err
	}

	switch {
	case out.Gated:
		// Only reachable if the toggle was switched off between the handler's
		// read and RunAutoGrab's gate. Report it rather than answering with an
		// empty outcome the frontend would render as a successful no-op.
		return apidto.SearchAutoGrabOutcome{}, http.StatusConflict,
			errors.New("usenet auto-grab was switched off while this search ran — try again")
	case out.Grabbed:
		return apidto.SearchAutoGrabOutcome{
			AutoGrabbed: true, Outcome: "grabbed", GrabID: out.GrabID,
			Title: out.Releases[out.Selection.PickIndex].Title,
		}, http.StatusOK, nil
	case out.AlreadyGrabbing:
		outcome := apidto.SearchAutoGrabOutcome{
			Outcome: "grabbed", Title: id.Title,
			Reason: "already grabbing this release",
		}
		if out.Grab != nil {
			outcome.GrabID = out.Grab.ID
			outcome.Title = out.Grab.Title
		}
		return outcome, http.StatusOK, nil
	default: // out.NoMatch — nothing cleared the quality floor
		return apidto.SearchAutoGrabOutcome{
			Outcome: string(out.RetryStatus), GrabID: out.GrabID,
			Title: id.Title, Reason: out.RetryReason,
		}, http.StatusOK, nil
	}
}

type grabRequest struct {
	Title            string `json:"title"`
	TMDBID           int    `json:"tmdbId,omitempty"`
	TVDBID           int    `json:"tvdbId,omitempty"`
	SeasonNumber     int    `json:"seasonNumber,omitempty"`
	EpisodeNumber    int    `json:"episodeNumber,omitempty"`
	SeasonSpecified  bool   `json:"seasonSpecified,omitempty"`
	QualityProfileID int    `json:"qualityProfileId,omitempty"`
	Indexer          string `json:"indexer"`
	Protocol         string `json:"protocol"`
	DownloadURL      string `json:"downloadUrl"`
	RootFolderPath   string `json:"rootFolderPath"`
}

// grabHandler sends one chosen search result to the appropriate download
// engine and records it in internal/grabs for status tracking. This is the
// one mutating action in the search workflow — Search itself never does —
// matching every other workflow's "Scan never mutates, exactly one
// human-approved action does" rule.
func grabHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store, whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		var req grabRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// Claude 2026-08-11: report the exact missing Grab request fields.
		// Reason: the previous all-fields message concealed whether the release
		// candidate, protocol mapping, or configured root caused the rejection.
		// Troubleshooting: makes malformed availability candidates diagnosable.
		// Review if: request decoding moves to shared field-level validation.
		var missing []string
		if strings.TrimSpace(req.DownloadURL) == "" {
			missing = append(missing, "downloadUrl")
		}
		if strings.TrimSpace(req.Protocol) == "" {
			missing = append(missing, "protocol")
		}
		if strings.TrimSpace(req.RootFolderPath) == "" {
			missing = append(missing, "rootFolderPath")
		}
		if len(missing) > 0 {
			http.Error(w, "missing required field(s): "+strings.Join(missing, ", "), http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		downloadClient, gid, status, err := dispatchToDownloadClient(ctx, settingsStore, sess, m, nzb, req.Protocol, req.DownloadURL, req.Title)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		// Idempotency guard: the download client dedupes by infohash, so a repeat
		// grab of the same release returns the SAME gid — don't record a duplicate
		// grabs row. Surface a clear 409 the operator can act on, not a raw error.
		if _, dup, status, err := activeGrabForGID(ctx, grabsStore, m, gid); dup || err != nil {
			if err != nil {
				http.Error(w, err.Error(), status)
				return
			}
			http.Error(w, "already grabbing this release", http.StatusConflict)
			return
		}

		created, err := grabsStore.Create(ctx, grabs.Grab{
			Mode: m, Title: req.Title, TMDBID: req.TMDBID, TVDBID: req.TVDBID,
			SeasonNumber: req.SeasonNumber, EpisodeNumber: req.EpisodeNumber, SeasonSpecified: req.SeasonSpecified,
			QualityProfileID: req.QualityProfileID, Indexer: req.Indexer, Protocol: req.Protocol,
			DownloadClient: downloadClient, RootFolderPath: req.RootFolderPath,
			// Provenance for a later retry. An RSS-sourced usenet item's entire
			// provenance is its enclosure URL plus title, so a row recorded
			// without it parks as pending_retry with nothing to re-derive from.
			// Same gap BE-8 fixed in grabDirectEnclosure; this is its twin on
			// the manual-pick path.
			DownloadURL: req.DownloadURL,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Record the download GID so the downloader's onComplete callback can tie
		// a finished download back to this grab for auto-import. Best-effort:
		// a store failure here doesn't undo the (already-submitted) download.
		if gid != "" {
			if err := grabsStore.SetDownloadGID(ctx, created.ID, gid); err != nil {
				log.Printf("grabHandler: failed to persist download GID %s for grab %d: %v", gid, created.ID, err)
			} else {
				created.DownloadGID = gid
			}
		}

		whStore.Dispatch(webhooks.EventGrabCompleted, map[string]any{
			"mode": string(m), "title": req.Title, "tmdbId": req.TMDBID,
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(created)
	}
}

// dispatchToDownloadClient sends one release to the appropriate download
// engine and returns the download-client name plus the GID assigned (used
// later to tie a completed download back to its grab for auto-import).
// Torrent releases go to the in-process anacrolix engine (internal/downloader);
// usenet/NZB releases go to the native NNTP engine (internal/usenet).
// Shared by the manual grabHandler and the auto-grab handler.
// On failure it returns the HTTP status the caller should surface.
//
// Global-pause gate: this is the ONE shared choke point every manual and
// auto-grab dispatch flows through, so the system-wide pause check lives here
// (once) rather than duplicated at each call site. When the pause flag is set it
// short-circuits before touching either engine, returning errDownloadsPaused and
// 423 Locked so the frontend can distinguish "blocked because paused" from any
// other grab failure.
func dispatchToDownloadClient(ctx context.Context, settingsStore *settings.Store, sess *mode.Session, m mode.Mode, nzb *usenet.Manager, protocol, downloadURL, title string) (downloadClient, gid string, status int, err error) {
	paused, err := settingsStore.GetBool(ctx, downloadsGlobalPausedKey, false)
	if err != nil {
		return "", "", http.StatusInternalServerError, err
	}
	if paused {
		return "", "", http.StatusLocked, errDownloadsPaused
	}
	switch prowlarr.Protocol(protocol) {
	case prowlarr.Torrent:
		if sess.Downloader == nil {
			return "", "", http.StatusBadRequest, errors.New("the download engine isn't running — check the server logs")
		}
		gid, err := sess.Downloader.AddTorrent(ctx, downloadURL)
		if err != nil {
			return "", "", http.StatusBadGateway, err
		}
		return "anacrolix", gid, http.StatusOK, nil
	case prowlarr.Usenet:
		if nzb == nil {
			return "", "", http.StatusBadRequest, errors.New("add a Usenet subscription on the Settings → Download → Usenet page to grab usenet releases")
		}
		gid, err := nzb.AddNZB(ctx, downloadURL, title)
		if err != nil {
			return "", "", http.StatusBadGateway, err
		}
		return "nntp", gid, http.StatusOK, nil
	default:
		return "", "", http.StatusBadRequest, fmt.Errorf("unrecognized protocol %q", protocol)
	}
}

func listGrabsHandler(grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		list, err := grabsStore.List(r.Context(), m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	}
}

// checkImportHandler refreshes one grab's status from the appropriate download
// engine, and — the moment its download is seen complete — performs the import
// via the shared importGrabContent core (relocate into the target root folder
// + record in SAK's own library). This is the manual, human-triggered refresh;
// the same import also happens automatically via each engine's onComplete
// callback, so a grab typically imports itself the moment the download
// finishes — this endpoint is the on-demand "check it now" the Grabs UI offers.
//
// GID routing: "nzb-" prefix → usenet engine; everything else → torrent engine.
func checkImportHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store, libStore *library.Store, prober dedup.Prober) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		g, err := grabsStore.Get(ctx, id)
		if err != nil {
			if errors.Is(err, grabs.ErrNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		if g.Status == grabs.Imported {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(g)
			return
		}

		if g.DownloadGID == "" {
			http.Error(w, "this grab has no download GID — try re-grabbing", http.StatusConflict)
			return
		}

		// Route usenet GIDs to the usenet engine.
		if strings.HasPrefix(g.DownloadGID, "nzb-") {
			if nzb == nil {
				http.Error(w, "the usenet engine isn't running", http.StatusServiceUnavailable)
				return
			}
			nzbItem, err := nzb.FindByGID(g.DownloadGID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			if nzbItem == nil {
				http.Error(w, "the usenet engine no longer knows about this download", http.StatusConflict)
				return
			}
			newStatus := classifyDownloadState(nzbItem.Status, nzbItem.Err)
			// A retryable retrieval failure needs a retry_after, not just a
			// status: DueForRetry ignores a pending_retry row whose retry_after
			// is empty, so a bare UpdateStatus here would strand it forever.
			// The reason is classified the same way the sweep classifies it, so
			// the two paths can never render different text for one failure.
			if newStatus == grabs.PendingRetry {
				if err := parkGrabForRetry(ctx, AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}, id, usenetRetrievalReason(nzbItem.Err)); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				updated, err := grabsStore.Get(ctx, id)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				writeJSON(w, updated)
				return
			}
			if newStatus == grabs.Completed {
				contentPath := downloadContentPath(nzbItem.Files, nzbItem.Dir, nzb.StagingDir())
				changes, err := importGrabContent(ctx, libStore, g, contentPath, string(autoGrabTier(ctx, settingsStore, g.Mode)))
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, g.Mode)
				if err != nil {
					// Layer 2 rejects an Adult g.Mode here. The security
					// property already held via the generic 400 below — the
					// grab was refused either way — but the SHAPE did not: a
					// plaintext 400 cannot raise the frontend's PIN overlay,
					// which keys on this exact code. Same mapping
					// proposals.go's two single-item handlers use.
					if errors.Is(err, sectionlock.ErrSectionLocked) {
						writeSectionLocked(w, sectionlock.SectionAdultContent)
						return
					}
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				postGrabRuntimeReview(ctx, prober, grabsStore, sess, g, changes)
				sess.NotifyPlayers(ctx, changes)
				_ = grabsStore.SetDownloadStatus(ctx, id, nzbItem.Status, contentPath)
				newStatus = grabs.Imported
			}
			_ = grabsStore.UpdateStatus(ctx, id, newStatus)
			updated, err := grabsStore.Get(ctx, id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(updated)
			return
		}

		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusBadRequest)
			return
		}

		dlItem, err := dl.FindByGID(g.DownloadGID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if dlItem == nil {
			http.Error(w, "the download engine no longer knows about this download", http.StatusConflict)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, g.Mode)
		if err != nil {
			// Same Layer 2 mapping as the usenet branch above — see its
			// comment for why the shape, not just the refusal, matters.
			if errors.Is(err, sectionlock.ErrSectionLocked) {
				writeSectionLocked(w, sectionlock.SectionAdultContent)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// nil: the torrent engine's Download carries no unflattened error field.
		newStatus := classifyDownloadState(dlItem.Status, nil)
		imported := false
		if newStatus == grabs.Completed {
			// SECOND CALLER of the seeding seam. dlItem.Files/.Dir are the
			// ORIGINAL staging paths — with seeding on, exactly the files the
			// torrent client is still serving, and relocating them kills the
			// seed silently. Consume the per-gid import copy instead, falling
			// back to the originals only when there is none (seeding off, no
			// copy made, or the copy failed and the completion flow already
			// fell through to the originals) — which is every seeding-off
			// install, byte-for-byte today's behavior.
			files, dir, staging := dlItem.Files, dlItem.Dir, dl.StagingDir()
			if copied := dl.ImportPaths(g.DownloadGID); len(copied) > 0 {
				if _, err := os.Stat(copied[0]); err != nil {
					// The copy was recorded but is gone: a previous import
					// already consumed it. Re-running would relocate the
					// seeding originals — refuse instead. 409 is the status
					// this handler already uses for "the engine no longer
					// knows about this download".
					http.Error(w, "this download's import copy has already been imported — the torrent is still seeding its original files", http.StatusConflict)
					return
				}
				files, dir, staging = copied, filepath.Dir(copied[0]), dl.ImportRoot(g.DownloadGID)
			}
			contentPath := downloadContentPath(files, dir, staging)
			changes, err := importGrabContent(ctx, libStore, g, contentPath, string(autoGrabTier(ctx, settingsStore, g.Mode)))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			// Post-grab mislabel check (auto-grab safety net): advisory only.
			postGrabRuntimeReview(ctx, prober, grabsStore, sess, g, changes)
			sess.NotifyPlayers(ctx, changes)
			_ = grabsStore.SetDownloadStatus(ctx, id, dlItem.Status, contentPath)
			newStatus = grabs.Imported
			imported = true
		} else {
			_ = grabsStore.SetDownloadStatus(ctx, id, dlItem.Status, "")
		}

		if err := grabsStore.UpdateStatus(ctx, id, newStatus); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if imported {
			// The import consumed the copy; Relocate moved the content out but
			// left the (now empty) .import/<gid>/ tree behind. The automatic
			// path (DownloadCompleteImporter, internal/api/import.go) already
			// reclaims this on success — mirror it here so a manually-triggered
			// check-import doesn't leave an empty per-gid directory behind
			// until the next restart's sweep (Manager.Start's sweepImportDir).
			dl.ClearImportCopy(g.DownloadGID)
		}

		updated, err := grabsStore.Get(ctx, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(updated)
	}
}

// classifyDownloadState maps the download engine's status vocabulary to grabs'
// lifecycle. "complete" is the only status that triggers an import; "error" is
// Failed; everything else (active/waiting/paused) is still in-flight.
//
// failure is the engine's UNFLATTENED error (usenet.Download.Err), and it is
// checked before state because a bare "error" string cannot tell a permanent
// takedown from a transient miss:
//   - ErrArticleRemoved (451, DMCA) is PERMANENT → Failed, never retried.
//   - ErrArticleNotFound (430 from every configured subscription) is retryable
//     → PendingRetry.
//   - ANY OTHER non-nil failure — a dial timeout, a decode error, the mixed
//     case where one provider answered 451 and another was unreachable
//     (usenet.fetchSegmentAny returns the raw transport error there) — is also
//     retryable → PendingRetry. Only a CONFIRMED takedown is terminal: an
//     unreachable server proves nothing about whether the article exists, and
//     failing on it strands a recoverable download forever, since nothing ever
//     re-searches a Failed row. PendingRetry rows now retry indefinitely
//     (2026-08-01: the retry-attempt cap was removed) — a permanently
//     ungradeable item keeps retrying on the normal interval rather than
//     eventually becoming Failed, a deliberate product decision.
//
// DEVIATION, reported not silently fixed: the plan asked for this to take the
// Download struct. It cannot — the two callers pass usenet.Download and
// downloader.Download, different types, and only the usenet one carries Err.
// (state, failure) is the shape that classifies both; the torrent caller passes
// nil until its engine grows an equivalent field.
//
// This is the FAST path only, reached by frontend polling. The authoritative
// transition is the retry scheduler's GID sweep, which runs with no client
// attached — a headless instance must not depend on a browser being open.
func classifyDownloadState(state string, failure error) grabs.Status {
	switch {
	case errors.Is(failure, usenet.ErrArticleRemoved):
		return grabs.Failed
	case errors.Is(failure, usenet.ErrArticleNotFound):
		return grabs.PendingRetry
	case failure != nil:
		// Unclassified — transient until proven otherwise. The torrent caller
		// passes nil, so this only ever sees a usenet Download.Err.
		return grabs.PendingRetry
	}
	switch state {
	case "complete":
		return grabs.Completed
	case "error":
		return grabs.Failed
	case "waiting", "paused":
		return grabs.Queued
	default: // "active", "removed", or anything unexpected
		return grabs.Downloading
	}
}

// postGrabRuntimeReview runs auto-grab's post-grab mislabel check on a
// freshly imported grab: it probes the imported file's actual duration and, on
// a gross mismatch with the title's known TMDB runtime, flags the grab for
// operator review (internal/autograb.RuntimeMismatch defines "gross").
//
// It is strictly ADVISORY — the import has already succeeded by the time it
// runs — so every uncertain path is a silent skip, never an error that fails
// the import or a false-positive flag: nil prober/TMDB client, unknown TMDB
// id, more than one imported file (ambiguous which to probe), a probe error,
// or an unknown/zero duration on either side all skip the check.
//
// Movies and single-episode Series are wired — both resolve a single,
// unambiguous expected runtime from TMDB (Movies from /movie/{id}, Series from
// the picked episode via seriesEpisodeRuntimeSeconds, the same source the
// pre-grab bitrate scorer already uses). A whole-season Series grab
// (EpisodeNumber == 0) is deliberately skipped: a season pack has no single
// per-file runtime to check against, exactly the pre-grab scorer's own decision
// to grade packs as unknown-bitrate. Adult is skipped because TPDB's pre-grab
// scene runtime is unconfirmed (see the plan's Open Items). All skips are safe,
// consistent with never false-positive-flagging.
func postGrabRuntimeReview(ctx context.Context, prober dedup.Prober, grabsStore *grabs.Store, sess *mode.Session, g *grabs.Grab, changes []mode.PathChange) {
	if prober == nil || sess.TMDB == nil || g.TMDBID == 0 {
		return
	}
	if len(changes) != 1 {
		return
	}

	var expectedSeconds float64
	switch g.Mode {
	case mode.Movies:
		details, err := sess.TMDB.MovieDetails(ctx, g.TMDBID)
		if err != nil || details.Runtime <= 0 {
			return
		}
		expectedSeconds = float64(details.Runtime * 60)
	case mode.Series:
		// Only single-episode grabs are checkable; a season pack
		// (EpisodeNumber == 0) has no single runtime and yields 0 here.
		expectedSeconds = seriesEpisodeRuntimeSeconds(ctx, sess, g.TMDBID, g.SeasonNumber, g.EpisodeNumber)
		if expectedSeconds <= 0 {
			return
		}
	default:
		return
	}

	probe, err := prober.Probe(ctx, changes[0].Path)
	if err != nil {
		return
	}

	mismatch, checked := autograb.RuntimeMismatch(probe.Duration, expectedSeconds)
	if !checked || !mismatch {
		return
	}

	// Best-effort: a flag failure must not undo an already-successful import.
	_ = grabsStore.Flag(ctx, g.ID, fmt.Sprintf(
		"imported file runs %.0f min but TMDB lists %.0f min — possible mislabel or wrong content",
		probe.Duration/60, expectedSeconds/60))
}
