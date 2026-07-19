package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/autograb"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/dedup"
	"github.com/curtiswtaylorjr/sakms/internal/downloader"
	"github.com/curtiswtaylorjr/sakms/internal/grabs"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/prowlarr"
	"github.com/curtiswtaylorjr/sakms/internal/quality"
	"github.com/curtiswtaylorjr/sakms/internal/release"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
	"github.com/curtiswtaylorjr/sakms/internal/usenet"
	"github.com/curtiswtaylorjr/sakms/internal/webhooks"
)

// searchResult is one scored release Prowlarr found, for a human to pick
// from — nothing here is persisted; internal/grabs is what tracks a release
// once it's actually grabbed (see grabHandler).
type searchResult struct {
	GUID        string `json:"guid"`
	Title       string `json:"title"`
	Indexer     string `json:"indexer"`
	Protocol    string `json:"protocol"`
	Size        int64  `json:"size"`
	Seeders     int    `json:"seeders"`
	DownloadURL string `json:"downloadUrl"`
	PublishDate string `json:"publishDate"`
	Score       int    `json:"score"`
}

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
func searchHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()
		query := r.URL.Query().Get("q")
		if query == "" {
			http.Error(w, "q query parameter is required", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, nil, m)
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

		prefs, err := searchQualityProfile(ctx, settingsStore, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		now := time.Now()
		out := make([]searchResult, len(releases))
		for i, rel := range releases {
			info := release.Parse(rel.Title)
			out[i] = searchResult{
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
func grabHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store, whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		var req grabRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.DownloadURL == "" || req.Protocol == "" || req.RootFolderPath == "" {
			http.Error(w, "downloadUrl, protocol, and rootFolderPath are required", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, dl, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		downloadClient, gid, status, err := dispatchToDownloadClient(ctx, sess, m, nzb, req.Protocol, req.DownloadURL, req.Title)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		created, err := grabsStore.Create(ctx, grabs.Grab{
			Mode: m, Title: req.Title, TMDBID: req.TMDBID, TVDBID: req.TVDBID,
			SeasonNumber: req.SeasonNumber, EpisodeNumber: req.EpisodeNumber, SeasonSpecified: req.SeasonSpecified,
			QualityProfileID: req.QualityProfileID, Indexer: req.Indexer, Protocol: req.Protocol,
			DownloadClient: downloadClient, RootFolderPath: req.RootFolderPath,
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
func dispatchToDownloadClient(ctx context.Context, sess *mode.Session, m mode.Mode, nzb *usenet.Manager, protocol, downloadURL, title string) (downloadClient, gid string, status int, err error) {
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
			return "", "", http.StatusBadRequest, errors.New("configure an NNTP server in Settings → Connections to grab usenet releases")
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
func checkImportHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store, libStore *library.Store, prober dedup.Prober) http.HandlerFunc {
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
			newStatus := classifyDownloadState(nzbItem.Status)
			if newStatus == grabs.Completed {
				contentPath := downloadContentPath(nzbItem.Files, nzbItem.Dir, nzb.StagingDir())
				changes, err := importGrabContent(ctx, libStore, g, contentPath)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, dl, g.Mode)
				if err != nil {
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, dl, g.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		newStatus := classifyDownloadState(dlItem.Status)
		if newStatus == grabs.Completed {
			contentPath := downloadContentPath(dlItem.Files, dlItem.Dir, dl.StagingDir())
			changes, err := importGrabContent(ctx, libStore, g, contentPath)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			// Post-grab mislabel check (auto-grab safety net): advisory only.
			postGrabRuntimeReview(ctx, prober, grabsStore, sess, g, changes)
			sess.NotifyPlayers(ctx, changes)
			_ = grabsStore.SetDownloadStatus(ctx, id, dlItem.Status, contentPath)
			newStatus = grabs.Imported
		} else {
			_ = grabsStore.SetDownloadStatus(ctx, id, dlItem.Status, "")
		}

		if err := grabsStore.UpdateStatus(ctx, id, newStatus); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
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
func classifyDownloadState(state string) grabs.Status {
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
