package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

// Claude 2026-09-17: settings API for native NNTP discovery.
// Reason: manual groups + required index dir; feature default off.
// Troubleshooting: probe_state degraded → check index dir / groups / subscriptions.
// Review if: per-mode group lists are added.

type usenetNNTPNativeResponse struct {
	Enabled       bool   `json:"enabled"`
	Movies        bool   `json:"movies"`
	Series        bool   `json:"series"`
	Adult         bool   `json:"adult"`
	Groups        string `json:"groups"`
	IndexDir      string `json:"indexDir"`
	IndexMaxGB    int    `json:"indexMaxGb"`
	WindowDays    int    `json:"windowDays"`
	CrawlInterval int    `json:"crawlIntervalSeconds"`
	ProbeState    string `json:"probeState"`
	ProbeDetail   string `json:"probeDetail"`
}

type usenetNNTPNativeRequest struct {
	Enabled       *bool   `json:"enabled"`
	Movies        *bool   `json:"movies"`
	Series        *bool   `json:"series"`
	Adult         *bool   `json:"adult"`
	Groups        *string `json:"groups"`
	IndexDir      *string `json:"indexDir"`
	IndexMaxGB    *int    `json:"indexMaxGb"`
	WindowDays    *int    `json:"windowDays"`
	CrawlInterval *int    `json:"crawlIntervalSeconds"`
}

func getUsenetNNTPNativeHandler(settingsStore *settings.Store, svc *usenetsearch.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		active := svc
		if active == nil {
			active = getNNTPNativeService()
		}
		resp, err := loadNNTPNativeSettings(r.Context(), settingsStore, active)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, resp)
	}
}

func putUsenetNNTPNativeHandler(settingsStore *settings.Store, svc *usenetsearch.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		active := svc
		if active == nil {
			active = getNNTPNativeService()
		}
		var req usenetNNTPNativeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		cur, err := loadNNTPNativeSettings(ctx, settingsStore, active)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if req.Enabled != nil {
			cur.Enabled = *req.Enabled
		}
		if req.Movies != nil {
			cur.Movies = *req.Movies
		}
		if req.Series != nil {
			cur.Series = *req.Series
		}
		if req.Adult != nil {
			cur.Adult = *req.Adult
		}
		if req.Groups != nil {
			cur.Groups = *req.Groups
		}
		if req.IndexDir != nil {
			cur.IndexDir = strings.TrimSpace(*req.IndexDir)
		}
		if req.IndexMaxGB != nil {
			cur.IndexMaxGB = *req.IndexMaxGB
		}
		if req.WindowDays != nil {
			cur.WindowDays = *req.WindowDays
		}
		if req.CrawlInterval != nil {
			cur.CrawlInterval = *req.CrawlInterval
		}
		if cur.IndexMaxGB <= 0 {
			cur.IndexMaxGB = usenetsearch.DefaultIndexMaxGB
		}
		if cur.WindowDays <= 0 {
			cur.WindowDays = usenetsearch.DefaultWindowDays
		}
		if cur.CrawlInterval < 0 {
			cur.CrawlInterval = 0
		}
		if cur.Enabled {
			if msg := usenetsearch.ValidateIndexDir(cur.IndexDir); msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
			if len(usenetsearch.ParseGroups(cur.Groups)) == 0 {
				http.Error(w, "at least one newsgroup is required when native search is enabled", http.StatusBadRequest)
				return
			}
		}
		sets := []struct{ k, v string }{
			{usenetsearch.KeyNativeEnabled, strconv.FormatBool(cur.Enabled)},
			{usenetsearch.KeyMoviesEnabled, strconv.FormatBool(cur.Movies)},
			{usenetsearch.KeySeriesEnabled, strconv.FormatBool(cur.Series)},
			{usenetsearch.KeyAdultEnabled, strconv.FormatBool(cur.Adult)},
			{usenetsearch.KeyGroups, cur.Groups},
			{usenetsearch.KeyIndexDir, cur.IndexDir},
			{usenetsearch.KeyIndexMaxGB, strconv.Itoa(cur.IndexMaxGB)},
			{usenetsearch.KeyWindowDays, strconv.Itoa(cur.WindowDays)},
			{usenetsearch.KeyCrawlInterval, strconv.Itoa(cur.CrawlInterval)},
		}
		for _, kv := range sets {
			if err := settingsStore.Set(ctx, kv.k, kv.v); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if active != nil {
			_ = active.Apply(configFromNNTPResponse(cur))
			probe := active.ProbeSnapshot()
			_ = settingsStore.Set(ctx, usenetsearch.KeyProbeState, probe.State)
			_ = settingsStore.Set(ctx, usenetsearch.KeyProbeDetail, probe.Detail)
			cur.ProbeState = probe.State
			cur.ProbeDetail = probe.Detail
		}
		writeJSON(w, cur)
	}
}

