package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/curtiswtaylorjr/tidyarr/internal/allowlist"
	"github.com/curtiswtaylorjr/tidyarr/internal/connections"
	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
	"github.com/curtiswtaylorjr/tidyarr/internal/settings"
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
// so this is honest today even though Tidyarr has no Jellyfin client yet
// and nothing acts on that connection until one exists.
type setupStatus struct {
	Modes              []modeStatus `json:"modes"`
	JellyfinConfigured bool         `json:"jellyfinConfigured"`
	OllamaConfigured   bool         `json:"ollamaConfigured"`
	Dismissed          bool         `json:"dismissed"`
	AnyConfigured      bool         `json:"anyConfigured"`
}

// wizardModes are the modes the setup wizard actually walks through.
// Adult is intentionally excluded from this list — see modeStatusFor's
// Available field for how it still gets reported.
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
		ms, err := modeStatusFor(ctx, m, connStore, allowStore)
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

func modeStatusFor(ctx context.Context, m mode.Mode, connStore *connections.Store, allowStore *allowlist.Store) (modeStatus, error) {
	if m == mode.Adult {
		// Adult's backend isn't wired up at all (see internal/mode.Build) —
		// report it as a real option the wizard can preview, never as
		// something that's actually configurable today.
		return modeStatus{Mode: m, Available: false}, nil
	}

	service := "radarr"
	if m == mode.Series {
		service = "sonarr"
	}
	arrConfigured, err := connectionExists(ctx, connStore, service)
	if err != nil {
		return modeStatus{}, err
	}

	tags, err := allowStore.List(ctx, m)
	if err != nil {
		return modeStatus{}, err
	}

	return modeStatus{Mode: m, Available: true, ArrConfigured: arrConfigured, AllowlistCount: len(tags)}, nil
}

func connectionExists(ctx context.Context, connStore *connections.Store, service string) (bool, error) {
	_, err := connStore.Get(ctx, service)
	if errors.Is(err, connections.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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
