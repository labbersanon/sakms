package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/dedupscan"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// dedupScanHandler runs the Dedup workflow's propose-phase for {mode}. Every
// error that can be a fast 4xx is validated SYNCHRONOUSLY on the request
// goroutine (bad mode, missing threshold/settings, empty root folder, Adult
// identify-not-configured); once past that gate the actual scan runs in a
// BACKGROUND goroutine derived from the Hub's signal-driven base context (so it
// outlives the request/tab and participates in server shutdown) and the handler
// returns 202 Accepted immediately. Live per-file progress, and the terminal
// done/error transition, travel over the SSE stream (dedupScanStreamHandler),
// never this response body — see the plan's §4.3.
//
// prober takes dedup.Prober's interface, not the concrete *mediainfo.Prober,
// so tests can inject a fake instead of depending on a real ffprobe binary.
//
// Movies/Series dispatch to the phash-primary scan (ScanLibraryPHash /
// ScanLibrarySeriesPHash): all files — tracked and orphans — are grouped by
// perceptual similarity alone; TMDB is used only for display labels and never
// determines whether files are grouped. Adult dispatches to ScanLibraryAdult
// (Whisparr eliminated, Stage 4), which groups by (box, scene_id) and refines
// by perceptual similarity.
func dedupScanHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, propStore *proposals.Store, prober dedup.Prober, hasher dedup.PHasher, libStore *library.Store, hub *dedupscan.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		// --- SYNCHRONOUS validation: every 4xx/5xx happens here, before
		//     backgrounding. mode.Build uses r.Context() for the early 400; the
		//     Session does not retain the ctx, so the background scan is free to
		//     pass its own shutdown-aware ctx to the scan functions instead.
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		threshold, tErr := resolvePHashThreshold(ctx, settingsStore, m)
		if tErr != nil {
			http.Error(w, tErr.Error(), http.StatusInternalServerError)
			return
		}

		key, _ := libraryRootFolderKey(m)
		rootPath, rpErr := settingsStore.Get(ctx, key)
		if rpErr != nil && !errors.Is(rpErr, settings.ErrNotFound) {
			http.Error(w, rpErr.Error(), http.StatusInternalServerError)
			return
		}
		if rootPath == "" {
			// Deliberate, flagged behavior improvement (plan §2/§6): the scan
			// functions surface this late as a 502; validating it here turns it
			// into a fast, correct 400.
			http.Error(w, "no "+string(m)+" library root folder configured yet — add one in Settings first", http.StatusBadRequest)
			return
		}
		if m == mode.Adult && sess.Identify == nil {
			// Pre-check the Adult scan's own identify guard so an unconfigured
			// pipeline fails fast as a 400 rather than a late 502 inside the scan.
			http.Error(w, "adult identification isn't configured — add an Ollama connection and set the Ollama model in Settings, plus at least one of StashDB/FansDB/TPDB", http.StatusBadRequest)
			return
		}

		// --- concurrent-same-mode guard (also seeds the "starting" priming
		//     state). A second scan of the SAME mode while one is running is 409;
		//     different modes may run concurrently (each is independently keyed).
		if !hub.TryStart(string(m)) {
			http.Error(w, "a "+string(m)+" dedup scan is already running", http.StatusConflict)
			return
		}

		// --- background the actual scan loop on the Hub's signal-driven base
		//     context (NOT r.Context()): it outlives the request/tab and cancels
		//     cleanly on SIGTERM, matching recheck/adultnewest/parseentity.
		bg := hub.BaseContext()
		go func() {
			// Finish is registered first so it runs LAST (after the recover
			// publish): together they guarantee the in-flight flag always clears
			// and a terminal event always fires — a mid-scan panic can never
			// wedge the mode in-flight (409 forever) or strand the UI "scanning".
			defer hub.Finish(string(m))
			defer func() {
				if rec := recover(); rec != nil {
					hub.PublishTerminal(dedupscan.Event{Type: "error", Mode: string(m),
						Error: fmt.Sprintf("scan panicked: %v", rec)})
				}
			}()

			// finalProcessed tracks the last progress Current so the done event
			// carries the authoritative final count (the scan functions return
			// only proposals + error, not the count — see progress.go).
			var finalProcessed int
			progress := func(ev dedup.ProgressEvent) {
				finalProcessed = ev.Current
				hub.Publish(dedupscan.Event{Type: "progress", Mode: string(m),
					Current: ev.Current, Total: ev.Total, Name: ev.Name, Phase: ev.Phase})
			}

			var found []proposals.Proposal
			var scanErr error
			switch m {
			case mode.Movies:
				found, scanErr = dedup.ScanLibraryPHash(bg, sess, libStore, rootPath, prober, hasher, threshold, progress)
			case mode.Series:
				found, scanErr = dedup.ScanLibrarySeriesPHash(bg, sess, libStore, rootPath, prober, hasher, threshold, progress)
			default:
				found, scanErr = dedup.ScanLibraryAdult(bg, sess, libStore, rootPath, prober, hasher, threshold, progress)
			}
			if scanErr != nil {
				hub.PublishTerminal(dedupscan.Event{Type: "error", Mode: string(m), Error: scanErr.Error()})
				return
			}

			saved, saveErr := propStore.ReplacePending(bg, m, proposals.Dedup, found)
			if saveErr != nil {
				if errors.Is(saveErr, proposals.ErrReplaceDeferred) {
					hub.PublishTerminal(dedupscan.Event{Type: "error", Mode: string(m), Error: "scan deferred: apply in flight — retry after the apply completes"})
				} else {
					hub.PublishTerminal(dedupscan.Event{Type: "error", Mode: string(m), Error: saveErr.Error()})
				}
				return
			}

			hub.PublishTerminal(dedupscan.Event{Type: "done", Mode: string(m), Count: len(saved), Total: finalProcessed})
		}()

		// 202: the SSE stream, not this response, tells the operator work is
		// happening.
		w.WriteHeader(http.StatusAccepted)
	}
}

