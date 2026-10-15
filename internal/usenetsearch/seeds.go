package usenetsearch

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed seeds/recommended.json
var recommendedSeedsJSON []byte

// SeedTier for curated recommended newsgroups.
const (
	SeedTierCore  = "core"
	SeedTierExtra = "extra"
)

// RecommendedSeed is one curated group suggestion for a mode.
type RecommendedSeed struct {
	Mode string `json:"mode"`
	Name string `json:"name"`
	Tier string `json:"tier"`
	Note string `json:"note"`
}

type recommendedFile struct {
	Version int               `json:"version"`
	Groups  []RecommendedSeed `json:"groups"`
}

// Claude 2026-10-15: embedded curated newsgroup recommendations.
// Reason: Settings dual-list of raw LIST ACTIVE is daunting; seeds give a
//   short Apply-recommended set filtered to what the provider carries.
// Troubleshooting: empty recommendations → provider lacks those groups.
// Review if: seeds gain remote updates (rejected — PR-only for now).
// Related files: seeds/recommended.json, mediagroups.go, frontend Usenet.tsx

func loadRecommendedSeeds() ([]RecommendedSeed, error) {
	var f recommendedFile
	if err := json.Unmarshal(recommendedSeedsJSON, &f); err != nil {
		return nil, err
	}
	if f.Version < 1 {
		return nil, fmt.Errorf("usenetsearch: recommended seeds version %d", f.Version)
	}
	seen := map[string]bool{}
	for i, g := range f.Groups {
		mode := strings.ToLower(strings.TrimSpace(g.Mode))
		name := strings.TrimSpace(g.Name)
		tier := strings.ToLower(strings.TrimSpace(g.Tier))
		if mode != "movies" && mode != "series" && mode != "adult" {
			return nil, fmt.Errorf("usenetsearch: seeds[%d]: bad mode %q", i, g.Mode)
		}
		if name == "" || !strings.Contains(name, ".") {
			return nil, fmt.Errorf("usenetsearch: seeds[%d]: bad name %q", i, g.Name)
		}
		if tier != SeedTierCore && tier != SeedTierExtra {
			return nil, fmt.Errorf("usenetsearch: seeds[%d]: bad tier %q", i, g.Tier)
		}
		key := mode + "\x00" + strings.ToLower(name)
		if seen[key] {
			return nil, fmt.Errorf("usenetsearch: duplicate seed %s/%s", mode, name)
		}
		seen[key] = true
		f.Groups[i].Mode = mode
		f.Groups[i].Name = name
		f.Groups[i].Tier = tier
	}
	return f.Groups, nil
}

// RecommendedForMode returns curated seeds for mode (movies|series|adult).
func RecommendedForMode(mode string) []RecommendedSeed {
	all, err := loadRecommendedSeeds()
	if err != nil {
		return nil
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	var out []RecommendedSeed
	for _, g := range all {
		if g.Mode == mode {
			out = append(out, g)
		}
	}
	return out
}

// FilterSeedsToProvider keeps seeds whose names appear in available (case-sensitive NNTP names).
func FilterSeedsToProvider(seeds []RecommendedSeed, available []string) []RecommendedSeed {
	have := map[string]bool{}
	for _, a := range available {
		have[a] = true
	}
	var out []RecommendedSeed
	for _, s := range seeds {
		if have[s.Name] {
			out = append(out, s)
		}
	}
	return out
}
