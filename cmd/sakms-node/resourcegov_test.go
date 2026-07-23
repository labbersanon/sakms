package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/nodes"
)

// TestCPUMaxForPercent is the mapping-function unit test: sliderPercent × nproc,
// with an INJECTED nproc so tests control it (never runtime.NumCPU here), and the
// boundary cases 0 (unlimited/clear) and 100.
func TestCPUMaxForPercent(t *testing.T) {
	tests := []struct {
		name    string
		percent int
		nproc   int
		want    string
	}{
		{"zero is unlimited/clear", 0, 8, "max 100000"},
		{"negative also clears", -5, 8, "max 100000"},
		{"50pct on 8 cores", 50, 8, "400000 100000"},
		{"100pct on 8 cores", 100, 8, "800000 100000"},
		{"100pct on 1 core", 100, 1, "100000 100000"},
		{"10pct on 16 cores", 10, 16, "160000 100000"},
		{"nproc floored to 1 when zero", 50, 0, "50000 100000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cpuMaxForPercent(tt.percent, tt.nproc); got != tt.want {
				t.Fatalf("cpuMaxForPercent(%d, %d) = %q, want %q", tt.percent, tt.nproc, got, tt.want)
			}
		})
	}
}

func TestLeafNeedsThreadedConversion(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"domain invalid\n", true},
		{"domain threaded\n", false},
		{"threaded\n", false},
		{"domain\n", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := leafNeedsThreadedConversion(tt.in); got != tt.want {
			t.Errorf("leafNeedsThreadedConversion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestHasToken(t *testing.T) {
	// The load-bearing case: "cpuset" must NOT satisfy a query for "cpu".
	if hasToken("cpuset memory pids", "cpu") {
		t.Fatal("hasToken matched cpu against cpuset — a substring false-match would report enforcement available when cpu is NOT delegated")
	}
	if !hasToken("cpuset cpu memory", "cpu") {
		t.Fatal("hasToken failed to find a genuine cpu token")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// writeSyntheticLeaf pre-populates a synthetic leaf/cgroup.type file, standing
// in for what a real cgroupfs auto-creates the instant a child directory is
// mkdir'd — a plain os.Mkdir on a temp-dir does NOT do this (no real kernel),
// so every setupLeaf test must seed it explicitly to be a faithful simulation
// of the real filesystem setupLeaf actually reads from (Stage-4 correction:
// setupLeaf now gates on the LEAF's own type, not the parent's).
func writeSyntheticLeaf(t *testing.T, dir, leafType string) {
	t.Helper()
	leafDir := filepath.Join(dir, leafDirName)
	if err := os.Mkdir(leafDir, 0o755); err != nil {
		t.Fatalf("mkdir synthetic leaf: %v", err)
	}
	writeTestFile(t, filepath.Join(leafDir, "cgroup.type"), leafType)
}

// TestSetupLeaf_ConvertsLeafWhenInvalid proves the domain-threaded finding: a
// leaf whose own cgroup.type reads "domain invalid" — real on every cold start
// of this daemon, since the parent always holds a member process (the daemon
// itself) while also delegating cpu to the leaf — is converted to "threaded"
// BEFORE the PID move (otherwise the move would fail EOPNOTSUPP on a real
// kernel). Runs against a synthetic temp-dir cgroup layout — no root, no real
// cgroupfs.
func TestSetupLeaf_ConvertsLeafWhenInvalid(t *testing.T) {
	dir := t.TempDir() // stands in for the daemon's own (delegated) cgroup dir
	writeTestFile(t, filepath.Join(dir, "cgroup.subtree_control"), "cpu memory\n") // cpu already enabled
	writeSyntheticLeaf(t, dir, "domain invalid\n")

	g := &cgroupGovernor{
		selfPath: dir,
		leafPath: filepath.Join(dir, leafDirName),
		pid:      4242,
		nproc:    func() int { return 4 },
	}
	if err := g.setupLeaf(); err != nil {
		t.Fatalf("setupLeaf: %v", err)
	}

	leafType := filepath.Join(dir, leafDirName, "cgroup.type")
	if got := strings.TrimSpace(readTestFile(t, leafType)); got != "threaded" {
		t.Fatalf("expected leaf converted to threaded, got %q", got)
	}
	// PID move happened (and thus occurred AFTER the conversion — a real kernel
	// rejects the move otherwise).
	procs := strings.TrimSpace(readTestFile(t, filepath.Join(dir, leafDirName, "cgroup.procs")))
	if procs != "4242" {
		t.Fatalf("expected daemon pid 4242 moved into leaf, got %q", procs)
	}
}

// TestSetupLeaf_DoesNotConvertWhenLeafAlreadyValid proves the other half: a
// leaf whose own type is already valid (e.g. "domain", or already "threaded"
// from a prior run reusing the same leaf dir) is never rewritten — the write
// count into cgroup.type is exactly zero, provable in the temp-dir simulation
// because writeTestFile's mtime would otherwise change; here we instead assert
// the value is untouched byte-for-byte, including its original trailing
// whitespace, which a defensive "always write threaded" would normalize away.
func TestSetupLeaf_DoesNotConvertWhenLeafAlreadyValid(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "cgroup.subtree_control"), "cpu\n")
	writeSyntheticLeaf(t, dir, "domain\n")

	g := &cgroupGovernor{
		selfPath: dir,
		leafPath: filepath.Join(dir, leafDirName),
		pid:      99,
		nproc:    func() int { return 2 },
	}
	if err := g.setupLeaf(); err != nil {
		t.Fatalf("setupLeaf: %v", err)
	}

	if got := readTestFile(t, filepath.Join(dir, leafDirName, "cgroup.type")); got != "domain\n" {
		t.Fatalf("leaf cgroup.type should be untouched for an already-valid leaf, got %q", got)
	}
	// The PID move still happens regardless.
	procs := strings.TrimSpace(readTestFile(t, filepath.Join(dir, leafDirName, "cgroup.procs")))
	if procs != "99" {
		t.Fatalf("expected pid 99 moved into leaf, got %q", procs)
	}
}

// TestSetupLeaf_EnablesCPUControllerWhenAbsent proves setupLeaf enables cpu in
// the parent's subtree_control when it isn't already present.
func TestSetupLeaf_EnablesCPUControllerWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "cgroup.subtree_control"), "memory pids\n") // no cpu yet
	writeSyntheticLeaf(t, dir, "domain\n")

	g := &cgroupGovernor{
		selfPath: dir,
		leafPath: filepath.Join(dir, leafDirName),
		pid:      7,
		nproc:    func() int { return 1 },
	}
	if err := g.setupLeaf(); err != nil {
		t.Fatalf("setupLeaf: %v", err)
	}
	if got := strings.TrimSpace(readTestFile(t, filepath.Join(dir, "cgroup.subtree_control"))); got != "+cpu" {
		t.Fatalf("expected cpu controller enable write %q, got %q", "+cpu", got)
	}
}

