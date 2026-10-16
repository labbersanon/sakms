package usenetsearch

import (
	"sort"
	"strconv"
	"strings"
)

// Claude 2026-10-16: package value = grade tolerance; groups = mode LIST ACTIVE.
// Reason: Wade — every provider group that applies to the mode, not packages.json.
// Troubleshooting: 10-group movies list → still intersecting seeds instead of LIST.
// Review if: wildmats in mediagroups.go drop a mode-relevant hierarchy.

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
// package KV is missing or invalid. Empty or the full mode LIST set → default
// tolerance; a proper subset (including the old packages.json shortlist) → custom.
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

// ResolveModeGroups returns the groups to monitor for mode.
// When available (mode-scoped LIST ACTIVE) is non-empty, that is the membership
// — every provider group that applies to the mode. packages.json is only a
// fallback when LIST has not run yet (and must not overwrite a live LIST set).
func ResolveModeGroups(mode string, available []string) []string {
	if len(available) > 0 {
		out := append([]string{}, available...)
		sort.Strings(out)
		return out
	}
	return ResolveAllModeGroups(mode)
}
