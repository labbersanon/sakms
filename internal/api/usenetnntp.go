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
	MoviesPackage string `json:"moviesPackage"`
	SeriesPackage string `json:"seriesPackage"`
	AdultPackage  string `json:"adultPackage"`
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
	MoviesPackage *string `json:"moviesPackage"`
	SeriesPackage *string `json:"seriesPackage"`
	AdultPackage  *string `json:"adultPackage"`
	IndexDir      *string `json:"indexDir"`
	IndexMaxGB    *int    `json:"indexMaxGb"`
	WindowDays    *int    `json:"windowDays"`
	CrawlInterval *int    `json:"crawlIntervalSeconds"`
}

type usenetNNTPGroupsResponse struct {
	Mode   string   `json:"mode"`
	Groups []string `json:"groups"`
	// Claude 2026-10-15: 5-step package slider data (reliability → breadth).
	// Reason: replace individual-group defaults with cumulative curated steps.
	// Troubleshooting: steps empty → provider lacks package groups.
	// Review if: slice 2 wires Settings slider; recommended shim removed.
	Recommended []usenetNNTPGroupItem            `json:"recommended"`
	Items       []usenetNNTPGroupItem            `json:"items"`
	Steps       map[string]usenetNNTPStepInfo    `json:"steps"`
	Packages    map[string][]usenetNNTPGroupItem `json:"packages"`
}

type usenetNNTPStepInfo struct {
	Step            int `json:"step"`
	GroupCount      int `json:"groupCount"`
	CumulativeCount int `json:"cumulativeCount"`
}

