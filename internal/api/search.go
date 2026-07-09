package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/grabs"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/prowlarr"
	"github.com/curtiswtaylorjr/sakms/internal/qbittorrent"
	"github.com/curtiswtaylorjr/sakms/internal/release"
	"github.com/curtiswtaylorjr/sakms/internal/rename"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
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

// categoriesForSearch restricts a search to m's Newznab category —
// the 2000-range for Movies, the 5000-range for TV.
func categoriesForSearch(m mode.Mode) []int {
	if m == mode.Series {
		return []int{5000}
	}
	return []int{2000}
}

// searchHandler queries Prowlarr for {mode} and scores every result against
// release.DefaultProfile — a read-only proxy+transform (like
// listRootFoldersHandler), nothing staged or persisted.
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

		prefs := release.DefaultProfile()
		out := make([]searchResult, len(releases))
		for i, rel := range releases {
			info := release.Parse(rel.Title)
			out[i] = searchResult{
				GUID: rel.GUID, Title: rel.Title, Indexer: rel.Indexer,
				Protocol: string(rel.Protocol), Size: rel.Size, Seeders: rel.Seeders,
				DownloadURL: rel.DownloadURL, PublishDate: rel.PublishDate,
				Score: release.Score(info, prefs),
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

type grabRequest struct {
	Title            string `json:"title"`
	TMDBID           int    `json:"tmdbId,omitempty"`
	TVDBID           int    `json:"tvdbId,omitempty"`
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
func grabHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, grabsStore *grabs.Store) http.HandlerFunc {
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

		var downloadClient, clientRef string
		switch prowlarr.Protocol(req.Protocol) {
		case prowlarr.Torrent:
			if sess.QBittorrent == nil {
				http.Error(w, "qbittorrent isn't configured yet — add it in Settings first", http.StatusBadRequest)
				return
			}
			if err := sess.QBittorrent.Add(ctx, req.DownloadURL, string(m)); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			downloadClient = "qbittorrent"
			// A non-magnet .torrent URL leaves clientRef blank — this grab
			// still gets recorded (visible in the Grabs list) but can't be
			// status-polled later. See qbittorrent.HashFromMagnet's doc
			// comment for why deriving a hash from a .torrent file itself
			// isn't attempted here.
			if hash, ok := qbittorrent.HashFromMagnet(req.DownloadURL); ok {
				clientRef = hash
			}
		case prowlarr.Usenet:
			if sess.NZBGet == nil {
				http.Error(w, "nzbget isn't configured yet — add it in Settings first", http.StatusBadRequest)
				return
			}
			id, err := sess.NZBGet.Append(ctx, req.DownloadURL, req.Title+".nzb", string(m))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			downloadClient = "nzbget"
			clientRef = strconv.FormatInt(id, 10)
		default:
			http.Error(w, fmt.Sprintf("unrecognized protocol %q", req.Protocol), http.StatusBadRequest)
			return
		}

		created, err := grabsStore.Create(ctx, grabs.Grab{
			Mode: m, Title: req.Title, TMDBID: req.TMDBID, TVDBID: req.TVDBID,
			QualityProfileID: req.QualityProfileID, Indexer: req.Indexer, Protocol: req.Protocol,
			DownloadClient: downloadClient, ClientRef: clientRef, RootFolderPath: req.RootFolderPath,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(created)
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
// internal/rename's exact Relocate logic) and registers it with the mode's
// *arr app, exactly like Rename's Apply does for a brand-new orphan. This is
// a manual, human-triggered refresh (there is no background poller anywhere
// in this program) — the user clicks it, same as every other mutating
// action in SAK.
func checkImportHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, grabsStore *grabs.Store) http.HandlerFunc {
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
			if _, err := rename.Relocate(contentPath, g.RootFolderPath); err != nil {
				http.Error(w, fmt.Sprintf("download completed but import failed: %v", err), http.StatusBadGateway)
				return
			}
			if _, err := sess.Servarr.Add(ctx, servarr.AddRequest{
				Title: g.Title, TVDBID: g.TVDBID, TMDBID: g.TMDBID,
				QualityProfileID: g.QualityProfileID, RootFolderPath: g.RootFolderPath, Monitored: true,
			}); err != nil {
				http.Error(w, fmt.Sprintf("file relocated but registering with %s failed: %v", g.Mode, err), http.StatusBadGateway)
				return
			}
			if err := sess.Servarr.ScanForDownloaded(ctx); err != nil {
				http.Error(w, fmt.Sprintf("registered but triggering a rescan failed: %v", err), http.StatusBadGateway)
				return
			}
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
