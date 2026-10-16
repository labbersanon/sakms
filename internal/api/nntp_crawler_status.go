package api

import (
	"net/http"

	"github.com/labbersanon/sakms/internal/usenetsearch"
)

// Claude 2026-10-16: Dashboard NNTP crawler snapshot (REST, not SSE).
// Reason: same pattern as storage-allocation — index counters are not sysinfo.
// Troubleshooting: empty/disabled card → native search off or service nil.
// Review if: last-chunk fields grow; keep this a small dashboard payload.

// nntpCrawlerChunk mirrors apidto.NNTPCrawlerChunk.
type nntpCrawlerChunk struct {
	Dir       string `json:"dir"`
	Group     string `json:"group"`
	RangeLow  int    `json:"rangeLow"`
	RangeHigh int    `json:"rangeHigh"`
	Rows      int    `json:"rows"`
	Releases  int    `json:"releases"`
	Admitted  int    `json:"admitted"`
	StagedObf int    `json:"stagedObf"`
	SkipRes   int    `json:"skipRes"`
	SkipStat  int    `json:"skipStat"`
	Gaps      int    `json:"gaps"`
	ElapsedMs int64  `json:"elapsedMs"`
	AtUnixMs  int64  `json:"atUnixMs"`
}

// nntpCrawlerStatusResponse mirrors apidto.NNTPCrawlerStatus.
type nntpCrawlerStatusResponse struct {
	Enabled            bool              `json:"enabled"`
	ProbeState         string            `json:"probeState"`
	ProbeDetail        string            `json:"probeDetail"`
	Groups             int               `json:"groups"`
	Headers            int64             `json:"headers"`
	IndexBytes         int64             `json:"indexBytes"`
	IndexMaxGB         int               `json:"indexMaxGb"`
	ReleasesSearchable int64             `json:"releasesSearchable"`
	ReleasesBelowGate  int64             `json:"releasesBelowGate"`
	PendingName        int64             `json:"pendingName"`
	MissedRanges       int64             `json:"missedRanges"`
	LastPassBusy       bool              `json:"lastPassBusy"`
	LastPassAdvanced   bool              `json:"lastPassAdvanced"`
	LastChunk          *nntpCrawlerChunk `json:"lastChunk,omitempty"`
}

func nntpCrawlerStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := nntpCrawlerStatusResponse{IndexMaxGB: usenetsearch.DefaultIndexMaxGB}
		svc := getNNTPNativeService()
		if svc != nil {
			st, err := svc.CrawlerStatus(r.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = nntpCrawlerStatusFrom(st)
		}
		writeJSON(w, out)
	}
}

func nntpCrawlerStatusFrom(st usenetsearch.CrawlerStatus) nntpCrawlerStatusResponse {
	out := nntpCrawlerStatusResponse{
		Enabled:            st.Enabled,
		ProbeState:         st.ProbeState,
		ProbeDetail:        st.ProbeDetail,
		Groups:             st.Groups,
		Headers:            st.Headers,
		IndexBytes:         st.IndexBytes,
		IndexMaxGB:         st.IndexMaxGB,
		ReleasesSearchable: st.ReleasesSearchable,
		ReleasesBelowGate:  st.ReleasesBelowGate,
		PendingName:        st.PendingName,
		MissedRanges:       st.MissedRanges,
		LastPassBusy:       st.LastPassBusy,
		LastPassAdvanced:   st.LastPassAdvanced,
	}
	if st.LastChunk != nil {
		c := nntpCrawlerChunk{
			Dir: st.LastChunk.Dir, Group: st.LastChunk.Group,
			RangeLow: st.LastChunk.RangeLow, RangeHigh: st.LastChunk.RangeHigh,
			Rows: st.LastChunk.Rows, Releases: st.LastChunk.Releases,
			Admitted: st.LastChunk.Admitted, StagedObf: st.LastChunk.StagedObf,
			SkipRes: st.LastChunk.SkipRes, SkipStat: st.LastChunk.SkipStat,
			Gaps: st.LastChunk.Gaps, ElapsedMs: st.LastChunk.ElapsedMs,
			AtUnixMs: st.LastChunk.AtUnixMs,
		}
		out.LastChunk = &c
	}
	return out
}
