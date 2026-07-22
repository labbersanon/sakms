// Command sakms-node-tray is a system-tray companion for sakms-node.
// It polls the daemon's local status server (GET /status on localhost) every
// 3 seconds and reflects the node's lifecycle state as a tray icon:
//
//	amber — pending pairing (displays the 6-char pairing code)
//	green — authenticated and connected to the sakms server
//	red   — daemon not running or disconnected
//
// The tray never talks to the sakms server and holds no credentials. It does,
// however, talk to the daemon over TWO local channels: it READS lifecycle
// state + mediaRoots containment status from the loopback TCP status server
// (GET /status), and — only for the security-sensitive write path — it edits
// the node's local mediaRoots allowlist over the daemon's unix-domain control
// socket (see mediaroots.go). Setting mediaRoots is a local-node-only operation
// by design; it is never reachable from the sakms server / wire.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
)

const (
	defaultStatusPort    = 7810
	defaultControlSocket = "/run/sakms-node/control.sock"
	pollInterval         = 3 * time.Second
	httpTimeout          = 2 * time.Second
	controlTimeout       = 5 * time.Second
	// maxRootSlots caps the number of media-root rows the tray renders. Roots
	// are typically 1–3; the pool is fixed because fyne.io/systray items are
	// created once (menu order is fixed at creation) and reused via show/hide.
	maxRootSlots = 12
	// maxKeySlots caps the library-path-key rows in the path-mapping section.
	// The catalog is a fixed, bounded set (~5 keys); the pool is oversized and
	// fixed for the same reason as maxRootSlots (menu order fixed at creation).
	maxKeySlots = 8
)

type statusResponse struct {
	State       string `json:"state"`
	PairingCode string `json:"pairingCode,omitempty"`
	ServerURL   string `json:"serverUrl"`
	DeviceName  string `json:"deviceName"`
	NodeID      string `json:"nodeId,omitempty"`

	// Warning surfaces the daemon's mediaRoots grace-period notice or the most
	// recent rejected settings-push reason. omitempty: absent on most responses.
	Warning string `json:"warning,omitempty"`

	// MediaRootScopes reports, per configured mediaRoots entry, its Phase 2
	// OS-level containment state (app_level_only / namespace_scoped /
	// namespace_scoped_but_unbound). omitempty: absent when mediaRoots is unset
	// (the grace period) or on a daemon that predates the field.
	MediaRootScopes []mediaRootStatus `json:"mediaRootScopes,omitempty"`
}

// mediaRootStatus mirrors the daemon's per-root containment record from
// GET /status. Scope is one of the daemon's mediaRootScope string values.
type mediaRootStatus struct {
	Path  string `json:"path"`
	Scope string `json:"scope"`
}

func main() {
	statusPort := flag.Int("status-port", defaultStatusPort,
		"port of the sakms-node status server")
	controlSocket := flag.String("control-socket", defaultControlSocket,
		"path to the sakms-node local mediaRoots control socket")
	flag.Parse()

	t := &trayUI{
		statusURL: fmt.Sprintf("http://127.0.0.1:%d/status", *statusPort),
		control:   newControlClient(*controlSocket),
	}
	systray.Run(t.run, nil)
}

// trayUI holds the tray's menu items and the state shared between the status
// poll loop and the click-handler goroutines (add/remove roots). All mutable
// fields and the rootSlot paths are guarded by mu.
type trayUI struct {
	statusURL string
	control   *controlClient

	mStatus  *systray.MenuItem
	mCopy    *systray.MenuItem
	mAddRoot *systray.MenuItem
	mWarning *systray.MenuItem
	mDrift   *systray.MenuItem
	mQuit    *systray.MenuItem
	roots    []*rootSlot

	// Path-mapping section (Stage 3). mPathHeader is a disabled label;
	// mAddRootFirst appears only while the mediaRoot gate is closed and routes to
	// the mediaRoots picker; mPathWarning is the persistent last-push-failed line.
	mPathHeader   *systray.MenuItem
	mAddRootFirst *systray.MenuItem
	mPathWarning  *systray.MenuItem
	keySlots      []*keySlot

	mu           sync.Mutex
	lastKey      string
	lastCode     string
	notifiedCode string // which pairing code we already notified about

	// Path-mapping state (guarded by mu). mediaRootCount drives the UX gate;
	// pmFetched/pmCatalog/pmAuthored/pmLastPushError hold the last GET /pathmap.
	mediaRootCount  int
	pmFetched       bool
	pmCatalog       []string
	pmAuthored      []authoredMapping
	pmLastPushError string
}

