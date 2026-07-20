// Command sakms-node-tray is a system-tray companion for sakms-node.
// It polls the daemon's local status server (GET /status on localhost) every
// 3 seconds and reflects the node's lifecycle state as a tray icon:
//
//	amber — pending pairing (displays the 6-char pairing code)
//	green — authenticated and connected to the sakms server
//	red   — daemon not running or disconnected
//
// The tray app is purely cosmetic: it never talks to the sakms server and
// holds no credentials.  The daemon itself (cmd/sakms-node) is the process
// that actually does work.
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
	"time"

	"fyne.io/systray"
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
}

func main() {
	statusPort := flag.Int("status-port", defaultStatusPort,
		"port of the sakms-node status server")
	flag.Parse()

	statusURL := fmt.Sprintf("http://127.0.0.1:%d/status", *statusPort)
	systray.Run(func() { run(statusURL) }, nil)
}

func run(statusURL string) {
	systray.SetTitle("sakms-node")
	systray.SetTooltip("sakms-node — starting…")
	systray.SetIcon(iconAmber())

	// Menu items.
	mStatus := systray.AddMenuItem("Starting…", "Current node state")
	mStatus.Disable()
	mCopy := systray.AddMenuItem("Copy pairing code", "Copy the 6-char code to the clipboard")
	mCopy.Hide()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit tray app", "Close this tray icon (does not stop sakms-node)")

	var lastState, lastCode string
	var notifiedCode string // track which code we already notified about

	poll := func() {
		s, err := fetchStatus(statusURL)

		state := "disconnected"
		code := ""
		if err == nil {
			state = s.State
			code = s.PairingCode
		}

		if state == lastState && code == lastCode {
			return
		}
		lastState, lastCode = state, code

		switch state {
		case "pending":
			systray.SetIcon(iconAmber())
			if code != "" {
				systray.SetTooltip("Pairing code: " + code + " — approve in Settings → Nodes")
				mStatus.SetTitle("Pairing code: " + code)
				mCopy.Show()
				if notifiedCode != code {
					notifiedCode = code
					notify("sakms-node pairing",
						"Code: "+code+" — approve in Settings → Nodes")
				}
			} else {
				systray.SetTooltip("sakms-node — waiting to pair")
				mStatus.SetTitle("Waiting to pair…")
				mCopy.Hide()
			}

		case "connected":
			systray.SetIcon(iconGreen())
			systray.SetTooltip("sakms-node — connected")
			mStatus.SetTitle("Connected")
			mCopy.Hide()
			notifiedCode = ""

		default: // "disconnected" or daemon unreachable
			systray.SetIcon(iconRed())
			if err != nil {
				systray.SetTooltip("sakms-node — not running")
				mStatus.SetTitle("Daemon not running")
			} else {
				systray.SetTooltip("sakms-node — disconnected")
				mStatus.SetTitle("Disconnected")
			}
			mCopy.Hide()
			notifiedCode = ""
		}
	}

	poll()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			poll()
		case <-mCopy.ClickedCh:
			copyToClipboard(lastCode)
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
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
