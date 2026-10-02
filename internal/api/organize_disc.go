package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/disc"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/organizeevents"
)

// unpackDiscFn is swappable in tests.
var unpackDiscFn = disc.Unpack

type discJob struct {
	mu            sync.Mutex
	Path          string
	Status        string
	Volume        string
	Done          int
	Total         int
	Outputs       []string
	Error         string
	DeletedSource bool
	Tracked       bool
}

var (
	discJobMu sync.Mutex
	liveDisc  *discJob
)

func resetDiscJobForTest() {
	discJobMu.Lock()
	defer discJobMu.Unlock()
	liveDisc = nil
}

func snapshotDiscJob() apidto.OrganizeDiscUnpackStatus {
	discJobMu.Lock()
	j := liveDisc
	discJobMu.Unlock()
	if j == nil {
		return apidto.OrganizeDiscUnpackStatus{Status: "idle"}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := append([]string(nil), j.Outputs...)
	return apidto.OrganizeDiscUnpackStatus{
		Path: j.Path, Status: j.Status, Volume: j.Volume,
		Done: j.Done, Total: j.Total, Outputs: out,
		Error: j.Error, DeletedSource: j.DeletedSource, Tracked: j.Tracked,
	}
}

func organizeDiscUnpackStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := snapshotDiscJob()
		want := r.URL.Query().Get("path")
		if want != "" && st.Path != "" && st.Path != want {
			writeJSON(w, apidto.OrganizeDiscUnpackStatus{Path: want, Status: "idle"})
			return
		}
		writeJSON(w, st)
	}
}

func organizeDiscUnpackStartHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.OrganizeDiscUnpackRequest
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

		discJobMu.Lock()
		if liveDisc != nil {
			liveDisc.mu.Lock()
			busy := liveDisc.Status == "probing" || liveDisc.Status == "extracting"
			same := liveDisc.Path == src
			liveDisc.mu.Unlock()
			if busy && !same {
				discJobMu.Unlock()
				http.Error(w, "another disc unpack is already running", http.StatusConflict)
				return
			}
			if busy && same {
				j := liveDisc
				discJobMu.Unlock()
				j.mu.Lock()
				st := apidto.OrganizeDiscUnpackStatus{
					Path: j.Path, Status: j.Status, Volume: j.Volume,
					Done: j.Done, Total: j.Total,
				}
				j.mu.Unlock()
				writeJSONStatus(w, http.StatusAccepted, st)
				return
			}
		}
		job := &discJob{Path: src, Status: "probing"}
		liveDisc = job
		discJobMu.Unlock()

		go runDiscUnpack(job, libStore, src)
		writeJSONStatus(w, http.StatusAccepted, snapshotDiscJob())
	}
}

func runDiscUnpack(job *discJob, libStore *library.Store, src string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	res, err := unpackDiscFn(ctx, src, disc.Options{
		OnProgress: func(done, total int) {
			job.mu.Lock()
			job.Status = "extracting"
			job.Done = done
			job.Total = total
			job.mu.Unlock()
		},
	})
	job.mu.Lock()
	defer job.mu.Unlock()
	if err != nil {
		job.Status = "error"
		job.Error = err.Error()
		fail := false
		organizeevents.Log(context.Background(), organizeevents.Event{
			Workflow: "browse", Kind: organizeevents.KindDiscUnpack, OK: &fail,
			Message: "unpack failed " + src + ": " + err.Error(),
		})
		return
	}
	job.Status = "done"
	job.Volume = res.Map.Volume
	job.Outputs = res.Outputs
	job.DeletedSource = res.DeletedSource
	job.Done = len(res.Outputs)
	job.Total = len(res.Outputs)
	if libStore != nil && res.DeletedSource {
		tracked, ferr := libStore.ForgetPath(context.Background(), src)
		if ferr != nil {
			job.Error = "extracted but library update failed: " + ferr.Error()
		} else {
			job.Tracked = tracked
		}
	}
	ok := true
	organizeevents.Log(context.Background(), organizeevents.Event{
		Workflow: "browse", Kind: organizeevents.KindDiscUnpack, OK: &ok,
		Message: "unpacked " + src + " → " + strings.Join(res.Outputs, ", "),
	})
}
