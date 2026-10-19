package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenetsearch"
	"github.com/labbersanon/sakms/internal/usenetsearch/predb"
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
	// Claude 2026-10-15: crawl-time quality admission kill switch (Step 4).
	// Reason: loaded fresh from usenetsearch.KeyAdmissionEnabled (default
	//   true) on every read so a PUT save never clobbers it — no request
	//   field exists to set it; see usenetsearch.KeyAdmissionEnabled.
	// Review if: a Settings UI toggle for this ships.
	AdmissionEnabled bool `json:"admissionEnabled"`
	// Claude 2026-10-19: dump path is the one writable gap-fill knob on PUT
	//   (`predbDumpPath` on the request). Other gap-fill keys stay GET-only so
	//   a Settings save cannot clobber them.
	// Reason: dumpLoop starts in NNTPPreDB.Reload (boot / this PUT / IRC-password
	//   PUT). SQL-only KV left the importer inert until a container restart.
	// Troubleshooting: dump path set but no "predb dump" log → Reload did not
	//   run (nntpPreDB nil) or the URL failed IsFetchURL.
	// Review if: a Settings UI for the rest of gap-fill ships.
	GapFill       usenetNNTPGapFill           `json:"gapFill"`
	GapFillStatus *usenetsearch.GapFillStatus `json:"gapFillStatus,omitempty"`
}

