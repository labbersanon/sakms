package usenetsearch

import (
	"sort"
	"strconv"
	"strings"
)

// Claude 2026-10-16: package value = grade tolerance only; groups = full mode set.
// Reason: slider must not shrink the monitored group list — only demotion does.
// Troubleshooting: groups missing cores → ResolveModeGroups not used on apply.
// Review if: custom mode still needed once repair always rewrites non-custom.

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

// InferPackageFromGroups maps a saved group list to a package value when the
// package KV is missing or invalid. Empty or the full curated mode set
// (∩ provider) → default tolerance; anything else → custom.
func InferPackageFromGroups(mode string, saved, available []string) string {
	saved = ParseGroups(FormatGroups(saved))
	if len(saved) == 0 {
		return FormatPackageStep(DefaultPackageStep)
	}
	if sameGroupSet(saved, ResolveModeGroups(mode, available)) {
		return FormatPackageStep(DefaultPackageStep)
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

// ResolveModeGroups returns every curated group for mode ∩ provider LIST ACTIVE.
// When available is empty, returns the unfiltered curated set (crawler skips
// missing groups later).
func ResolveModeGroups(mode string, available []string) []string {
	names := ResolveAllModeGroups(mode)
	if len(available) == 0 {
		return names
	}
	return intersectNames(names, available)
}
