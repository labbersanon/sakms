package usenetsearch

import (
	"sort"
	"strconv"
	"strings"
)

// Claude 2026-10-15: resolve slider step → effective group list.
// Reason: Settings stores package step (or custom); crawler still reads group keys.
// Troubleshooting: custom with empty groups fails validate; step with 0 provider
//   hits still saves unfiltered names (crawler skips missing).
// Review if: accepted re-rank overrides (slice 4) layer on top of curated step.

// ParsePackageValue returns step (1–5), custom=true, or ok=false if invalid.
func ParsePackageValue(raw string) (step int, custom bool, ok bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return 0, false, false
	}
	if raw == PackageCustom {
		return 0, true, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, false
	}
	if n < PackageStepMin || n > PackageStepMax {
		return 0, false, false
	}
	return n, false, true
}

// FormatPackageStep stores step as decimal string.
func FormatPackageStep(step int) string {
	return strconv.Itoa(ClampPackageStep(step))
}

// sameGroupSet reports whether a and b contain the same names (order ignored).
func sameGroupSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[string]bool{}
	for _, x := range a {
		am[x] = true
	}
	for _, x := range b {
		if !am[x] {
			return false
		}
	}
	return true
}

// InferPackageFromGroups maps a saved group list to step or custom.
// Empty → default step (draft). Exact match to a provider-filtered cumulative
// step → that step. Anything else → custom.
func InferPackageFromGroups(mode string, saved, available []string) string {
	saved = ParseGroups(FormatGroups(saved))
	if len(saved) == 0 {
		return FormatPackageStep(DefaultPackageStep)
	}
	var filter []string
	if len(available) > 0 {
		filter = available
	}
	for step := PackageStepMin; step <= PackageStepMax; step++ {
		resolved := ResolveGroupsForStep(mode, step)
		if len(filter) > 0 {
			resolved = intersectNames(resolved, filter)
		}
		if sameGroupSet(saved, resolved) {
			return FormatPackageStep(step)
		}
	}
	// Also try unfiltered equality when available was empty at infer time.
	if len(filter) == 0 {
		for step := PackageStepMin; step <= PackageStepMax; step++ {
			if sameGroupSet(saved, ResolveGroupsForStep(mode, step)) {
				return FormatPackageStep(step)
			}
		}
	}
	return PackageCustom
}

func intersectNames(names, available []string) []string {
	have := map[string]bool{}
	for _, a := range available {
		have[a] = true
	}
	var out []string
	for _, n := range names {
		if have[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// ResolvePackageGroups expands a non-custom package to group names.
// When available is non-empty, intersect with provider LIST ACTIVE.
func ResolvePackageGroups(mode string, step int, available []string) []string {
	names := ResolveGroupsForStep(mode, step)
	if len(available) == 0 {
		return names
	}
	return intersectNames(names, available)
}
