package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
)

type aiModelResponse struct {
	Model string `json:"model"`
}

type aiModelRequest struct {
	Model string `json:"model"`
}

// getOllamaModelHandler returns the configured AI model stored under
// settingsKey, or an empty string if none is set yet (unset is a normal
// state, not an error). Shared by every settings-backed AI model endpoint —
// Adult's and Mainstream's alike, since both read the one shared
// mode.AIModelKey.
func getOllamaModelHandler(settingsStore *settings.Store, settingsKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		model, err := settingsStore.Get(r.Context(), settingsKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(aiModelResponse{Model: model})
	}
}

// putOllamaModelHandler stores the AI model name under settingsKey.
func putOllamaModelHandler(settingsStore *settings.Store, settingsKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req aiModelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Model == "" {
			http.Error(w, "model is required", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), settingsKey, req.Model); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type aiProviderResponse struct {
	Provider string `json:"provider"`
}

type aiProviderRequest struct {
	Provider string `json:"provider"`
}

// aiProviders is the set mode.buildAIClient actually knows how to build a
// client for — validated here so a typo'd provider name fails fast with a
// clear 400 at save time, rather than surfacing as an opaque error the next
// time a Scan tries to use it.
var aiProviders = map[string]bool{
	mode.AIProviderOllama:    true,
	mode.AIProviderOpenAI:    true,
	mode.AIProviderGemini:    true,
	mode.AIProviderAnthropic: true,
}

// getAIProviderHandler returns the configured AI provider, defaulting to
// mode.AIProviderOllama when unset — the same default mode.buildAIClient
// itself falls back to, so what this reports always matches what a Scan
// would actually use.
func getAIProviderHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider, err := settingsStore.Get(r.Context(), mode.AIProviderKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if provider == "" {
			provider = mode.AIProviderOllama
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(aiProviderResponse{Provider: provider})
	}
}

type aiFallbackEnabledResponse struct {
	Enabled bool `json:"enabled"`
}

type aiFallbackEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// getAIFallbackEnabledHandler reports whether the BYOAI fallback is on.
// Unset defaults to false — DB-first parsing runs alone by default.
func getAIFallbackEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := settingsStore.Get(r.Context(), mode.AIFallbackEnabledKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(aiFallbackEnabledResponse{Enabled: v == "true"})
	}
}

// putAIFallbackEnabledHandler toggles the BYOAI fallback. When disabled,
// ParseFilename is never called regardless of provider/model configuration.
func putAIFallbackEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req aiFallbackEnabledRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		v := "false"
		if req.Enabled {
			v = "true"
		}
		if err := settingsStore.Set(r.Context(), mode.AIFallbackEnabledKey, v); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// Claude 2026-08-06: Rename give-back toggles (mainstream + adult)
// Reason: deep-interview-rename-apply-all-giveback-settings
func getRenameGiveBackMainstreamHandler(settingsStore *settings.Store) http.HandlerFunc {
	return boolSettingGetHandler(settingsStore, mode.RenameGiveBackMainstreamKey)
}

func putRenameGiveBackMainstreamHandler(settingsStore *settings.Store) http.HandlerFunc {
	return boolSettingPutHandler(settingsStore, mode.RenameGiveBackMainstreamKey)
}

func getRenameGiveBackAdultHandler(settingsStore *settings.Store) http.HandlerFunc {
	return boolSettingGetHandler(settingsStore, mode.RenameGiveBackAdultKey)
}

func putRenameGiveBackAdultHandler(settingsStore *settings.Store) http.HandlerFunc {
	return boolSettingPutHandler(settingsStore, mode.RenameGiveBackAdultKey)
}

type enabledSettingResponse struct {
	Enabled bool `json:"enabled"`
}

type enabledSettingRequest struct {
	Enabled bool `json:"enabled"`
}

func boolSettingGetHandler(settingsStore *settings.Store, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := settingsStore.Get(r.Context(), key)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(enabledSettingResponse{Enabled: v == "true"})
	}
}

func boolSettingPutHandler(settingsStore *settings.Store, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req enabledSettingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		v := "false"
		if req.Enabled {
			v = "true"
		}
		if err := settingsStore.Set(r.Context(), key, v); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type webSearchPrimaryResponse struct {
	Primary string `json:"primary"`
}

type webSearchPrimaryRequest struct {
	Primary string `json:"primary"`
}

// Claude 2026-08-05: web_search_primary setting (searxng|brave)
// Reason: deep-interview-searxng-websearch
func getWebSearchPrimaryHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := settingsStore.Get(r.Context(), mode.WebSearchPrimaryKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if v != mode.WebSearchPrimaryBrave && v != mode.WebSearchPrimarySearXNG {
			v = mode.WebSearchPrimarySearXNG
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(webSearchPrimaryResponse{Primary: v})
	}
}

func putWebSearchPrimaryHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req webSearchPrimaryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Primary != mode.WebSearchPrimaryBrave && req.Primary != mode.WebSearchPrimarySearXNG {
			http.Error(w, "primary must be searxng or brave", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), mode.WebSearchPrimaryKey, req.Primary); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// BrowserNotificationsEnabledKey is the settings key for the opt-in browser
// (desktop) notifications preference. Off by default.
const BrowserNotificationsEnabledKey = "browser_notifications_enabled"

type browserNotificationsEnabledResponse struct {
	Enabled bool `json:"enabled"`
}

type browserNotificationsEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// getBrowserNotificationsEnabledHandler reports whether browser notifications
// are enabled. Unset defaults to false — no notifications until the operator
// explicitly turns the toggle on (and grants browser permission, tracked
// separately client-side).
func getBrowserNotificationsEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := settingsStore.Get(r.Context(), BrowserNotificationsEnabledKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(browserNotificationsEnabledResponse{Enabled: v == "true"})
	}
}

// putBrowserNotificationsEnabledHandler toggles browser notifications. This is
// only the persisted preference; the browser's own Notification permission is a
// separate state the frontend tracks and cannot be forced from here.
func putBrowserNotificationsEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req browserNotificationsEnabledRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		v := "false"
		if req.Enabled {
			v = "true"
		}
		if err := settingsStore.Set(r.Context(), BrowserNotificationsEnabledKey, v); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// putAIProviderHandler stores which AI backend every AI-assisted feature
// should use.
func putAIProviderHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req aiProviderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if !aiProviders[req.Provider] {
			http.Error(w, "provider must be one of: ollama, openai, gemini, anthropic", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), mode.AIProviderKey, req.Provider); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