type usenetNNTPGroupItem struct {
	Name        string `json:"name"`
	Recommended bool   `json:"recommended"`
	Tier        string `json:"tier,omitempty"` // shim: core|extra from step 1|2
	Step        int    `json:"step,omitempty"`
	Pinned      bool   `json:"pinned,omitempty"`
	Grade       int    `json:"grade,omitempty"` // 1=most reliable … 5=least (sticky)
	Note        string `json:"note,omitempty"`
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
		emptySteps := map[string]usenetNNTPStepInfo{}
		emptyPkgs := map[string][]usenetNNTPGroupItem{}
		for s := usenetsearch.PackageStepMin; s <= usenetsearch.PackageStepMax; s++ {
			key := strconv.Itoa(s)
			emptySteps[key] = usenetNNTPStepInfo{Step: s}
			emptyPkgs[key] = []usenetNNTPGroupItem{}
		}
		if active == nil {
			writeJSON(w, usenetNNTPGroupsResponse{
				Mode: mode, Groups: []string{},
				Recommended: []usenetNNTPGroupItem{}, Items: []usenetNNTPGroupItem{},
				Steps: emptySteps, Packages: emptyPkgs,
			})
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
		pkgSeeds := usenetsearch.FilterPackagesToProvider(usenetsearch.PackagesForMode(mode), groups)
		byName := map[string]usenetNNTPGroupItem{}
		packages := map[string][]usenetNNTPGroupItem{}
		steps := map[string]usenetNNTPStepInfo{}
		for s := usenetsearch.PackageStepMin; s <= usenetsearch.PackageStepMax; s++ {
			packages[strconv.Itoa(s)] = []usenetNNTPGroupItem{}
		}
		for _, s := range pkgSeeds {
			tier := ""
			rec := s.Step <= 2
			if s.Step == 1 {
				tier = usenetsearch.SeedTierCore
			} else if s.Step == 2 {
				tier = usenetsearch.SeedTierExtra
			}
			it := usenetNNTPGroupItem{
				Name: s.Name, Recommended: rec, Tier: tier,
				Step: s.Step, Pinned: s.Pinned, Note: s.Note,
			}
			byName[s.Name] = it
			key := strconv.Itoa(s.Step)
			packages[key] = append(packages[key], it)
		}
		for s := usenetsearch.PackageStepMin; s <= usenetsearch.PackageStepMax; s++ {
			key := strconv.Itoa(s)
			cumN := 0
			for _, p := range pkgSeeds {
				if p.Step <= s {
					cumN++
				}
			}
			steps[key] = usenetNNTPStepInfo{
				Step:            s,
				GroupCount:      len(packages[key]),
				CumulativeCount: cumN,
			}
		}
		rec := make([]usenetNNTPGroupItem, 0)
		for _, s := range usenetsearch.FilterSeedsToProvider(usenetsearch.RecommendedForMode(mode), groups) {
			if it, ok := byName[s.Name]; ok {
				rec = append(rec, it)
			}
		}
		items := make([]usenetNNTPGroupItem, 0, len(groups))
		gradeByName := map[string]int{}
		if active != nil {
			if grades, gerr := active.ListGroupGrades(r.Context(), mode); gerr == nil {
				for _, gg := range grades {
					gradeByName[gg.Group] = gg.Grade
				}
			}
		}
		for _, g := range groups {
			it, ok := byName[g]
			if !ok {
				it = usenetNNTPGroupItem{Name: g}
			}
			if gr, ok := gradeByName[g]; ok {
				it.Grade = gr
			} else {
				it.Grade = 1
			}
			items = append(items, it)
		}
		writeJSON(w, usenetNNTPGroupsResponse{
			Mode: mode, Groups: groups, Recommended: rec, Items: items,
			Steps: steps, Packages: packages,
		})
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
		if req.MoviesPackage != nil {
			cur.MoviesPackage = strings.TrimSpace(*req.MoviesPackage)
		}
		if req.SeriesPackage != nil {
			cur.SeriesPackage = strings.TrimSpace(*req.SeriesPackage)
		}
		if req.AdultPackage != nil {
			cur.AdultPackage = strings.TrimSpace(*req.AdultPackage)
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
		// Claude 2026-10-15: non-custom packages expand server-side on Save.
		// Reason: slider owns the list; client *Groups ignored unless custom.
		// Troubleshooting: empty after Save → provider filter removed all names.
		// Review if: re-rank auto-apply writes groups without going through PUT.
		if errMsg := applyPackageResolution(&cur, active); errMsg != "" {
			http.Error(w, errMsg, http.StatusBadRequest)
			return
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
			{usenetsearch.KeyMoviesPackage, cur.MoviesPackage},
			{usenetsearch.KeySeriesPackage, cur.SeriesPackage},
			{usenetsearch.KeyAdultPackage, cur.AdultPackage},
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
			// Lowering the ceiling prunes index rows for demoted-beyond groups.
			_ = active.PruneBeyondCeilings(ctx)
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

// applyPackageResolution expands non-custom packages into *Groups on cur.
func applyPackageResolution(cur *usenetNNTPNativeResponse, svc *usenetsearch.Service) string {
	type modePack struct {
		mode string
		pkg  *string
		grp  *string
	}
	modes := []modePack{
		{"movies", &cur.MoviesPackage, &cur.MoviesGroups},
		{"series", &cur.SeriesPackage, &cur.SeriesGroups},
		{"adult", &cur.AdultPackage, &cur.AdultGroups},
	}
	for _, m := range modes {
		pkg := strings.TrimSpace(*m.pkg)
		if pkg == "" {
			pkg = usenetsearch.FormatPackageStep(usenetsearch.DefaultPackageStep)
			*m.pkg = pkg
		}
		step, custom, ok := usenetsearch.ParsePackageValue(pkg)
		if !ok {
			return "invalid " + m.mode + " package (want 1–5 or custom)"
		}
		if custom {
			*m.pkg = usenetsearch.PackageCustom
			continue
		}
		var avail []string
		if svc != nil {
			avail = svc.CachedAvailableGroups(m.mode)
		}
		*m.grp = usenetsearch.FormatGroups(usenetsearch.ResolvePackageGroups(m.mode, step, avail))
		*m.pkg = usenetsearch.FormatPackageStep(step)
	}
	return ""
}

func loadOrInferPackage(ctx context.Context, settingsStore *settings.Store, key, mode, groups string, svc *usenetsearch.Service) (string, error) {
	raw, err := settingsStore.Get(ctx, key)
	if err != nil && !isSettingsNotFound(err) {
		return "", err
	}
	if strings.TrimSpace(raw) != "" {
		if _, _, ok := usenetsearch.ParsePackageValue(raw); ok {
			return strings.TrimSpace(raw), nil
		}
	}
	var avail []string
	if svc != nil {
		avail = svc.CachedAvailableGroups(mode)
	}
	return usenetsearch.InferPackageFromGroups(mode, usenetsearch.ParseGroups(groups), avail), nil
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
	moviesPkg, err := loadOrInferPackage(ctx, settingsStore, usenetsearch.KeyMoviesPackage, "movies", moviesGroups, svc)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	seriesPkg, err := loadOrInferPackage(ctx, settingsStore, usenetsearch.KeySeriesPackage, "series", seriesGroups, svc)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	adultPkg, err := loadOrInferPackage(ctx, settingsStore, usenetsearch.KeyAdultPackage, "adult", adultGroups, svc)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	return usenetNNTPNativeResponse{
		Enabled:       enabled,
		Movies:        movies,
		Series:        series,
		Adult:         adult,
		MoviesGroups:  moviesGroups,
		SeriesGroups:  seriesGroups,
		AdultGroups:   adultGroups,
		MoviesPackage: moviesPkg,
		SeriesPackage: seriesPkg,
		AdultPackage:  adultPkg,
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
		MoviesPackage: r.MoviesPackage,
		SeriesPackage: r.SeriesPackage,
		AdultPackage:  r.AdultPackage,
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
