package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/newznabondemand"
	"github.com/labbersanon/sakms/internal/playback"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-23: On Demand Play downloads a verified NZB to staging only.
// Reason: no grab row, so UsenetCompleteImporter does not import to the library.
//   STAT already ran at verify; AddParsedNZB skips it. Start-while-downloading
//   and the 24h staging sweep are later.
// Troubleshooting: Play 404 → not verified / cache missing; Play then library
//   row → a grab was created. Never return enclosure/apikey URLs.
// Review if: Play streams before BODY completes (keep skip-STAT).
// Related files: internal/usenet/manager.go AddParsedNZB, DetailPopup ondemandQualities

type ondemandPlayRequest struct {
	Indexer string `json:"indexer"`
	NzbID   string `json:"nzbId"`
}

type ondemandPlayStatus struct {
	GID          string `json:"gid"`
	Status       string `json:"status"`
	Phase        string `json:"phase,omitempty"`
	PlaySrc      string `json:"playSrc,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type ondemandPlay struct {
	GID     string
	Mode    string
	Indexer string
	NzbID   string
}

var (
	ondemandPlayMu sync.Mutex
	ondemandByGID  = map[string]ondemandPlay{}
	ondemandByKey  = map[string]string{}
)

func ondemandPlayKey(indexer, nzbID string) string {
	return indexer + "\x00" + nzbID
}

func rememberOndemandPlay(p ondemandPlay) {
	ondemandPlayMu.Lock()
	defer ondemandPlayMu.Unlock()
	key := ondemandPlayKey(p.Indexer, p.NzbID)
	if prev, ok := ondemandByKey[key]; ok && prev != p.GID {
		delete(ondemandByGID, prev)
	}
	ondemandByGID[p.GID] = p
	ondemandByKey[key] = p.GID
}

func lookupOndemandPlay(gid string) (ondemandPlay, bool) {
	ondemandPlayMu.Lock()
	defer ondemandPlayMu.Unlock()
	p, ok := ondemandByGID[gid]
	return p, ok
}

func lookupOndemandPlayGID(indexer, nzbID string) (string, bool) {
	ondemandPlayMu.Lock()
	defer ondemandPlayMu.Unlock()
	gid, ok := ondemandByKey[ondemandPlayKey(indexer, nzbID)]
	return gid, ok
}

func resetOndemandPlaysForTest() {
	ondemandPlayMu.Lock()
	ondemandByGID = map[string]ondemandPlay{}
	ondemandByKey = map[string]string{}
	ondemandPlayMu.Unlock()
}

func ondemandPlayMode(r *http.Request) (mode.Mode, bool) {
	m := mode.Mode(r.PathValue("mode"))
	switch m {
	case mode.Movies, mode.Series, mode.Adult:
		return m, true
	default:
		return "", false
	}
}

func ondemandPlayHandler(nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, ok := ondemandPlayMode(r)
		if !ok {
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		if m == mode.Adult && denyIfAdultLocked(w, r) {
			return
		}
		if nzb == nil {
			http.Error(w, "usenet unavailable", http.StatusServiceUnavailable)
			return
		}
		ondemandMu.RLock()
		st := ondemandStore
		dataDir := ondemandDataDir
		ondemandMu.RUnlock()
		if st == nil || dataDir == "" {
			http.Error(w, "on demand unavailable", http.StatusServiceUnavailable)
			return
		}
		var req ondemandPlayRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.Indexer = strings.TrimSpace(req.Indexer)
		req.NzbID = strings.TrimSpace(req.NzbID)
		if req.Indexer == "" || req.NzbID == "" || newznabondemand.LooksLikeURL(req.Indexer) || newznabondemand.LooksLikeURL(req.NzbID) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if gid, ok := lookupOndemandPlayGID(req.Indexer, req.NzbID); ok {
			if snap := ondemandPlaySnapshot(m, nzb, gid); snap != nil && snap.Status != "error" && snap.Status != "removed" {
				writeJSON(w, *snap)
				return
			}
		}
		row, ok, err := st.GetVerified(r.Context(), string(m), req.Indexer, req.NzbID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		parsed, err := newznabondemand.LoadNZB(dataDir, row.NZBRelPath)
		if err != nil {
			http.Error(w, "nzb cache missing", http.StatusNotFound)
			return
		}
		gid, err := nzb.AddParsedNZB(r.Context(), parsed, row.Title)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		rememberOndemandPlay(ondemandPlay{
			GID: gid, Mode: string(m), Indexer: req.Indexer, NzbID: req.NzbID,
		})
		out := ondemandPlaySnapshot(m, nzb, gid)
		if out == nil {
			writeJSON(w, ondemandPlayStatus{GID: gid, Status: "active"})
			return
		}
		writeJSON(w, *out)
	}
}

func ondemandPlayStatusHandler(nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, ok := ondemandPlayMode(r)
		if !ok {
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		if m == mode.Adult && denyIfAdultLocked(w, r) {
			return
		}
		gid := strings.TrimSpace(r.PathValue("gid"))
		if !usenet.IsOwnedStagingName(gid) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if nzb == nil {
			http.Error(w, "usenet unavailable", http.StatusServiceUnavailable)
			return
		}
		out := ondemandPlaySnapshot(m, nzb, gid)
		if out == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, *out)
	}
}

func ondemandPlayVideoHandler(nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path, kind, ok := resolveOndemandPlayVideo(w, r, nzb)
		if !ok {
			return
		}
		serveLocalVideoFile(w, r, path, kind)
	}
}

func ondemandPlayVideoHLSHandler(nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path, kind, ok := resolveOndemandPlayVideo(w, r, nzb)
		if !ok {
			return
		}
		serveResolvedHLS(w, r, path, kind)
	}
}

func resolveOndemandPlayVideo(w http.ResponseWriter, r *http.Request, nzb *usenet.Manager) (path, kind string, ok bool) {
	m, modeOK := ondemandPlayMode(r)
	if !modeOK {
		http.Error(w, "unknown mode", http.StatusBadRequest)
		return "", "", false
	}
	if m == mode.Adult && denyIfAdultLocked(w, r) {
		return "", "", false
	}
	gid := strings.TrimSpace(r.PathValue("gid"))
	if !usenet.IsOwnedStagingName(gid) {
		http.Error(w, "not found", http.StatusNotFound)
		return "", "", false
	}
	if nzb == nil {
		http.Error(w, "usenet unavailable", http.StatusServiceUnavailable)
		return "", "", false
	}
	p, found := lookupOndemandPlay(gid)
	if !found || p.Mode != string(m) {
		http.Error(w, "not found", http.StatusNotFound)
		return "", "", false
	}
	d, err := nzb.FindByGID(gid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", "", false
	}
	if d == nil || d.Status != "complete" {
		http.Error(w, "not ready", http.StatusConflict)
		return "", "", false
	}
	file := ondemandPlayableFile(d)
	if file == "" {
		http.Error(w, "no video", http.StatusNotFound)
		return "", "", false
	}
	return file, "ondemand", true
}

func ondemandPlaySnapshot(m mode.Mode, nzb *usenet.Manager, gid string) *ondemandPlayStatus {
	p, found := lookupOndemandPlay(gid)
	if !found || p.Mode != string(m) {
		return nil
	}
	out := &ondemandPlayStatus{GID: gid, Status: "active"}
	d, err := nzb.FindByGID(gid)
	if err != nil || d == nil {
		return out
	}
	out.Status = d.Status
	out.Phase = d.Phase
	out.ErrorMessage = d.ErrorMessage
	if d.Status == "complete" {
		if file := ondemandPlayableFile(d); file != "" {
			serve := "/api/modes/" + string(m) + "/ondemand/play/" + gid + "/video"
			out.PlaySrc = playback.VideoURL(serve, file)
		} else if out.ErrorMessage == "" {
			out.ErrorMessage = "no video"
		}
	}
	return out
}

func ondemandPlayableFile(d *usenet.Download) string {
	if d == nil {
		return ""
	}
	for _, f := range d.Files {
		if playback.PlayableInApp(f) {
			return f
		}
	}
	return ""
}
