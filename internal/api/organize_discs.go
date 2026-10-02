package api

// Claude 2026-10-02: Organize Discs identify + existing-library fill.
// Reason: ISOs are manual-import-only; TMDB runs after the operator picks
//   one ISO. Existing movie FilePath / episode FilePath start unchecked.
// Troubleshooting: empty hits — TMDB search used SearchQueries(volume, src).
// Review if: extract relocates MKVs into the library dest.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/disc"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// inspectDiscFn is swappable in tests.
var inspectDiscFn = disc.Inspect

func organizeDiscIdentifyHandler(
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.OrganizeDiscIdentifyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		src, err := resolveBrowsablePath(req.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !disc.IsDiscImage(src) {
			http.Error(w, "path must be an .iso or .img file", http.StatusBadRequest)
			return
		}
		m, err := inspectDiscFn(r.Context(), src)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		works := discWorksFromMap(m)
		queries := disc.SearchQueries(m.Volume, src)
		hits := identifyDiscHits(r, httpClient, connStore, scStore, settingsStore, libStore, queries)
		writeJSON(w, apidto.OrganizeDiscIdentifyResponse{
			Path: src, Volume: m.Volume, Queries: queries, Works: works, Hits: hits,
		})
	}
}

func discWorksFromMap(m *disc.Map) []apidto.OrganizeDiscWork {
	if m == nil {
		return nil
	}
	titles := disc.AssignRoles(m.Titles)
	byN := map[int]disc.Title{}
	for _, t := range titles {
		byN[t.N] = t
	}
	var out []apidto.OrganizeDiscWork
	for _, w := range disc.PlanWorks(titles) {
		dur := byN[w.Title].DurationS
		if w.ChapterStart > 0 && byN[w.Title].Chapters > 0 {
			dur = byN[w.Title].DurationS / float64(byN[w.Title].Chapters)
		}
		out = append(out, apidto.OrganizeDiscWork{
			Name: w.Name, Title: w.Title, Chapter: w.ChapterStart,
			DurationS: dur, Role: w.Role,
		})
	}
	return out
}

func identifyDiscHits(
	r *http.Request,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
	queries []string,
) []apidto.OrganizeDiscHit {
	if len(queries) == 0 || connStore == nil {
		return nil
	}
	sess, err := mode.Build(r.Context(), connStore, scStore, settingsStore, httpClient, nil, mode.Movies)
	if err != nil || sess == nil || sess.TMDB == nil {
		return nil
	}
	seen := map[string]bool{}
	var hits []apidto.OrganizeDiscHit
	add := func(modeName string, it tmdb.Item) {
		key := modeName + ":" + strconv.Itoa(it.ID)
		if it.ID == 0 || seen[key] {
			return
		}
		seen[key] = true
		hit := apidto.OrganizeDiscHit{
			Mode: modeName, TMDBID: it.ID, Title: it.Title, Year: yearFromDate(it.ReleaseDate),
		}
		if libStore != nil {
			fillDiscExisting(r.Context(), libStore, &hit)
		}
		hits = append(hits, hit)
	}
	for _, q := range queries {
		movies, err := sess.TMDB.SearchMovies(r.Context(), q)
		if err == nil {
			for i, it := range movies {
				if i >= 5 {
					break
				}
				add(string(mode.Movies), it)
			}
		}
		shows, err := sess.TMDB.SearchTV(r.Context(), q)
		if err == nil {
			for i, it := range shows {
				if i >= 5 {
					break
				}
				add(string(mode.Series), it)
			}
		}
		if len(hits) >= 12 {
			break
		}
	}
	return hits
}

func fillDiscExisting(ctx context.Context, libStore *library.Store, hit *apidto.OrganizeDiscHit) {
	switch hit.Mode {
	case string(mode.Movies):
		item, err := libStore.GetByTMDBID(ctx, mode.Movies, hit.TMDBID)
		if err != nil || item.FilePath == "" {
			return
		}
		hit.ExistingPath = item.FilePath
		hit.ExistingTitle = item.Title
	case string(mode.Series):
		series, err := libStore.GetSeriesByTMDBID(ctx, hit.TMDBID)
		if err != nil {
			return
		}
		eps, err := libStore.ListEpisodes(ctx, series.ID)
		if err != nil {
			return
		}
		for _, ep := range eps {
			if strings.TrimSpace(ep.FilePath) == "" {
				continue
			}
			hit.Episodes = append(hit.Episodes, apidto.OrganizeDiscExistingEpisode{
				Season: ep.SeasonNumber, Episode: ep.EpisodeNumber,
				Title: ep.Title, Path: ep.FilePath,
			})
		}
	}
}

func discOnlyNames(items []apidto.OrganizeDiscUnpackItem) []string {
	var names []string
	for _, it := range items {
		if strings.TrimSpace(it.Name) != "" {
			names = append(names, it.Name)
		}
	}
	return names
}

func applyDiscConflicts(ctx context.Context, libStore *library.Store, items []apidto.OrganizeDiscUnpackItem, hit *apidto.OrganizeDiscHit) error {
	if libStore == nil || hit == nil {
		return nil
	}
	for _, it := range items {
		if it.Conflict != "replace" {
			continue
		}
		path := ""
		if hit.Mode == string(mode.Movies) {
			path = hit.ExistingPath
		}
		if hit.Mode == string(mode.Series) && it.SeasonNumber >= 0 && it.EpisodeNumber >= 1 {
			for _, ep := range hit.Episodes {
				if ep.Season == it.SeasonNumber && ep.Episode == it.EpisodeNumber {
					path = ep.Path
					break
				}
			}
		}
		if path == "" {
			continue
		}
		if err := removeExistingLibraryFile(ctx, libStore, path); err != nil {
			return err
		}
	}
	return nil
}

func removeExistingLibraryFile(ctx context.Context, libStore *library.Store, path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, err := libStore.ForgetPath(ctx, path)
	return err
}
