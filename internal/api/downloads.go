package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Settings keys for the unified downloader's operator-tunable knobs.
//
// Claude 2026-08-01: DownloaderMaxConnectionsKey narrowed to torrent-only.
// Reason: it used to also seed the usenet Manager's connection count
// (cmd/sakms/main.go's old buildUsenetManager), which collided with the new
// per-subscription MaxConns field once multiple Usenet subscriptions were
// possible. Usenet connection counts are now configured per-subscription in
// the Usenet settings page, with internal/usenet's defaultMaxConnsPerServer
// covering an unset value — this setting no longer has any effect on Usenet.
// Troubleshooting: a Usenet connection-count change here doing nothing.
// Review if: this comment predates a future Downloader settings UI, which
// should inherit "torrent engine only" wording rather than re-litigate it.
const (
	DownloaderStagingDirKey     = "downloader_staging_dir"
	DownloaderMaxConcurrentKey  = "downloader_max_concurrent"
	DownloaderMaxConnectionsKey = "downloader_max_connections"

	TorrentDownloadRateLimitKey     = "torrent_download_rate_limit_bytes"
	TorrentDHTEnabledKey            = "torrent_dht_enabled"
	TorrentPEXEnabledKey            = "torrent_pex_enabled"
	TorrentListenPortKey            = "torrent_listen_port"
	TorrentObfuscationModeKey       = "torrent_obfuscation_mode"
	TorrentSeedingEnabledKey        = "torrent_seeding_enabled"
	TorrentSeedRatioLimitKey        = "torrent_seed_ratio_limit"
	TorrentSeedDurationMinutesKey   = "torrent_seed_duration_minutes"
	TorrentStaleThresholdMinutesKey = "torrent_stale_threshold_minutes"
)

// Defaults for the concurrency knobs when unset (per the feature spec).
//
// Claude 2026-08-01: exported (was downloaderDefault*, package-private).
// Reason: cmd/sakms/main.go's buildDownloader is the boot-time half of a
// two-reader contract with this file — both must read the same keys with the
// same defaults, or a saved setting applies while the process is up and
// silently reverts on the next restart. main.go previously re-declared its own
// copy of every default (and hardcoded bare 3/4 for these two), so the two
// halves agreed only by review. They now share one definition and the compiler
// enforces it; no drift test is needed or possible.
// Troubleshooting: a torrent setting that works until the container restarts.
// Review if: main.go stops importing internal/api, or these move to a shared
// config package.
const (
	DownloaderDefaultMaxConcurrent  = 3
	DownloaderDefaultMaxConnections = 4
)

// Defaults for the torrent-engine knobs when unset. DHT/PEX mirror the
// anacrolix/torrent library's own un-overridden defaults, so an install that
// never opens this settings page behaves exactly as it did before these knobs
// existed. Seeding defaults OFF deliberately — same "off by default, manual
// first" convention as usenet_autograb_enabled (autograb_shared.go) — because
// turning it on changes disk usage and upload behavior for in-progress
// downloads too.
//
// Exported for the same reason as the two above — see their comment.
const (
	TorrentDefaultDownloadRateLimit     = 0 // bytes/sec; 0 = unlimited
	TorrentDefaultDHTEnabled            = true
	TorrentDefaultPEXEnabled            = true
	TorrentDefaultListenPort            = 42069
	TorrentDefaultObfuscationMode       = torrentObfuscationPrefer
	TorrentDefaultSeedingEnabled        = false
	TorrentDefaultSeedRatioLimit        = 1.0
	TorrentDefaultSeedDurationMinutes   = 2880 // 48h; 0 = no duration limit
	TorrentDefaultStaleThresholdMinutes = 240  // 4h; 0 = stale detection off
)

// The three accepted protocol-obfuscation modes. These map onto the torrent
// library's HeaderObfuscationPolicy{Preferred, RequirePreferred} pair:
// require → {true,true}; prefer → {true,false} (the library default); off →
// {false,true}. Note that "off" is the STRICTEST mode, not the most permissive
// one — RequirePreferred means "Preferred is a hard requirement", so off
// rejects every encrypted peer rather than merely preferring plaintext.
const (
	torrentObfuscationRequire = "require"
	torrentObfuscationPrefer  = "prefer"
	torrentObfuscationOff     = "off"
)

// torrentListenPortMin/Max bound the listen port to the unprivileged range —
// the engine runs as a non-root user, so a privileged port could never bind.
const (
	torrentListenPortMin = 1024
	torrentListenPortMax = 65535
)

