package api

import (
	"net/http"
	"sync"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/rename"
)

// RenameScanStatus is the operator-visible progress for a Rename/library
// catalog scan. Library and Settings poll this after a kids-root save.
//
// Claude 2026-09-23: kids-root save had no progress surface.
// Reason: PUT kids-root-path only stored the path.
// Review if: Rename Scan on Organize shares this hub.
type RenameScanStatus struct {
	Running   bool   `json:"running"`
	Phase     string `json:"phase,omitempty"`
	Current   int    `json:"current,omitempty"`
	Total     int    `json:"total,omitempty"`
	Name      string `json:"name,omitempty"`
	Error     string `json:"error,omitempty"`
	Proposals int    `json:"proposals,omitempty"`
}

type renameScanHub struct {
	mu sync.Mutex
	by map[string]RenameScanStatus
}

var renameScans = &renameScanHub{by: map[string]RenameScanStatus{}}

func (h *renameScanHub) start(m mode.Mode) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := string(m)
	if h.by[key].Running {
		return false
	}
	h.by[key] = RenameScanStatus{Running: true, Phase: "starting"}
	return true
}

func (h *renameScanHub) progress(m mode.Mode) rename.ProgressFunc {
	return func(current, total int, name string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		st := h.by[string(m)]
		st.Running = true
		st.Phase = "scanning"
		st.Current = current
		st.Total = total
		st.Name = name
		h.by[string(m)] = st
	}
}

func (h *renameScanHub) done(m mode.Mode, proposals int, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := RenameScanStatus{Running: false, Phase: "done", Proposals: proposals}
	if err != nil {
		st.Phase = "error"
		st.Error = err.Error()
	}
	h.by[string(m)] = st
}

func (h *renameScanHub) get(m mode.Mode) RenameScanStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.by[string(m)]
}

func (h *renameScanHub) all() map[string]RenameScanStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]RenameScanStatus, len(h.by))
	for k, v := range h.by {
		out[k] = v
	}
	return out
}

func getRenameScanStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		if m != mode.Movies && m != mode.Series && m != mode.Adult {
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		writeJSON(w, renameScans.get(m))
	}
}

func getLibraryScanStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, renameScans.all())
	}
}
