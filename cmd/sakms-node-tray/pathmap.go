package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"fyne.io/systray"
)

// Stage 3 of the node-path-config-ui plan: the tray's path-mapping UI. It mirrors
// mediaroots.go's shipped interaction grain exactly — a native folder-pick ladder
// (pickDirectory), a unix-socket control client, per-row Remove sub-items, and
// notify() surfacing — rather than a from-scratch design. The one deliberate
// deviation from the plan's "clicking a key opens the picker" wording: the key row
// is a DISABLED display item (like a rootSlot's parent) and the picker/clear live
// on "Set folder…"/"Remove mapping" SUB-items, because point 3 forces a submenu on
// the parent and an enabled parent-with-submenu does not reliably emit ClickedCh on
// Linux DBusMenu (the same reason mediaroots.go disables its root rows).

// remapEntry mirrors the daemon's server→local Remap pair from GET /pathmap. The
// tray does not render it (it displays authored keys), but decoding it keeps the
// view a faithful mirror of the daemon response.
type remapEntry struct {
	Server string `json:"server"`
	Local  string `json:"local"`
}

// authoredMapping mirrors the daemon's AuthoredPathMapping: one library-path-key →
// node-local path the operator authored on this node.
type authoredMapping struct {
	Key      string `json:"key"`
	NodePath string `json:"nodePath"`
}

// pathMapView decodes the daemon's pathmapState (GET /pathmap and the set/clear
// echoes). Error carries the daemon's 400 body ("error"); set/clear echoes omit
// LibraryPathKeys (the tray reads the catalog from GET /pathmap).
type pathMapView struct {
	AuthoredPaths   []authoredMapping `json:"authoredPaths"`
	PathMap         []remapEntry      `json:"pathMap"`
	LibraryPathKeys []string          `json:"libraryPathKeys"`
	LastPushError   string            `json:"lastPushError"`
	Error           string            `json:"error"`
}

// doPathMap issues one /pathmap control-socket request and decodes the pathmapState
// reply. It mirrors controlClient.do (mediaroots.go) but for the path-mapping
// response shape; dial failures propagate unwrapped so classifyDialError can bucket
// them (EACCES relogin / ENOENT daemon-down).
func (c *controlClient) doPathMap(ctx context.Context, method, path string, body any) (pathMapView, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return pathMapView{}, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://sakms-node"+path, rdr)
	if err != nil {
		return pathMapView{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return pathMapView{}, err
	}
	defer resp.Body.Close()

	var out pathMapView
	if decErr := json.NewDecoder(resp.Body).Decode(&out); decErr != nil && decErr != io.EOF {
		return pathMapView{}, fmt.Errorf("decoding pathmap response: %w", decErr)
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return pathMapView{}, errors.New(out.Error)
		}
		return pathMapView{}, fmt.Errorf("control socket returned %s", resp.Status)
	}
	return out, nil
}

func (c *controlClient) getPathMap(ctx context.Context) (pathMapView, error) {
	return c.doPathMap(ctx, http.MethodGet, "/pathmap", nil)
}

func (c *controlClient) setPathMap(ctx context.Context, key, localPath string) (pathMapView, error) {
	return c.doPathMap(ctx, http.MethodPost, "/pathmap/set", map[string]string{"key": key, "localPath": localPath})
}

func (c *controlClient) clearPathMap(ctx context.Context, key string) (pathMapView, error) {
	return c.doPathMap(ctx, http.MethodPost, "/pathmap/clear", map[string]string{"key": key})
}

// --- pure display logic (unit-tested; no systray / no I/O) -----------------

// keyRow is one library-path-key's render state: the key, its authored node path
// (empty when unset), and whether a mapping exists.
type keyRow struct {
	Key      string
	NodePath string
	Mapped   bool
}