// Claude 2026-08-01: upper bounds added for the three "how long / how much"
// knobs, which previously had a lower bound only.
// Reason: both minute fields reach downloader.go as
// `time.Duration(minutes) * time.Minute` — int64 NANOSECONDS — so any value
// above 153,722,867 wraps to a NEGATIVE duration. That inverts the setting
// instead of saturating it: `now.Sub(lastProgressAt) >= negativeThreshold` is
// true for every entry, so a stale threshold an operator entered meaning
// "effectively never" auto-cancels and DELETES the partial files of every
// active torrent on the next poll tick. seedRatioLimit has the same shape via
// downloader.go's `int64(float64(seedTotalBytes) * ratio)`: an out-of-range
// float→int64 conversion is implementation-defined in Go and yields min-int64
// on amd64, so the ratio target goes negative and seeding stops instantly.
// 10 years of minutes is far past any real use case and two orders of
// magnitude below the wrap point, so a rejected value is unambiguously a typo
// or an attack, never a legitimate "very large" setting.
// Troubleshooting: unattended, irreversible mass-cancellation of active
// torrents after a settings save.
// Review if: these values stop being converted to time.Duration/int64.
const (
	torrentMinutesMax   = 5256000 // 10 years, in minutes
	torrentSeedRatioMax = 10000.0
)

// stagingDirDenylist is the set of system directories the staging dir may never
// be pointed at. The torrent engine legitimately creates, writes, and DELETES
// torrent-declared filenames under StagingDir, so a save pointing it at one of
// these hands that authority to whatever a .torrent file declares. Matched on
// the CLEANED path exactly, never as a prefix: "/var/lib" must reject itself
// while still allowing the common production default /var/lib/sakms/downloads.
// It does not need to be exhaustive — it catches the obvious footguns, and the
// absolute-path and creatable checks alongside it do the rest.
var stagingDirDenylist = map[string]bool{
	"/":        true,
	"/bin":     true,
	"/boot":    true,
	"/dev":     true,
	"/etc":     true,
	"/home":    true,
	"/lib":     true,
	"/proc":    true,
	"/root":    true,
	"/sbin":    true,
	"/sys":     true,
	"/usr":     true,
	"/var":     true,
	"/var/lib": true,
}

// validateStagingDir checks an incoming staging-dir value, returning an
// operator-facing message when it must be rejected with 400.
//
// An EMPTY string is legal and is NOT validated here: it means "leave it at the
// boot-computed default", which this handler cannot resolve (it has no dataDir)
// and which is the one value guaranteed to already be creatable, since boot
// created it. Note filepath.Clean("") is ".", so the early return also has to
// come before the absolute-path check.
//
// MkdirAll is the last check because it is the only one with a side effect: a
// request rejected on some other field must not leave a directory behind.
func validateStagingDir(dir string) string {
	if dir == "" {
		return ""
	}
	if !filepath.IsAbs(dir) {
		return "stagingDir must be an absolute path"
	}
	clean := filepath.Clean(dir)
	if stagingDirDenylist[clean] {
		return fmt.Sprintf("stagingDir must not be the system directory %q — the torrent engine creates and deletes torrent-declared filenames under it", clean)
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return fmt.Sprintf("stagingDir %q could not be created: %v", clean, err)
	}
	return ""
}

// downloadsGlobalPausedKey is the settings flag backing the system-wide download
// pause toggle (GET/PUT /api/downloads/pause-state). It's a simple app-wide
// bool, so it lives in the flat settings KV store — the established home for
// one-off flags (see internal/settings' package doc) — rather than its own table.
const downloadsGlobalPausedKey = "downloads_global_paused"

// errDownloadsPaused is returned by dispatchToDownloadClient when the global
// pause is on, so the frontend can distinguish "blocked because paused" from any
// other grab failure. The frontend keys on this exact message (the codebase's
// fixed-error-string convention — see Discover's GrabError matching), so the
// "globally paused" token must stay stable; it deliberately does NOT reuse the
// 409 grabHandler already gives for "already grabbing this release" (it surfaces
// as 423 Locked instead — see dispatchToDownloadClient).
var errDownloadsPaused = errors.New("downloads are globally paused — resume in the Downloads screen before grabbing new releases")

// toDTODownload maps a downloader.Download to the wire DTO, deriving a display
// filename (basename of the first file, GID fallback). Protocol is a literal
// "torrent" — this mapper's argument type already tells it that with
// certainty, so it does not need to re-derive it from the GID (D-2).
func toDTODownload(d downloader.Download) apidto.Download {
	name := d.Filename
	if name != "" {
		name = filepath.Base(name)
	}
	if name == "" {
		name = d.GID
	}
	return apidto.Download{
		GID:             d.GID,
		Status:          d.Status,
		Filename:        name,
		TotalLength:     d.TotalLength,
		CompletedLength: d.CompletedLength,
		DownloadSpeed:   d.DownloadSpeed,
		SeedCount:       d.SeedCount,
		UploadSpeed:     d.UploadSpeed,
		Protocol:        apidto.DownloadProtocolTorrent,
		ErrorMessage:    d.ErrorMessage,
	}
}

