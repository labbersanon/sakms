package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// resourcegov.go is the node CPU governor (node-resource-governor plan, Stage 3):
// a real, kernel-enforced cgroup-v2 CPU ceiling on the sakms-node.service unit's
// own delegated subtree. It is Option C (the default the Stage-1 spike proved):
// Delegate=yes on the unit + a self-managed leaf cgroup whose cpu.max the
// non-root daemon writes. Option A (systemctl set-property behind a polkit grant)
// is deliberately NOT built here — the spike confirmed C works and the plan
// defaults to it.
//
// Everything in this file is isolated and testable without root or a live
// Delegate=yes setup: the cgroup filesystem interaction goes through paths on a
// *cgroupGovernor, so tests point it at a synthetic temp-dir layout, and the
// percentage math / applier / last-apply-result plumbing take injectable
// functions rather than reaching for the real cgroupfs or runtime.NumCPU
// directly.

const (
	// enforcementAvailable / enforcementUnavailable are the STATIC capability
	// values reported up the status path. "available" means a real cpu.max write
	// would actually work on this node (the cpu controller is delegated into this
	// unit's subtree AND the leaf cpu.max is writable by this uid) — NOT merely
	// that cgroup-v2 exists in general.
	enforcementAvailable   = "available"
	enforcementUnavailable = "unavailable"

	// cgroupCPUPeriod is the cpu.max period (µs). Quota is expressed against it:
	// "<quota> <period>" allows <quota> µs of CPU time per <period> µs.
	cgroupCPUPeriod = 100000

	// cgroupV2Root is the unified cgroup-v2 mount and procSelfCgroup is where the
	// daemon's own current cgroup path is read from (more robust than assuming a
	// systemd-unit-name-derived path — see resolveSelfCgroupPath).
	cgroupV2Root   = "/sys/fs/cgroup"
	procSelfCgroup = "/proc/self/cgroup"

	// cgroupFileMode is the perm passed to os.WriteFile for cgroup interface
	// files. In a real cgroupfs these files always pre-exist (the kernel
	// auto-populates them on mkdir), so O_CREATE never fires and the mode is
	// ignored — cgroup control files are 0644 regardless. It matters only for the
	// synthetic temp-dir layout the unit tests use, where the file may be created
	// by the write and must remain readable.
	cgroupFileMode = 0o644

	// leafDirName is the single leaf cgroup created under the delegated subtree.
	// cgroup-v2's no-internal-process rule means the daemon can't both hold
	// processes at the subtree root and set limits there, so the leaf is
	// mandatory: the daemon moves its own PID here and every ffmpeg it forks
	// inherits this cgroup automatically.
	leafDirName = "leaf"
)

// errCPUCapUnavailable is the apply error surfaced when real cgroup enforcement
// is not available on this node (no systemd cgroup-v2 delegation). It is
// returned only for a non-zero cap: a 0 (clear/unlimited) is trivially satisfied
// with nothing to enforce, so it never errors.
var errCPUCapUnavailable = errors.New("cpu cap enforcement unavailable on this node (needs systemd cgroup-v2 Delegate=yes)")

// setupCPUGovernor performs the production leaf-cgroup setup and returns the
// applyFn the applier + startup re-apply use. It resolves the daemon's own
// delegated cgroup, creates + prepares the leaf (mkdir, domain-threaded
// conversion, cpu-controller enable, self-PID move), probes the static
// capability into state, and — only when enforcement is genuinely available —
// returns the real gov.apply. On any failure it degrades cleanly to
// unavailableApply (state.enforcement stays "unavailable"), so a dev/non-systemd
// host never crashes and never fakes enforcement.
func setupCPUGovernor(state *capState) func(percent int) (int, error) {
	gov, err := newCgroupGovernor(cgroupV2Root, procSelfCgroup)
	if err != nil {
		log.Printf("sakms-node: cpu governor unavailable (resolving own cgroup): %v", err)
		return unavailableApply
	}
	if err := gov.setupLeaf(); err != nil {
		log.Printf("sakms-node: cpu governor unavailable (leaf setup): %v", err)
		return unavailableApply
	}
	enforcement := gov.capabilityDetect()
	state.setEnforcement(enforcement)
	if enforcement != enforcementAvailable {
		log.Printf("sakms-node: cpu governor present but not enforceable (cpu controller not delegated or leaf cpu.max not writable)")
		return unavailableApply
	}
	log.Printf("sakms-node: cpu governor ready (leaf=%s, enforcement=available)", gov.leafPath)
	return gov.apply
}

