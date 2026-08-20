package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

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
func qualityTiersKey(m mode.Mode) string       { return string(m) + "_quality_tiers" }
func maxResolutionKey(m mode.Mode) string      { return string(m) + "_max_resolution" }
func protocolPreferenceKey(m mode.Mode) string { return string(m) + "_protocol_preference" }

// Claude 2026-08-10: added undoDepthKey / MaxUndoDepth / UndoDepthFor.
// Reason: deep-interview-rename-undo — Rename Undo needs a per-mode rolling
//   depth. It rides the flat KV `settings` table with a mode-scoped key, exactly
//   like qualityTierKey above, rather than a new table; and it is read/written
//   through the EXISTING per-mode quality-prefs request so the frontend gains
//   one field instead of a second round trip. Stored as a string-encoded int
//   because internal/settings has no GetInt/SetInt and one call site does not
//   justify adding a pair.
// Troubleshooting: the configured depth had no effect on eviction — main.go
//   never wired UndoDepthFor, so the store fell back to DefaultUndoDepth.
// Review if: internal/settings grows typed getters, or per-mode settings move
//   off the flat KV table.
// Related files: internal/rename/undo_store.go, cmd/sakms/main.go

// undoDepthKey is Rename Undo's per-mode rolling depth: how many of this mode's
// most recent Applies stay undoable. Unset falls back to
// rename.DefaultUndoDepth wherever it is READ — never pre-seeded.
func undoDepthKey(m mode.Mode) string { return string(m) + "_undo_depth" }

// MaxUndoDepth bounds the configurable rolling depth. An unbounded value would
// let the archive grow without limit, defeating the eviction mechanism the spec
// requires ("prune, don't accumulate unboundedly").
const MaxUndoDepth = 100

// UndoDepthFor builds the rename.DepthFunc the undo archive evicts against.
// internal/rename owns the eviction but deliberately does not import
// internal/settings, so the resolver is supplied from here — where the key
// lives — and wired in main.go.
func UndoDepthFor(settingsStore *settings.Store) rename.DepthFunc {
	return func(ctx context.Context, m mode.Mode) int {
		raw, err := settingsStore.Get(ctx, undoDepthKey(m))
		if err != nil || raw == "" {
			return rename.DefaultUndoDepth
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return rename.DefaultUndoDepth
		}
		return n
	}
}

type qualityPrefsResponse struct {
	Tier          string   `json:"tier"`
	Tiers         []string `json:"tiers"`
	MaxResolution int      `json:"maxResolution"`
	Protocol      string   `json:"protocol"`
	UndoDepth     int      `json:"undoDepth"`
}

// UndoDepth is a POINTER, mirroring apidto.QualityPrefsRequest — see that type
// for why (a required TS field breaks the frontend build, and nil must mean
// "not sent, leave the stored value alone" rather than "use the default").
// Tiers is omitempty so an older client that only sends Tier still round-trips:
// empty/omitted Tiers means "treat Tier as a floor and expand".
type qualityPrefsRequest struct {
	Tier          string   `json:"tier"`
	Tiers         []string `json:"tiers,omitempty"`
	MaxResolution int      `json:"maxResolution"`
	Protocol      string   `json:"protocol"`
	UndoDepth     *int     `json:"undoDepth,omitempty"`
}

