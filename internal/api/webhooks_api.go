// Webhook CRUD handlers — GET/POST/PUT/DELETE /api/webhooks and
// POST /api/webhooks/{id}/test. All handlers require the operator to be
// authenticated (same middleware as every other /api/* route).
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/apidto"
	"github.com/curtiswtaylorjr/sakms/internal/webhooks"
)

func listWebhooksHandler(whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := whStore.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]apidto.WebhookSummary, len(list))
		for i, s := range list {
			out[i] = apidto.WebhookSummary{
				ID: s.ID, URL: s.URL, SecretSet: s.SecretSet,
				Events: s.Events, Enabled: s.Enabled,
				CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
			}
		}
		writeJSON(w, out)
	}
}

func createWebhookHandler(whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.WebhookCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		if err := webhooks.ValidateEvents(req.Events); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, err := whStore.Create(r.Context(), req.URL, req.Secret, req.Events, req.Enabled)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		list, err := whStore.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, s := range list {
			if s.ID == id {
				w.WriteHeader(http.StatusCreated)
				writeJSON(w, apidto.WebhookSummary{
					ID: s.ID, URL: s.URL, SecretSet: s.SecretSet,
					Events: s.Events, Enabled: s.Enabled,
					CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
				})
				return
			}
		}
		http.Error(w, "created but not found", http.StatusInternalServerError)
	}
}

func updateWebhookHandler(whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var req apidto.WebhookUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		if err := webhooks.ValidateEvents(req.Events); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := whStore.Update(r.Context(), id, req.URL, req.Secret, req.Events, req.Enabled); err != nil {
			if errors.Is(err, webhooks.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteWebhookHandler(whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := whStore.Delete(r.Context(), id); err != nil {
			if errors.Is(err, webhooks.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func testWebhookHandler(whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := whStore.SendTest(r.Context(), id, map[string]string{"message": "SAK webhook test"}); err != nil {
			if errors.Is(err, webhooks.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
