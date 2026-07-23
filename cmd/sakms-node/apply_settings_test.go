package main

import (
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/nodes"
)

// TestApplyServerSettings_PauseAppliedDespitePathMapRejection is the P8 test — the
// most safety-critical case in Stage 3. A settings frame carrying a valid
// PauseDispatch but an INVALID pathMap (one mapping outside the node's mediaRoots)
// must STILL update the node's cached pause display, because the pause write is
// hoisted into its own mutateAndSave ABOVE the pathMap-validation early-return.
//
// The test proves BOTH halves so it cannot false-green: (a) the pathMap was
// genuinely rejected (its Remap did NOT merge — the early-return actually fired),
// AND (b) cfg.DispatchPaused updated anyway.
func TestApplyServerSettings_PauseAppliedDespitePathMapRejection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	// Non-empty mediaRoots so applyServerSettings takes the validation branch
	// (not the empty-list grace period), and a pathMap mapping OUTSIDE it so
	// validateSettingsPush rejects the frame and returns early.
	cfg := &NodeConfig{
		ServerURL:  "http://unused.invalid",
		APIKey:     "k",
		NodeName:   "n",
		MediaRoots: []string{"/mnt/media"},
	}
	statusSrv := newStatusServer(cfg)

	applyServerSettings(cfg, configPath, statusSrv, nil, nodes.NodeSettings{
		PathMap:       []nodes.PathMapping{{Server: "/srv/x", Local: "/var/other"}}, // outside /mnt/media → rejected
		MaxJobs:       7,
		PauseDispatch: true,
	})

	// (a) The pathMap portion was genuinely rejected: the early-return fired, so
	// nothing merged and Remap leaves the server prefix unchanged. (If this ever
	// resolved, the frame took the SUCCESS path and the test below would pass
	// vacuously — this assertion is what keeps P8 honestly exercised.)
	pm, _ := cfg.snapshot()
	if got := Remap(pm, "/srv/x/a.mkv"); got != "/srv/x/a.mkv" {
		t.Fatalf("pathMap was NOT rejected (frame took the success path): Remap = %q — P8 not exercised", got)
	}

	// (b) The pause echo still updated the node's cached display state.
	if !cfg.pauseSnapshot() {
		t.Fatal("P8 FAILED: a pathMap-rejected frame suppressed the pause-display update (cfg.DispatchPaused still false)")
	}
}

// TestApplyServerSettings_PauseAppliedOnAcceptedFrame is the happy-path companion:
// a fully-valid frame applies both the pathMap and the pause echo.
func TestApplyServerSettings_PauseAppliedOnAcceptedFrame(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{
		ServerURL:  "http://unused.invalid",
		APIKey:     "k",
		NodeName:   "n",
		MediaRoots: []string{"/mnt/media"},
	}
	statusSrv := newStatusServer(cfg)

	applyServerSettings(cfg, configPath, statusSrv, nil, nodes.NodeSettings{
		PathMap:       []nodes.PathMapping{{Server: "/srv/movies", Local: "/mnt/media/movies"}},
		MaxJobs:       2,
		PauseDispatch: true,
	})

	pm, _ := cfg.snapshot()
	if got := Remap(pm, "/srv/movies/a.mkv"); got != "/mnt/media/movies/a.mkv" {
		t.Fatalf("valid frame did not apply pathMap: Remap = %q", got)
	}
	if !cfg.pauseSnapshot() {
		t.Fatal("valid frame did not apply the pause echo")
	}
}
