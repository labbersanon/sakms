package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/autograb"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/dedup"
	"github.com/curtiswtaylorjr/sakms/internal/grabs"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/prowlarr"
	"github.com/curtiswtaylorjr/sakms/internal/qbittorrent"
	"github.com/curtiswtaylorjr/sakms/internal/quality"
	"github.com/curtiswtaylorjr/sakms/internal/release"
	"github.com/curtiswtaylorjr/sakms/internal/rename"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
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
// client (qBittorrent for torrent, NZBGet for usenet) and records it in
// internal/grabs for status tracking. This is the one mutating action in the
// search workflow — Search itself never does — matching every other
// workflow's "Scan never mutates, exactly one human-approved action does" rule.
func grabHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, grabsStore *grabs.Store, whStore *webhooks.Store) http.HandlerFunc {
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		downloadClient, clientRef, status, err := dispatchToDownloadClient(ctx, sess, m, req.Protocol, req.DownloadURL, req.Title)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		created, err := grabsStore.Create(ctx, grabs.Grab{
			Mode: m, Title: req.Title, TMDBID: req.TMDBID, TVDBID: req.TVDBID,
			SeasonNumber: req.SeasonNumber, EpisodeNumber: req.EpisodeNumber, SeasonSpecified: req.SeasonSpecified,
			QualityProfileID: req.QualityProfileID, Indexer: req.Indexer, Protocol: req.Protocol,
			DownloadClient: downloadClient, ClientRef: clientRef, RootFolderPath: req.RootFolderPath,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		whStore.Dispatch(webhooks.EventGrabCompleted, map[string]any{
			"mode": string(m), "title": req.Title, "tmdbId": req.TMDBID,
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(created)
	}
}

// dispatchToDownloadClient sends one release to the appropriate download
// client (qBittorrent for torrent, NZBGet for usenet) and returns the client
// name plus a pollable client-side reference (a torrent hash, or an NZBGet id;
// "" when it can't be derived — e.g. a non-magnet .torrent URL, which is still
// recorded but can't be status-polled later; see qbittorrent.HashFromMagnet).
// Shared by the manual grabHandler and the auto-grab handler — auto-grab is
// the genuine second caller that justifies the extraction. On failure it
// returns the HTTP status the caller should surface, preserving each path's
// original code (400 not-configured / unrecognized-protocol, 502 client error).
func dispatchToDownloadClient(ctx context.Context, sess *mode.Session, m mode.Mode, protocol, downloadURL, title string) (downloadClient, clientRef string, status int, err error) {
	switch prowlarr.Protocol(protocol) {
	case prowlarr.Torrent:
		if sess.QBittorrent == nil {
			return "", "", http.StatusBadRequest, errors.New("qbittorrent isn't configured yet — add it in Settings first")
		}
		if err := sess.QBittorrent.Add(ctx, downloadURL, string(m)); err != nil {
			return "", "", http.StatusBadGateway, err
		}
		if hash, ok := qbittorrent.HashFromMagnet(downloadURL); ok {
			clientRef = hash
		}
		return "qbittorrent", clientRef, http.StatusOK, nil
	case prowlarr.Usenet:
		if sess.NZBGet == nil {
			return "", "", http.StatusBadRequest, errors.New("nzbget isn't configured yet — add it in Settings first")
		}
		id, err := sess.NZBGet.Append(ctx, downloadURL, title+".nzb", string(m))
		if err != nil {
			return "", "", http.StatusBadGateway, err
		}
		return "nzbget", strconv.FormatInt(id, 10), http.StatusOK, nil
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

// classifyQBittorrentState maps qBittorrent's many torrent states down to
// grabs' simpler lifecycle — "uploading"/stalled-seeding/paused-seeding
// states all mean the download itself finished, it's just seeding now.
func classifyQBittorrentState(state string) grabs.Status {
	switch state {
	case "error", "missingFiles":
		return grabs.Failed
	case "uploading", "stalledUP", "pausedUP", "queuedUP", "forcedUP", "checkingUP":
		return grabs.Completed
	default:
		return grabs.Downloading
	}
}

// classifyNZBGetState maps NZBGet's Status values down to grabs' lifecycle.
// A history-sourced status always looks like "SUCCESS/ALL" or "FAILURE/PAR"
// (contains a slash); an active-queue status from listgroups is a bare word
// like "DOWNLOADING" or "PAUSED" — this is a heuristic, not a documented
// distinction, since (like the rest of this client) it isn't confirmed
// against a real NZBGet instance yet.
func classifyNZBGetState(state string) grabs.Status {
	if strings.Contains(state, "/") {
		if strings.HasPrefix(state, "SUCCESS") {
			return grabs.Completed
		}
		return grabs.Failed
	}
	switch state {
	case "PAUSED", "QUEUED":
		return grabs.Queued
	default:
		return grabs.Downloading
	}
}

// checkImportHandler refreshes one grab's status from its download client,
// and — the moment it's seen as complete — performs the import: relocates
// the downloaded content into the grab's target root folder (reusing
// internal/rename's exact Relocate logic) and records it in SAK's own library
// (Movies/Series), exactly like Rename's Apply does for a brand-new orphan.
// Adult records nothing here — it has no scene identity at grab time — and
// defers tracking to the next Rename scan (see the mode.Adult branch). This is
// a manual, human-triggered refresh (there is no background poller anywhere
// in this program) — the user clicks it, same as every other mutating
// action in SAK.
func checkImportHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, grabsStore *grabs.Store, libStore *library.Store, prober dedup.Prober) http.HandlerFunc {
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, g.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var newStatus grabs.Status
		var contentPath string
		switch g.DownloadClient {
		case "qbittorrent":
			if sess.QBittorrent == nil {
				http.Error(w, "qbittorrent isn't configured", http.StatusBadRequest)
				return
			}
			if g.ClientRef == "" {
				http.Error(w, "this grab has no tracked hash (it wasn't added via a magnet link) — check qbittorrent directly", http.StatusConflict)
				return
			}
			status, err := sess.QBittorrent.Status(ctx, g.ClientRef)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			newStatus = classifyQBittorrentState(status.State)
			contentPath = status.ContentPath
		case "nzbget":
			if sess.NZBGet == nil {
				http.Error(w, "nzbget isn't configured", http.StatusBadRequest)
				return
			}
			refID, err := strconv.ParseInt(g.ClientRef, 10, 64)
			if err != nil {
				http.Error(w, "this grab has no valid nzbget id", http.StatusConflict)
				return
			}
			status, err := sess.NZBGet.Status(ctx, refID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			newStatus = classifyNZBGetState(status.State)
			contentPath = status.DestDir
		default:
			http.Error(w, fmt.Sprintf("unknown download client %q", g.DownloadClient), http.StatusInternalServerError)
			return
		}

		if newStatus == grabs.Completed && contentPath != "" {
			movedPath, err := rename.Relocate(contentPath, g.RootFolderPath)
			if err != nil {
				http.Error(w, fmt.Sprintf("download completed but import failed: %v", err), http.StatusBadGateway)
				return
			}
			// changes accumulates the exact file(s) this import created, fed
			// to sess.NotifyPlayers once below (Jellyfin=Movies/Series,
			// Stash=Adult — hardcoded scoping via mode.Build's nil-ness,
			// same as every other call site). movedPath itself can be a
			// wrapping directory (Relocate moves contentPath's whole tree),
			// so Movies/Series notify with the resolved video file path(s) —
			// same "actual path, not the directory" discipline as rename.go's
			// row 1/2 — while Adult (no per-file resolution here: the scene is
			// left untracked for the next Rename scan to identify, and Stash's
			// RescanPaths scans directory trees fine) notifies with movedPath
			// directly.
			var changes []mode.PathChange
			switch g.Mode {
			case mode.Movies:
				videoPath, err := library.ResolveVideoFile(movedPath)
				if err != nil {
					http.Error(w, fmt.Sprintf("file relocated but resolving the video file failed: %v", err), http.StatusBadGateway)
					return
				}
				if _, err := libStore.Upsert(ctx, library.Item{
					Mode: mode.Movies, TMDBID: g.TMDBID, Title: g.Title,
					FilePath: videoPath, RootFolderPath: g.RootFolderPath,
				}); err != nil {
					http.Error(w, fmt.Sprintf("file relocated but recording it in the library failed: %v", err), http.StatusBadGateway)
					return
				}
				changes = []mode.PathChange{{Path: videoPath, Kind: mode.Created}}
			case mode.Series:
				videoPaths, err := library.ResolveEpisodeVideoFiles(movedPath)
				if err != nil {
					http.Error(w, fmt.Sprintf("file relocated but resolving the video file(s) failed: %v", err), http.StatusBadGateway)
					return
				}
				series, err := libStore.UpsertSeries(ctx, library.Series{
					TMDBID: g.TMDBID, Title: g.Title, RootFolderPath: g.RootFolderPath,
				})
				if err != nil {
					http.Error(w, fmt.Sprintf("file relocated but recording the series failed: %v", err), http.StatusBadGateway)
					return
				}
				for _, videoPath := range videoPaths {
					season, episodes, ok := library.ParseEpisodeNumbers(filepath.Base(videoPath))
					if !ok {
						// A season-pack grab's own request already recorded
						// which season it targeted; a single-episode grab
						// whose relocated file name didn't carry its own
						// SxxExx token falls back to what was requested —
						// only sound when there's exactly one resolved file
						// and a season was actually specified at grab time
						// (SeasonNumber alone can't tell "Season 0/Specials"
						// apart from "no season was picked at all").
						if len(videoPaths) != 1 || !g.SeasonSpecified {
							continue
						}
						season, episodes = g.SeasonNumber, []int{g.EpisodeNumber}
					}
					// Logical episode-splitting: a bundled multi-episode
					// filename (e.g. "S01E01-E02") relocates as ONE file but
					// must record an Episode row for EVERY number it
					// contains — episodes[1:] were silently dropped before
					// this fix (a real, confirmed gap: a grabbed multi-
					// episode file used to leave every episode past the
					// first untracked forever). One Created PathChange per
					// physical file still, not per episode row.
					for _, episode := range episodes {
						if _, err := libStore.UpsertEpisode(ctx, library.Episode{
							SeriesID: series.ID, SeasonNumber: season, EpisodeNumber: episode, FilePath: videoPath,
						}); err != nil {
							http.Error(w, fmt.Sprintf("file relocated but recording episode s%de%d failed: %v", season, episode, err), http.StatusBadGateway)
							return
						}
					}
					changes = append(changes, mode.PathChange{Path: videoPath, Kind: mode.Created})
				}
			case mode.Adult:
				// Adult owns its own library now (Whisparr eliminated, Stage 4),
				// but — unlike Movies/Series — an Adult grab carries NO stable
				// scene identity at grab time: grabRequest has no box/scene_id,
				// and TMDBID is always 0 for Adult. library.Scene is keyed on
				// (box, scene_id), so there is nothing to UpsertScene on yet.
				//
				// DELIBERATE DEVIATION from the Stage-4 plan's literal
				// "resolve the grabbed file and UpsertScene here": recording a
				// scene with an empty (box, scene_id) would be actively harmful —
				// (a) every unidentified grab collides onto the single
				// ON CONFLICT(box, scene_id) = ("","") row, clobbering the prior
				// one; and (b) a scene recorded at import time is masked from the
				// next Rename scan, since ScanLibraryAdult builds its `known` set
				// from scene FilePaths and ScanRootFolder skips known paths — so
				// the very pass meant to identify it would never see it. Both
				// defeat the plan's own stated goal ("let the next Rename scan
				// fully identify/reconcile it later").
				//
				// So we relocate the download into the Adult root folder and stop
				// there, exactly as before but without the Whisparr registration.
				// The next Adult Rename scan discovers the untracked file,
				// identifies it, and UpsertScenes it with a real (box, scene_id).
				// Stash's RescanPaths handles a directory tree fine, so notify
				// with movedPath directly (no per-file resolution here).
				changes = []mode.PathChange{{Path: movedPath, Kind: mode.Created}}
			default:
				http.Error(w, fmt.Sprintf("unknown mode %q", g.Mode), http.StatusInternalServerError)
				return
			}
			// Post-grab mislabel check (auto-grab safety net): probe the
			// imported file's real duration and flag the grab for review if it's
			// wildly inconsistent with the known TMDB runtime. Strictly advisory
			// — the import already succeeded — so it never fails the handler.
			postGrabRuntimeReview(ctx, prober, grabsStore, sess, g, changes)

			sess.NotifyPlayers(ctx, changes)
			newStatus = grabs.Imported
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
