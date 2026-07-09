package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
	"github.com/curtiswtaylorjr/tidyarr/internal/settings"
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
