package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/phash"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/settings"
)

// moviesLibraryRootFolderKey, seriesLibraryRootFolderKey, and
// adultLibraryRootFolderKey are the settings keys holding each mode's
// library root folder path — the free-typed replacement for picking a path
// from a *arr app's own RootFolders response, since SAK owns its own
// library (see internal/library's package doc). Adult now carries its own
// free-typed key too; the generic root-folder LISTING route
// (GET /api/modes/{mode}/root-folders) that used to proxy each mode's *arr
// app has been removed entirely (Stage 4 cleanup) — every mode's path comes
// from its own library setting here instead.
const (
	moviesLibraryRootFolderKey = "movies_library_root_folder"
	seriesLibraryRootFolderKey = "series_library_root_folder"
	adultLibraryRootFolderKey  = "adult_library_root_folder"
)

// libraryRootFolderKey returns m's library-root-folder settings key, or
// ok=false if m doesn't have one.
func libraryRootFolderKey(m mode.Mode) (key string, ok bool) {
	switch m {
	case mode.Movies:
		return moviesLibraryRootFolderKey, true
	case mode.Series:
		return seriesLibraryRootFolderKey, true
	case mode.Adult:
		return adultLibraryRootFolderKey, true
	default:
		return "", false
	}
}

type libraryRootFolderResponse struct {
	Path string `json:"path"`
}

type libraryRootFolderRequest struct {
	Path string `json:"path"`
}

// getLibraryRootFolderHandler returns {mode}'s configured library root
// folder path, or an empty string if unset. Movies, Series, and Adult all
// have a free-typed key now; only a mode without one (via
// libraryRootFolderKey) 400s.
func getLibraryRootFolderHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := libraryRootFolderKey(mode.Mode(r.PathValue("mode")))
		if !ok {
			http.Error(w, "a library root folder is only applicable to movies and series right now", http.StatusBadRequest)
			return
		}
		path, err := settingsStore.Get(r.Context(), key)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(libraryRootFolderResponse{Path: path})
	}
}

// putLibraryRootFolderHandler stores {mode}'s library root folder path.
func putLibraryRootFolderHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := libraryRootFolderKey(mode.Mode(r.PathValue("mode")))
		if !ok {
			http.Error(w, "a library root folder is only applicable to movies and series right now", http.StatusBadRequest)
			return
		}
		var req libraryRootFolderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), key, req.Path); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// pathTestRequest is the small JSON body for the root-folder path test — just
// the candidate path the operator typed. The {mode} path param isn't used: the
// check validates whatever path string is sent, full stop (see
// testLibraryRootFolderHandler).
type pathTestRequest struct {
	Path string `json:"path"`
}

// pathTestResult mirrors ConnectionTestResult's {ok,error} shape so the
// frontend can treat a path test and a connection test identically. A false OK
// with a populated Error is the normal, expected shape for a wrong/missing/
// unwritable path — not a server error.
type pathTestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// testLibraryRootFolderHandler validates that the posted path both EXISTS as a
// directory and is WRITABLE — existence alone isn't enough, since SAK writes
// into the root folder for rename/dedup. Writability is proven by actually
// creating and removing a temp file (the ground truth; a bare permission-bit
// check can lie under some filesystems/ACLs), matching the Linux-container
// deployment target.
//
// Deliberately NOT confined to browse.go's browsableRoots: that allowlist
// scopes only the autocomplete helper's suggestion range. The root folder
// itself is free-typed under this app's single-operator trust model, so the
// test validates whatever path is configured.
//
// A wrong/missing/not-a-directory/unwritable path is ordinary user input, so
// it returns {ok:false} with a clear message, never a 500 — 500 is reserved
// for a genuinely malformed request body.
func testLibraryRootFolderHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pathTestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Path == "" {
			writeJSON(w, pathTestResult{Error: "path is required"})
			return
		}

		info, err := os.Stat(req.Path)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, pathTestResult{Error: "path does not exist"})
				return
			}
			// A permission or other stat error is still "the path is wrong from
			// the operator's side," not a server fault — report it as a normal
			// failed result rather than a 500.
			writeJSON(w, pathTestResult{Error: "path is not accessible"})
			return
		}
		if !info.IsDir() {
			writeJSON(w, pathTestResult{Error: "path is not a directory"})
			return
		}

		// Write probe: creates and immediately removes a temp file to verify
		// the directory is writable by this process. This is intentionally
		// unrestricted to any known root list — single-operator trust model.
		// Under "none" auth this is reachable unauthenticated; acceptable given
		// the deployment model (internal-only middleware, no multi-tenant use).
		f, err := os.CreateTemp(req.Path, ".sak-write-test-*")
		if err != nil {
			writeJSON(w, pathTestResult{Error: "path is not writable"})
			return
		}
		f.Close()
		os.Remove(f.Name())

		writeJSON(w, pathTestResult{OK: true})
	}
}