// buildKeyRows pairs each catalog key with its authored node path (if any),
// preserving catalog order. A blank authored NodePath is treated as unset (blank
// means "skip" everywhere in the daemon, never "mapped").
func buildKeyRows(catalog []string, authored []authoredMapping) []keyRow {
	byKey := make(map[string]string, len(authored))
	for _, a := range authored {
		if a.NodePath != "" {
			byKey[a.Key] = a.NodePath
		}
	}
	rows := make([]keyRow, 0, len(catalog))
	for _, k := range catalog {
		p, ok := byKey[k]
		rows = append(rows, keyRow{Key: k, NodePath: p, Mapped: ok})
	}
	return rows
}

// keyRowTitle formats a key row's disabled display line.
func keyRowTitle(kr keyRow) string {
	if kr.Mapped {
		return kr.Key + "  →  " + kr.NodePath
	}
	return kr.Key + "  →  not set"
}

// setItemTitle labels the picker sub-item by whether a mapping already exists.
func setItemTitle(mapped bool) string {
	if mapped {
		return "Change folder…"
	}
	return "Set folder…"
}

// pathMappingGateOpen reports whether path-mapping edits are allowed: the node must
// have at least one configured media root first (Stage 4 ruling, surfaced here as a
// UX gate — the daemon and server enforce the real safety boundary).
func pathMappingGateOpen(mediaRootCount int) bool {
	return mediaRootCount > 0
}

// pathPushWarningLine formats the persistent push-failure status line from the
// daemon's last-push-error state. It shows iff a failure is recorded; the daemon
// clears lastPushError to "" on the next successful echo (pathmap_push.go fire),
// so this line disappears on its own once a push succeeds. Stage 2 records-and-
// surfaces only (no auto-retry backoff), so the copy tells the operator to re-pick
// to force a retry rather than promising an automatic one.
func pathPushWarningLine(lastPushError string) (string, bool) {
	if lastPushError == "" {
		return "", false
	}
	return "⚠ Path mapping: last push failed — " + lastPushError + " (re-pick a folder to retry)", true
}

// --- tray wiring (holds/uses systray items; lock discipline mirrors mediaroots) --

// keySlot is one reusable library-path-key menu row: a display-only parent item
// (key + current node path) with a "Set folder…" picker sub-item and a
// "Remove mapping" clear sub-item. key is what the slot currently represents
// ("" when hidden/unused). Guarded by trayUI.mu, exactly like rootSlot.path.
type keySlot struct {
	item       *systray.MenuItem
	setItem    *systray.MenuItem
	removeItem *systray.MenuItem
	key        string // guarded by trayUI.mu
}

// pollPathMap fetches GET /pathmap OUTSIDE the lock (a slow/stuck socket must never
// freeze the click handlers or the poll loop — the deadlock lesson), then applies
// it under the lock. On error it silently keeps the last-known view: the status
// poll already signals a down/unreachable daemon, and notifying every tick would
// spam (the startup probeControlSocket surfaces the EACCES relogin hint once).
func (t *trayUI) pollPathMap() {
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	view, err := t.control.getPathMap(ctx)

	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		return
	}
	t.pmFetched = true
	t.pmCatalog = view.LibraryPathKeys
	t.pmAuthored = view.AuthoredPaths
	t.pmLastPushError = view.LastPushError
	t.renderPathMap()
}