// toUsenetDTODownload maps a usenet.Download to the wire DTO. Mirrors
// toDTODownload, except SeedCount and UploadSpeed are left at their zero
// value: usenet has no seeder/upload concept, and that omission is
// intentional, not a missed field — the frontend hides both entirely for
// protocol == "usenet" rather than rendering "0 seeds" / "0 KB/s", so a
// future reader must not "fix" this by wiring up fake values.
func toUsenetDTODownload(d usenet.Download) apidto.Download {
	name := d.Filename
	if name != "" {
		name = filepath.Base(name)
	}
	if name == "" {
		name = d.GID
	}
	return apidto.Download{
		GID:             d.GID,
		Status:          d.Status,
		Filename:        name,
		TotalLength:     d.TotalLength,
		CompletedLength: d.CompletedLength,
		DownloadSpeed:   d.DownloadSpeed,
		Protocol:        apidto.DownloadProtocolUsenet,
		ErrorMessage:    d.ErrorMessage,
	}
}

// mergedDownloads returns the combined torrent + usenet download queue as a
// DTO slice. Returns a non-nil empty slice so JSON encodes [] not null.
func mergedDownloads(dl *downloader.Manager, nzb *usenet.Manager) []apidto.Download {
	out := make([]apidto.Download, 0)
	if dl != nil {
		for _, d := range dl.List() {
			out = append(out, toDTODownload(d))
		}
	}
	if nzb != nil {
		for _, d := range nzb.List() {
			out = append(out, toUsenetDTODownload(d))
		}
	}
	return out
}

// Claude 2026-08-03: the Downloads queue now hides Adult rows while
// adult-content is locked.
// Reason: /api/downloads carries no {mode} in its path, so Layer 1 classifies
// it as {queue} ONLY and never adds adult-content. Locking Adult alone
// therefore left an in-progress Adult grab's RELEASE NAME — the most
// identifying string the app holds — plainly visible in the Downloads tab.
// Troubleshooting: requests.go already had this filter; downloads.go had no
// equivalent and was missed because its URL looks mode-agnostic.
// Review if: apidto.Download ever carries its own mode, which would remove the
// per-GID grab lookup below.
//
// FILTER, not refuse — the same rule requests.go states. Queue itself may be
// unlocked, so the request as a whole must still succeed; taking the entire
// download queue away whenever Adult alone is locked is exactly what AC6's
// "without also locking Mainstream" forbids.
//
// # Fail CLOSED on an indeterminate row
//
// A download's mode lives in its grabs row, not in the download itself, so this
// resolves each GID through grabs. A row whose mode cannot be determined — no
// grab row, or a failed lookup — is DROPPED rather than shown. Every path that
// reaches a download manager creates a grabs row first (dispatchToDownloadClient's
// four callers all do), so in practice this drops nothing; when that assumption
// breaks, hiding a mainstream row is a visible bug and showing an Adult one is a
// silent confidentiality failure. The lookups run ONLY while Adult is locked,
// and download_gid is indexed (migration 0035).
func filterAdultDownloads(ctx context.Context, grabsStore *grabs.Store, rows []apidto.Download) []apidto.Download {
	if grabsStore == nil {
		// No way to resolve any row's mode. Fail closed: an empty queue is a
		// visible, reportable state; a leaked Adult release name is not.
		return []apidto.Download{}
	}
	out := make([]apidto.Download, 0, len(rows))
	for _, row := range rows {
		g, err := grabsStore.GetByDownloadGID(ctx, row.GID)
		if err != nil || g == nil || g.Mode == mode.Adult {
			continue
		}
		out = append(out, row)
	}
	return out
}

// listDownloadsHandler returns the current download queue.
func listDownloadsHandler(dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil && nzb == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		rows := mergedDownloads(dl, nzb)
		if adultLocked(r.Context()) {
			rows = filterAdultDownloads(r.Context(), grabsStore, rows)
		}
		writeJSON(w, rows)
	}
}

// downloadsStreamHandler streams the combined torrent + usenet download queue
// as server-sent events. It subscribes to both managers; an event from either
// triggers a full re-snapshot so the UI always sees the merged queue.
// Nil channels (unconfigured engine) simply never fire in the select, so the
// handler works correctly when only one engine is running.
//
// It also carries §4.5's section-lock re-check ticker: this stream
// classifies as {queue}, so locking the Queue tab while a stream is open
// terminates it within one tick rather than letting it stream the queue for
// the tab's lifetime. recheckInterval is a test-only override.
// It applies the same Adult filter listDownloadsHandler does, but resolved
// LIVE on every snapshot rather than from the frozen context Decision.
//
// A stream outlives the request that opened it, so the Decision it captured
// cannot see a section locked afterwards — and this route classifies as
// {queue}, so the re-check ticker terminates it on a QUEUE lock but not on an
// adult-content one. Without a live read, locking Adult mid-stream would leave
// Adult release names flowing to an already-open tab until it was reloaded.
// sectionlock.StreamRevoked is exactly that live read (§4.5's sanctioned
// exception, see sectionlock.LiveLookup); asking it about the adult-content set
// answers "must this stream stop showing Adult now", which is the question here
// — it already handles the frozen-decision trap, the ticket, and the epoch.
func adultHiddenNow(r *http.Request) bool {
	if adultLocked(r.Context()) {
		return true
	}
	return sectionlock.StreamRevoked(r, sectionlock.NewSet(sectionlock.SectionAdultContent))
}

