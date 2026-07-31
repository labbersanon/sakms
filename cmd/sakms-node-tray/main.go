// Command sakms-node-tray is a system-tray companion for sakms-node.
// It polls the daemon's local status server (GET /status on localhost) every
// 3 seconds and reflects the node's lifecycle state as a tray icon:
//
//	amber — pending pairing (displays the 6-char pairing code)
//	green — authenticated and connected to the sakms server
//	red   — daemon not running or disconnected
//
// The tray is read-only. It never talks to the sakms server, holds no
// credentials, and — since the Stage 3 windowed-config split — no longer opens
// the daemon's unix-domain control socket at all: all interactive node
// configuration (media roots, path mappings, dispatch pause) now lives in the
// on-demand sakms-node-config window, launched from the "Open configuration…"
// item. The tray only READS lifecycle state + mediaRoots containment status from
// the loopback TCP status server (GET /status).
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

	"github.com/labbersanon/sakms/internal/nodecontrol"
)

const (
	defaultStatusPort = 7810
	pollInterval      = 3 * time.Second
	httpTimeout       = 2 * time.Second
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
	MediaRootScopes []nodecontrol.MediaRootStatus `json:"mediaRootScopes,omitempty"`
}

func main() {
	statusPort := flag.Int("status-port", defaultStatusPort,
		"port of the sakms-node status server")
	flag.Parse()

	t := &trayUI{
		statusURL: fmt.Sprintf("http://127.0.0.1:%d/status", *statusPort),
	}
	systray.Run(t.run, nil)
}

// trayUI holds the tray's read-only status items and the state shared between
// the poll loop and the copy handler. All mutable fields are guarded by mu.
type trayUI struct {
	statusURL string

	mStatus     *systray.MenuItem
	mCopy       *systray.MenuItem
	mOpenConfig *systray.MenuItem
	mWarning    *systray.MenuItem
	mDrift      *systray.MenuItem
	mQuit       *systray.MenuItem

	mu           sync.Mutex
	lastKey      string
	lastCode     string
	notifiedCode string // which pairing code we already notified about
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
	t.mOpenConfig = systray.AddMenuItem("Open configuration…",
		"Open the sakms-node configuration window (media roots, path mappings, dispatch pause)")

	systray.AddSeparator()
	t.mWarning = systray.AddMenuItem("", "sakms-node warning")
	t.mWarning.Disable()
	t.mWarning.Hide()
	t.mDrift = systray.AddMenuItem("", "OS-level containment status")
	t.mDrift.Disable()
	t.mDrift.Hide()

	systray.AddSeparator()
	t.mQuit = systray.AddMenuItem("Quit tray app", "Close this tray icon (does not stop sakms-node)")

	// The "Open configuration…" click handler runs in its OWN goroutine, never in
	// loop()'s select: handleOpenConfig blocks on cmd.Run() for the config
	// window's whole lifetime (see openconfig.go), so servicing it from the poll
	// loop would freeze status polling until the window closed. Its own goroutine
	// also gives free single-instance-ish behavior for repeated tray clicks.
	go func() {
		for range t.mOpenConfig.ClickedCh {
			t.handleOpenConfig()
		}
	}()

	t.poll()
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
	var scopes []nodecontrol.MediaRootStatus
	if err == nil {
		state = s.State
		code = s.PairingCode
		warning = s.Warning
		scopes = s.MediaRootScopes
	}

	// Re-assert the icon/tooltip for the current state on every poll tick,
	// unconditionally — not just on a state change. fyne.io/systray's
	// SetIcon/SetTooltip silently no-op if called before nativeStart() has
	// finished connecting to DBus (instance.props still nil), which can
	// happen when this callback's very first call races systray's own DBus
	// setup during a slow/congested boot. Re-issuing these calls every 3s is
	// idempotent and cheap, so the display self-heals within one tick once
	// instance.props becomes non-nil, regardless of who won that race.
	switch state {
	case "pending":
		systray.SetIcon(iconAmber())
		if code != "" {
			systray.SetTooltip("Pairing code: " + code + " — approve in Settings → Nodes")
		} else {
			systray.SetTooltip("sakms-node — waiting to pair")
		}

	case "connected":
		systray.SetIcon(iconGreen())
		systray.SetTooltip("sakms-node — connected")

	default: // "disconnected" or daemon unreachable
		systray.SetIcon(iconRed())
		if err != nil {
			systray.SetTooltip("sakms-node — not running")
		} else {
			systray.SetTooltip("sakms-node — disconnected")
		}
	}

	key := statusKey(state, code, warning, scopes)
	if key == t.lastKey {
		return
	}
	t.lastKey = key
	t.lastCode = code

	switch state {
	case "pending":
		if code != "" {
			t.mStatus.SetTitle("Pairing code: " + code)
			t.mCopy.Show()
			if t.notifiedCode != code {
				t.notifiedCode = code
				notify("sakms-node pairing",
					"Code: "+code+" — approve in Settings → Nodes")
			}
		} else {
			t.mStatus.SetTitle("Waiting to pair…")
			t.mCopy.Hide()
		}

	case "connected":
		t.mStatus.SetTitle("Connected")
		t.mCopy.Hide()
		t.notifiedCode = ""

	default: // "disconnected" or daemon unreachable
		if err != nil {
			t.mStatus.SetTitle("Daemon not running")
		} else {
			t.mStatus.SetTitle("Disconnected")
		}
		t.mCopy.Hide()
		t.notifiedCode = ""
	}

	t.renderDrift(scopes)

	if warning != "" {
		t.mWarning.SetTitle("⚠ " + warning)
		t.mWarning.Show()
	} else {
		t.mWarning.Hide()
	}
}

// renderDrift shows the aggregate OS-level containment-drift warning. The per-
// root display rows (and their interactive Remove actions) were retired in
// Stage 3 — media-root management now lives in the config window — but this
// aggregate drift signal is security-relevant (it means the app-level allowlist
// has diverged from the applied namespace sandbox) and stays in the tray as
// read-only status (plan U6). Caller must hold t.mu.
func (t *trayUI) renderDrift(scopes []nodecontrol.MediaRootStatus) {
	if nodecontrol.ContainmentDrift(scopes) {
		t.mDrift.SetTitle("⚠ OS-level containment out of sync — a root operator must re-run apply-mediaroots.sh and restart the daemon")
		t.mDrift.Show()
	} else {
		t.mDrift.Hide()
	}
}

// statusKey is the change-detection fingerprint: the poll loop only re-renders
// the UI when state, pairing code, warning, or the roots/scopes list changes.
func statusKey(state, code, warning string, scopes []nodecontrol.MediaRootStatus) string {
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