// qualityTierKey, maxResolutionKey, and protocolPreferenceKey are per-mode —
// Movies, Series, and Adult each get their own tier/cap/protocol default
// (the Discover detail popup's availability grid applies to all three, so
// all three get a configurable default — this used to say Adult had no key
// since it had no Search workflow; that stopped being true once Adult grew
// its own availability-popup search path).
func qualityTierKey(m mode.Mode) string        { return string(m) + "_quality_tier" }
func maxResolutionKey(m mode.Mode) string      { return string(m) + "_max_resolution" }
func protocolPreferenceKey(m mode.Mode) string { return string(m) + "_protocol_preference" }

type qualityPrefsResponse struct {
	Tier          string `json:"tier"`
	MaxResolution int    `json:"maxResolution"`
	Protocol      string `json:"protocol"`
}

type qualityPrefsRequest struct {
	Tier          string `json:"tier"`
	MaxResolution int    `json:"maxResolution"`
	Protocol      string `json:"protocol"`
}

// getQualityPrefsHandler returns {mode}'s Search scoring preferences —
// defaults to quality.Default ("high"), maxResolution=0 (no cap), and
// protocol="" (no preference) when unset, matching quality.ProfileFor's own
// zero-config fallback exactly for the first two.
func getQualityPrefsHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		tier, err := settingsStore.Get(ctx, qualityTierKey(m))
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if tier == "" {
			tier = string(quality.Default)
		}

		maxResStr, err := settingsStore.Get(ctx, maxResolutionKey(m))
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		maxRes := 0
		if maxResStr != "" {
			maxRes, _ = strconv.Atoi(maxResStr) // stored only via putQualityPrefsHandler, which validates first
		}

		protocol, err := settingsStore.Get(ctx, protocolPreferenceKey(m))
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(qualityPrefsResponse{Tier: tier, MaxResolution: maxRes, Protocol: protocol})
	}
}

var validQualityTiers = map[string]bool{
	string(quality.Low): true, string(quality.Medium): true,
	string(quality.High): true, string(quality.Lossless): true,
}

var validProtocolPreferences = map[string]bool{
	"": true, "usenet": true, "torrent": true,
}

// putQualityPrefsHandler stores {mode}'s Search scoring preferences.
// maxResolution must be one of the resolutions internal/release actually
// recognizes, or 0 (no cap) — an arbitrary number would silently never
// match anything in quality.ProfileFor's ladder. protocol is "" (no
// preference), "usenet", or "torrent" — matching prowlarr.Protocol's own
// values, kept as a plain string here the same way every other package that
// touches protocol does (release.Candidate, autograb.Candidate), rather than
// importing the prowlarr package solely for its two constants.
func putQualityPrefsHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		var req qualityPrefsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if !validQualityTiers[req.Tier] {
			http.Error(w, "tier must be one of: low, medium, high, lossless", http.StatusBadRequest)
			return
		}
		switch req.MaxResolution {
		case 0, 480, 720, 1080, 2160:
		default:
			http.Error(w, "maxResolution must be one of 480, 720, 1080, 2160, or 0 for no cap", http.StatusBadRequest)
			return
		}
		if !validProtocolPreferences[req.Protocol] {
			http.Error(w, "protocol must be one of: \"\" (no preference), usenet, torrent", http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		if err := settingsStore.Set(ctx, qualityTierKey(m), req.Tier); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, maxResolutionKey(m), strconv.Itoa(req.MaxResolution)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, protocolPreferenceKey(m), req.Protocol); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// namingPresetKey is per-mode — Movies and Series each pick their own
// naming convention independently (e.g. a small Movies library on the
// Jellyfin/Emby standard while an already-renamed Series library stays on
// Legacy). Adult has no Rename-into-a-computed-name concept, so no key
// exists for it.
func namingPresetKey(m mode.Mode) string { return string(m) + "_naming_preset" }

// resolveNamingPreset loads m's naming-preset setting, defaulting to
// naming.Jellyfin when unset — the same fallback getNamingPresetHandler
// reports over the API, reused by rename.go/proposals.go's Scan/Apply
// handlers so Rename actually applies whatever preset is configured.
func resolveNamingPreset(ctx context.Context, settingsStore *settings.Store, m mode.Mode) (naming.Preset, error) {
	presetStr, err := settingsStore.Get(ctx, namingPresetKey(m))
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", err
	}
	if presetStr == "" {
		return naming.Jellyfin, nil
	}
	return naming.Preset(presetStr), nil
}