func downloadsStreamHandler(dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store, recheckInterval ...time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil && nzb == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ctx := r.Context()

		// snapshot resolves the Adult filter FRESH on every frame — see
		// adultHiddenNow for why a stream cannot use the frozen context
		// Decision the way listDownloadsHandler does.
		snapshot := func() []apidto.Download {
			rows := mergedDownloads(dl, nzb)
			if adultHiddenNow(r) {
				rows = filterAdultDownloads(ctx, grabsStore, rows)
			}
			return rows
		}

		// Paint an initial snapshot immediately so the screen isn't blank until
		// the queue next changes.
		writeSSEData(w, flusher, snapshot())

		// Subscribe to both managers. A nil channel blocks forever in a select,
		// so the unconfigured engine's case simply never fires — correct behavior.
		var dlCh <-chan []downloader.Download
		var dlCancel func()
		if dl != nil {
			dlCh, dlCancel = dl.Subscribe()
			defer dlCancel()
		}
		var nzbCh <-chan []usenet.Download
		var nzbCancel func()
		if nzb != nil {
			nzbCh, nzbCancel = nzb.Subscribe()
			defer nzbCancel()
		}

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
			case _, ok := <-dlCh:
				if !ok {
					return
				}
				writeSSEData(w, flusher, snapshot())
			case _, ok := <-nzbCh:
				if !ok {
					return
				}
				writeSSEData(w, flusher, snapshot())
			}
		}
	}
}

// writeSSEData marshals v and writes it as one SSE data frame, then flushes.
func writeSSEData(w http.ResponseWriter, flusher http.Flusher, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// routeGIDAction routes a single-GID download action to the correct engine by
// GID prefix ("nzb-" → usenet; anything else → torrent). Returns true on
// success, false (with the error already written) on failure.
func routeGIDAction(w http.ResponseWriter, r *http.Request, dl *downloader.Manager, nzb *usenet.Manager, dlFn, nzbFn func(string) error) bool {
	gid := r.PathValue("gid")
	var err error
	if strings.HasPrefix(gid, "nzb-") {
		if nzb == nil {
			http.Error(w, "the usenet engine isn't running", http.StatusServiceUnavailable)
			return false
		}
		err = nzbFn(gid)
	} else {
		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return false
		}
		err = dlFn(gid)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return false
	}
	return true
}

func cancelDownloadHandler(dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if routeGIDAction(w, r, dl, nzb, dl.Cancel, nzb.Cancel) {
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func pauseDownloadHandler(dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if routeGIDAction(w, r, dl, nzb, dl.Pause, nzb.Pause) {
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func resumeDownloadHandler(dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if routeGIDAction(w, r, dl, nzb, dl.Resume, nzb.Resume) {
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

// cancelOne routes a single GID to the correct engine's Cancel (same "nzb-"
// prefix routing as routeGIDAction) and returns any error — the non-HTTP core
// the bulk-cancel loop reuses so one item's failure can be recorded and skipped
// rather than aborting the whole request.
func cancelOne(dl *downloader.Manager, nzb *usenet.Manager, gid string) error {
	if strings.HasPrefix(gid, "nzb-") {
		if nzb == nil {
			return errors.New("the usenet engine isn't running")
		}
		return nzb.Cancel(gid)
	}
	if dl == nil {
		return errors.New("the download engine isn't running")
	}
	return dl.Cancel(gid)
}

// bulkCancelHandler backs POST /api/downloads/cancel-batch — cancel several
// downloads (deleting their files, same as the per-item DELETE) in one call.
// Reuses the per-GID Cancel methods in a loop with skip-and-continue semantics
// (one item's failure never blocks the rest), matching this project's bulk-apply
// convention. Always HTTP 200 (except the empty-body 400); per-item results
// carry each GID's outcome.
func bulkCancelHandler(dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil && nzb == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		var req apidto.BulkCancelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.GIDs) == 0 {
			http.Error(w, "gids is required", http.StatusBadRequest)
			return
		}
		results := make([]apidto.BulkCancelResultItem, 0, len(req.GIDs))
		for _, gid := range req.GIDs {
			res := apidto.BulkCancelResultItem{GID: gid}
			if err := cancelOne(dl, nzb, gid); err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
			}
			results = append(results, res)
		}
		writeJSON(w, apidto.BulkCancelResponse{Results: results})
	}
}

// getPauseStateHandler returns the global download pause flag.
func getPauseStateHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		paused, err := settingsStore.GetBool(r.Context(), downloadsGlobalPausedKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, apidto.DownloadPauseState{Paused: paused})
	}
}

// putPauseStateHandler sets the global download pause flag. Setting it true
// pauses every currently-active download (reusing each engine's per-item Pause,
// best-effort/skip-and-continue) AND — via the gate in dispatchToDownloadClient
// keyed on the same flag — blocks any new grab until it's set back to false.
// Setting it false only lifts that gate: it does NOT auto-resume the downloads
// this toggle paused (per-item Resume stays the operator's tool, and usenet has
// no resume anyway — re-submit the NZB), scoped deliberately to the spec.
func putPauseStateHandler(settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.DownloadPauseState
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.SetBool(ctx, downloadsGlobalPausedKey, req.Paused); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if req.Paused {
			pauseAllActive(dl, nzb)
		}
		writeJSON(w, apidto.DownloadPauseState{Paused: req.Paused})
	}
}