// TestCapabilityDetect confirms the static probe reports available/unavailable
// from real filesystem state: available requires BOTH the cpu controller present
// in the delegated subtree_control AND a writable leaf cpu.max — not merely that
// cgroup-v2 exists generally.
func TestCapabilityDetect(t *testing.T) {
	newGov := func(dir string) *cgroupGovernor {
		return &cgroupGovernor{selfPath: dir, leafPath: filepath.Join(dir, leafDirName)}
	}

	t.Run("available: cpu delegated and cpu.max writable", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "cgroup.subtree_control"), "cpuset cpu memory\n")
		if err := os.Mkdir(filepath.Join(dir, leafDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(dir, leafDirName, "cpu.max"), "max 100000\n")
		if got := newGov(dir).capabilityDetect(); got != enforcementAvailable {
			t.Fatalf("capabilityDetect = %q, want %q", got, enforcementAvailable)
		}
	})

	t.Run("unavailable: cpu NOT in subtree_control (cpuset must not false-match)", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "cgroup.subtree_control"), "cpuset memory\n")
		if err := os.Mkdir(filepath.Join(dir, leafDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(dir, leafDirName, "cpu.max"), "max 100000\n")
		if got := newGov(dir).capabilityDetect(); got != enforcementUnavailable {
			t.Fatalf("capabilityDetect = %q, want %q", got, enforcementUnavailable)
		}
	})

	t.Run("unavailable: leaf cpu.max missing", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "cgroup.subtree_control"), "cpu\n")
		if err := os.Mkdir(filepath.Join(dir, leafDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := newGov(dir).capabilityDetect(); got != enforcementUnavailable {
			t.Fatalf("capabilityDetect = %q, want %q", got, enforcementUnavailable)
		}
	})
}

