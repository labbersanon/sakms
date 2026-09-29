// IMDb list ingest — operator-configured ls… / ur… IDs fetched from
// rss.imdb.com, resolved to TMDB via /find. Same movie/series semantics
// as Trakt watchlist ingest.
//
// NO GOROUTINE, NO TICKER, NO INTERVAL KEY. Called from monitorListIngests,
// the SEVENTH step of runUsenetRetryCycle.
package api

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/httpx"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/settings"
)

const grabOriginIMDbList = "imdb-list"

const (
	imdbListIngestEnabledKey = "imdb_list_ingest_enabled"
	imdbListIDsKey           = "imdb_list_ids"
)

const imdbListIngestOffReason = "IMDb list ingest was turned off, so this search was cancelled"

const imdbListIngestLog = "imdb list ingest"

// imdbRSSBaseURL is Radarr's IMDb list RSS host. A var so tests can point
// it at an httptest server.
var imdbRSSBaseURL = "https://rss.imdb.com"

type imdbListIngestResponse struct {
	Enabled bool   `json:"enabled"`
	ListIDs string `json:"listIds"`
}

type imdbListIngestRequest struct {
	Enabled bool   `json:"enabled"`
	ListIDs string `json:"listIds"`
}

func getIMDbListIngestHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, err := settingsStore.GetBool(r.Context(), imdbListIngestEnabledKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, imdbListIngestResponse{
			Enabled: enabled,
			ListIDs: loadSettingString(r.Context(), settingsStore, imdbListIDsKey, imdbListIngestLog),
		})
	}
}

func putIMDbListIngestHandler(settingsStore *settings.Store, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req imdbListIngestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.SetBool(ctx, imdbListIngestEnabledKey, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, imdbListIDsKey, req.ListIDs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !req.Enabled {
			cancelListOriginRetries(ctx, grabsStore, grabOriginIMDbList, imdbListIngestOffReason, imdbListIngestLog)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func ingestIMDbLists(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, excluded map[string]bool, budget *listIngestBudget) bool {
	if deps.SettingsStore == nil {
		return false
	}
	enabled, err := deps.SettingsStore.GetBool(ctx, imdbListIngestEnabledKey, false)
	if err != nil {
		log.Printf("%s: reading ingest toggle: %v", imdbListIngestLog, err)
		return false
	}
	if !enabled {
		return false
	}
	if !listIngestAutoGrabOn(ctx, deps, imdbListIngestLog) {
		return false
	}
	client := tmdbIngestClient(ctx, deps)
	if client == nil {
		return false
	}
	httpClient := listIngestCatalog(deps).httpClient
	if deps.TraktIngest != nil && deps.TraktIngest.HTTPClient != nil {
		httpClient = deps.TraktIngest.HTTPClient
	}
	if httpClient == nil {
		return false
	}

	var items []listIngestItem
	seenTMDB := map[string]bool{}
	seenIMDb := map[string]bool{}
	for _, raw := range parseSettingLines(loadSettingString(ctx, deps.SettingsStore, imdbListIDsKey, imdbListIngestLog)) {
		ref := parseIMDbListRef(raw)
		if ref == "" {
			continue
		}
		feedURL := imdbRSSURL(ref)
		if feedURL == "" {
			continue
		}
		entries, err := fetchIMDbRSS(ctx, httpClient, feedURL)
		if err != nil {
			log.Printf("%s: fetching %s: %v", imdbListIngestLog, ref, err)
			continue
		}
		for _, entry := range entries {
			if entry.IMDbID == "" || seenIMDb[entry.IMDbID] {
				continue
			}
			seenIMDb[entry.IMDbID] = true
			movieID, tvID, findErr := client.FindByIMDBID(ctx, entry.IMDbID)
			if findErr != nil {
				log.Printf("%s: resolving %s: %v", imdbListIngestLog, entry.IMDbID, findErr)
				continue
			}
			title := entry.Title
			if title == "" {
				title = entry.IMDbID
			}
			if movieID > 0 {
				key := "movie:" + strconv.Itoa(movieID)
				if !seenTMDB[key] {
					seenTMDB[key] = true
					items = append(items, listIngestItem{Type: "movie", TMDBID: movieID, Title: title})
				}
				continue
			}
			if tvID > 0 {
				key := "show:" + strconv.Itoa(tvID)
				if !seenTMDB[key] {
					seenTMDB[key] = true
					items = append(items, listIngestItem{Type: "show", TMDBID: tvID, Title: title})
				}
			}
		}
	}
	return ingestListItems(ctx, deps, build, libStore, excluded, items, listIngestKind{
		Origin:    grabOriginIMDbList,
		Trigger:   TriggerIMDbList,
		LogPrefix: imdbListIngestLog,
	}, budget)
}

func imdbRSSURL(ref string) string {
	switch {
	case strings.HasPrefix(ref, "ls"):
		return strings.TrimRight(imdbRSSBaseURL, "/") + "/list/" + ref + "/"
	case strings.HasPrefix(ref, "ur"):
		return strings.TrimRight(imdbRSSBaseURL, "/") + "/user/" + ref + "/watchlist"
	default:
		return ""
	}
}

func parseIMDbListRef(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	if id := extractPrefixedID(raw, "ls"); id != "" {
		return id
	}
	return extractPrefixedID(raw, "ur")
}

func extractPrefixedID(raw, prefix string) string {
	start := 0
	for {
		idx := strings.Index(raw[start:], prefix)
		if idx < 0 {
			return ""
		}
		idx += start
		rest := raw[idx+len(prefix):]
		n := 0
		for n < len(rest) && unicode.IsDigit(rune(rest[n])) {
			n++
		}
		if n >= 3 {
			return prefix + rest[:n]
		}
		start = idx + 1
	}
}

type imdbRSSEntry struct {
	Title  string
	IMDbID string
}

type imdbRSSDoc struct {
	Channel struct {
		Items []struct {
			Title string `xml:"title"`
			Link  string `xml:"link"`
			GUID  string `xml:"guid"`
		} `xml:"item"`
	} `xml:"channel"`
}

func fetchIMDbRSS(ctx context.Context, httpClient *http.Client, feedURL string) ([]imdbRSSEntry, error) {
	parsed, err := url.Parse(feedURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("unsupported URL scheme")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sakms-imdb-list-ingest/1.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, httpx.WrapTransportError(req.URL.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned status %d", req.URL.Host, resp.StatusCode)
	}
	var doc imdbRSSDoc
	if err := xml.NewDecoder(io.LimitReader(resp.Body, httpx.MaxResponseBodySize)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing IMDb RSS: %w", err)
	}
	out := make([]imdbRSSEntry, 0, len(doc.Channel.Items))
	for _, it := range doc.Channel.Items {
		id := extractIMDbTitleID(it.GUID)
		if id == "" {
			id = extractIMDbTitleID(it.Link)
		}
		if id == "" {
			id = extractIMDbTitleID(it.Title)
		}
		if id == "" {
			continue
		}
		out = append(out, imdbRSSEntry{Title: strings.TrimSpace(it.Title), IMDbID: id})
	}
	return out, nil
}

func extractIMDbTitleID(raw string) string {
	raw = strings.ToLower(raw)
	idx := strings.Index(raw, "tt")
	for idx >= 0 {
		rest := raw[idx+2:]
		n := 0
		for n < len(rest) && unicode.IsDigit(rune(rest[n])) {
			n++
		}
		if n >= 7 {
			return "tt" + rest[:n]
		}
		next := strings.Index(raw[idx+1:], "tt")
		if next < 0 {
			return ""
		}
		idx = idx + 1 + next
	}
	return ""
}
