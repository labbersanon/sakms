package usenetsearch

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed seeds/packages.json
var packagesJSON []byte

// Claude 2026-10-15: 5-step reliability↔breadth packages (replaces core/extra seeds).
// Reason: Settings moves from individual group picking to a slider; step N is
//   cumulative (includes 1..N). pinned anchors never leave via re-rank.
// Troubleshooting: empty resolve → provider LIST ACTIVE miss or bad step.
// Review if: unselected-group sampling (slice 5) proposes new package members.
// Related files: seeds/packages.json, usenetnntp.go, frontend Usenet.tsx

const (
	PackageStepMin = 1
	PackageStepMax = 5
	PackageCustom  = "custom"

	// DefaultPackageStep is the first-run / "most reliable" draft.
	DefaultPackageStep = 1
)

// PackageSeed is one curated group at a slider step for a mode.
type PackageSeed struct {
	Mode   string `json:"mode"`
	Name   string `json:"name"`
	Step   int    `json:"step"`
	Pinned bool   `json:"pinned"`
	Note   string `json:"note"`
}

type packagesFile struct {
	Version int           `json:"version"`
	Groups  []PackageSeed `json:"groups"`
}

func loadPackageSeeds() ([]PackageSeed, error) {
	var f packagesFile
	if err := json.Unmarshal(packagesJSON, &f); err != nil {
		return nil, err
	}
	if f.Version < 2 {
		return nil, fmt.Errorf("usenetsearch: packages version %d (want ≥2)", f.Version)
	}
	seen := map[string]bool{}
	for i, g := range f.Groups {
		mode := strings.ToLower(strings.TrimSpace(g.Mode))
		name := strings.TrimSpace(g.Name)
		if mode != "movies" && mode != "series" && mode != "adult" {
			return nil, fmt.Errorf("usenetsearch: packages[%d]: bad mode %q", i, g.Mode)
		}
		if name == "" || !strings.Contains(name, ".") {
			return nil, fmt.Errorf("usenetsearch: packages[%d]: bad name %q", i, g.Name)
		}
		if g.Step < PackageStepMin || g.Step > PackageStepMax {
			return nil, fmt.Errorf("usenetsearch: packages[%d]: bad step %d", i, g.Step)
		}
		key := mode + "\x00" + strings.ToLower(name)
		if seen[key] {
			return nil, fmt.Errorf("usenetsearch: duplicate package seed %s/%s", mode, name)
		}
		seen[key] = true
		f.Groups[i].Mode = mode
		f.Groups[i].Name = name
	}
	return f.Groups, nil
}

// ClampPackageStep returns step in [1,5]; non-positive becomes DefaultPackageStep.
func ClampPackageStep(step int) int {
	if step < PackageStepMin {
		return DefaultPackageStep
	}
	if step > PackageStepMax {
		return PackageStepMax
	}
	return step
}

// PackagesForMode returns all curated seeds for mode (any step).
func PackagesForMode(mode string) []PackageSeed {
	all, err := loadPackageSeeds()
	if err != nil {
		return nil
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	var out []PackageSeed
	for _, g := range all {
		if g.Mode == mode {
			out = append(out, g)
		}
	}
	return out
}

// ResolveGroupsForStep returns cumulative group names for mode at step (1..N).
func ResolveGroupsForStep(mode string, step int) []string {
	step = ClampPackageStep(step)
	mode = strings.ToLower(strings.TrimSpace(mode))
	var names []string
	seen := map[string]bool{}
	for _, g := range PackagesForMode(mode) {
		if g.Step > step || seen[g.Name] {
			continue
		}
		seen[g.Name] = true
		names = append(names, g.Name)
	}
	sort.Strings(names)
	return names
}

// FilterPackagesToProvider keeps seeds whose names appear in available.
func FilterPackagesToProvider(seeds []PackageSeed, available []string) []PackageSeed {
	have := map[string]bool{}
	for _, a := range available {
		have[a] = true
	}
	var out []PackageSeed
	for _, s := range seeds {
		if have[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// PackageSeedByName maps group name → seed for mode (first match).
func PackageSeedByName(mode string) map[string]PackageSeed {
	out := map[string]PackageSeed{}
	for _, g := range PackagesForMode(mode) {
		out[g.Name] = g
	}
	return out
}

// --- Compatibility shim (slice 1): old recommended API ≈ steps 1–2 ----------

// SeedTier kept so older call sites/tests compile until slice 2 removes them.
const (
	SeedTierCore  = "core"
	SeedTierExtra = "extra"
)

// RecommendedSeed is the pre-slider seed shape (shim).
type RecommendedSeed struct {
	Mode string `json:"mode"`
	Name string `json:"name"`
	Tier string `json:"tier"`
	Note string `json:"note"`
}

// RecommendedForMode returns package seeds at step ≤ 2 as recommended (shim).
func RecommendedForMode(mode string) []RecommendedSeed {
	var out []RecommendedSeed
	for _, g := range PackagesForMode(mode) {
		if g.Step > 2 {
			continue
		}
		tier := SeedTierCore
		if g.Step == 2 {
			tier = SeedTierExtra
		}
		out = append(out, RecommendedSeed{Mode: g.Mode, Name: g.Name, Tier: tier, Note: g.Note})
	}
	return out
}

// FilterSeedsToProvider keeps recommended seeds present in available (shim).
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