// TestApply_WritesCPUMaxLine confirms apply writes the resolved cpu.max line and
// returns the effective percent.
func TestApply_WritesCPUMaxLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, leafDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	g := &cgroupGovernor{
		selfPath: dir,
		leafPath: filepath.Join(dir, leafDirName),
		nproc:    func() int { return 8 },
	}
	eff, err := g.apply(50)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if eff != 50 {
		t.Fatalf("apply effective = %d, want 50", eff)
	}
	if got := strings.TrimSpace(readTestFile(t, filepath.Join(dir, leafDirName, "cpu.max"))); got != "400000 100000" {
		t.Fatalf("cpu.max = %q, want %q", got, "400000 100000")
	}
}

// TestReapplyPersistedCap_FromLocalConfigServerUnreachable is THE MOST IMPORTANT
// test in this stage (Critic MAJOR-2). It proves the persisted cap is re-applied
// at startup INDEPENDENT of any server re-push: the config carries a deliberately
// unreachable ServerURL, and reapplyPersistedCap takes no client / no SSE frame —
// it structurally CANNOT contact a server, so the applied cap can only have come
// from the local config.json.
func TestReapplyPersistedCap_FromLocalConfigServerUnreachable(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	// A prior run persisted a 50% cap. ServerURL points at a black hole
	// (127.0.0.1:1) so nothing in this test could reach a server even if it tried.
	writeTestFile(t, cfgPath, `{"serverUrl":"http://127.0.0.1:1/unreachable","nodeName":"n","cpuCapPercent":50}`)

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.CPUCapPercent != 50 {
		t.Fatalf("persisted cap not loaded: got %d, want 50", cfg.CPUCapPercent)
	}

	var applied []int
	applyFn := func(percent int) (int, error) {
		applied = append(applied, percent)
		return percent, nil
	}
	state := &capState{enforcement: enforcementAvailable}

	reapplyPersistedCap(cfg.CPUCapPercent, applyFn, state)

	if len(applied) != 1 || applied[0] != 50 {
		t.Fatalf("startup re-apply did not apply the persisted 50%% cap independent of the server: applied=%v", applied)
	}
	enf, ap := state.snapshot()
	if enf != enforcementAvailable {
		t.Fatalf("enforcement = %q, want available", enf)
	}
	if ap.EffectivePercent != 50 || ap.Error != "" {
		t.Fatalf("last-apply result = %+v, want {EffectivePercent:50}", ap)
	}
}

// TestCapApplier_EnqueueNeverBlocksWhenWorkerHung proves the async apply lane:
// the dispatch loop's enqueue must NEVER block, even while the worker is stuck
// mid-apply on a slow/hung cgroup write. If enqueue could block, a hung apply
// would stall job dispatch — the exact hazard the async lane exists to prevent.
func TestCapApplier_EnqueueNeverBlocksWhenWorkerHung(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	applyFn := func(percent int) (int, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release // hang until the test lets go
		return percent, nil
	}
	a := newCapApplier(applyFn, &capState{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	a.enqueue(10) // worker picks this up and hangs inside applyFn
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never started applying")
	}

	// With the worker hung, a burst of further enqueues must all return promptly.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			a.enqueue(i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue blocked while the worker was hung — the dispatch hot path would stall")
	}
	close(release)
}