// usenetNNTPGapFill mirrors the gap-fill fields of usenetsearch.Config plus the
// PreDB provider state. IRCPasswordSet reports presence only — the password
// never leaves the server.
type usenetNNTPGapFill struct {
	NameFixEnabled         bool   `json:"namefixEnabled"`
	NameFixBodyMBPerHour   int    `json:"namefixBodyMbPerHour"`
	UnnamedTTLDays         int    `json:"unnamedTtlDays"`
	UnnamedMaxPct          int    `json:"unnamedMaxPct"`
	PreDBHTTPEnabled       bool   `json:"predbHttpEnabled"`
	PreDBHTTPURL           string `json:"predbHttpUrl"`
	PreDBIRCEnabled        bool   `json:"predbIrcEnabled"`
	IRCPasswordSet         bool   `json:"predbIrcPasswordSet"`
	PreDBDumpPath          string `json:"predbDumpPath"`
	MinCompletenessPct     int    `json:"minCompletenessPct"`
	IncompleteTTLDays      int    `json:"incompleteTtlDays"`
	RepairEnabled          bool   `json:"repairEnabled"`
	BackfillEnabled        bool   `json:"backfillEnabled"`
	BackfillDays           int    `json:"backfillDays"`
	BackfillBudgetPct      int    `json:"backfillBudgetPct"`
	CollectionDriftEnabled bool   `json:"collectionDriftEnabled"`
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
	// Claude 2026-10-19: optional; omitted leaves the dump KV unchanged.
	PredbDumpPath *string `json:"predbDumpPath"`
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
		active := resolveNNTPNativeService(svc)
		resp, err := loadNNTPNativeSettings(r.Context(), settingsStore, active)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Expand non-custom modes to the live LIST ACTIVE set for the UI.
		if errMsg := applyPackageResolution(r.Context(), &resp, active); errMsg != "" {
			http.Error(w, errMsg, http.StatusInternalServerError)
			return
		}
		if active != nil {
			st, err := active.GapFillStatus(r.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			resp.GapFillStatus = &st
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

// Claude 2026-10-15: operator escape to restore grade 1 after demotions.
// Reason: sticky grade 5 / mistaken demotes need a Settings reset without SQL.
// Troubleshooting: POST returns 503 when native index is closed (disabled).
// Review if: bulk reset-all-for-mode is needed.
// Related files: usenetsearch.ResetGroupGrade, frontend Usenet.tsx
type usenetNNTPResetGradeRequest struct {
	Mode  string `json:"mode"`
	Group string `json:"group"`
}

func postUsenetNNTPResetGradeHandler(svc *usenetsearch.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req usenetNNTPResetGradeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		mode := strings.ToLower(strings.TrimSpace(req.Mode))
		group := strings.TrimSpace(req.Group)
		if mode != "movies" && mode != "series" && mode != "adult" {
			http.Error(w, "mode must be movies, series, or adult", http.StatusBadRequest)
			return
		}
		if group == "" {
			http.Error(w, "group is required", http.StatusBadRequest)
			return
		}
		active := resolveNNTPNativeService(svc)
		if active == nil {
			http.Error(w, "native usenet search is not available", http.StatusServiceUnavailable)
			return
		}
		if err := active.ResetGroupGrade(r.Context(), mode, group); err != nil {
			if errors.Is(err, usenetsearch.ErrIndexClosed) {
				http.Error(w, "native index is not open — enable Native search first", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "mode": mode, "group": group, "grade": 1})
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
		if req.PredbDumpPath != nil {
			cur.GapFill.PreDBDumpPath = strings.TrimSpace(*req.PredbDumpPath)
			if strings.Contains(cur.GapFill.PreDBDumpPath, "://") && !predb.IsFetchURL(cur.GapFill.PreDBDumpPath) {
				http.Error(w, "predbDumpPath must be a local TSV/gz path or an http(s) dump file URL, not a search API", http.StatusBadRequest)
				return
			}
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
		// Claude 2026-10-16: non-custom packages expand to LIST ACTIVE on Save.
		// Reason: client *Groups ignored unless custom; LIST is membership.
		// Troubleshooting: empty after Save → LIST miss (keep previous groups).
		// Review if: re-rank auto-apply writes groups without going through PUT.
		if errMsg := applyPackageResolution(ctx, &cur, active); errMsg != "" {
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
		if req.PredbDumpPath != nil {
			sets = append(sets, struct{ k, v string }{usenetsearch.KeyPreDBDumpPath, cur.GapFill.PreDBDumpPath})
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
		if req.PredbDumpPath != nil {
			if rt := getNNTPPreDB(); rt != nil {
				rt.Reload(ctx)
			}
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

// Claude 2026-10-16: non-custom → every LIST ACTIVE group for the mode.
// Reason: packages.json is badges; membership is provider groups that apply.
// Troubleshooting: still 10 movies groups → LIST not called (svc nil / cache empty
//
//	rewritten from seeds). Empty LIST must keep the saved list, not shrink.
//
// Review if: custom remains the only opt-out of the rewrite.
func applyPackageResolution(ctx context.Context, cur *usenetNNTPNativeResponse, svc *usenetsearch.Service) string {
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
		if avail := modeAvailableGroups(ctx, svc, m.mode); len(avail) > 0 {
			*m.grp = usenetsearch.FormatGroups(usenetsearch.ResolveModeGroups(m.mode, avail))
		}
		*m.pkg = usenetsearch.FormatPackageStep(step)
	}
	return ""
}

func modeAvailableGroups(ctx context.Context, svc *usenetsearch.Service, mode string) []string {
	if svc == nil {
		return nil
	}
	if cached := svc.CachedAvailableGroups(mode); len(cached) > 0 {
		return cached
	}
	groups, err := svc.ListAvailableGroups(ctx, mode)
	if err != nil {
		return nil
	}
	return groups
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
	admissionEnabled, err := settingsStore.GetBool(ctx, usenetsearch.KeyAdmissionEnabled, true)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	gapFill, err := loadNNTPGapFill(ctx, settingsStore)
	if err != nil {
		return usenetNNTPNativeResponse{}, err
	}
	return usenetNNTPNativeResponse{
		GapFill:          gapFill,
		Enabled:          enabled,
		Movies:           movies,
		Series:           series,
		Adult:            adult,
		MoviesGroups:     moviesGroups,
		SeriesGroups:     seriesGroups,
		AdultGroups:      adultGroups,
		MoviesPackage:    moviesPkg,
		SeriesPackage:    seriesPkg,
		AdultPackage:     adultPkg,
		IndexDir:         indexDir,
		IndexMaxGB:       maxGB,
		WindowDays:       window,
		CrawlInterval:    crawl,
		ProbeState:       probeState,
		ProbeDetail:      probeDetail,
		AdmissionEnabled: admissionEnabled,
	}, nil
}

func configFromNNTPResponse(r usenetNNTPNativeResponse) usenetsearch.Config {
	return usenetsearch.Config{
		Enabled:          r.Enabled,
		Movies:           r.Movies,
		Series:           r.Series,
		Adult:            r.Adult,
		MoviesGroups:     usenetsearch.ParseGroups(r.MoviesGroups),
		SeriesGroups:     usenetsearch.ParseGroups(r.SeriesGroups),
		AdultGroups:      usenetsearch.ParseGroups(r.AdultGroups),
		MoviesPackage:    r.MoviesPackage,
		SeriesPackage:    r.SeriesPackage,
		AdultPackage:     r.AdultPackage,
		IndexDir:         r.IndexDir,
		IndexMaxGB:       r.IndexMaxGB,
		WindowDays:       r.WindowDays,
		CrawlInterval:    time.Duration(r.CrawlInterval) * time.Second,
		AdmissionEnabled: r.AdmissionEnabled,

		MinCompletenessPct:     r.GapFill.MinCompletenessPct,
		IncompleteTTLDays:      r.GapFill.IncompleteTTLDays,
		RepairEnabled:          r.GapFill.RepairEnabled,
		NameFixEnabled:         r.GapFill.NameFixEnabled,
		UnnamedTTLDays:         r.GapFill.UnnamedTTLDays,
		UnnamedMaxPct:          r.GapFill.UnnamedMaxPct,
		NameFixBodyMBPerHour:   r.GapFill.NameFixBodyMBPerHour,
		BackfillEnabled:        r.GapFill.BackfillEnabled,
		BackfillDays:           r.GapFill.BackfillDays,
		BackfillBudgetPct:      r.GapFill.BackfillBudgetPct,
		CollectionDriftEnabled: r.GapFill.CollectionDriftEnabled,
	}
}

// loadNNTPGapFill reads the gap-fill KV knobs. Defaults make every feature on
// (including repair, whose Go zero value is false, and the synIRC PRE feed);
// each bool key is its feature's kill switch.
// Claude 2026-10-18: PreDB IRC defaults on (random nick, no NickServ).
// Reason: synIRC confirm mail never arrived; #PreNNTmux allows unregistered
// JOIN. Kill switch remains usenet_nntp_predb_irc_enabled=false.
// Troubleshooting: no "predb irc connected" → this default did not apply or
// outbound 6697 is blocked.
// Review if: the feed is disabled per-deploy via KV.
func loadNNTPGapFill(ctx context.Context, st *settings.Store) (usenetNNTPGapFill, error) {
	var g usenetNNTPGapFill
	var err error
	bools := []struct {
		dst *bool
		key string
		def bool
	}{
		{&g.NameFixEnabled, usenetsearch.KeyNameFixEnabled, true},
		{&g.PreDBHTTPEnabled, usenetsearch.KeyPreDBHTTPEnabled, true},
		{&g.PreDBIRCEnabled, usenetsearch.KeyPreDBIRCEnabled, true},
		{&g.RepairEnabled, usenetsearch.KeyRepairEnabled, true},
		{&g.BackfillEnabled, usenetsearch.KeyBackfillEnabled, true},
		{&g.CollectionDriftEnabled, usenetsearch.KeyCollectionDriftEnabled, true},
	}
	for _, b := range bools {
		if *b.dst, err = st.GetBool(ctx, b.key, b.def); err != nil {
			return g, err
		}
	}
	ints := []struct {
		dst *int
		key string
		def int
	}{
		{&g.NameFixBodyMBPerHour, usenetsearch.KeyNameFixBodyMBPerHour, usenetsearch.DefaultNameFixBodyMBPerHour},
		{&g.UnnamedTTLDays, usenetsearch.KeyUnnamedTTLDays, usenetsearch.DefaultUnnamedTTLDays},
		{&g.UnnamedMaxPct, usenetsearch.KeyUnnamedMaxPct, usenetsearch.DefaultUnnamedMaxPct},
		{&g.MinCompletenessPct, usenetsearch.KeyMinCompletenessPct, usenetsearch.DefaultMinCompletenessPct},
		{&g.IncompleteTTLDays, usenetsearch.KeyIncompleteTTLDays, usenetsearch.DefaultIncompleteTTLDays},
		{&g.BackfillDays, usenetsearch.KeyBackfillDays, 0},
		{&g.BackfillBudgetPct, usenetsearch.KeyBackfillBudgetPct, usenetsearch.DefaultBackfillBudgetPct},
	}
	for _, i := range ints {
		if *i.dst, err = getSettingInt(ctx, st, i.key, i.def); err != nil {
			return g, err
		}
	}
	if g.PreDBHTTPURL, err = st.Get(ctx, usenetsearch.KeyPreDBHTTPURL); err != nil && !isSettingsNotFound(err) {
		return g, err
	}
	if strings.TrimSpace(g.PreDBHTTPURL) == "" {
		g.PreDBHTTPURL = predb.DefaultHTTPURL
	}
	enc, err := st.Get(ctx, usenetsearch.KeyPreDBIRCPasswordEnc)
	if err != nil && !isSettingsNotFound(err) {
		return g, err
	}
	g.IRCPasswordSet = enc != ""
	if g.PreDBDumpPath, err = st.Get(ctx, usenetsearch.KeyPreDBDumpPath); err != nil && !isSettingsNotFound(err) {
		return g, err
	}
	g.PreDBDumpPath = strings.TrimSpace(g.PreDBDumpPath)
	return g, nil
}

func isSettingsNotFound(err error) bool {
	return err == settings.ErrNotFound
}

// Claude 2026-10-16: repair non-custom modes to LIST ACTIVE membership on boot.
// Reason: packages.json shortlist was wrongly used as the crawl set.
// Troubleshooting: after upgrade, moviesGroups should match LIST ACTIVE wildmats
//   (not 10 seeds). Empty LIST keeps the previous KV list.
// Review if: custom remains the only opt-out.

// LoadUsenetSearchConfig reads native-search settings for Apply at boot / drain.
// Custom modes are left untouched; repaired lists are persisted best-effort.
func LoadUsenetSearchConfig(ctx context.Context, settingsStore *settings.Store) (usenetsearch.Config, error) {
	svc := getNNTPNativeService()
	r, err := loadNNTPNativeSettings(ctx, settingsStore, svc)
	if err != nil {
		return usenetsearch.Config{}, err
	}
	before := r
	if errMsg := applyPackageResolution(ctx, &r, svc); errMsg != "" {
		return usenetsearch.Config{}, errors.New(errMsg)
	}
	changed := r.MoviesGroups != before.MoviesGroups || r.SeriesGroups != before.SeriesGroups ||
		r.AdultGroups != before.AdultGroups || r.MoviesPackage != before.MoviesPackage ||
		r.SeriesPackage != before.SeriesPackage || r.AdultPackage != before.AdultPackage
	if changed {
		union := usenetsearch.FormatGroups(usenetsearch.MergeGroups(
			usenetsearch.ParseGroups(r.MoviesGroups),
			usenetsearch.ParseGroups(r.SeriesGroups),
			usenetsearch.ParseGroups(r.AdultGroups),
		))
		sets := []struct{ k, v string }{
			{usenetsearch.KeyMoviesGroups, r.MoviesGroups},
			{usenetsearch.KeySeriesGroups, r.SeriesGroups},
			{usenetsearch.KeyAdultGroups, r.AdultGroups},
			{usenetsearch.KeyMoviesPackage, r.MoviesPackage},
			{usenetsearch.KeySeriesPackage, r.SeriesPackage},
			{usenetsearch.KeyAdultPackage, r.AdultPackage},
			{usenetsearch.KeyGroups, union},
		}
		for _, kv := range sets {
			_ = settingsStore.Set(ctx, kv.k, kv.v)
		}
	}
	return configFromNNTPResponse(r), nil
}
