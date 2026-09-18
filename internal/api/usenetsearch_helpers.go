package api

import (
	"context"
	"log"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

func nativePhaseWanted(ctx context.Context, deps AutoGrabDeps, m mode.Mode) bool {
	if deps.UsenetSearch == nil || deps.SettingsStore == nil {
		return false
	}
	cfg := deps.UsenetSearch.Config()
	if !cfg.Enabled {
		// Re-read settings in case Apply hasn't run yet this process.
		loaded, err := LoadUsenetSearchConfig(ctx, deps.SettingsStore)
		if err != nil || !loaded.Enabled {
			return false
		}
		cfg = loaded
	}
	switch m {
	case mode.Movies:
		return cfg.Movies
	case mode.Series:
		return cfg.Series
	case mode.Adult:
		return cfg.Adult
	default:
		return false
	}
}

func prependNativePhase(phases []prowlarr.Scope) []prowlarr.Scope {
	for _, p := range phases {
		if p.IsNative() {
			return phases
		}
	}
	out := make([]prowlarr.Scope, 0, len(phases)+1)
	out = append(out, prowlarr.ScopeNative)
	out = append(out, phases...)
	return out
}

func stripNativePhase(phases []prowlarr.Scope) []prowlarr.Scope {
	out := make([]prowlarr.Scope, 0, len(phases))
	for _, p := range phases {
		if !p.IsNative() {
			out = append(out, p)
		}
	}
	return out
}

func phasesAfterNative(phases []prowlarr.Scope) []prowlarr.Scope {
	seen := false
	var out []prowlarr.Scope
	for _, p := range phases {
		if p.IsNative() {
			seen = true
			continue
		}
		if seen {
			out = append(out, p)
		}
	}
	if !seen {
		return stripNativePhase(phases)
	}
	return out
}

func nativeAutoGrabSearch(ctx context.Context, deps AutoGrabDeps, req AutoGrabRequest) ([]prowlarr.Release, error) {
	svc := deps.UsenetSearch
	if svc == nil {
		return nil, nil
	}
	ready, err := svc.Ready(ctx)
	if err != nil {
		return nil, err
	}
	if !ready.Ready {
		log.Printf("auto-grab: native not ready (%s): %s", ready.State, ready.Detail)
		return nil, nil
	}
	q := usenetsearch.Query{
		Terms:  usenetsearch.NormalizeTitleTerms(req.Title),
		Limit:  40,
		Groups: svc.Config().GroupsForMode(string(req.Mode)),
	}
	if req.ReleaseTitle != "" {
		q.Terms = append(q.Terms, usenetsearch.NormalizeTitleTerms(req.ReleaseTitle)...)
	}
	cs, err := svc.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	return adaptNativeCandidates(cs), nil
}
