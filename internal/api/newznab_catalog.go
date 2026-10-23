package api

import (
	"net/http"
	"sync"

	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/newznabcatalog"
)

// Claude 2026-10-23: Dock card reads the Newznab catalog overlap report.
// Reason: Lighthouse Mainstream filter is later; this pass only indexes and counts at 1 page / 5s / host.
// Troubleshooting: empty card → AttachNewznabCatalog not called, or both hosts unset.
// Review if: Lighthouse availability filter ships.
var (
	catalogMu    sync.RWMutex
	catalogStore *newznabcatalog.Store
)

func AttachNewznabCatalog(st *newznabcatalog.Store) {
	catalogMu.Lock()
	catalogStore = st
	catalogMu.Unlock()
}

type newznabCatalogCursor struct {
	Indexer     string `json:"indexer"`
	Kind        string `json:"kind"`
	PageOffset  int    `json:"pageOffset"`
	Complete    bool   `json:"complete"`
	LastPageNew int    `json:"lastPageNew"`
	LastError   string `json:"lastError"`
}

type newznabCatalogCount struct {
	Indexer string `json:"indexer"`
	Kind    string `json:"kind"`
	Nzbs    int    `json:"nzbs"`
}

type newznabCatalogOverlap struct {
	Kind      string `json:"kind"`
	Host1Nzbs int    `json:"host1Nzbs"`
	Host2Nzbs int    `json:"host2Nzbs"`
	Both      int    `json:"both"`
	OnlyHost1 int    `json:"onlyHost1"`
	OnlyHost2 int    `json:"onlyHost2"`
}

type newznabCatalogReport struct {
	IntervalSeconds int                     `json:"intervalSeconds"`
	PageSize        int                     `json:"pageSize"`
	Cursors         []newznabCatalogCursor  `json:"cursors"`
	Counts          []newznabCatalogCount   `json:"counts"`
	Overlap         []newznabCatalogOverlap `json:"overlap"`
}

func newznabCatalogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		catalogMu.RLock()
		st := catalogStore
		catalogMu.RUnlock()
		if st == nil {
			writeJSON(w, newznabCatalogReport{
				IntervalSeconds: int(newznabcatalog.Interval.Seconds()),
				PageSize:        newznab.CatalogPageSize(),
				Cursors:         []newznabCatalogCursor{},
				Counts:          []newznabCatalogCount{},
				Overlap:         []newznabCatalogOverlap{},
			})
			return
		}
		rep, err := st.Report(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := newznabCatalogReport{
			IntervalSeconds: rep.IntervalSeconds,
			PageSize:        rep.PageSize,
			Cursors:         make([]newznabCatalogCursor, 0, len(rep.Cursors)),
			Counts:          make([]newznabCatalogCount, 0, len(rep.Counts)),
			Overlap:         make([]newznabCatalogOverlap, 0, len(rep.Overlap)),
		}
		for _, c := range rep.Cursors {
			out.Cursors = append(out.Cursors, newznabCatalogCursor{
				Indexer: c.Indexer, Kind: c.Kind, PageOffset: c.PageOffset,
				Complete: c.Complete, LastPageNew: c.LastPageNew, LastError: c.LastError,
			})
		}
		for _, c := range rep.Counts {
			out.Counts = append(out.Counts, newznabCatalogCount{Indexer: c.Indexer, Kind: c.Kind, Nzbs: c.NZBs})
		}
		for _, o := range rep.Overlap {
			out.Overlap = append(out.Overlap, newznabCatalogOverlap{
				Kind: o.Kind, Host1Nzbs: o.Host1Nzbs, Host2Nzbs: o.Host2Nzbs,
				Both: o.Both, OnlyHost1: o.OnlyHost1, OnlyHost2: o.OnlyHost2,
			})
		}
		writeJSON(w, out)
	}
}
