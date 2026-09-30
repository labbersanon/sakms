package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// Claude 2026-09-29: Organize Import includes Adult.
// Reason: Movies/Series still Relocate*; Adult reuses identifyAdultFiles +
//   ApplyLibraryAdult. Confirm reconstructs identity (box/sceneId/phash).
//   Unmatched+phash rows are Pending local scenes and can MOVE.
// Troubleshooting: 400 path must be within mounted roots. destRoot is
//   settings, never a client-trusted path (Kids only when it matches
//   settings; Adult has no Kids split). Body-addressed Adult uses
//   denyIfAdultLocked + mode.Build. Hasher is required for Adult scan.
// Review if: scan results are queued, or Adult gains a Kids root.

type manualImportScanRequest struct {
	Mode string `json:"mode"`
	Path string `json:"path"`
}

type manualImportItem struct {
	SourcePath          string `json:"sourcePath"`
	SourceName          string `json:"sourceName"`
	DestPath            string `json:"destPath,omitempty"`
	DestRoot            string `json:"destRoot,omitempty"`
	Title               string `json:"title,omitempty"`
	Year                int    `json:"year,omitempty"`
	TMDBID              int    `json:"tmdbId,omitempty"`
	SeasonNumber        int    `json:"seasonNumber,omitempty"`
	EpisodeNumber       int    `json:"episodeNumber,omitempty"`
	ExtraEpisodeNumbers []int  `json:"extraEpisodeNumbers,omitempty"`
	Box                 string `json:"box,omitempty"`
	SceneID             string `json:"sceneId,omitempty"`
	Studio              string `json:"studio,omitempty"`
	Date                string `json:"date,omitempty"`
	PHash               string `json:"phash,omitempty"`
	DurationSeconds     int    `json:"durationSeconds,omitempty"`
	Status              string `json:"status"`
	Reason              string `json:"reason,omitempty"`
	Mode                string `json:"mode"`
}

type manualImportScanResponse struct {
	Path     string             `json:"path"`
	DestRoot string             `json:"destRoot"`
	Items    []manualImportItem `json:"items"`
}

type manualImportApplyRequest struct {
	Mode  string             `json:"mode"`
	Items []manualImportItem `json:"items"`
}