// cpuMaxForPercent maps a slider percentage to a cgroup-v2 cpu.max file line.
//
// The mapping is sliderPercent × nproc, where nproc is the NODE-LOCAL logical
// CPU count. IMPORTANT: nproc here is CPU affinity / logical-CPU count (the value
// runtime.NumCPU() reports) — NOT a cgroup-aware bandwidth figure — and it is
// ALWAYS read on the node, NEVER a value from the server (using the server's core
// count would be a silent correctness bug). It is injected as a parameter so this
// logic is unit-testable and so a future refactor can't silently swap the source
// out from under the percentage→cores meaning. percent<=0 yields "max <period>"
// (unlimited/clear, mirroring MaxJobs' 0 = unlimited convention).
func cpuMaxForPercent(percent, nproc int) string {
	if percent <= 0 {
		return fmt.Sprintf("max %d", cgroupCPUPeriod)
	}
	if nproc < 1 {
		nproc = 1
	}
	// e.g. 50% on an 8-core node → 50 * 8 * 1000 = 400000 → "400000 100000".
	quota := percent * nproc * (cgroupCPUPeriod / 100)
	return fmt.Sprintf("%d %d", quota, cgroupCPUPeriod)
}

// hasToken reports whether whitespace-separated field list s contains exactly
// tok. Used against cgroup.controllers / cgroup.subtree_control, where "cpuset"
// must NOT match a query for "cpu" — a plain strings.Contains would false-match.
func hasToken(s, tok string) bool {
	for _, f := range strings.Fields(s) {
		if f == tok {
			return true
		}
	}
	return false
}

// leafNeedsThreadedConversion reports whether a leaf's OWN cgroup.type value
// means it cannot yet accept a cgroup.procs write and must be converted to
// "threaded" first. A freshly mkdir'd leaf whose parent holds a member process
// (the daemon itself) while also delegating a child controller comes up
// "domain invalid" — real on every cold start of this daemon (Stage-4 finding;
// see setupLeaf's doc comment for why the PARENT's own type is not a reliable
// signal here). Any other observed value ("domain", "threaded", or already
// converted) needs no write.
func leafNeedsThreadedConversion(leafType string) bool {
	return hasToken(leafType, "invalid")
}

// cgroupGovernor owns the daemon's leaf cgroup under its delegated subtree and
// performs the actual cpu.max writes. All filesystem paths are fields so tests
// can point it at a synthetic temp-dir cgroup layout — no root, no real cgroupfs,
// no live Delegate=yes required to exercise the logic.
type cgroupGovernor struct {
	selfPath string     // the daemon's own (delegated) cgroup dir, e.g. /sys/fs/cgroup/system.slice/sakms-node.service
	leafPath string     // selfPath/leaf — where the daemon's PID + ffmpeg children live
	pid      int        // the daemon's own PID (self-migration only; moving an external PID is an EACCES security boundary, never attempted)
	nproc    func() int // node-local logical CPU source (runtime.NumCPU in production)
}

// newCgroupGovernor resolves the daemon's own current cgroup path from
// /proc/self/cgroup (robust against systemd-unit-name assumptions) and returns a
// governor rooted there. It does not touch the filesystem beyond that read;
// setupLeaf performs the mkdir / type-conversion / controller-enable / PID move.
func newCgroupGovernor(root, procSelfCgroupPath string) (*cgroupGovernor, error) {
	selfPath, err := resolveSelfCgroupPath(root, procSelfCgroupPath)
	if err != nil {
		return nil, err
	}
	return &cgroupGovernor{
		selfPath: selfPath,
		leafPath: filepath.Join(selfPath, leafDirName),
		pid:      os.Getpid(),
		nproc:    runtime.NumCPU,
	}, nil
}

// resolveSelfCgroupPath reads procSelfCgroupPath and returns the absolute path to
// the daemon's own cgroup dir under the unified root. cgroup-v2 unified mode
// writes a single "0::<path>" line; anything else (v1 / hybrid) is an error here,
// which the caller treats as "enforcement unavailable".
func resolveSelfCgroupPath(root, procSelfCgroupPath string) (string, error) {
	data, err := os.ReadFile(procSelfCgroupPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", procSelfCgroupPath, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			rel := strings.TrimSpace(rest)
			return filepath.Join(root, filepath.FromSlash(rel)), nil
		}
	}
	return "", fmt.Errorf("no cgroup-v2 unified (0::) entry in %s", procSelfCgroupPath)
}