// TestCapApplier_ForcedFailureReportsErrorNotSilentSuccess simulates an apply
// failure (an injected filesystem-style error) and confirms the reported state is
// available (static capability) + a last-apply error — NOT a silent success, and
// NOT a fabricated EffectivePercent equal to the failed target.
func TestCapApplier_ForcedFailureReportsErrorNotSilentSuccess(t *testing.T) {
	wantErr := errors.New("simulated cpu.max write EACCES")
	applyFn := func(percent int) (int, error) { return 0, wantErr }
	state := &capState{enforcement: enforcementAvailable}
	a := newCapApplier(applyFn, state)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	a.enqueue(50)

	// Poll until the applier records the result.
	deadline := time.After(2 * time.Second)
	for {
		_, ap := state.snapshot()
		if ap.Error != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("forced apply failure was never recorded")
		case <-time.After(5 * time.Millisecond):
		}
	}

	enf, ap := state.snapshot()
	if enf != enforcementAvailable {
		t.Fatalf("static capability must stay %q on an apply failure, got %q", enforcementAvailable, enf)
	}
	if ap.Error == "" {
		t.Fatal("forced apply failure must surface as a last-apply error, not silent success")
	}
	if ap.EffectivePercent == 50 {
		t.Fatal("a FAILED apply must NOT report the target (50%) as effective — that is a fabricated success")
	}
}

// TestCapState_RecordApply_KeepsLastGoodEffectiveOnError confirms a failed apply
// preserves the previously-applied effective value (reality) rather than zeroing
// or overwriting it with the failed target.
func TestCapState_RecordApply_KeepsLastGoodEffectiveOnError(t *testing.T) {
	c := &capState{}
	c.recordApply(50, nil) // a genuine success: 50% is really in force
	c.recordApply(0, errors.New("later write failed"))
	_, ap := c.snapshot()
	if ap.EffectivePercent != 50 {
		t.Fatalf("effective after a failed apply = %d, want the last-good 50", ap.EffectivePercent)
	}
	if ap.Error == "" {
		t.Fatal("the failure must still be recorded as an error")
	}
}

// TestUnavailableApply confirms the degraded applyFn errors for a non-zero cap
// (so the UI shows "not currently enforced") but treats a 0/clear as trivially
// satisfied.
func TestUnavailableApply(t *testing.T) {
	if _, err := unavailableApply(0); err != nil {
		t.Fatalf("clearing a cap on an unenforceable node should not error, got %v", err)
	}
	if _, err := unavailableApply(50); err == nil {
		t.Fatal("a non-zero cap on an unenforceable node must surface an error, not a silent no-op")
	}
}