type manualImportApplyResult struct {
	SourcePath string `json:"sourcePath"`
	DestPath   string `json:"destPath,omitempty"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

type manualImportApplyResponse struct {
	Results []manualImportApplyResult `json:"results"`
}

func parseImportMode(raw string) (mode.Mode, error) {
	m := mode.Mode(strings.TrimSpace(raw))
	switch m {
	case mode.Movies, mode.Series, mode.Adult:
		return m, nil
	default:
		return "", errors.New("mode must be movies, series, or adult")
	}
}

func libraryDestRoot(r *http.Request, settingsStore *settings.Store, m mode.Mode) (string, error) {
	key, ok := libraryRootFolderKey(m)
	if !ok {
		return "", errors.New("no library root folder key for this mode")
	}
	path, err := settingsStore.Get(r.Context(), key)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		switch m {
		case mode.Series:
			return "", errors.New("no Series library root folder configured yet — add one in Settings first")
		case mode.Adult:
			return "", errors.New("no Adult library root folder configured yet — add one in Settings first")
		default:
			return "", errors.New("no Movies library root folder configured yet — add one in Settings first")
		}
	}
	return path, nil
}

func kidsDestRoot(r *http.Request, settingsStore *settings.Store, m mode.Mode) string {
	key, ok := m.KidsRootPathKey()
	if !ok {
		return ""
	}
	path, err := settingsStore.Get(r.Context(), key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(path)
}

// importApplyDestRoot picks the library root Apply will MOVE into. The
// client destRoot is honored only when it is the configured Kids root;
// anything else uses the mode library root.
func importApplyDestRoot(requested, libraryRoot, kidsRoot string) string {
	if kidsRoot != "" && requested == kidsRoot {
		return kidsRoot
	}
	return libraryRoot
}

func importItemToProposal(item manualImportItem, m mode.Mode, destRoot string) (proposals.Proposal, error) {
	src, err := resolveBrowsablePath(item.SourcePath)
	if err != nil {
		return proposals.Proposal{}, err
	}
	if m == mode.Adult {
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Box) == "" || strings.TrimSpace(item.SceneID) == "" {
			return proposals.Proposal{}, errors.New("only identified scenes can be imported")
		}
		p := proposals.Proposal{
			Mode:            mode.Adult,
			Workflow:        proposals.Rename,
			Status:          proposals.Pending,
			SourceName:      item.SourceName,
			SourcePath:      src,
			RootFolderPath:  destRoot,
			Title:           strings.TrimSpace(item.Title),
			Studio:          item.Studio,
			Date:            item.Date,
			GiveBackBox:     strings.TrimSpace(item.Box),
			GiveBackSceneID: strings.TrimSpace(item.SceneID),
			PHash:           item.PHash,
			DurationSeconds: item.DurationSeconds,
		}
		if p.SourceName == "" {
			p.SourceName = src
		}
		return p, nil
	}
	if strings.TrimSpace(item.Title) == "" || item.TMDBID == 0 {
		return proposals.Proposal{}, errors.New("only identified titles can be imported")
	}
	if m == mode.Series && item.EpisodeNumber < 1 {
		return proposals.Proposal{}, errors.New("series import needs a season and episode")
	}
	p := proposals.Proposal{
		Mode:                m,
		Workflow:            proposals.Rename,
		Status:              proposals.Pending,
		SourceName:          item.SourceName,
		SourcePath:          src,
		RootFolderPath:      destRoot,
		Title:               strings.TrimSpace(item.Title),
		TMDBID:              item.TMDBID,
		Year:                item.Year,
		SeasonNumber:        item.SeasonNumber,
		EpisodeNumber:       item.EpisodeNumber,
		ExtraEpisodeNumbers: item.ExtraEpisodeNumbers,
	}
	if p.SourceName == "" {
		p.SourceName = src
	}
	return p, nil
}

func manualImportScanHandler(
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
	prober dedup.Prober,
	videoHasher rename.PHasher,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req manualImportScanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		m, err := parseImportMode(req.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if m == mode.Adult && denyIfAdultLocked(w, r) {
			return
		}
		src, err := resolveBrowsablePath(req.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		destRoot, err := libraryDestRoot(r, settingsStore, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sess, err := mode.Build(r.Context(), connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		matchCfg, err := resolveMatchConfig(r.Context(), settingsStore, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		preset, err := resolveNamingPreset(r.Context(), settingsStore, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var found []proposals.Proposal
		switch m {
		case mode.Movies:
			found, err = rename.ScanImportMovies(r.Context(), sess, libStore, src, destRoot, matchCfg, prober)
		case mode.Series:
			found, err = rename.ScanImportSeries(r.Context(), sess, libStore, src, destRoot, matchCfg, prober)
		default:
			found, err = rename.ScanImportAdult(r.Context(), sess, libStore, videoHasher, prober, src, destRoot, matchCfg)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		items := make([]manualImportItem, 0, len(found))
		for _, p := range found {
			items = append(items, manualImportItem{
				SourcePath:          p.SourcePath,
				SourceName:          p.SourceName,
				DestPath:            rename.ImportDestPath(p, preset),
				DestRoot:            p.RootFolderPath,
				Title:               p.Title,
				Year:                p.Year,
				TMDBID:              p.TMDBID,
				SeasonNumber:        p.SeasonNumber,
				EpisodeNumber:       p.EpisodeNumber,
				ExtraEpisodeNumbers: p.ExtraEpisodeNumbers,
				Box:                 p.GiveBackBox,
				SceneID:             p.GiveBackSceneID,
				Studio:              p.Studio,
				Date:                p.Date,
				PHash:               p.PHash,
				DurationSeconds:     p.DurationSeconds,
				Status:              string(p.Status),
				Reason:              p.Reason,
				Mode:                string(p.Mode),
			})
		}
		writeJSON(w, manualImportScanResponse{Path: src, DestRoot: destRoot, Items: items})
	}
}

func manualImportApplyHandler(
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
	prober dedup.Prober,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req manualImportApplyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		m, err := parseImportMode(req.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if m == mode.Adult && denyIfAdultLocked(w, r) {
			return
		}
		if len(req.Items) == 0 {
			http.Error(w, "no files selected", http.StatusBadRequest)
			return
		}
		libraryRoot, err := libraryDestRoot(r, settingsStore, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		kidsRoot := kidsDestRoot(r, settingsStore, m)
		sess, err := mode.Build(r.Context(), connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		preset, err := resolveNamingPreset(r.Context(), settingsStore, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tier := string(autoGrabTier(r.Context(), settingsStore, m))

		results := make([]manualImportApplyResult, 0, len(req.Items))
		var changes []mode.PathChange
		for _, item := range req.Items {
			destRoot := importApplyDestRoot(item.DestRoot, libraryRoot, kidsRoot)
			p, convErr := importItemToProposal(item, m, destRoot)
			if convErr != nil {
				results = append(results, manualImportApplyResult{SourcePath: item.SourcePath, Error: convErr.Error()})
				continue
			}
			var one []mode.PathChange
			var applyErr error
			var dest string
			switch m {
			case mode.Movies:
				_, one, applyErr = rename.ApplyLibrary(r.Context(), libStore, p, preset, tier, prober)
			case mode.Series:
				_, one, applyErr = rename.ApplyLibrarySeries(r.Context(), libStore, sess.TMDB, sess.TVDB, p, preset, tier, prober)
			default:
				_, _, one, applyErr = rename.ApplyLibraryAdult(r.Context(), sess, libStore, p, tier, prober)
			}
			if applyErr != nil {
				results = append(results, manualImportApplyResult{SourcePath: p.SourcePath, Error: applyErr.Error()})
				if len(one) > 0 {
					changes = append(changes, one...)
				}
				continue
			}
			if len(one) > 0 {
				dest = one[len(one)-1].Path
				changes = append(changes, one...)
			} else {
				dest = rename.ImportDestPath(p, preset)
			}
			results = append(results, manualImportApplyResult{SourcePath: p.SourcePath, DestPath: dest, OK: true})
		}
		if len(changes) > 0 {
			sess.NotifyPlayers(r.Context(), changes)
		}
		writeJSON(w, manualImportApplyResponse{Results: results})
	}
}
