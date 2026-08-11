package api

import (
	"testing"

	"github.com/labbersanon/sakms/internal/scanschedule"
)

// TestScanScheduleSettingsKeyParity guards the one seam scanschedule_settings.go's
// import-avoidance leaves unverified: its key constants are typed out by value
// rather than imported from internal/scanschedule (so this package still
// compiles, with the settings endpoints simply managing inert keys, if
// scanschedule is ever deleted — see scanschedule_settings.go's header
// comment). That means nothing at compile time stops the two copies from
// drifting apart, which would make the settings UI silently write a key the
// scheduler never reads. This test is the deliberate exception: it's fine for
// it to import scanschedule, because if scanschedule is ever deleted this
// file is deleted right alongside it, same as the production code it guards.
func TestScanScheduleSettingsKeyParity(t *testing.T) {
	cases := []struct {
		name     string
		apiKey   string
		schedKey string
	}{
		{"rename interval", renameScanIntervalKey, scanschedule.RenameIntervalKey},
		{"purge interval", purgeScanIntervalKey, scanschedule.PurgeIntervalKey},
		{"dedup interval", dedupScanIntervalKey, scanschedule.DedupIntervalKey},
		{"dedup vmaf enabled", dedupVMAFScanEnabledKey, scanschedule.DedupVMAFEnabledKey},
		{"rename enabled", renameScanEnabledKey, scanschedule.RenameEnabledKey},
		{"purge enabled", purgeScanEnabledKey, scanschedule.PurgeEnabledKey},
		{"dedup enabled", dedupScanEnabledKey, scanschedule.DedupEnabledKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.apiKey != c.schedKey {
				t.Errorf("key drift: internal/api has %q, internal/scanschedule has %q", c.apiKey, c.schedKey)
			}
		})
	}
}

// TestScanScheduleDefaultIntervalParity guards the SECOND value this package
// mirrors by hand rather than importing (see defaultScanIntervalSeconds' own
// doc): the 24h "on by default" cadence. This one is more dangerous to drift
// than a key string, because a drift here fails SILENTLY in the worst possible
// direction — internal/api's copy only decides what a GET reports (so what the
// Settings UI displays), while internal/scanschedule's copy decides whether
// anything actually scans. Diverge them and the UI cheerfully reports "on,
// every 24 hours" for a key the scheduler is treating as off.
func TestScanScheduleDefaultIntervalParity(t *testing.T) {
	if defaultScanIntervalSeconds != scanschedule.DefaultIntervalSeconds {
		t.Errorf("default-interval drift: internal/api has %d, internal/scanschedule has %d",
			defaultScanIntervalSeconds, scanschedule.DefaultIntervalSeconds)
	}
}