type namingPresetResponse struct {
	Preset string `json:"preset"`
}

type namingPresetRequest struct {
	Preset string `json:"preset"`
}

// getNamingPresetHandler returns {mode}'s configured file/folder naming
// preset — defaults to "jellyfin" when unset.
func getNamingPresetHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preset, err := resolveNamingPreset(r.Context(), settingsStore, mode.Mode(r.PathValue("mode")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(namingPresetResponse{Preset: string(preset)})
	}
}

// putNamingPresetHandler stores {mode}'s file/folder naming preset.
func putNamingPresetHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		var req namingPresetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if !naming.Valid(naming.Preset(req.Preset)) {
			http.Error(w, "preset must be one of: jellyfin, legacy", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), namingPresetKey(m), req.Preset); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// phashThresholdKey is per-mode — the Dedup perceptual-hash similarity cut is
// configured independently per mode (only Movies reads it today, but the
// endpoint is per-mode-generic like naming-preset). Stored as the string form
// of an int (per-frame average Hamming bits).
func phashThresholdKey(m mode.Mode) string { return string(m) + "_phash_dedup_threshold" }

// phashModeDefault returns the factory-default per-frame Hamming threshold for
// mode m. Movies uses 25 (≈60% similarity) — more permissive than Series
// because there is no within-show shared-intro false-positive risk for Movies.
// All other modes use phash.DefaultThreshold (10).
func phashModeDefault(m mode.Mode) int {
	if m == mode.Movies {
		return phash.DefaultMoviesThreshold
	}
	return phash.DefaultThreshold
}

// resolvePHashThreshold loads m's Dedup phash similarity threshold, defaulting
// to phashModeDefault(m) when unset — the same fallback getPHashThresholdHandler
// reports, reused by dedup.go's Scan handler. A stored value is always a
// validated int (putPHashThresholdHandler rejects otherwise), so a parse
// failure falls back to the mode default rather than failing a Scan.
func resolvePHashThreshold(ctx context.Context, settingsStore *settings.Store, m mode.Mode) (int, error) {
	raw, err := settingsStore.Get(ctx, phashThresholdKey(m))
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return 0, err
	}
	if raw == "" {
		return phashModeDefault(m), nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return phashModeDefault(m), nil
	}
	return v, nil
}

type phashThresholdResponse struct {
	Threshold int `json:"threshold"`
}

type phashThresholdRequest struct {
	Threshold int `json:"threshold"`
}

// confidenceThresholdKey is per-mode — the Rename match-confidence cut is
// configured independently per mode (only Movies/Series read it today, since
// Adult's identification path doesn't use TMDB's items[0]-style search at
// all), mirroring phashThresholdKey's per-mode shape. Stored as the string
// form of an int (a 0-100 percentage, see internal/rename's matchConfidence).
func confidenceThresholdKey(m mode.Mode) string { return string(m) + "_rename_confidence_threshold" }

// resolveConfidenceThreshold loads m's Rename match-confidence threshold,
// defaulting to rename.DefaultConfidenceThreshold when unset — the same
// fallback getConfidenceThresholdHandler reports, reused by rename.go's Scan
// handler so ScanLibrary/ScanLibrarySeries gate on whatever is configured. A
// stored value is always a validated int (putConfidenceThresholdHandler
// rejects otherwise), so a parse failure falls back to the default rather
// than failing a Scan — same tolerance as resolvePHashThreshold.
func resolveConfidenceThreshold(ctx context.Context, settingsStore *settings.Store, m mode.Mode) (int, error) {
	raw, err := settingsStore.Get(ctx, confidenceThresholdKey(m))
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return 0, err
	}
	if raw == "" {
		return rename.DefaultConfidenceThreshold, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return rename.DefaultConfidenceThreshold, nil
	}
	return v, nil
}