// pauseAllActive pauses every currently in-flight download across both engines,
// best-effort: a per-item Pause failure (e.g. an entry that just completed) is
// skipped, never fatal. nil engines are simply skipped.
func pauseAllActive(dl *downloader.Manager, nzb *usenet.Manager) {
	if dl != nil {
		for _, d := range dl.List() {
			if d.Status == "active" || d.Status == "waiting" {
				_ = dl.Pause(d.GID)
			}
		}
	}
	if nzb != nil {
		for _, d := range nzb.List() {
			if d.Status == "active" {
				_ = nzb.Pause(d.GID)
			}
		}
	}
}

// getDownloaderConfigHandler returns the downloader's staging dir + concurrency
// knobs plus the torrent-engine behavior knobs, filling in defaults for unset
// fields (staging dir "" when unset — the caller/boot supplies the real default
// path, this handler has no dataDir to synthesize one from).
func getDownloaderConfigHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := readDownloaderConfig(r.Context(), settingsStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, cfg)
	}
}

// readDownloaderConfig reads the whole downloader settings document, filling in
// the documented default for every unset key. Two callers: the GET handler, and
// the PUT handler — which needs the PRE-EDIT document to work out whether the
// incoming save changed a rebuild-class field or only live-patchable ones.
func readDownloaderConfig(ctx context.Context, settingsStore *settings.Store) (apidto.DownloaderConfig, error) {
	var zero apidto.DownloaderConfig
	staging, err := getSetting(ctx, settingsStore, DownloaderStagingDirKey)
	if err != nil {
		return zero, err
	}
	conc, err := getSettingInt(ctx, settingsStore, DownloaderMaxConcurrentKey, DownloaderDefaultMaxConcurrent)
	if err != nil {
		return zero, err
	}
	conn, err := getSettingInt(ctx, settingsStore, DownloaderMaxConnectionsKey, DownloaderDefaultMaxConnections)
	if err != nil {
		return zero, err
	}
	rate, err := getSettingInt(ctx, settingsStore, TorrentDownloadRateLimitKey, TorrentDefaultDownloadRateLimit)
	if err != nil {
		return zero, err
	}
	dht, err := getSettingBool(ctx, settingsStore, TorrentDHTEnabledKey, TorrentDefaultDHTEnabled)
	if err != nil {
		return zero, err
	}
	pex, err := getSettingBool(ctx, settingsStore, TorrentPEXEnabledKey, TorrentDefaultPEXEnabled)
	if err != nil {
		return zero, err
	}
	port, err := getSettingInt(ctx, settingsStore, TorrentListenPortKey, TorrentDefaultListenPort)
	if err != nil {
		return zero, err
	}
	obfs, err := getSetting(ctx, settingsStore, TorrentObfuscationModeKey)
	if err != nil {
		return zero, err
	}
	if obfs == "" {
		obfs = TorrentDefaultObfuscationMode
	}
	seeding, err := getSettingBool(ctx, settingsStore, TorrentSeedingEnabledKey, TorrentDefaultSeedingEnabled)
	if err != nil {
		return zero, err
	}
	ratio, err := getSettingFloat(ctx, settingsStore, TorrentSeedRatioLimitKey, TorrentDefaultSeedRatioLimit)
	if err != nil {
		return zero, err
	}
	seedMinutes, err := getSettingInt(ctx, settingsStore, TorrentSeedDurationMinutesKey, TorrentDefaultSeedDurationMinutes)
	if err != nil {
		return zero, err
	}
	staleMinutes, err := getSettingInt(ctx, settingsStore, TorrentStaleThresholdMinutesKey, TorrentDefaultStaleThresholdMinutes)
	if err != nil {
		return zero, err
	}
	return apidto.DownloaderConfig{
		StagingDir:             staging,
		MaxConcurrent:          conc,
		MaxConnections:         conn,
		DownloadRateLimitBytes: rate,
		DHTEnabled:             dht,
		PEXEnabled:             pex,
		ListenPort:             port,
		ObfuscationMode:        obfs,
		SeedingEnabled:         seeding,
		SeedRatioLimit:         ratio,
		SeedDurationMinutes:    seedMinutes,
		StaleThresholdMinutes:  staleMinutes,
	}, nil
}