// rootSlot is one reusable media-root menu row: a display-only parent item
// (path + containment scope) with a "Remove" sub-item. path is the root the
// slot currently represents ("" when the slot is hidden/unused).
type rootSlot struct {
	item   *systray.MenuItem
	remove *systray.MenuItem
	path   string // guarded by trayUI.mu
}

func (t *trayUI) run() {
	systray.SetTitle("sakms-node")
	systray.SetTooltip("sakms-node — starting…")
	systray.SetIcon(iconAmber())

	t.mStatus = systray.AddMenuItem("Starting…", "Current node state")
	t.mStatus.Disable()
	t.mCopy = systray.AddMenuItem("Copy pairing code", "Copy the 6-char code to the clipboard")
	t.mCopy.Hide()

	systray.AddSeparator()
	t.mAddRoot = systray.AddMenuItem("Add media root…",
		"Pick a folder to add to this node's media-root allowlist")
	for i := 0; i < maxRootSlots; i++ {
		item := systray.AddMenuItem("", "")
		item.Disable() // display-only; the Remove sub-item is the action
		item.Hide()
		rm := item.AddSubMenuItem("Remove from allowlist", "Remove this media root")
		t.roots = append(t.roots, &rootSlot{item: item, remove: rm})
	}

	systray.AddSeparator()
	t.mPathHeader = systray.AddMenuItem("Configure path mappings",
		"Map each server library path key to a local folder on this node")
	t.mPathHeader.Disable()
	t.mPathHeader.Hide()
	t.mAddRootFirst = systray.AddMenuItem("Add a media root first…",
		"A media root must be configured before path mappings can be set")
	t.mAddRootFirst.Hide()
	for i := 0; i < maxKeySlots; i++ {
		item := systray.AddMenuItem("", "")
		item.Disable() // display-only; the Set/Remove sub-items are the actions
		item.Hide()
		set := item.AddSubMenuItem("Set folder…", "Pick a local folder for this library path key")
		rm := item.AddSubMenuItem("Remove mapping", "Clear this key's local path mapping")
		rm.Hide()
		t.keySlots = append(t.keySlots, &keySlot{item: item, setItem: set, removeItem: rm})
	}
	t.mPathWarning = systray.AddMenuItem("", "Path-mapping push status")
	t.mPathWarning.Disable()
	t.mPathWarning.Hide()

	systray.AddSeparator()
	t.mWarning = systray.AddMenuItem("", "sakms-node warning")
	t.mWarning.Disable()
	t.mWarning.Hide()
	t.mDrift = systray.AddMenuItem("", "OS-level containment status")
	t.mDrift.Disable()
	t.mDrift.Hide()

	systray.AddSeparator()
	t.mQuit = systray.AddMenuItem("Quit tray app", "Close this tray icon (does not stop sakms-node)")

	// Click handlers that may block (the native picker can sit open for a long
	// time; socket I/O can stall) run in their own goroutines so the status
	// poll ticker is never held up.
	go func() {
		for range t.mAddRoot.ClickedCh {
			t.handleAddRoot()
		}
	}()
	for i := range t.roots {
		rs := t.roots[i]
		go func() {
			for range rs.remove.ClickedCh {
				t.handleRemoveRoot(rs)
			}
		}()
	}

	// Path-mapping click handlers. Each runs in its own goroutine so a blocking
	// picker or a stalled control-socket call never holds up the poll ticker.
	go func() {
		for range t.mAddRootFirst.ClickedCh {
			t.handleAddRoot() // route the operator to the existing mediaRoots picker
		}
	}()
	for i := range t.keySlots {
		ks := t.keySlots[i]
		go func() {
			for range ks.setItem.ClickedCh {
				t.handlePathMapSet(ks)
			}
		}()
		go func() {
			for range ks.removeItem.ClickedCh {
				t.handlePathMapClear(ks)
			}
		}()
	}

	// Surface the group-membership relogin diagnostic early: if this desktop
	// session can't reach the control socket because it predates the RPM adding
	// the user to the shared group, connecting fails with EACCES.
	go t.probeControlSocket()

	t.poll()
	// The initial pathmap fetch runs asynchronously: run() is the onReady callback
	// and must return promptly (it already blocks up to httpTimeout on t.poll());
	// a synchronous control-socket fetch could stack another controlTimeout on top.
	go t.pollPathMap()

	go t.loop()
}