// setupLeaf prepares the leaf cgroup so the daemon can enforce a cpu.max:
//
//  1. mkdir the leaf (idempotent).
//  2. Enable the cpu controller in the parent's subtree_control (if not
//     already) so leaf/cpu.max exists and actually applies. THIS MUST HAPPEN
//     BEFORE the leaf-type check below, not after (a real ordering bug this
//     comment corrects — see the Stage-4 finding paragraph).
//  3. If the leaf's OWN cgroup.type reads "domain invalid" — checked AFTER
//     step 2, since that write is what can retroactively cause it — convert
//     it to "threaded" before the PID move, or the move fails EOPNOTSUPP
//     (the Stage-1 empirical finding).
//  4. Move the daemon's OWN PID into leaf/cgroup.procs (self-migration within
//     its own delegated subtree — the only move the daemon ever performs).
//
// Stage-4 real-hardware finding (this ordering is the fix, not a
// simplification): a freshly mkdir'd leaf under a parent with NO controllers
// yet delegated to children comes up perfectly valid ("domain", not
// "domain invalid") — enabling a domain controller like cpu in the PARENT's
// subtree_control is the exact operation that retroactively invalidates an
// already-existing plain-"domain" child, because the parent now simultaneously
// holds a member process (the daemon itself, until this move) AND delegates a
// domain controller to children — the "no internal process constraint" a
// domain cgroup can only satisfy by becoming "domain threaded" once a
// threaded descendant exists. The original Stage-3 code checked the leaf's
// type BEFORE calling ensureCPUController, so on a genuine cold start (cpu
// not yet enabled) it observed the leaf as still-valid "domain", skipped the
// conversion, then unknowingly invalidated its own leaf one line later by
// enabling cpu — and the PID move failed. Checking the parent's type instead
// of the leaf's (an earlier, now-superseded fix) never surfaced this either,
// because both bugs only manifest together on a truly cold start with no
// pre-existing threaded descendant and no cpu already delegated — exactly the
// state a fresh production install starts in, and exactly why this needed a
// real E2E on real hardware to find (CG-S4), not another round of synthetic
// unit tests.
func (g *cgroupGovernor) setupLeaf() error {
	if err := os.Mkdir(g.leafPath, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("creating leaf cgroup %s: %w", g.leafPath, err)
	}

	if err := g.ensureCPUController(); err != nil {
		return err
	}

	leafType, err := os.ReadFile(filepath.Join(g.leafPath, "cgroup.type"))
	if err != nil {
		return fmt.Errorf("reading leaf cgroup.type: %w", err)
	}
	if leafNeedsThreadedConversion(string(leafType)) {
		if err := os.WriteFile(filepath.Join(g.leafPath, "cgroup.type"), []byte("threaded"), cgroupFileMode); err != nil {
			return fmt.Errorf("converting leaf %s to threaded: %w", g.leafPath, err)
		}
	}

	if err := os.WriteFile(filepath.Join(g.leafPath, "cgroup.procs"), []byte(strconv.Itoa(g.pid)), cgroupFileMode); err != nil {
		return fmt.Errorf("moving pid %d into leaf %s: %w", g.pid, g.leafPath, err)
	}
	return nil
}

// ensureCPUController enables the cpu controller in the parent's
// subtree_control if it isn't already present, so the leaf gets a cpu.max the
// daemon can write. Delegation of cpu is not automatic — it may need enabling.
func (g *cgroupGovernor) ensureCPUController() error {
	stPath := filepath.Join(g.selfPath, "cgroup.subtree_control")
	data, err := os.ReadFile(stPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", stPath, err)
	}
	if hasToken(string(data), "cpu") {
		return nil
	}
	if err := os.WriteFile(stPath, []byte("+cpu"), cgroupFileMode); err != nil {
		return fmt.Errorf("enabling cpu controller in %s: %w", stPath, err)
	}
	return nil
}

// apply writes the cpu.max line for percent to the leaf and returns the resolved
// effective percent actually put in force plus any error. This is the function
// that populates the last-apply result. percent<=0 clears the cap (writes "max").
func (g *cgroupGovernor) apply(percent int) (int, error) {
	line := cpuMaxForPercent(percent, g.nproc())
	if err := os.WriteFile(filepath.Join(g.leafPath, "cpu.max"), []byte(line), cgroupFileMode); err != nil {
		return 0, fmt.Errorf("writing cpu.max %q: %w", line, err)
	}
	if percent < 0 {
		return 0, nil
	}
	return percent, nil
}

// capabilityDetect returns the STATIC enforcement capability. "available" means
// a real write would actually work here: the cpu controller is present in this
// unit's DELEGATED subtree_control AND the leaf's cpu.max is writable by this
// process's uid — not merely that cgroup-v2 exists in general. Anything short of
// that is "unavailable".
func (g *cgroupGovernor) capabilityDetect() string {
	data, err := os.ReadFile(filepath.Join(g.selfPath, "cgroup.subtree_control"))
	if err != nil || !hasToken(string(data), "cpu") {
		return enforcementUnavailable
	}
	f, err := os.OpenFile(filepath.Join(g.leafPath, "cpu.max"), os.O_WRONLY, 0)
	if err != nil {
		return enforcementUnavailable
	}
	_ = f.Close()
	return enforcementAvailable
}

