package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/newznabcatalog"
	"github.com/labbersanon/sakms/internal/newznabondemand"
)

// Claude 2026-10-24: Dock GET must not run the catalog overlap GROUP BY.
// Reason: that CTE exceeded 120s on ~1.3M rows; Traefik 499'd and the card
//   stayed on "Loading NZB catalog…". Overlap is newznab_catalog_overlap;
//   5s timeout surfaces an error instead of a hung fetch.
// Troubleshooting: perpetual Loading → handler still calling Store.Report's
//   old CTE, or the 5s budget is too tight for COUNT(*) under load.
// Review if: Lighthouse overlap replaces this card.
// Claude 2026-10-23: Dock card reads catalog overlap, On Demand counts, indexer health.
// Reason: Lighthouse filter is later; health is crawl cursors, not a live t=caps.
// Troubleshooting: empty card → AttachNewznabCatalog; always Waiting → no cursor rows.
// Review if: Lighthouse availability filter or Play ships.
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

type newznabCatalogOndemand struct {
	Kind     string `json:"kind"`
	Verified int    `json:"verified"`
	Fetched  int    `json:"fetched"`
	Skipped  int    `json:"skipped"`
}

type newznabIndexerHealth struct {
	Indexer         string `json:"indexer"`
	Status          string `json:"status"`
	Detail          string `json:"detail"`
	UpdatedAt       int64  `json:"updatedAt"`
	MissingNzbs     int    `json:"missingNzbs"`
	MissingArticles int    `json:"missingArticles"`
}

type newznabCatalogReport struct {
	IntervalSeconds int                      `json:"intervalSeconds"`
	PageSize        int                      `json:"pageSize"`
	Cursors         []newznabCatalogCursor   `json:"cursors"`
	Counts          []newznabCatalogCount    `json:"counts"`
	Overlap         []newznabCatalogOverlap  `json:"overlap"`
	Ondemand        []newznabCatalogOndemand `json:"ondemand"`
	Indexers        []newznabIndexerHealth   `json:"indexers"`
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
				Ondemand:        []newznabCatalogOndemand{},
				Indexers:        copyIndexerHealth(newznabcatalog.IndexerHealthFromCursors(nil)),
			})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		rep, err := st.Report(ctx)
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
			Ondemand:        []newznabCatalogOndemand{},
			Indexers:        copyIndexerHealth(rep.Indexers),
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
		ondemandMu.RLock()
		od := ondemandStore
		ondemandMu.RUnlock()
		if od != nil {
			kinds, err := od.CountByKind(ctx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out.Ondemand = make([]newznabCatalogOndemand, 0, len(kinds))
			for _, k := range kinds {
				out.Ondemand = append(out.Ondemand, newznabCatalogOndemand{
					Kind: k.Kind, Verified: k.Verified, Fetched: k.Fetched, Skipped: k.Skipped,
				})
			}
			dead, err := od.CountDeadByIndexer(ctx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			applyDeadCounts(out.Indexers, dead)
		}
		writeJSON(w, out)
	}
}

func copyIndexerHealth(in []newznabcatalog.IndexerHealth) []newznabIndexerHealth {
	out := make([]newznabIndexerHealth, 0, len(in))
	for _, h := range in {
		out = append(out, newznabIndexerHealth{
			Indexer: h.Indexer, Status: h.Status, Detail: h.Detail, UpdatedAt: h.UpdatedAt,
		})
	}
	return out
}

// Claude 2026-10-23: Overlay dead NZB counts onto crawl health rows.
// Reason: Indexer health status stays cursor-derived; missing articles are retention.
// Troubleshooting: zeros with verified NZBs → ondemand store unattached or no dead rows.
// Review if: Play treats dead as retryable (keep overlay, do not flip Status).
func applyDeadCounts(health []newznabIndexerHealth, dead []newznabondemand.DeadCount) {
	by := map[string]newznabondemand.DeadCount{}
	for _, d := range dead {
		by[d.Indexer] = d
	}
	for i, h := range health {
		d := by[h.Indexer]
		health[i].MissingNzbs = d.Nzbs
		health[i].MissingArticles = d.Articles
	}
}
