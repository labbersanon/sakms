package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

// Claude 2026-09-19: Advanced blocked release-group list for Usenet autograb (3c).
// Reason: password-prone groups (e.g. TupaC) waste full NZB downloads before
//   unpack fails; hard-exclude from unattended pick list. Manual Search can still
//   show them via release.BlockedGroups when wired later.
// Troubleshooting: autograb keeps picking password RARs; edit Advanced list.
// Review if: learning from ErrPasswordProtected should append groups dynamically.
// Related: plan item 3c; usenetcontent.go password park.

const (
	UsenetBlockedReleaseGroupsKey = "usenet_blocked_release_groups"
)

// Unset key → default. An explicit empty store value means the operator cleared the list.
var defaultBlockedReleaseGroups = []string{"TupaC"}

type usenetBlockedReleaseGroupsBody struct {
	Groups []string `json:"groups"`
}

func getUsenetBlockedReleaseGroupsHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groups, err := loadBlockedReleaseGroups(r.Context(), settingsStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, usenetBlockedReleaseGroupsBody{Groups: groups})
	}
}

func putUsenetBlockedReleaseGroupsHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req usenetBlockedReleaseGroupsBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		cleaned := normalizeBlockedGroups(req.Groups)
		// Store even when empty — distinguishes "cleared" from "unset → default".
		if err := settingsStore.Set(r.Context(), UsenetBlockedReleaseGroupsKey, strings.Join(cleaned, "\n")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func loadBlockedReleaseGroups(ctx context.Context, store *settings.Store) ([]string, error) {
	if store == nil {
		return append([]string(nil), defaultBlockedReleaseGroups...), nil
	}
	raw, err := store.Get(ctx, UsenetBlockedReleaseGroupsKey)
	if errors.Is(err, settings.ErrNotFound) {
		return append([]string(nil), defaultBlockedReleaseGroups...), nil
	}
	if err != nil {
		return nil, err
	}
	return normalizeBlockedGroups(usenetsearch.ParseGroups(raw)), nil
}

func normalizeBlockedGroups(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		key := strings.ToLower(g)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, g)
	}
	return out
}

func filterBlockedReleaseGroups(releases []prowlarr.Release, blocked []string) []prowlarr.Release {
	if len(blocked) == 0 || len(releases) == 0 {
		return releases
	}
	block := map[string]struct{}{}
	for _, g := range blocked {
		block[strings.ToLower(g)] = struct{}{}
	}
	out := make([]prowlarr.Release, 0, len(releases))
	for _, r := range releases {
		info := release.Parse(r.Title)
		if _, hit := block[strings.ToLower(info.Group)]; hit {
			continue
		}
		// Also skip titles that advertise a password in the name (2b fail-fast).
		if releaseTitleLooksPasswordProtected(r.Title) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func releaseTitleLooksPasswordProtected(title string) bool {
	lower := strings.ToLower(title)
	return strings.Contains(lower, "password") ||
		strings.Contains(lower, ".pass.") ||
		strings.Contains(lower, "-pass-") ||
		strings.Contains(lower, "{{password}}")
}