func loadNNTPNativeSettings(ctx context.Context, settingsStore *settings.Store, svc *usenetsearch.Service) (usenetNNTPNativeResponse, error) {
	enabled, err := settingsStore.GetBool(ctx, usenetsearch.KeyNativeEnabled, false)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	movies, err := settingsStore.GetBool(ctx, usenetsearch.KeyMoviesEnabled, false)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	series, err := settingsStore.GetBool(ctx, usenetsearch.KeySeriesEnabled, false)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	adult, err := settingsStore.GetBool(ctx, usenetsearch.KeyAdultEnabled, false)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	groups, err := settingsStore.Get(ctx, usenetsearch.KeyGroups)
	if err != nil && !isSettingsNotFound(err) {
		return usenetNNTPNativeResponse{}, err
	}
	indexDir, err := settingsStore.Get(ctx, usenetsearch.KeyIndexDir)
	if err != nil && !isSettingsNotFound(err) {
		return usenetNNTPNativeResponse{}, err
	}
	maxGB, err := getSettingInt(ctx, settingsStore, usenetsearch.KeyIndexMaxGB, usenetsearch.DefaultIndexMaxGB)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	window, err := getSettingInt(ctx, settingsStore, usenetsearch.KeyWindowDays, usenetsearch.DefaultWindowDays)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	crawl, err := getSettingInt(ctx, settingsStore, usenetsearch.KeyCrawlInterval, 0)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	probeState, _ := settingsStore.Get(ctx, usenetsearch.KeyProbeState)
	probeDetail, _ := settingsStore.Get(ctx, usenetsearch.KeyProbeDetail)
	if svc != nil {
		p := svc.ProbeSnapshot()
		if p.State != "" {
			probeState = p.State
			probeDetail = p.Detail
		}
	}
	if probeState == "" {
		probeState = "unknown"
	}
	return usenetNNTPNativeResponse{
		Enabled:       enabled,
		Movies:        movies,
		Series:        series,
		Adult:         adult,
		Groups:        groups,
		IndexDir:      indexDir,
		IndexMaxGB:    maxGB,
		WindowDays:    window,
		CrawlInterval: crawl,
		ProbeState:    probeState,
		ProbeDetail:   probeDetail,
	}, nil
}

func configFromNNTPResponse(r usenetNNTPNativeResponse) usenetsearch.Config {
	return usenetsearch.Config{
		Enabled:       r.Enabled,
		Movies:        r.Movies,
		Series:        r.Series,
		Adult:         r.Adult,
		Groups:        usenetsearch.ParseGroups(r.Groups),
		IndexDir:      r.IndexDir,
		IndexMaxGB:    r.IndexMaxGB,
		WindowDays:    r.WindowDays,
		CrawlInterval: time.Duration(r.CrawlInterval) * time.Second,
	}
}

func isSettingsNotFound(err error) bool {
	return err == settings.ErrNotFound
}

// LoadUsenetSearchConfig reads native-search settings for Apply at boot / drain.
func LoadUsenetSearchConfig(ctx context.Context, settingsStore *settings.Store) (usenetsearch.Config, error) {
	r, err := loadNNTPNativeSettings(ctx, settingsStore, nil)
	if err != nil {
		return usenetsearch.Config{}, err
	}
	return configFromNNTPResponse(r), nil
}