// unavailableApply is the applyFn used when real enforcement is not available on
// this node. A non-zero cap surfaces errCPUCapUnavailable (so the UI shows
// "not currently enforced" rather than a fabricated success); a 0 (clear) is
// trivially satisfied.
func unavailableApply(percent int) (int, error) {
	if percent <= 0 {
		return 0, nil
	}
	return 0, errCPUCapUnavailable
}

// cpuCapApplyResult is the last-apply result: the quota actually in force right
// now plus any error from the most recent apply attempt. Two distinct pieces of
// information, never collapsed with the static enforcement capability.
type cpuCapApplyResult struct {
	EffectivePercent int    `json:"effectivePercent"`
	Error            string `json:"error,omitempty"`
}

// capState holds the two DISTINCT reporting values the status path exposes: the
// STATIC enforcement capability (available|unavailable), and the LAST-APPLY
// result (effective quota + last error). Kept separate so a forced/simulated
// apply failure surfaces as "available" + an error, never as a silent success.
type capState struct {
	mu          sync.RWMutex
	enforcement string
	apply       cpuCapApplyResult
}

// setEnforcement records the static capability (called once at startup).
func (c *capState) setEnforcement(s string) {
	c.mu.Lock()
	c.enforcement = s
	c.mu.Unlock()
}

// recordApply stores the outcome of one apply attempt. On error it keeps the
// previous EffectivePercent (which reflects the cap genuinely last in force) and
// records the error — it MUST NOT report the failed target as effective, which
// would be a fabricated success. On success it updates EffectivePercent and
// clears the error.
func (c *capState) recordApply(effective int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.apply.Error = err.Error()
		return
	}
	c.apply.EffectivePercent = effective
	c.apply.Error = ""
}

// snapshot returns the static enforcement value and the last-apply result under
// the read lock, for the GET /status handler.
func (c *capState) snapshot() (enforcement string, apply cpuCapApplyResult) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enforcement, c.apply
}

// capApplier runs cap applies on a dedicated worker goroutine so the dispatch
// loop's select (applyServerSettings) never blocks on a cgroup file write. The
// mailbox is a size-1, latest-wins channel: enqueue never blocks (even if the
// worker is mid-apply or hung), and only the most recent requested cap is ever
// pending — a rapid burst of settings frames coalesces to the final value.
type capApplier struct {
	applyFn func(percent int) (int, error)
	state   *capState
	ch      chan int
}

func newCapApplier(applyFn func(percent int) (int, error), state *capState) *capApplier {
	return &capApplier{applyFn: applyFn, state: state, ch: make(chan int, 1)}
}

// enqueue hands a new target percent to the worker without ever blocking the
// caller (the dispatch loop). With a size-1 channel and a single producer, the
// drain-then-send leaves exactly the latest value queued.
func (a *capApplier) enqueue(percent int) {
	select {
	case a.ch <- percent:
		return
	default:
	}
	// Channel full: drop the stale pending value, queue the newest.
	select {
	case <-a.ch:
	default:
	}
	select {
	case a.ch <- percent:
	default:
	}
}

// run is the worker loop: it applies each queued cap and records the result for
// the status path. Runs until ctx is cancelled.
func (a *capApplier) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-a.ch:
			eff, err := a.applyFn(p)
			a.state.recordApply(eff, err)
			if err != nil {
				log.Printf("sakms-node: cpu cap apply (%d%%) failed: %v", p, err)
			} else {
				log.Printf("sakms-node: cpu cap applied: %d%% (effective quota via node-local nproc)", eff)
			}
		}
	}
}

// reapplyPersistedCap re-applies the locally-persisted cap at daemon startup,
// SYNCHRONOUSLY, BEFORE the dispatch loop accepts its first job — and crucially
// INDEPENDENT of any server re-push (node-resource-governor plan, Critic
// MAJOR-2). A node that restarts while the server is unreachable would otherwise
// run uncapped indefinitely while the UI still shows "enforced": relying on the
// server re-pushing the cap after reconnect fails exactly when it matters most.
// This reads the value purely from local config.json (passed in as percent) and
// applies it directly, so no network / SSE frame is involved.
func reapplyPersistedCap(percent int, applyFn func(percent int) (int, error), state *capState) {
	eff, err := applyFn(percent)
	state.recordApply(eff, err)
	if err != nil {
		log.Printf("sakms-node: startup cpu cap re-apply (%d%%) failed: %v", percent, err)
	} else if percent > 0 {
		log.Printf("sakms-node: startup cpu cap re-applied from local config: %d%%", eff)
	}
}