// putDownloaderConfigHandler stores the torrent downloader's staging dir +
// concurrency knobs. MaxConnections here applies to the torrent engine only —
// Usenet connection counts are configured per-subscription in the Usenet
// settings page instead (see DownloaderMaxConnectionsKey's doc comment).
// Concurrency values must be positive.
//
// Claude 2026-08-01: staging dir is validated HERE now (absolute, not an
// obvious system directory, and creatable — see validateStagingDir).
// Reason: this comment used to claim the value was "validated for existence and
// writability by the engine rebuild this save triggers". That was false — the
// engine's only staging-dir handling is downloader.go:429-431, which MkdirAlls
// and log.Printfs any error without returning it, so nothing ever reached this
// handler and any writable path (/etc, /) was accepted. Note the check is
// "creatable", NOT "writable": os.MkdirAll returns nil for an existing
// directory without probing it. A read-only existing directory still fails
// later, at download time, in the log.
// Troubleshooting: the torrent engine creating and deleting torrent-declared
// filenames somewhere it should never have been pointed.
// Review if: the engine starts returning its staging-dir error from
// Reconfigure, which would make a handler-side creatability check redundant.
//
// A change takes effect IMMEDIATELY, not on restart: the validated document is
// handed to Manager.Reconfigure, which either live-patches the running engine
// or rebuilds it (see downloaderRebuildClass). The response says which happened.
//
// ORDER IS LOAD-BEARING: validate -> Reconfigure -> persist. Reconfigure can
// REFUSE a rebuild that isn't safe right now (a staging-dir move with an open
// file handle; any torrent still fetching its metadata), and a refusal must
// leave the operator's stored config exactly as it was — persisting first would
// strand a setting that the engine rejected, which is the "stored and ignored"
// failure this whole feature exists to prevent. The residual risk runs the
// other way and is accepted: if a settings write fails AFTER a successful
// Reconfigure, the live engine leads the store until the next restart. That is
// a 500 the operator sees and can retry, versus a silent permanent divergence.
//
// This is a FULL-DOCUMENT REPLACE, not a patch: none of the fields are
// pointers or omitempty, so an omitted field arrives as its zero value and is
// stored as such. A client must therefore GET, mutate, and PUT the whole
// document back — omitting maxConcurrent/maxConnections/listenPort is a loud
// 400, but omitting dhtEnabled/pexEnabled silently DISABLES them (their
// defaults are true), and omitting the rate/ratio/duration/stale fields
// silently means unlimited/off. The plain-type choice is deliberate (it keeps
// the wire contract readable and matches the pre-existing maxConcurrent
// guard's shape); the round-trip requirement is what pays for it, and
// TestDownloaderConfig_OmittedMaxConcurrentIs400 pins it.
func putDownloaderConfigHandler(settingsStore *settings.Store, dl *downloader.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.DownloaderConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// Everything is validated before anything is stored: interleaving
		// validate-then-Set would leave a rejected request half-persisted.
		if req.MaxConcurrent < 1 || req.MaxConnections < 1 {
			http.Error(w, "maxConcurrent and maxConnections must be at least 1", http.StatusBadRequest)
			return
		}
		if req.ListenPort < torrentListenPortMin || req.ListenPort > torrentListenPortMax {
			http.Error(w, fmt.Sprintf("listenPort must be between %d and %d", torrentListenPortMin, torrentListenPortMax), http.StatusBadRequest)
			return
		}
		switch req.ObfuscationMode {
		case torrentObfuscationRequire, torrentObfuscationPrefer, torrentObfuscationOff:
		default:
			http.Error(w, fmt.Sprintf("obfuscationMode must be one of %q, %q, %q", torrentObfuscationRequire, torrentObfuscationPrefer, torrentObfuscationOff), http.StatusBadRequest)
			return
		}
		if req.DownloadRateLimitBytes < 0 {
			http.Error(w, "downloadRateLimitBytes must be zero (unlimited) or greater", http.StatusBadRequest)
			return
		}
		// The three bounds below are UPPER bounds as well as lower ones. An
		// unbounded value does not saturate, it WRAPS NEGATIVE and inverts the
		// setting — see torrentMinutesMax's comment for the full mechanism.
		// NaN is checked explicitly because `< 0` is false for it; encoding/json
		// has no NaN literal so it cannot arrive over the wire today, but the
		// guard costs nothing and the ceiling is what does the real work.
		if req.SeedRatioLimit < 0 || math.IsNaN(req.SeedRatioLimit) || req.SeedRatioLimit > torrentSeedRatioMax {
			http.Error(w, fmt.Sprintf("seedRatioLimit must be between zero (no ratio limit) and %g", torrentSeedRatioMax), http.StatusBadRequest)
			return
		}
		if req.SeedDurationMinutes < 0 || req.SeedDurationMinutes > torrentMinutesMax {
			http.Error(w, fmt.Sprintf("seedDurationMinutes must be between zero (no duration limit) and %d", torrentMinutesMax), http.StatusBadRequest)
			return
		}
		if req.StaleThresholdMinutes < 0 || req.StaleThresholdMinutes > torrentMinutesMax {
			http.Error(w, fmt.Sprintf("staleThresholdMinutes must be between zero (detection disabled) and %d", torrentMinutesMax), http.StatusBadRequest)
			return
		}
		// Last, because it is the only validation with a side effect (MkdirAll).
		if msg := validateStagingDir(req.StagingDir); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		// The pre-edit document is what classifies the save. It comes from the
		// same store the engine's boot-time reader uses, and now from the same
		// default constants too (cmd/sakms/main.go's buildDownloader reads
		// api.TorrentDefault*/api.DownloaderDefault* directly), so old-vs-new
		// here matches what Reconfigure computes internally — with the one
		// documented staging-dir exception in downloaderRebuildClass's comment.
		old, err := readDownloaderConfig(ctx, settingsStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Classification compares the STORED old document against the STORED new
		// one — req, unsubstituted. See downloaderRebuildClass's doc comment for
		// why the staging-dir case is deliberately classified this way.
		rebuild := downloaderRebuildClass(old, req)

		// Claude 2026-08-01: substitution now affects the APPLIED value only;
		// it no longer rewrites either side of the classification.
		// Reason: substituting the resolved live path into both `old` and `next`
		// made "clear stagingDir back to the boot default" compare equal to
		// itself, so a save that really did change the stored value (a real path
		// -> "") was reported as a live, no-restart-needed change. The next boot
		// then re-derives <dataDir>/downloads, which need not be the path that
		// was in use — behavior silently changing across a restart with no
		// warning, the exact failure class M11 exists to prevent.
		// Troubleshooting: staging dir reverting on restart after a save the UI
		// reported as applied instantly.
		// Review if: this handler gains a dataDir and can resolve the default
		// itself, which would remove the store/engine disagreement entirely.
		//
		// "" in the request still means "leave it at the boot default", so it
		// resolves to the live path rather than handing the torrent client an
		// empty DataDir (which would write to the cwd). "" remains what gets
		// STORED, so boot keeps re-deriving the default.
		next := req
		if dl != nil && next.StagingDir == "" {
			next.StagingDir = dl.StagingDir()
		}

		if dl != nil {
			applied := downloader.Config{
				StagingDir:            next.StagingDir,
				MaxConc:               next.MaxConcurrent,
				MaxConn:               next.MaxConnections,
				DownloadRateLimit:     next.DownloadRateLimitBytes,
				DHTEnabled:            next.DHTEnabled,
				PEXEnabled:            next.PEXEnabled,
				ListenPort:            next.ListenPort,
				ObfuscationMode:       next.ObfuscationMode,
				SeedingEnabled:        next.SeedingEnabled,
				SeedRatioLimit:        next.SeedRatioLimit,
				SeedDurationMinutes:   next.SeedDurationMinutes,
				StaleThresholdMinutes: next.StaleThresholdMinutes,
			}
			if err := dl.Reconfigure(ctx, applied); err != nil {
				if errors.Is(err, downloader.ErrRebuildRefused) {
					// Not a failure to save — a "not right now". The message
					// names the blocking download and what to do about it, so
					// it goes to the operator verbatim. Nothing was persisted
					// and nothing was applied; a retry in a moment succeeds.
					http.Error(w, err.Error(), http.StatusConflict)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		for _, kv := range []struct{ key, value string }{
			{DownloaderStagingDirKey, req.StagingDir},
			{DownloaderMaxConcurrentKey, strconv.Itoa(req.MaxConcurrent)},
			{DownloaderMaxConnectionsKey, strconv.Itoa(req.MaxConnections)},
			{TorrentDownloadRateLimitKey, strconv.Itoa(req.DownloadRateLimitBytes)},
			{TorrentDHTEnabledKey, strconv.FormatBool(req.DHTEnabled)},
			{TorrentPEXEnabledKey, strconv.FormatBool(req.PEXEnabled)},
			{TorrentListenPortKey, strconv.Itoa(req.ListenPort)},
			{TorrentObfuscationModeKey, req.ObfuscationMode},
			{TorrentSeedingEnabledKey, strconv.FormatBool(req.SeedingEnabled)},
			{TorrentSeedRatioLimitKey, strconv.FormatFloat(req.SeedRatioLimit, 'f', -1, 64)},
			{TorrentSeedDurationMinutesKey, strconv.Itoa(req.SeedDurationMinutes)},
			{TorrentStaleThresholdMinutesKey, strconv.Itoa(req.StaleThresholdMinutes)},
		} {
			if err := settingsStore.Set(ctx, kv.key, kv.value); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, downloaderApplyResult(rebuild))
	}
}

// downloaderApplyResult turns the rebuild classification into §6.3's
// apply-result body. Split out so the copy has one home and the tests can
// assert the classification without matching prose.
func downloaderApplyResult(rebuild bool) apidto.DownloaderConfigApplyResult {
	if rebuild {
		return apidto.DownloaderConfigApplyResult{
			Applied:         "rebuilt",
			RestartRequired: true,
			Message:         "Saved. Applying these settings restarted the torrent engine — any downloads in flight were briefly interrupted and have resumed.",
		}
	}
	return apidto.DownloaderConfigApplyResult{
		Applied:         "live",
		RestartRequired: false,
		Message:         "Saved and applied immediately. No downloads were interrupted.",
	}
}

// downloaderRebuildClass reports whether moving from old to next needs the
// torrent engine rebuilt rather than live-patched.
//
// THIS MIRRORS downloader.rebuildRequired, WHICH IS UNEXPORTED. The engine
// decides the real apply path from its own copy of this comparison; this one
// only decides what the response TELLS the operator. They must agree, so a
// change to either belongs in both — the failure mode of a divergence is a save
// that quietly reports "applied immediately" while the engine tore itself down
// (or the reverse), which is a lie in the exact place this feature exists to
// stop lying.
//
// Claude 2026-08-01: ONE deliberate, permanent exception to that mirror.
// Reason: this function is now called with the STORED old and new documents,
// while the engine compares RESOLVED paths. Those agree for every field except
// staging dir cleared to "": stored "/srv/dl" -> "" is a real change here and a
// no-op to the engine (whose resolved path is unchanged either way), so this
// over-reports rebuild for that one transition. That is the correct direction to
// be wrong in. The stored value DID change, and the next boot re-derives
// <dataDir>/downloads, which need not be the path currently in use — so
// "restartRequired" is the honest answer even though nothing was torn down
// right now. RestartRequired's contract (see apidto.DownloaderConfigApplyResult)
// is "this change did not take effect quietly and instantly", which holds.
// Reporting "live" instead would be the far worse lie: applies now, silently
// reverts on restart.
// Troubleshooting: a staging-dir clear reported as an instant no-op save.
// Review if: this handler gains a dataDir and can resolve the default itself,
// at which point stored and resolved comparisons converge and the exception
// should be deleted rather than documented.
//
// Note the seeding case is DIRECTIONAL, not !=. The engine's upload gate
// (config.NoUpload / config.Seed) is fixed at client construction, so turning
// seeding ON cannot be patched into a running client and needs a rebuild;
// turning it OFF is genuinely live, because the per-torrent DisallowDataUpload
// call short-circuits ahead of it.
func downloaderRebuildClass(old, next apidto.DownloaderConfig) bool {
	switch {
	case old.StagingDir != next.StagingDir:
		return true
	case old.ListenPort != next.ListenPort:
		return true
	case old.DHTEnabled != next.DHTEnabled:
		return true
	case old.PEXEnabled != next.PEXEnabled:
		return true
	case old.ObfuscationMode != next.ObfuscationMode:
		return true
	}
	return next.SeedingEnabled && !old.SeedingEnabled
}

// getSetting returns a settings value, "" when unset (ErrNotFound is a normal
// "not configured" state, not an error).
func getSetting(ctx context.Context, store *settings.Store, key string) (string, error) {
	v, err := store.Get(ctx, key)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", err
	}
	return v, nil
}

// getSettingInt returns a settings value parsed as int, or def when unset or
// unparseable.
func getSettingInt(ctx context.Context, store *settings.Store, key string, def int) (int, error) {
	v, err := getSetting(ctx, store, key)
	if err != nil {
		return 0, err
	}
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, nil
	}
	return n, nil
}

// getSettingBool returns a settings value parsed as bool, or def when unset or
// unparseable. Same tolerance as getSettingInt: a corrupted value degrades to
// the documented default rather than failing the whole config read.
func getSettingBool(ctx context.Context, store *settings.Store, key string, def bool) (bool, error) {
	v, err := getSetting(ctx, store, key)
	if err != nil {
		return false, err
	}
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, nil
	}
	return b, nil
}

// getSettingFloat returns a settings value parsed as float64, or def when unset
// or unparseable. Mirrors getSettingInt's tolerance.
func getSettingFloat(ctx context.Context, store *settings.Store, key string, def float64) (float64, error) {
	v, err := getSetting(ctx, store, key)
	if err != nil {
		return 0, err
	}
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def, nil
	}
	return f, nil
}