// loop runs the poll ticker and the copy/quit click handlers. It must run in
// its own goroutine so run() (the systray onReady callback) can return: the
// fyne.io/systray library only signals initialMenuBuilt.Done() after onReady
// returns, and GetLayout (the DBusMenu call that builds the popup on click)
// blocks on that signal — never returning here deadlocks the menu.
func (t *trayUI) loop() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.poll()
			// In its own goroutine: the control-socket fetch can stall up to
			// controlTimeout, and this loop goroutine also services the quit/copy
			// clicks — it must not block on the pathmap refresh.
			go t.pollPathMap()
		case <-t.mCopy.ClickedCh:
			t.mu.Lock()
			code := t.lastCode
			t.mu.Unlock()
			copyToClipboard(code)
		case <-t.mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// poll fetches status (outside the lock, so a slow HTTP call never blocks the
// click handlers) then applies it to the UI under the lock.
func (t *trayUI) poll() {
	s, err := fetchStatus(t.statusURL)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.applyStatus(s, err)
}

// applyStatus updates the tray from a status snapshot. Caller must hold t.mu.
func (t *trayUI) applyStatus(s *statusResponse, err error) {
	state := "disconnected"
	code := ""
	warning := ""
	var scopes []mediaRootStatus
	if err == nil {
		state = s.State
		code = s.PairingCode
		warning = s.Warning
		scopes = s.MediaRootScopes
	}

	key := statusKey(state, code, warning, scopes)
	if key == t.lastKey {
		return
	}
	t.lastKey = key
	t.lastCode = code

	switch state {
	case "pending":
		systray.SetIcon(iconAmber())
		if code != "" {
			systray.SetTooltip("Pairing code: " + code + " — approve in Settings → Nodes")
			t.mStatus.SetTitle("Pairing code: " + code)
			t.mCopy.Show()
			if t.notifiedCode != code {
				t.notifiedCode = code
				notify("sakms-node pairing",
					"Code: "+code+" — approve in Settings → Nodes")
			}
		} else {
			systray.SetTooltip("sakms-node — waiting to pair")
			t.mStatus.SetTitle("Waiting to pair…")
			t.mCopy.Hide()
		}

	case "connected":
		systray.SetIcon(iconGreen())
		systray.SetTooltip("sakms-node — connected")
		t.mStatus.SetTitle("Connected")
		t.mCopy.Hide()
		t.notifiedCode = ""

	default: // "disconnected" or daemon unreachable
		systray.SetIcon(iconRed())
		if err != nil {
			systray.SetTooltip("sakms-node — not running")
			t.mStatus.SetTitle("Daemon not running")
		} else {
			systray.SetTooltip("sakms-node — disconnected")
			t.mStatus.SetTitle("Disconnected")
		}
		t.mCopy.Hide()
		t.notifiedCode = ""
	}

	t.renderRoots(scopes)

	// The mediaRoots count (len of the per-root scopes) drives the path-mapping
	// gate; re-render that section whenever status changes. The catalog/authored
	// data itself comes from pollPathMap, not here.
	t.mediaRootCount = len(scopes)
	t.renderPathMap()

	if warning != "" {
		t.mWarning.SetTitle("⚠ " + warning)
		t.mWarning.Show()
	} else {
		t.mWarning.Hide()
	}
}