// dedupScanStreamHandler serves the per-mode live progress SSE stream for a
// running Dedup scan — a near-verbatim sibling of notificationsStreamHandler,
// differing only in that it subscribes to the dedupscan Hub keyed on {mode} and
// emits dedupscan.Event frames.
//
// One deliberate divergence from notificationsStreamHandler: Subscribe is called
// BEFORE the ": connected" prime flush (not after). This guarantees a client
// that has read the connected comment is already registered, so reconnect
// priming (the last progress / "starting" seed enqueued by Subscribe) and any
// events published immediately after connect can never be missed in the gap
// between the flush and the subscribe.
//
// It also carries §4.5's section-lock re-check ticker, and this is the
// stream that most needs it: GET /api/modes/adult/dedup/scan/stream
// classifies as {organize, adult-content}, so locking Adult (or Organize)
// while a scan is streaming terminates it within one tick instead of
// letting Adult filenames keep arriving for the tab's lifetime.
// recheckInterval is a test-only override.
//
// Its polling sibling, GET /api/modes/{mode}/dedup/scan/status, needs
// nothing here: it is an ordinary request/response route, so Layer 1
// classifies and denies it on every single poll.
func dedupScanStreamHandler(hub *dedupscan.Hub, recheckInterval ...time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ctx := r.Context()

		// A nil hub yields a nil channel + no-op unsubscribe (see Hub.Subscribe);
		// a nil channel blocks forever in the select, so the loop simply waits on
		// ctx.Done() — never busy-spins.
		ch, unsubscribe := hub.Subscribe(r.PathValue("mode"))
		defer unsubscribe()

		// Flush an SSE comment so the 200 + headers land on connect (this is a
		// pure event stream with no initial snapshot). Done AFTER Subscribe so
		// the client's receipt of it proves the subscription is live.
		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		sections := sectionlock.Classify(r.URL.Path)
		recheck := time.NewTicker(streamRecheckInterval(recheckInterval))
		defer recheck.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-recheck.C:
				if sectionlock.StreamRevoked(r, sections) {
					return
				}
			case ev := <-ch:
				writeSSEData(w, flusher, ev)
			}
		}
	}
}

// dedupScanStatusHandler reports whether a scan for {mode} is currently running.
// This is the frontend liveness backstop's reconcile signal: it disambiguates
// "scan still running" from "scan finished (possibly with zero groups)", which a
// proposals-count poll cannot. Reads only the in-memory in-flight map.
func dedupScanStatusHandler(hub *dedupscan.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, struct {
			Inflight bool `json:"inflight"`
		}{Inflight: hub.Inflight(r.PathValue("mode"))})
	}
}