type confidenceThresholdResponse struct {
	Threshold int `json:"threshold"`
}

type confidenceThresholdRequest struct {
	Threshold int `json:"threshold"`
}

// getConfidenceThresholdHandler returns {mode}'s Rename match-confidence
// threshold (0-100 percentage) — defaults to rename.DefaultConfidenceThreshold
// when unset.
func getConfidenceThresholdHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		threshold, err := resolveConfidenceThreshold(r.Context(), settingsStore, mode.Mode(r.PathValue("mode")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(confidenceThresholdResponse{Threshold: threshold})
	}
}

// putConfidenceThresholdHandler stores {mode}'s Rename match-confidence
// threshold. Rejects a value outside 0-100 (a percentage), mirroring
// putPHashThresholdHandler's invalid-input rejection.
func putConfidenceThresholdHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		var req confidenceThresholdRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Threshold < 0 || req.Threshold > 100 {
			http.Error(w, "threshold must be between 0 and 100", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), confidenceThresholdKey(m), strconv.Itoa(req.Threshold)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// adultIdentifyEnabledKey gates Adult phash-first identification. Unlike the
// per-mode naming-preset/phash-threshold keys, this is a fixed const, not
// string(m)+"...": only Adult ever reaches rename.Scan (Movies/Series dispatch
// to ScanLibrary*), so the toggle is Adult-only. Stored as "true"/"false".
const adultIdentifyEnabledKey = "adult_identify_enabled"

// resolveAdultIdentifyEnabled loads Adult's identify-enabled toggle, defaulting
// to true (phash-first is the intended default now that it no longer needs a
// live Stash). Returns true both when unset AND on any parse error — never fail
// a scan over a malformed setting, the same tolerance resolvePHashThreshold has.
func resolveAdultIdentifyEnabled(ctx context.Context, settingsStore *settings.Store) (bool, error) {
	raw, err := settingsStore.Get(ctx, adultIdentifyEnabledKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return false, err
	}
	if raw == "" {
		return true, nil // default ON
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return true, nil // tolerate garbage -> default ON
	}
	return v, nil
}

type identifyEnabledResponse struct {
	Enabled bool `json:"enabled"`
}

type identifyEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// getIdentifyEnabledHandler returns Adult's phash-first identify toggle
// (default true). 400s for any non-Adult mode — identification is Adult-only
// (Movies/Series don't run rename.Scan), mirroring the kids-root-path guard.
func getIdentifyEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mode.Mode(r.PathValue("mode")) != mode.Adult {
			http.Error(w, "the identification toggle only applies to adult", http.StatusBadRequest)
			return
		}
		enabled, err := resolveAdultIdentifyEnabled(r.Context(), settingsStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(identifyEnabledResponse{Enabled: enabled})
	}
}

// putIdentifyEnabledHandler stores Adult's phash-first identify toggle. 400s
// for any non-Adult mode. A bool needs no range validation (unlike the
// threshold's 0-64).
func putIdentifyEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mode.Mode(r.PathValue("mode")) != mode.Adult {
			http.Error(w, "the identification toggle only applies to adult", http.StatusBadRequest)
			return
		}
		var req identifyEnabledRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), adultIdentifyEnabledKey, strconv.FormatBool(req.Enabled)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getPHashThresholdHandler returns {mode}'s Dedup perceptual-hash similarity
// threshold (per-frame average Hamming bits) — defaults to phashModeDefault(m)
// when unset (25 for Movies, phash.DefaultThreshold for all other modes).
func getPHashThresholdHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		threshold, err := resolvePHashThreshold(r.Context(), settingsStore, mode.Mode(r.PathValue("mode")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(phashThresholdResponse{Threshold: threshold})
	}
}

// putPHashThresholdHandler stores {mode}'s Dedup perceptual-hash similarity
// threshold. Rejects a value outside 0–64 (a per-frame Hamming distance over a
// 64-bit-per-frame hash), mirroring putNamingPresetHandler's invalid-input
// rejection.
func putPHashThresholdHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		var req phashThresholdRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Threshold < 0 || req.Threshold > 64 {
			http.Error(w, "threshold must be between 0 and 64", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), phashThresholdKey(m), strconv.Itoa(req.Threshold)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
