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

// Claude 2026-09-18: per-mode group multi-select + LIST ACTIVE picker.
// Reason: crawl = union of selections; search = active mode's list only.
// Troubleshooting: GET .../groups empty → no subscriptions or LIST fail.
// Review if: Discover title filter ships (groups endpoint stays).

type usenetNNTPNativeResponse struct {
	Enabled       bool   `json:"enabled"`
	Movies        bool   `json:"movies"`
	Series        bool   `json:"series"`
	Adult         bool   `json:"adult"`
	MoviesGroups  string `json:"moviesGroups"`
	SeriesGroups  string `json:"seriesGroups"`
	AdultGroups   string `json:"adultGroups"`
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
	MoviesGroups  *string `json:"moviesGroups"`
	SeriesGroups  *string `json:"seriesGroups"`
	AdultGroups   *string `json:"adultGroups"`
	IndexDir      *string `json:"indexDir"`
	IndexMaxGB    *int    `json:"indexMaxGb"`
	WindowDays    *int    `json:"windowDays"`
	CrawlInterval *int    `json:"crawlIntervalSeconds"`
}

type usenetNNTPGroupsResponse struct {
	Mode   string   `json:"mode"`
	Groups []string `json:"groups"`
}

func resolveNNTPNativeService(svc *usenetsearch.Service) *usenetsearch.Service {
	if svc != nil {
		return svc
	}
	return getNNTPNativeService()
}

func getUsenetNNTPNativeHandler(settingsStore *settings.Store, svc *usenetsearch.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := loadNNTPNativeSettings(r.Context(), settingsStore, resolveNNTPNativeService(svc))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, resp)
	}
}

func getUsenetNNTPGroupsHandler(svc *usenetsearch.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
		if mode != "movies" && mode != "series" && mode != "adult" {
			http.Error(w, "mode query must be movies, series, or adult", http.StatusBadRequest)
			return
		}
		active := resolveNNTPNativeService(svc)
		if active == nil {
			writeJSON(w, usenetNNTPGroupsResponse{Mode: mode, Groups: []string{}})
			return
		}
		groups, err := active.ListAvailableGroups(r.Context(), mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if groups == nil {
			groups = []string{}
		}
		writeJSON(w, usenetNNTPGroupsResponse{Mode: mode, Groups: groups})
	}
}

func putUsenetNNTPNativeHandler(settingsStore *settings.Store, svc *usenetsearch.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		active := resolveNNTPNativeService(svc)
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
		if req.MoviesGroups != nil {
			cur.MoviesGroups = *req.MoviesGroups
		}
		if req.SeriesGroups != nil {
			cur.SeriesGroups = *req.SeriesGroups
		}
		if req.AdultGroups != nil {
			cur.AdultGroups = *req.AdultGroups
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
			if errMsg := validatePerModeGroups(cur); errMsg != "" {
				http.Error(w, errMsg, http.StatusBadRequest)
				return
			}
		}
		union := usenetsearch.FormatGroups(usenetsearch.MergeGroups(
			usenetsearch.ParseGroups(cur.MoviesGroups),
			usenetsearch.ParseGroups(cur.SeriesGroups),
			usenetsearch.ParseGroups(cur.AdultGroups),
		))
		sets := []struct{ k, v string }{
			{usenetsearch.KeyNativeEnabled, strconv.FormatBool(cur.Enabled)},
			{usenetsearch.KeyMoviesEnabled, strconv.FormatBool(cur.Movies)},
			{usenetsearch.KeySeriesEnabled, strconv.FormatBool(cur.Series)},
			{usenetsearch.KeyAdultEnabled, strconv.FormatBool(cur.Adult)},
			{usenetsearch.KeyMoviesGroups, cur.MoviesGroups},
			{usenetsearch.KeySeriesGroups, cur.SeriesGroups},
			{usenetsearch.KeyAdultGroups, cur.AdultGroups},
			{usenetsearch.KeyGroups, union}, // legacy mirror for soft-migrate readers
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

func validatePerModeGroups(cur usenetNNTPNativeResponse) string {
	if cur.Movies && len(usenetsearch.ParseGroups(cur.MoviesGroups)) == 0 {
		return "select at least one Movies newsgroup when Movies native search is enabled"
	}
	if cur.Series && len(usenetsearch.ParseGroups(cur.SeriesGroups)) == 0 {
		return "select at least one Series newsgroup when Series native search is enabled"
	}
	if cur.Adult && len(usenetsearch.ParseGroups(cur.AdultGroups)) == 0 {
		return "select at least one Adult newsgroup when Adult native search is enabled"
	}
	return ""
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
	moviesGroups, err := settingsStore.Get(ctx, usenetsearch.KeyMoviesGroups)
	if err != nil && !isSettingsNotFound(err) {
		return usenetNNTPNativeResponse{}, err
	}
	seriesGroups, err := settingsStore.Get(ctx, usenetsearch.KeySeriesGroups)
	if err != nil && !isSettingsNotFound(err) {
		return usenetNNTPNativeResponse{}, err
	}
	adultGroups, err := settingsStore.Get(ctx, usenetsearch.KeyAdultGroups)
	if err != nil && !isSettingsNotFound(err) {
		return usenetNNTPNativeResponse{}, err
	}
	legacy, err := settingsStore.Get(ctx, usenetsearch.KeyGroups)
	if err != nil && !isSettingsNotFound(err) {
		return usenetNNTPNativeResponse{}, err
	}
	// Soft-migrate: pre-per-mode installs only had KeyGroups.
	if moviesGroups == "" && seriesGroups == "" && adultGroups == "" && legacy != "" {
		moviesGroups, seriesGroups, adultGroups = legacy, legacy, legacy
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
		MoviesGroups:  moviesGroups,
		SeriesGroups:  seriesGroups,
		AdultGroups:   adultGroups,
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
		MoviesGroups:  usenetsearch.ParseGroups(r.MoviesGroups),
		SeriesGroups:  usenetsearch.ParseGroups(r.SeriesGroups),
		AdultGroups:   usenetsearch.ParseGroups(r.AdultGroups),
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
