package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/allowlist"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
)

// setupWizardDismissedKey is the internal/settings key backing the wizard's
// skip state — see internal/api/setup.go's own doc comment for why this is
// a shortcut flag, not a gate.
const setupWizardDismissedKey = "setup_wizard_dismissed"

// modeStatus reports what's configured for one mode — enough for a wizard
// to know which steps are already done and skip straight past them.
type modeStatus struct {
	Mode           mode.Mode `json:"mode"`
	Available      bool      `json:"available"`
	ArrConfigured  bool      `json:"arrConfigured"`
	AllowlistCount int       `json:"allowlistCount"`
}

// setupStatus is GET /api/setup/status's response shape. It's a pure read
// model over connections.Store/allowlist.Store/settings.Store — the wizard
// (once a frontend exists) still saves everything through the exact same
// endpoints Settings uses (PUT /api/connections/{service}, POST .../purge/
// allowlist); this endpoint only answers "what's already true," so the
// wizard knows what to show and whether to show itself at all.
//
// JellyfinConfigured reports whether a "jellyfin" connection has been
// saved — connections.Store already accepts any service key generically,
// so this is honest today even though SAK has no Jellyfin client yet
// and nothing acts on that connection until one exists.
type setupStatus struct {
	Modes              []modeStatus `json:"modes"`
	JellyfinConfigured bool         `json:"jellyfinConfigured"`
	OllamaConfigured   bool         `json:"ollamaConfigured"`
	Dismissed          bool         `json:"dismissed"`
	AnyConfigured      bool         `json:"anyConfigured"`
}

// wizardModes are the modes the setup wizard actually walks through.
// Adult is reported (via buildSetupStatus appending it) and is now a fully
// available mode — all three Adult workflows (Rename/Purge/Dedup) exist — but
// its identify-pipeline setup (Ollama + a stash-box) is a different shape from
// the wizard's *arr-connection walk, so it stays out of this list.
var wizardModes = []mode.Mode{mode.Movies, mode.Series}

func setupStatusHandler(connStore *connections.Store, allowStore *allowlist.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		status, err := buildSetupStatus(ctx, connStore, allowStore, settingsStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	}
}

func buildSetupStatus(ctx context.Context, connStore *connections.Store, allowStore *allowlist.Store, settingsStore *settings.Store) (setupStatus, error) {
	var status setupStatus

	for _, m := range append(append([]mode.Mode{}, wizardModes...), mode.Adult) {
		ms, err := modeStatusFor(ctx, m, allowStore, settingsStore)
		if err != nil {
			return setupStatus{}, err
		}
		status.Modes = append(status.Modes, ms)
		if ms.ArrConfigured {
			status.AnyConfigured = true
		}
	}

	jellyfinConfigured, err := connectionExists(ctx, connStore, "jellyfin")
	if err != nil {
		return setupStatus{}, err
	}
	status.JellyfinConfigured = jellyfinConfigured
	status.AnyConfigured = status.AnyConfigured || jellyfinConfigured

	ollamaConfigured, err := connectionExists(ctx, connStore, "ollama")
	if err != nil {
		return setupStatus{}, err
	}
	status.OllamaConfigured = ollamaConfigured
	status.AnyConfigured = status.AnyConfigured || ollamaConfigured

	dismissed, err := settingsStore.GetBool(ctx, setupWizardDismissedKey, false)
	if err != nil {
		return setupStatus{}, err
	}
	status.Dismissed = dismissed

	return status, nil
}

// modeStatusFor reports whether m is ready to use. No mode has an *arr
// connection to check anymore — each is "configured" once its own library
// root folder setting is populated (see internal/library's package doc). The
// field stays named ArrConfigured across all three (the wizard's "is this
// mode ready" signal), even though the check isn't really an *arr connection
// anymore.
func modeStatusFor(ctx context.Context, m mode.Mode, allowStore *allowlist.Store, settingsStore *settings.Store) (modeStatus, error) {
	var arrConfigured bool
	switch m {
	case mode.Movies, mode.Series, mode.Adult:
		key, _ := libraryRootFolderKey(m)
		rootPath, getErr := settingsStore.Get(ctx, key)
		if getErr != nil && !errors.Is(getErr, settings.ErrNotFound) {
			return modeStatus{}, getErr
		}
		arrConfigured = rootPath != ""
	}

	tags, err := allowStore.List(ctx, m)
	if err != nil {
		return modeStatus{}, err
	}

	return modeStatus{Mode: m, Available: true, ArrConfigured: arrConfigured, AllowlistCount: len(tags)}, nil
}

// connectionExists reports whether service has a stored connection —
// delegates to optionalConnAPI (adultdiscover_stashbox.go) for the actual
// "not-found is not an error" logic rather than re-implementing it, so
// package api's "is this connection configured" idiom exists in exactly one
// place.
func connectionExists(ctx context.Context, connStore *connections.Store, service string) (bool, error) {
	conn, err := optionalConnAPI(ctx, connStore, service)
	if err != nil {
		return false, err
	}
	return conn != nil, nil
}

type dismissSetupRequest struct {
	Dismissed bool `json:"dismissed"`
}

// dismissSetupHandler persists whether the wizard has been skipped —
// PUT rather than POST since it's an idempotent full replace of one flag's
// value, not an action with side effects.
func dismissSetupHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req dismissSetupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := settingsStore.SetBool(r.Context(), setupWizardDismissedKey, req.Dismissed); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