func parseQualityTiers(ss []string) []quality.Tier {
	seen := make(map[quality.Tier]bool, len(ss))
	out := make([]quality.Tier, 0, len(ss))
	for _, s := range ss {
		t := quality.Tier(s)
		if quality.Rank(t) <= 0 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func qualityTiersToStrings(ts []quality.Tier) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}

// resolveQualityTiers returns the accepted quality set for mode m.
// A stored JSON array (movies_quality_tiers / …) wins when it parses to at
// least one valid tier. Otherwise the legacy single movies_quality_tier
// value is treated as a floor and expanded (high → high+lossless).
func resolveQualityTiers(ctx context.Context, settingsStore *settings.Store, m mode.Mode) []quality.Tier {
	raw, err := settingsStore.Get(ctx, qualityTiersKey(m))
	if err == nil && raw != "" {
		var ss []string
		if json.Unmarshal([]byte(raw), &ss) == nil {
			if parsed := parseQualityTiers(ss); len(parsed) > 0 {
				return parsed
			}
		}
	}
	tier, err := settingsStore.Get(ctx, qualityTierKey(m))
	if err != nil || quality.Rank(quality.Tier(tier)) <= 0 {
		return quality.TiersAtOrAbove(quality.Default)
	}
	return quality.TiersAtOrAbove(quality.Tier(tier))
}

// getQualityPrefsHandler returns {mode}'s Search scoring preferences —
// defaults to quality.Default ("high") expanded to high+lossless,
// maxResolution=0 (no cap), and protocol="" (no preference) when unset,
// matching quality.ProfileFor's own zero-config fallback exactly for the
// first two.
func getQualityPrefsHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		tiers := resolveQualityTiers(ctx, settingsStore, m)
		tier := string(quality.Lowest(tiers))

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

		// Claude 2026-08-10: report the effective undo depth on this GET.
		// Reason: deep-interview-rename-undo — the response carries the RESOLVED
		//   value (default substituted when unset or malformed) rather than an
		//   empty/zero, so Pass 2's control renders the depth actually in force
		//   without duplicating the fallback client-side. Malformed values fall
		//   through to the default rather than erroring the GET, mirroring
		//   resolveAdultModeEnabled.
		// Troubleshooting: the Settings field rendered 0 on a fresh install.
		// Review if: the response DTO's UndoDepth becomes a pointer (it must
		//   not — only the REQUEST needs three-state; see qualityPrefsRequest).
		undoDepthStr, err := settingsStore.Get(ctx, undoDepthKey(m))
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Default applied where the value is READ, not by pre-seeding the
		// settings table — same shape as resolveAdultModeEnabled's fallback,
		// and a malformed stored value falls through to the default rather
		// than erroring the GET.
		undoDepth := rename.DefaultUndoDepth
		if undoDepthStr != "" {
			if n, convErr := strconv.Atoi(undoDepthStr); convErr == nil && n > 0 {
				undoDepth = n
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(qualityPrefsResponse{
			Tier: tier, Tiers: qualityTiersToStrings(tiers),
			MaxResolution: maxRes, Protocol: protocol, UndoDepth: undoDepth,
		})
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
		var stored []quality.Tier
		if len(req.Tiers) > 0 {
			for _, s := range req.Tiers {
				if !validQualityTiers[s] {
					http.Error(w, "each tiers value must be one of: low, medium, high, lossless", http.StatusBadRequest)
					return
				}
			}
			stored = parseQualityTiers(req.Tiers)
			if len(stored) == 0 {
				http.Error(w, "tiers must contain at least one of: low, medium, high, lossless", http.StatusBadRequest)
				return
			}
		} else {
			if !validQualityTiers[req.Tier] {
				http.Error(w, "tier must be one of: low, medium, high, lossless", http.StatusBadRequest)
				return
			}
			stored = quality.TiersAtOrAbove(quality.Tier(req.Tier))
		}
		floor := quality.Lowest(stored)
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
		// Claude 2026-08-10: undoDepth is validated only when actually sent.
		// Reason: deep-interview-rename-undo — nil means "this client does not
		//   know about the field", which is every client until Pass 2 ships the
		//   control. Substituting the default for nil would silently RESET an
		//   operator's configured depth to 10 on every unrelated quality save.
		//   Nil therefore skips the Set entirely, further down, rather than
		//   writing anything.
		// Troubleshooting: a configured undo depth kept reverting to 10 after
		//   saving Tier/MaxResolution/Protocol.
		// Review if: the field becomes a plain int again (it must not — see
		//   apidto.QualityPrefsRequest for why the pointer is load-bearing).
		if req.UndoDepth != nil && (*req.UndoDepth < 1 || *req.UndoDepth > MaxUndoDepth) {
			http.Error(w, fmt.Sprintf("undoDepth must be between 1 and %d", MaxUndoDepth), http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		if err := settingsStore.Set(ctx, qualityTierKey(m), string(floor)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tiersJSON, err := json.Marshal(qualityTiersToStrings(stored))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, qualityTiersKey(m), string(tiersJSON)); err != nil {
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
		// Only written when the client actually sent one — a nil pointer leaves
		// whatever is stored completely untouched (see the validation block).
		if req.UndoDepth != nil {
			if err := settingsStore.Set(ctx, undoDepthKey(m), strconv.Itoa(*req.UndoDepth)); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
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
// endpoint is per-mode-generic like naming-preset). Stored scale-tagged as
// "<PerFrameBits>:<value>" (e.g. "256:100") so a value tuned under one
// per-frame bit scale is never silently reinterpreted after an algorithm/width
// swap — see resolvePHashThreshold's version gate.
func phashThresholdKey(m mode.Mode) string { return string(m) + "_phash_dedup_threshold" }

// phashModeDefault returns the factory-default per-frame Hamming threshold for
// mode m. Movies uses phash.DefaultMoviesThreshold (64) — more permissive than
// Series because there is no within-show shared-intro false-positive risk for
// Movies. All other modes use phash.DefaultThreshold (40). Both are PDQ-scale
// (0–256) calibrated values; see internal/phash/distance.go.
func phashModeDefault(m mode.Mode) int {
	if m == mode.Movies {
		return phash.DefaultMoviesThreshold
	}
	return phash.DefaultThreshold
}

// resolvePHashThreshold loads m's Dedup phash similarity threshold, defaulting
// to phashModeDefault(m) when unset — the same fallback getPHashThresholdHandler
// reports, reused by dedup.go's Scan handler.
//
// The stored value is scale-tagged "<scale>:<value>" (putPHashThresholdHandler
// writes "<PerFrameBits>:<v>"). This function version-gates on that scale so an
// operator's threshold tuned on one per-frame bit scale is never silently
// reinterpreted on a different one after an algorithm/width swap (PHash 64-bit
// -> PDQ 256-bit): a value whose scale token != the current phash.PerFrameBits
// — including a legacy bare int with no colon at all — is treated as
// stale-scale and falls back to phashModeDefault(m), exactly as the prior
// default-on-unparseable tolerance did, just extended from "not an int" to
// "not the current scale". Only a current-scale value is parsed and honored.
func resolvePHashThreshold(ctx context.Context, settingsStore *settings.Store, m mode.Mode) (int, error) {
	raw, err := settingsStore.Get(ctx, phashThresholdKey(m))
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return 0, err
	}
	if v, ok := parseScaledThreshold(raw); ok {
		return v, nil
	}
	return phashModeDefault(m), nil
}

// parseScaledThreshold decodes a scale-tagged stored threshold
// "<scale>:<value>" and reports whether it is usable on the CURRENT
// phash.PerFrameBits scale. It returns (value, true) only when the string has a
// colon, the scale token parses and equals phash.PerFrameBits, and the value
// token parses; every other shape (unset/empty, legacy bare int, wrong scale,
// non-numeric) returns (0, false) so callers fall back to the mode default.
func parseScaledThreshold(raw string) (int, bool) {
	scaleTok, valTok, ok := strings.Cut(raw, ":")
	if !ok {
		return 0, false
	}
	scale, err := strconv.Atoi(scaleTok)
	if err != nil || scale != phash.PerFrameBits {
		return 0, false
	}
	v, err := strconv.Atoi(valTok)
	if err != nil {
		return 0, false
	}
	return v, true
}

// SweepStalePHashThresholds is a one-time-per-boot startup detection pass that
// finds per-mode Dedup phash thresholds stored on a stale bit scale (a legacy
// bare int, or a "<scale>:<v>" whose scale != the current phash.PerFrameBits)
// and resets them, logging ONE operator-visible line per affected mode. It is
// the notice half of the version gate: resolvePHashThreshold already refuses to
// reinterpret a stale-scale value on every read, but that read path fires on
// every Scan/GET and so cannot be "one-time" — this boot sweep is where the
// operator learns their previously-tuned value was dropped and why. Clearing
// the key (to unset, so it falls back to phashModeDefault) means the sweep does
// not re-fire on the next boot. A current-scale or unset value is left
// untouched and silent. Never fatal: a settings read/write hiccup is logged and
// the boot continues, same tolerance the rest of the threshold path has.
func SweepStalePHashThresholds(ctx context.Context, settingsStore *settings.Store) {
	for _, m := range []mode.Mode{mode.Movies, mode.Series, mode.Adult} {
		key := phashThresholdKey(m)
		raw, err := settingsStore.Get(ctx, key)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			log.Printf("phash threshold sweep: reading %s: %v", key, err)
			continue
		}
		if raw == "" {
			continue // unset — nothing tuned, nothing to reset
		}
		if _, ok := parseScaledThreshold(raw); ok {
			continue // already on the current scale — honored as-is
		}
		def := phashModeDefault(m)
		log.Printf("phash threshold for %s reset to PDQ default %d — previously-stored value %q was set on a different per-frame bit scale and is not comparable on the current %d-bit PDQ scale; re-tune against the new default if desired",
			m, def, raw, phash.PerFrameBits)
		if err := settingsStore.Set(ctx, key, ""); err != nil {
			log.Printf("phash threshold sweep: clearing stale %s: %v", key, err)
		}
	}
}

type phashThresholdResponse struct {
	Threshold int `json:"threshold"`
}

type phashThresholdRequest struct {
	Threshold int `json:"threshold"`
}

// Claude 2026-08-05: per-mode Rename drilldown MatchConfig (N + duration %)
// Reason: replaced Dice match-confidence threshold with multi-signal walk settings
// Troubleshooting: wrong Pending/Unmatched rates — check candidateN / durationTolerancePct
// Review if: confidence threshold keys are fully migrated off disk
func renameCandidateNKey(m mode.Mode) string { return string(m) + "_rename_candidate_n" }
func renameDurationToleranceKey(m mode.Mode) string {
	return string(m) + "_rename_duration_tolerance_pct"
}

// resolveMatchConfig loads Movies/Series Rename drilldown settings.
func resolveMatchConfig(ctx context.Context, settingsStore *settings.Store, m mode.Mode) (rename.MatchConfig, error) {
	cfg := rename.DefaultMatchConfig()
	rawN, err := settingsStore.Get(ctx, renameCandidateNKey(m))
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return cfg, err
	}
	if rawN != "" {
		if v, err := strconv.Atoi(rawN); err == nil {
			cfg.CandidateN = v
		}
	}
	rawT, err := settingsStore.Get(ctx, renameDurationToleranceKey(m))
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return cfg, err
	}
	if rawT != "" {
		if v, err := strconv.Atoi(rawT); err == nil {
			cfg.DurationTolerancePct = v
		}
	}
	return cfg.Normalize(), nil
}

type matchConfigResponse struct {
	CandidateN           int `json:"candidateN"`
	DurationTolerancePct int `json:"durationTolerancePct"`
}

type matchConfigRequest struct {
	CandidateN           int `json:"candidateN"`
	DurationTolerancePct int `json:"durationTolerancePct"`
}

func getMatchConfigHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := resolveMatchConfig(r.Context(), settingsStore, mode.Mode(r.PathValue("mode")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(matchConfigResponse{
			CandidateN: cfg.CandidateN, DurationTolerancePct: cfg.DurationTolerancePct,
		})
	}
}

func putMatchConfigHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		var req matchConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.CandidateN < 1 || req.CandidateN > rename.MaxCandidateN {
			http.Error(w, fmt.Sprintf("candidateN must be between 1 and %d", rename.MaxCandidateN), http.StatusBadRequest)
			return
		}
		if req.DurationTolerancePct < 0 || req.DurationTolerancePct > rename.MaxDurationTolerancePct {
			http.Error(w, fmt.Sprintf("durationTolerancePct must be between 0 and %d", rename.MaxDurationTolerancePct), http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), renameCandidateNKey(m), strconv.Itoa(req.CandidateN)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(r.Context(), renameDurationToleranceKey(m), strconv.Itoa(req.DurationTolerancePct)); err != nil {
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
// threshold's 0–PerFrameBits range).
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
// when unset (64 for Movies, phash.DefaultThreshold (40) for all other modes).
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
// threshold. Rejects a value outside 0–phash.PerFrameBits (a per-frame Hamming
// distance over the active algorithm's per-frame hash width — 0–256 for PDQ),
// mirroring putNamingPresetHandler's invalid-input rejection. The bound is
// derived from PerFrameBits, not a fixed literal, so it tracks the active
// algorithm's width automatically. The value is stored scale-tagged
// ("<PerFrameBits>:<v>") so resolvePHashThreshold's version gate can reject a
// value tuned under a different scale — see phashThresholdKey.
func putPHashThresholdHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		var req phashThresholdRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Threshold < 0 || req.Threshold > phash.PerFrameBits {
			http.Error(w, fmt.Sprintf("threshold must be between 0 and %d", phash.PerFrameBits), http.StatusBadRequest)
			return
		}
		stored := fmt.Sprintf("%d:%d", phash.PerFrameBits, req.Threshold)
		if err := settingsStore.Set(r.Context(), phashThresholdKey(m), stored); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