// pollUntil polls cond every 5ms until it is true or the deadline passes.
func pollUntil(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if cond() {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestApplyServerSettings_CPUCapAppliedDespitePathMapRejection is the CPU-cap
// analogue of the P8 pause test: a settings frame carrying a valid cpuCapPercent
// but an INVALID pathMap (mapping outside mediaRoots) must STILL persist and
// re-apply the cap, because the cap write is hoisted into its own mutateAndSave
// ABOVE the pathMap-validation early-return. Enforcement honesty must not hinge
// on pathMap validity.
func TestApplyServerSettings_CPUCapAppliedDespitePathMapRejection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{
		ServerURL:  "http://unused.invalid",
		APIKey:     "k",
		NodeName:   "n",
		MediaRoots: []string{"/mnt/media"}, // non-empty → validation branch, pathMap below is rejected
	}
	statusSrv := newStatusServer(cfg)

	state := &capState{enforcement: enforcementAvailable}
	var mu sync.Mutex
	var applied []int
	applyFn := func(percent int) (int, error) {
		mu.Lock()
		applied = append(applied, percent)
		mu.Unlock()
		return percent, nil
	}
	applier := newCapApplier(applyFn, state)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go applier.run(ctx)

	applyServerSettings(cfg, configPath, statusSrv, applier, nodes.NodeSettings{
		PathMap:       []nodes.PathMapping{{Server: "/srv/x", Local: "/var/other"}}, // outside /mnt/media → rejected
		CPUCapPercent: 30,
	})

	// (a) the pathMap portion was genuinely rejected (early-return fired).
	pm, _ := cfg.snapshot()
	if got := Remap(pm, "/srv/x/a.mkv"); got != "/srv/x/a.mkv" {
		t.Fatalf("pathMap was NOT rejected (frame took success path): Remap = %q", got)
	}
	// (b) the cap was persisted despite the rejection.
	if cfg.cpuCapSnapshot() != 30 {
		t.Fatalf("cpu cap not persisted on a pathMap-rejected frame: got %d, want 30", cfg.cpuCapSnapshot())
	}
	// (c) the cap was applied (async) despite the rejection.
	ok := pollUntil(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range applied {
			if p == 30 {
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("cpu cap was not applied on a pathMap-rejected frame: applied=%v", applied)
	}
}

// TestStatusEndpoint_SurfacesDistinctCPUCapFields drives the REAL GET /status
// handler and confirms the three CPU-governor values surface as genuinely
// distinct fields: the configured cpuCapPercent (from cfg), the static
// enforcement capability, AND the last-apply result (effective + error) — a
// forced apply failure must read as available + error, never collapsed into a
// silent success.
func TestStatusEndpoint_SurfacesDistinctCPUCapFields(t *testing.T) {
	// Grab a free port, then hand it to the real status server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() //nolint:errcheck

	cfg := &NodeConfig{ServerURL: "http://unused.invalid", NodeName: "n", StatusPort: port, CPUCapPercent: 50}
	srv := newStatusServer(cfg)

	// available static capability, but the LAST apply failed → not enforced.
	state := &capState{enforcement: enforcementAvailable}
	state.recordApply(0, errors.New("cpu.max write denied"))
	srv.attachCapState(state)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.ListenAndServe(ctx)

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/status"
	var body []byte
	ok := pollUntil(t, 2*time.Second, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		body, _ = io.ReadAll(resp.Body)
		return true
	})
	if !ok {
		t.Fatal("status endpoint never became reachable")
	}

	var got statusSnapshot
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding status: %v (body=%s)", err, body)
	}
	if got.CPUCapPercent != 50 {
		t.Errorf("configured cpuCapPercent = %d, want 50", got.CPUCapPercent)
	}
	if got.Enforcement != enforcementAvailable {
		t.Errorf("enforcement = %q, want %q", got.Enforcement, enforcementAvailable)
	}
	if got.CPUCapApply.Error == "" {
		t.Error("cpuCapApply.error must surface the last-apply failure distinctly, not be collapsed into a green enforcement flag")
	}
	if got.CPUCapApply.EffectivePercent == 50 {
		t.Error("a failed apply must NOT report EffectivePercent == the configured 50 (that would be a fabricated success)")
	}
}

// TestCapApplier_LatestWins confirms the size-1 mailbox coalesces a burst so the
// final value is the one applied (no stale intermediate cap left in force).
func TestCapApplier_LatestWins(t *testing.T) {
	var mu sync.Mutex
	var last int
	gate := make(chan struct{})
	applyFn := func(percent int) (int, error) {
		<-gate // hold the worker until the burst is fully queued
		mu.Lock()
		last = percent
		mu.Unlock()
		return percent, nil
	}
	a := newCapApplier(applyFn, &capState{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	// Prime the worker so it is blocked in applyFn, then queue a burst behind it.
	a.enqueue(1)
	for i := 2; i <= 9; i++ {
		a.enqueue(i)
	}
	close(gate) // release worker; it drains 1, then the coalesced latest (9)

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		got := last
		mu.Unlock()
		if got == 9 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("final applied cap = %d, want the latest (9)", got)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
