package api

import (
	"context"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

// Claude 2026-10-15: Step 4 of .omc/plans/nntp-crawl-quality-admission.md —
// builds the usenetsearch.AdmissionPolicy the crawler consults every pass
// (via usenetsearch.Service.SetAdmissionPolicy, wired once in cmd/sakms).
// Reason: locked decision #3 — per-title prefs when matched, else
//   loosest-of (mode defaults ∪ all overrides in that mode); locked
//   decision #5 — Discover-only titles (blank Title column) fall back to
//   mode defaults rather than appearing as a phantom override.
// Troubleshooting: a title override not taking effect at crawl time — check
//   library_quality_prefs.title is non-empty for that row (putTitleQualityPrefs
//   must have resolved it) and that InvalidateAdmissionPolicy was called.
// Review if: a mode gets its own title-override table instead of sharing
//   library_quality_prefs.
// Related files: usenetsearch/admission.go, usenetsearch/service.go,
//   api/titlequality.go (writes Title/Year), cmd/sakms/main.go (wiring).

// nntpAdmissionPolicy implements usenetsearch.AdmissionPolicy by reading the
// same Settings mode defaults and library_quality_prefs title overrides that
// already drive Search/auto-grab quality gating (resolveQualityTiers,
// resolveModeResolutionBand, resolveAutoGrabTiersForTitle's merge rule).
type nntpAdmissionPolicy struct {
	lib      *library.Store
	settings *settings.Store
}

// NewNNTPAdmissionPolicy returns the AdmissionPolicy wired into
// usenetsearch.Service.SetAdmissionPolicy at boot (cmd/sakms/main.go).
func NewNNTPAdmissionPolicy(lib *library.Store, settingsStore *settings.Store) usenetsearch.AdmissionPolicy {
	return &nntpAdmissionPolicy{lib: lib, settings: settingsStore}
}

// Snapshot builds a fresh usenetsearch.PolicySnapshot. Called on the
// admitter's refresh timer and immediately after an explicit invalidation
// (title/mode quality-prefs PUT or DELETE) — see Service.InvalidateAdmissionPolicy.
func (p *nntpAdmissionPolicy) Snapshot(ctx context.Context) (*usenetsearch.PolicySnapshot, error) {
	snap := &usenetsearch.PolicySnapshot{
		Modes:  map[string]usenetsearch.Standard{},
		Titles: map[string][]usenetsearch.TitleStd{},
	}
	for _, m := range []mode.Mode{mode.Movies, mode.Series, mode.Adult} {
		tiers := resolveQualityTiers(ctx, p.settings, m)
		minRes, maxRes := resolveModeResolutionBand(ctx, p.settings, m)
		snap.Modes[string(m)] = usenetsearch.Standard{
			Mode: string(m), Policy: "mode", Tiers: tiers, MinRes: minRes, MaxRes: maxRes,
		}
	}
	if p.lib == nil {
		return snap, nil
	}
	rows, err := p.lib.ListTitleQualityPrefs(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		// Claude 2026-10-15: skip adult (no title overrides, locked decision
		// #3) and rows with no resolvable Title (migration 0043 default "" —
		// a Discover-only movie override set before TMDB lookup succeeded).
		// Both fall back to the mode-default Standard at crawl time, same as
		// before this column existed.
		if row.Mode == mode.Adult || row.Title == "" {
			continue
		}
		modeDef := snap.Modes[string(row.Mode)]
		tiers := row.Tiers
		if len(tiers) == 0 {
			tiers = modeDef.Tiers
		}
		minRes := modeDef.MinRes
		if row.MinResolutionSet {
			minRes = row.MinResolution
		}
		maxRes := modeDef.MaxRes
		if row.MaxResolutionSet {
			maxRes = row.MaxResolution
		}
		key := usenetsearch.NormalizeTitleKey(row.Title)
		snap.Titles[key] = append(snap.Titles[key], usenetsearch.TitleStd{
			Mode: string(row.Mode), TMDBID: row.TMDBID, Title: row.Title, Year: row.Year,
			Tiers: tiers, MinRes: minRes, MaxRes: maxRes,
		})
	}
	return snap, nil
}
