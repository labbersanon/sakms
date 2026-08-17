package api

import (
	"encoding/json"
	"net/http"

	"github.com/labbersanon/sakms/internal/settings"
)

// This file holds the GET/PUT settings endpoints for the general scan
// scheduler (internal/scanschedule): per workflow, one enabled toggle and one
// interval, plus the Dedup eager-VMAF toggle. Enabled and interval are
// deliberately separate endpoints, never one combined DTO — that is what makes
// turning a workflow off leave its stored interval untouched.
//
// Each key string is mirrored here by value rather than importing
// internal/scanschedule — the same import-avoidance recheck.go uses so the
// scheduler stays independently deletable (these endpoints would just manage
// inert settings nothing reads). The 24h default is mirrored the same way, for
// the same reason; both mirrors are guarded by
// scanschedule_settings_key_parity_test.go. Interval parse/degrade/validate
// logic is the shared loadIntervalSeconds/storeIntervalSeconds (interval.go);
// the bools use the settings store directly.
const (
	renameScanIntervalKey   = "rename_scan_interval_seconds"
	purgeScanIntervalKey    = "purge_scan_interval_seconds"
	dedupScanIntervalKey    = "dedup_scan_interval_seconds"
	dedupVMAFScanEnabledKey = "dedup_vmaf_scan_enabled"

	// Per-workflow master on/off keys, mirrored by value from
	// internal/scanschedule's RenameEnabledKey/PurgeEnabledKey/DedupEnabledKey
	// exactly like the interval keys above, and covered by the same parity test.
	// Genuinely separate from the interval keys: turning a workflow off leaves
	// its stored interval untouched, so re-enabling restores the operator's
	// configured cadence.
	renameScanEnabledKey = "rename_scan_enabled"
	purgeScanEnabledKey  = "purge_scan_enabled"
	dedupScanEnabledKey  = "dedup_scan_enabled"

	// minScanIntervalSeconds is the floor storeIntervalSeconds enforces for
	// these keys only (recheck/adult-newest-scan pass 0, no floor) — see the
	// call site in putScanIntervalHandler for why a mutating-Scan scheduler
	// needs one and a read-only probe doesn't.
	minScanIntervalSeconds = 60

	// defaultScanIntervalSeconds is the unset-key default for the three
	// workflow interval keys: 24h, on by default (changed 2026-08-10 from 0/off
	// — see .omc/specs/deep-interview-scanschedule-settings-ui.md).
	//
	// It is a VALUE MIRROR of scanschedule.DefaultIntervalSeconds, not an
	// import, for the same independently-deletable reason this file's header
	// gives for the key strings — and it is guarded the same way, by
	// TestScanScheduleSettingsKeyParity. That pairing matters more here than for
	// the keys: this default only controls what a GET (and so the Settings UI)
	// reports, while internal/scanschedule's own copy is what actually decides
	// whether a scan runs. If the two drifted, the UI could report "on, every
	// 24h" for an unset key while nothing ever scanned.
	defaultScanIntervalSeconds = 24 * 60 * 60 // 86400

	// defaultScanEnabled is the unset-key default for the three *_scan_enabled
	// keys. dedupVMAFScanEnabledKey now shares the same true default (changed
	// 2026-08-17) — still a separate key, still not a master schedule switch.
	defaultScanEnabled = true

	// defaultDedupVMAFScanEnabled is the unset-key default for
	// dedup_vmaf_scan_enabled. Mirrored by value with scanschedule.runCycle's
	// GetBool default; both must stay true so the Organize Switch and the
	// scheduler cannot disagree on an unset key.
	defaultDedupVMAFScanEnabled = true
)

type scanIntervalResponse struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type scanIntervalRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type dedupVMAFScanEnabledResponse struct {
	Enabled bool `json:"enabled"`
}

type dedupVMAFScanEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

type scanEnabledResponse struct {
	Enabled bool `json:"enabled"`
}

type scanEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// getScanIntervalHandler returns the configured interval-in-seconds for one
// scan-scheduler key. A genuinely-unset key reads as defaultScanIntervalSeconds
// (24h, on by default); a stored-but-corrupt or explicitly-zeroed value reads
// as 0 ("off"). Never a 500 for a bad stored value.
func getScanIntervalHandler(settingsStore *settings.Store, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secs, err := loadIntervalSeconds(r.Context(), settingsStore, key, defaultScanIntervalSeconds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(scanIntervalResponse{IntervalSeconds: secs})
	}
}

// putScanIntervalHandler stores an interval-in-seconds for one scan-scheduler
// key. 0 disables that workflow's schedule (and, once written, suppresses the
// unset-key default permanently — that is the point of the unset-vs-stored
// distinction in loadIntervalSeconds/scanschedule.LoadInterval); a negative
// value is rejected. A change takes effect on the running loop's next tick if
// it was already running, or on next restart if it was off at boot (see
// scanschedule.Run's doc).
func putScanIntervalHandler(settingsStore *settings.Store, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req scanIntervalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// 60s floor: unlike recheck/adult-newest-scan's read-only probes, a scan
		// cycle here triggers real Rename/Purge/Dedup Scan work (and, for Dedup,
		// eager VMAF) — an operator setting e.g. 1s would drive continuous
		// back-to-back scanning. AC15's skip-not-queue drain bounds the worst
		// case to "always running," not runaway goroutines, but a sane floor
		// prevents the misconfiguration outright rather than merely bounding it.
		badRequest, err := storeIntervalSeconds(r.Context(), settingsStore, key, req.IntervalSeconds, minScanIntervalSeconds)
		if err != nil {
			status := http.StatusInternalServerError
			if badRequest {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getScanEnabledHandler returns one workflow's master schedule switch. Unset
// reads as true (on by default) — the API-layer half of that behavior; the
// scheduler reads the same key itself with the same default (see
// scanschedule.Run), which is the half that actually decides whether a scan
// runs. Same factory shape as getScanIntervalHandler, parameterized by key.
func getScanEnabledHandler(settingsStore *settings.Store, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, err := settingsStore.GetBool(r.Context(), key, defaultScanEnabled)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(scanEnabledResponse{Enabled: enabled})
	}
}

// putScanEnabledHandler turns one workflow's schedule on or off. It never
// touches that workflow's interval key — toggling off and back on restores the
// operator's configured cadence rather than making them retype it. Turning a
// workflow ON takes effect on the app's next restart (a loop that was off at
// boot started no ticker); turning one OFF stops a running loop on its next
// tick.
func putScanEnabledHandler(settingsStore *settings.Store, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req scanEnabledRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := settingsStore.SetBool(r.Context(), key, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getDedupVMAFScanEnabledHandler returns whether a scheduled Dedup cycle
// eagerly computes VMAF scores for the groups it finds (on by default).
func getDedupVMAFScanEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, err := settingsStore.GetBool(r.Context(), dedupVMAFScanEnabledKey, defaultDedupVMAFScanEnabled)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dedupVMAFScanEnabledResponse{Enabled: enabled})
	}
}

// putDedupVMAFScanEnabledHandler enables or disables eager VMAF on scheduled
// Dedup cycles. Independent of the Dedup scan interval (a schedule can run with
// or without eager VMAF).
func putDedupVMAFScanEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req dedupVMAFScanEnabledRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := settingsStore.SetBool(r.Context(), dedupVMAFScanEnabledKey, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
