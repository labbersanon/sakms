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

// Claude 2026-09-22: Global preferred grab languages (multi-row include).
// Reason: autograb ignored the Discover-only English language filter.
// Troubleshooting: Settings → Advanced → Global → Preferred grab languages.
// Review if: per-series language overrides are added.
// Related: release.TitleLanguageAllowed; FilterReleases; RunAutoGrab.

const GrabPreferredLanguagesKey = "grab_preferred_languages"

type grabPreferredLanguagesBody struct {
	Languages []string `json:"languages"`
	// Options is the canonical dropdown catalog (GET only) — abbreviations
	// are implied by selection and are not listed separately.
	Options []string `json:"options,omitempty"`
}

func getGrabPreferredLanguagesHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		langs, err := loadPreferredLanguages(r.Context(), settingsStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, grabPreferredLanguagesBody{
			Languages: langs,
			Options:   release.LanguageOptionIDs(),
		})
	}
}

func putGrabPreferredLanguagesHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req grabPreferredLanguagesBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		cleaned, err := normalizePreferredLanguageList(req.Languages)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Store even when empty — empty means English-assumed unmarked-only.
		if err := settingsStore.Set(r.Context(), GrabPreferredLanguagesKey, strings.Join(cleaned, "\n")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func loadPreferredLanguages(ctx context.Context, store *settings.Store) ([]string, error) {
	if store == nil {
		return nil, nil
	}
	raw, err := store.Get(ctx, GrabPreferredLanguagesKey)
	if errors.Is(err, settings.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cleaned, err := normalizePreferredLanguageList(usenetsearch.ParseGroups(raw))
	if err != nil {
		return filterKnownOnly(usenetsearch.ParseGroups(raw)), nil
	}
	return cleaned, nil
}

func filterKnownOnly(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, g := range in {
		id := release.CanonicalLanguageID(g)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// normalizePreferredLanguageList canonicalizes to group IDs (eng → english)
// and rejects unknown tokens on PUT. Load path uses filterKnownOnly to drop
// garbage without failing the whole list.
func normalizePreferredLanguageList(in []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	var unknown []string
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		id := release.CanonicalLanguageID(g)
		if id == "" {
			unknown = append(unknown, g)
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(unknown) > 0 {
		return nil, errors.New("unknown language: " + unknown[0])
	}
	return out, nil
}

func filterPreferredLanguages(releases []prowlarr.Release, preferred []string) []prowlarr.Release {
	if len(releases) == 0 {
		return releases
	}
	out := make([]prowlarr.Release, 0, len(releases))
	for _, r := range releases {
		if release.TitleLanguageAllowed(r.Title, preferred) {
			out = append(out, r)
		}
	}
	return out
}