// renderRoots updates the media-root rows from the latest scopes. Caller must
// hold t.mu.
func (t *trayUI) renderRoots(scopes []mediaRootStatus) {
	overflow := len(scopes) > len(t.roots)
	for i, rs := range t.roots {
		last := i == len(t.roots)-1
		switch {
		case overflow && last:
			// Graceful cap: roots are typically 1–3, but if they ever exceed
			// the fixed pool, use the last slot as a non-removable summary
			// rather than silently dropping rows.
			rs.path = ""
			extra := len(scopes) - len(t.roots) + 1
			rs.item.SetTitle(fmt.Sprintf("…and %d more (edit config to manage)", extra))
			rs.item.SetTooltip("More media roots than the tray can list")
			rs.item.Show()
		case i < len(scopes):
			s := scopes[i]
			rs.path = s.Path
			rs.item.SetTitle("• " + s.Path + "  [" + scopeLabel(s.Scope) + "]")
			rs.item.SetTooltip(s.Path + " — " + s.Scope)
			rs.item.Show()
		default:
			rs.path = ""
			rs.item.Hide()
		}
	}

	// Signal OS-level containment drift only when containment is actually active
	// on this node AND a root is app-level-only — see containmentDrift. On a pure
	// app-level node (the common case, containment never applied) nothing is out
	// of sync, so the hint stays hidden; each root still shows its own scope
	// annotation unconditionally above.
	if containmentDrift(scopes) {
		t.mDrift.SetTitle("⚠ OS-level containment out of sync — a root operator must re-run apply-mediaroots.sh and restart the daemon")
		t.mDrift.Show()
	} else {
		t.mDrift.Hide()
	}
}

// containmentDrift reports whether the app-level allowlist has diverged from the
// last-applied OS-level (Phase 2) sandbox: true only when containment is active
// on this node (some root is namespace_scoped / namespace_scoped_but_unbound)
// AND at least one root is app_level_only (added/changed but not yet re-applied).
// It is deliberately false on a node where containment was never applied (no root
// is namespace_scoped*), so the drift hint never false-alarms the common
// app-level-only case.
func containmentDrift(scopes []mediaRootStatus) bool {
	active, appOnly := false, false
	for _, s := range scopes {
		switch s.Scope {
		case "namespace_scoped", "namespace_scoped_but_unbound":
			active = true
		case "app_level_only":
			appOnly = true
		}
	}
	return active && appOnly
}

// scopeLabel renders a mediaRootScope value as a short human-readable tag.
func scopeLabel(scope string) string {
	switch scope {
	case "namespace_scoped":
		return "OS-contained"
	case "namespace_scoped_but_unbound":
		return "OS-contained, mount missing"
	case "app_level_only":
		return "app-level only"
	case "":
		return "unknown"
	default:
		return scope
	}
}

// statusKey is the change-detection fingerprint: the poll loop only re-renders
// the UI when state, pairing code, warning, or the roots/scopes list changes.
func statusKey(state, code, warning string, scopes []mediaRootStatus) string {
	var b strings.Builder
	b.WriteString(state)
	b.WriteByte('|')
	b.WriteString(code)
	b.WriteByte('|')
	b.WriteString(warning)
	b.WriteByte('|')
	for _, s := range scopes {
		b.WriteString(s.Path)
		b.WriteByte('=')
		b.WriteString(s.Scope)
		b.WriteByte(';')
	}
	return b.String()
}

func fetchStatus(url string) (*statusResponse, error) {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var s statusResponse
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// notify fires a desktop notification via notify-send if available; silently
// does nothing if notify-send is not installed.
func notify(title, body string) {
	if err := exec.Command("notify-send", "-a", "sakms-node", title, body).Run(); err != nil {
		log.Printf("sakms-node-tray: notify-send: %v", err)
	}
}

// copyToClipboard writes text to the system clipboard using the first
// available tool (wl-copy for Wayland, xclip or xsel for X11).
func copyToClipboard(text string) {
	if text == "" {
		return
	}
	for _, cmd := range [][]string{
		{"wl-copy"},
		{"xclip", "-selection", "clipboard"},
		{"xsel", "--clipboard", "--input"},
	} {
		c := exec.Command(cmd[0], cmd[1:]...)
		c.Stdin = strings.NewReader(text)
		if err := c.Run(); err == nil {
			return
		}
	}
	log.Printf("sakms-node-tray: no clipboard tool found (tried wl-copy, xclip, xsel)")
}
