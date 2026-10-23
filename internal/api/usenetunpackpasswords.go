package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-11: operator unpack-password list (NZBGet UnpackPassFile).
// Reason: standing group passwords (when known) should be tried at unrar
//
//	instead of burning BODY then parking. Empty default — no guessing.
//
// Troubleshooting: PUT /api/settings/usenet-unpack-passwords; one password per line.
// Review if: Advanced UI grows a field for this list.
const UsenetUnpackPasswordsKey = "usenet_unpack_passwords"

type usenetUnpackPasswordsBody struct {
	Passwords []string `json:"passwords"`
}

func getUsenetUnpackPasswordsHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pw, err := LoadUnpackPasswords(r.Context(), settingsStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, usenetUnpackPasswordsBody{Passwords: pw})
	}
}

func putUsenetUnpackPasswordsHandler(settingsStore *settings.Store, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req usenetUnpackPasswordsBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		cleaned := normalizeUnpackPasswords(req.Passwords)
		if err := settingsStore.Set(r.Context(), UsenetUnpackPasswordsKey, strings.Join(cleaned, "\n")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if nzb != nil {
			nzb.SetUnpackPasswords(cleaned)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func LoadUnpackPasswords(ctx context.Context, store *settings.Store) ([]string, error) {
	if store == nil {
		return nil, nil
	}
	raw, err := store.Get(ctx, UsenetUnpackPasswordsKey)
	if errors.Is(err, settings.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return normalizeUnpackPasswords(parseGroupList(raw)), nil
}

func normalizeUnpackPasswords(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