// renderPathMap updates the path-mapping section from the latest fetched view and
// mediaRootCount. Caller MUST hold t.mu.
func (t *trayUI) renderPathMap() {
	if !t.pmFetched {
		t.mPathHeader.Hide()
		t.mAddRootFirst.Hide()
		t.mPathWarning.Hide()
		for _, ks := range t.keySlots {
			ks.key = ""
			ks.item.Hide()
			ks.setItem.Hide()
			ks.removeItem.Hide()
		}
		return
	}

	t.mPathHeader.Show()
	gateOpen := pathMappingGateOpen(t.mediaRootCount)
	if gateOpen {
		t.mAddRootFirst.Hide()
	} else {
		t.mAddRootFirst.Show()
	}

	rows := buildKeyRows(t.pmCatalog, t.pmAuthored)
	overflow := len(rows) > len(t.keySlots)
	for i, ks := range t.keySlots {
		last := i == len(t.keySlots)-1
		switch {
		case overflow && last:
			// Graceful cap (mirrors renderRoots): the catalog is ~5 keys and the
			// pool is larger, but if it ever overflows, use the last slot as a
			// non-actionable summary instead of silently dropping keys.
			ks.key = ""
			extra := len(rows) - len(t.keySlots) + 1
			ks.item.SetTitle(fmt.Sprintf("…and %d more key(s) (edit config to manage)", extra))
			ks.item.Show()
			ks.setItem.Hide()
			ks.removeItem.Hide()
		case i < len(rows):
			r := rows[i]
			ks.key = r.Key
			ks.item.SetTitle(keyRowTitle(r))
			ks.item.Show()

			// Set/Change is gated on mediaRoots (the safety-relevant write).
			ks.setItem.SetTitle(setItemTitle(r.Mapped))
			ks.setItem.Show()
			if gateOpen {
				ks.setItem.Enable()
			} else {
				ks.setItem.Disable()
			}

			// Remove is shown only when mapped and stays enabled regardless of the
			// gate: the daemon's local clear handler is not mediaRoots-gated, and an
			// operator must always be able to drop a now-stale mapping (D7).
			if r.Mapped {
				ks.removeItem.Show()
				ks.removeItem.Enable()
			} else {
				ks.removeItem.Hide()
			}
		default:
			ks.key = ""
			ks.item.Hide()
			ks.setItem.Hide()
			ks.removeItem.Hide()
		}
	}

	if text, show := pathPushWarningLine(t.pmLastPushError); show {
		t.mPathWarning.SetTitle(text)
		t.mPathWarning.Show()
	} else {
		t.mPathWarning.Hide()
	}
}

// handlePathMapSet pops the native folder picker for a key and POSTs /pathmap/set.
// It reads the key + gate state under the lock, then releases before any blocking
// picker/socket I/O (never hold t.mu across I/O). The post-set notify does NOT
// branch on the echo's LastPushError: the push is debounced (~1.5s), so that field
// still reflects the PREVIOUS push, not the one just scheduled — the polling
// warning line surfaces the real result.
func (t *trayUI) handlePathMapSet(ks *keySlot) {
	t.mu.Lock()
	key := ks.key
	gateOpen := pathMappingGateOpen(t.mediaRootCount)
	t.mu.Unlock()

	if key == "" {
		return
	}
	if !gateOpen {
		notify("sakms-node", "Add a media root first — a path mapping cannot be set until this node has at least one media root.")
		return
	}

	path, picked, err := pickDirectory()
	if err != nil {
		if errors.Is(err, errNoPicker) {
			notify("sakms-node", "No folder picker found — install zenity (GNOME) or kdialog (KDE) to configure a path mapping.")
		} else {
			notify("sakms-node", "Folder picker failed: "+err.Error())
		}
		return
	}
	if !picked {
		return // cancelled / empty selection — no-op
	}

	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	if _, err := t.control.setPathMap(ctx, key, path); err != nil {
		t.reportControlError("set path mapping", err)
		return
	}
	notify("sakms-node", fmt.Sprintf("Path mapping set: %s → %s (pushing to server).", key, path))
	t.pollPathMap()
}

// handlePathMapClear POSTs /pathmap/clear for a key (D7). Same lock discipline: read
// the key under the lock, release before the socket call.
func (t *trayUI) handlePathMapClear(ks *keySlot) {
	t.mu.Lock()
	key := ks.key
	t.mu.Unlock()

	if key == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	if _, err := t.control.clearPathMap(ctx, key); err != nil {
		t.reportControlError("remove path mapping", err)
		return
	}
	notify("sakms-node", fmt.Sprintf("Path mapping removed: %s.", key))
	t.pollPathMap()
}
