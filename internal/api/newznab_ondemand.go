package api

import (
	"net/http"
	"sync"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/newznabondemand"
)

// Claude 2026-10-23: Discover On Demand chip lists verified NZBs only.
// Reason: join catalog ids, group titles, quality labels. Never recrawl on
//   render. Never return enclosure/apikey/details URLs (nzbId is GetNZBID).
// Troubleshooting: empty items → AttachNewznabOndemand not called, no
//   status=verified rows, or verified rows have no TMDB id.
// Review if: Play starts-while-downloading (do not add URL fields).

var (
	ondemandMu      sync.RWMutex
	ondemandStore   *newznabondemand.Store
	ondemandDataDir string
)

func AttachNewznabOndemand(st *newznabondemand.Store) {
	ondemandMu.Lock()
	ondemandStore = st
	ondemandMu.Unlock()
}

func AttachNewznabOndemandDir(dir string) {
	ondemandMu.Lock()
	ondemandDataDir = dir
	ondemandMu.Unlock()
}

type ondemandQuality struct {
	Indexer string `json:"indexer"`
	NzbID   string `json:"nzbId"`
	Label   string `json:"label"`
}

type ondemandTitle struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Year      int               `json:"year,omitempty"`
	ImdbID    string            `json:"imdbId,omitempty"`
	TmdbID    int               `json:"tmdbId,omitempty"`
	TvdbID    string            `json:"tvdbId,omitempty"`
	Qualities []ondemandQuality `json:"qualities"`
}

type ondemandList struct {
	Items []ondemandTitle `json:"items"`
}

func ondemandHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		switch m {
		case mode.Movies, mode.Series, mode.Adult:
		default:
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		ondemandMu.RLock()
		st := ondemandStore
		ondemandMu.RUnlock()
		if st == nil {
			writeJSON(w, ondemandList{Items: []ondemandTitle{}})
			return
		}
		titles, err := st.ListVerified(r.Context(), string(m))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := ondemandList{Items: make([]ondemandTitle, 0, len(titles))}
		for _, t := range titles {
			item := ondemandTitle{
				ID: t.ID, Title: t.Title, Year: t.Year,
				ImdbID: t.ImdbID, TmdbID: t.TmdbID, TvdbID: t.TvdbID,
				Qualities: make([]ondemandQuality, 0, len(t.Qualities)),
			}
			for _, q := range t.Qualities {
				item.Qualities = append(item.Qualities, ondemandQuality{
					Indexer: q.Indexer, NzbID: q.NzbID, Label: q.Label,
				})
			}
			out.Items = append(out.Items, item)
		}
		writeJSON(w, out)
	}
}
