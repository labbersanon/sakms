//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/nodes"
)

// pushRecorder is a fake sakms server that records every node-auth settings
// push and replies with a configurable status.
type pushRecorder struct {
	mu     sync.Mutex
	bodies []apidto.NodeSettingsRequest
	ids    []string
	auths  []string
	status int
}

func (p *pushRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/nodes/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		var body apidto.NodeSettingsRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.bodies = append(p.bodies, body)
		p.ids = append(p.ids, r.PathValue("id"))
		p.auths = append(p.auths, r.Header.Get("Authorization"))
		st := p.status
		p.mu.Unlock()
		if st == 0 {
			st = http.StatusNoContent
		}
		w.WriteHeader(st)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (p *pushRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func (p *pushRecorder) last() apidto.NodeSettingsRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bodies[len(p.bodies)-1]
}

// postPathMap posts to a /pathmap control route and returns the status + decoded
// state (or error payload).
func postPathMap(t *testing.T, client *http.Client, url string, body pathmapPayload) (int, pathmapPayload) {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(buf)))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out pathmapPayload
	json.NewDecoder(resp.Body).Decode(&out) //nolint:errcheck
	return resp.StatusCode, out
}

func getPathMap(t *testing.T, client *http.Client) pathmapState {
	t.Helper()
	resp, err := client.Get("http://unix/pathmap")
	if err != nil {
		t.Fatalf("GET /pathmap: %v", err)
	}
	defer resp.Body.Close()
	var out pathmapState
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding GET /pathmap: %v", err)
	}
	return out
}

// validMediaRoots is a non-empty, non-trivial mediaRoots list good enough to
// clear the node-side + server-side PRESENCE gate (mediaRootsUsable is a pure
// lexical depth check). It is used by tests that never author a real on-disk set
// (clears, GET, rejection-before-validation cases). Tests that DO author a set
// must instead use a real mediaRoot that actually contains the local dir, since
// Stage 4's validatePathMapLocal enforces containment (withinMediaRoots) — see
// mediaRootWithSubdir.
var validMediaRoots = []string{"/mnt/media"}

// mediaRootWithSubdir creates a real, non-trivial mediaRoot dir plus a named
// subdirectory inside it, returning both. A nodePath set to the subdir passes
// Stage 4's containment check (it resolves within the returned root); callers
// set cfg.MediaRoots = []string{root}.
func mediaRootWithSubdir(t *testing.T, name string) (root, sub string) {
	t.Helper()
	root = t.TempDir()
	sub = filepath.Join(root, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("creating media subdir: %v", err)
	}
	return root, sub
}

const moviesKey = "movies_library_root_folder"

// TestPathMap_DebounceCoalescesRapidEdits proves N rapid sets to one key produce
// exactly ONE push carrying the final value — not one push per call.
func TestPathMap_DebounceCoalescesRapidEdits(t *testing.T) {
	rec := &pushRecorder{}
	srv := rec.server(t)

	// A real mediaRoot the three sets map inside (Stage 4 containment check).
	mediaRoot := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{ServerURL: srv.URL, APIKey: "k", NodeName: "n", MediaRoots: []string{mediaRoot}}
	pusher := newPathmapPusher(cfg, &nodeSession{}, http.DefaultClient, 80*time.Millisecond)
	pushed := make(chan string, 8)
	pusher.pushHook = func(key string, _ pathmapOp, _ error) { pushed <- key }
	client, _, _ := startTestSocketWith(t, cfg, configPath, pusher, &nodeSession{})

	// Three rapid sets to the same key, each a distinct valid local dir WITHIN
	// the configured mediaRoot.
	var lastDir string
	for i := 0; i < 3; i++ {
		lastDir = filepath.Join(mediaRoot, fmt.Sprintf("movies-%d", i))
		if err := os.MkdirAll(lastDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if status, out := postPathMap(t, client, "http://unix/pathmap/set", pathmapPayload{Key: moviesKey, LocalPath: lastDir}); status != http.StatusOK {
			t.Fatalf("set #%d: status %d (%s)", i, status, out.Error)
		}
	}

	// Exactly one push fires...
	select {
	case <-pushed:
	case <-time.After(3 * time.Second):
		t.Fatal("expected a debounced push to fire")
	}
	// ...and no second one (coalesced).
	select {
	case <-pushed:
		t.Fatal("a second push fired — rapid edits were NOT coalesced")
	case <-time.After(300 * time.Millisecond):
	}

	if n := rec.count(); n != 1 {
		t.Fatalf("expected exactly 1 push recorded, got %d", n)
	}
	body := rec.last()
	// Exactly one key per push (no full-map replace).
	if len(body.PathMap) != 1 {
		t.Fatalf("push must carry exactly one key, got %d entries", len(body.PathMap))
	}
	wantResolved, _ := filepath.EvalSymlinks(lastDir)
	if body.PathMap[0].Key != moviesKey || body.PathMap[0].NodePath != wantResolved {
		t.Fatalf("push carried the wrong/stale entry: %+v (want key=%s path=%s)", body.PathMap[0], moviesKey, wantResolved)
	}
	// D9: the push self-reports mediaRoots.
	if len(body.MediaRoots) != 1 || body.MediaRoots[0] != mediaRoot {
		t.Fatalf("push must self-report mediaRoots, got %v", body.MediaRoots)
	}
}

// TestPathMap_ServerPushDoesNotTriggerOutboundPush is the explicit D5 test: a
// server SSE settings push is APPLIED locally without ever scheduling an
// outbound push (no echo loop).
func TestPathMap_ServerPushDoesNotTriggerOutboundPush(t *testing.T) {
	rec := &pushRecorder{}
	srv := rec.server(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{ServerURL: srv.URL, APIKey: "k", NodeName: "n", MediaRoots: validMediaRoots}
	pusher := newPathmapPusher(cfg, &nodeSession{}, http.DefaultClient, 20*time.Millisecond)
	fired := make(chan string, 4)
	pusher.pushHook = func(key string, _ pathmapOp, _ error) { fired <- key }
	statusSrv := newStatusServer(cfg)

	// Apply an authoritative server push (the SSE settings-apply path). Local
	// path must be within mediaRoots or it's rejected by validateSettingsPush,
	// so map into /mnt/media.
	applyServerSettings(cfg, configPath, statusSrv, nil, nodes.NodeSettings{
		PathMap: []nodes.PathMapping{{Server: "/srv/movies", Local: "/mnt/media/movies"}},
		MaxJobs: 3,
	})

	// The apply landed in the Remap table.
	pm, _ := cfg.snapshot()
	if got := Remap(pm, "/srv/movies/a.mkv"); got != "/mnt/media/movies/a.mkv" {
		t.Fatalf("server push was not applied: Remap = %q", got)
	}

	// And it scheduled NO outbound push (the pusher is untouched).
	pusher.mu.Lock()
	pending := len(pusher.pending)
	pusher.mu.Unlock()
	if pending != 0 {
		t.Fatalf("server push scheduled %d outbound pushes — echo loop (D5 violated)", pending)
	}
	select {
	case k := <-fired:
		t.Fatalf("server push triggered an outbound push for %q — echo loop (D5 violated)", k)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestPathMap_FailedPushPreservesLastKnownGood proves a failed push leaves the
// pre-existing, actively-resolving mapping (NodeConfig.PathMap / Remap) exactly
// as it was — the literal Stage 2 criterion and PM-3's mid-edit-clobber
// defense — and that the failure is surfaced via GET /pathmap.
func TestPathMap_FailedPushPreservesLastKnownGood(t *testing.T) {
	rec := &pushRecorder{status: http.StatusInternalServerError}
	srv := rec.server(t)

	// Steady state: a previously-verified mapping the node is actively remapping
	// with (in PathMap), plus its authored record. Both the good and the new
	// (bad-push) dirs live inside a real mediaRoot so they clear Stage 4's
	// containment check; the push failure is the SERVER's 500, not local
	// validation.
	mediaRoot := t.TempDir()
	goodDir := filepath.Join(mediaRoot, "good")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatalf("mkdir good: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{
		ServerURL:     srv.URL,
		APIKey:        "k",
		NodeName:      "n",
		MediaRoots:    []string{mediaRoot},
		AuthoredPaths: []AuthoredPathMapping{{Key: moviesKey, NodePath: goodDir}},
		PathMap:       []PathMapEntry{{Server: "/srv/movies", Local: goodDir}},
	}
	pusher := newPathmapPusher(cfg, &nodeSession{}, http.DefaultClient, 20*time.Millisecond)
	done := make(chan error, 4)
	pusher.pushHook = func(_ string, _ pathmapOp, err error) { done <- err }
	client, _, _ := startTestSocketWith(t, cfg, configPath, pusher, &nodeSession{})

	// Precondition: the good mapping resolves.
	pm, _ := cfg.snapshot()
	wantResolve := goodDir + "/a.mkv"
	if got := Remap(pm, "/srv/movies/a.mkv"); got != wantResolve {
		t.Fatalf("precondition: Remap = %q, want %q", got, wantResolve)
	}

	// Author a NEW path for the same key; the push will fail (server 500).
	badDir := filepath.Join(mediaRoot, "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}
	if status, out := postPathMap(t, client, "http://unix/pathmap/set", pathmapPayload{Key: moviesKey, LocalPath: badDir}); status != http.StatusOK {
		t.Fatalf("set: status %d (%s)", status, out.Error)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the push to fail (server returns 500)")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("push never fired")
	}

	// The authoritative Remap table is untouched — the good mapping still
	// resolves (a failed proposal never rewrote it; only the SSE echo could).
	pm, _ = cfg.snapshot()
	if got := Remap(pm, "/srv/movies/a.mkv"); got != wantResolve {
		t.Fatalf("failed push clobbered the last-known-good mapping: Remap = %q, want %q", got, wantResolve)
	}
	// And the failure is surfaced for Stage 3 to consume.
	if state := getPathMap(t, client); state.LastPushError == "" {
		t.Fatal("expected GET /pathmap to surface the last push failure, got empty")
	}
}

// TestPathMap_ClearRemovesRemapResolution is the critical D7 node-local test:
// after a clear, the node's own Remap no longer resolves the cleared key.
func TestPathMap_ClearRemovesRemapResolution(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	// Steady state as if the server had already echoed a verified mapping: the
	// authored record plus the server-authoritative Remap entry for one key.
	cfg := &NodeConfig{
		ServerURL:     "http://unused.invalid",
		APIKey:        "k",
		NodeName:      "n",
		MediaRoots:    validMediaRoots,
		AuthoredPaths: []AuthoredPathMapping{{Key: moviesKey, NodePath: "/mnt/media/movies"}},
		PathMap:       []PathMapEntry{{Server: "/srv/movies", Local: "/mnt/media/movies"}},
	}
	// Long debounce so the scheduled clear push never fires mid-test.
	pusher := newPathmapPusher(cfg, &nodeSession{}, http.DefaultClient, time.Hour)
	client, _, _ := startTestSocketWith(t, cfg, configPath, pusher, &nodeSession{})

	// Precondition: Remap resolves the movies key.
	pm, _ := cfg.snapshot()
	if got := Remap(pm, "/srv/movies/a.mkv"); got != "/mnt/media/movies/a.mkv" {
		t.Fatalf("precondition failed: Remap = %q", got)
	}

	if status, out := postPathMap(t, client, "http://unix/pathmap/clear", pathmapPayload{Key: moviesKey}); status != http.StatusOK {
		t.Fatalf("clear: status %d (%s)", status, out.Error)
	}

	// Remap no longer resolves the cleared key (returns the input unchanged).
	pm, _ = cfg.snapshot()
	if got := Remap(pm, "/srv/movies/a.mkv"); got != "/srv/movies/a.mkv" {
		t.Fatalf("after clear, Remap still resolves the cleared key: %q", got)
	}
	if authored := cfg.authoredSnapshot(); len(authored) != 0 {
		t.Fatalf("after clear, authored map should be empty, got %+v", authored)
	}

	// Persisted to disk too.
	pathMap, authored := readPersistedPathState(t, configPath)
	if len(pathMap) != 0 || len(authored) != 0 {
		t.Fatalf("clear not persisted: pathMap=%v authored=%v", pathMap, authored)
	}
}

// TestPathMap_ClearedKeyDoesNotReappearAfterReconnectMerge proves clear is not
// reintroduced by mergePathMap on a simulated reconnect: the server, having
// deleted its row, omits the key from the reconnect echo, and add/replace-only
// merge leaves it gone.
func TestPathMap_ClearedKeyDoesNotReappearAfterReconnectMerge(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{
		ServerURL:     "http://unused.invalid",
		APIKey:        "k",
		NodeName:      "n",
		MediaRoots:    validMediaRoots,
		AuthoredPaths: []AuthoredPathMapping{{Key: moviesKey, NodePath: "/mnt/media/movies"}},
		PathMap:       []PathMapEntry{{Server: "/srv/movies", Local: "/mnt/media/movies"}},
	}
	pusher := newPathmapPusher(cfg, &nodeSession{}, http.DefaultClient, time.Hour)
	client, _, _ := startTestSocketWith(t, cfg, configPath, pusher, &nodeSession{})

	if status, out := postPathMap(t, client, "http://unix/pathmap/clear", pathmapPayload{Key: moviesKey}); status != http.StatusOK {
		t.Fatalf("clear: status %d (%s)", status, out.Error)
	}

	// Simulate a reconnect settings echo. The server deleted the movies row, so
	// the echo does NOT carry it (it carries only other, still-configured keys).
	statusSrv := newStatusServer(cfg)
	applyServerSettings(cfg, configPath, statusSrv, nil, nodes.NodeSettings{
		PathMap: []nodes.PathMapping{{Server: "/srv/series", Local: "/mnt/media/series"}},
		MaxJobs: 2,
	})

	pm, _ := cfg.snapshot()
	if got := Remap(pm, "/srv/movies/a.mkv"); got != "/srv/movies/a.mkv" {
		t.Fatalf("cleared key reappeared after reconnect merge: Remap = %q", got)
	}
	// The unrelated, still-configured key merged in normally.
	if got := Remap(pm, "/srv/series/b.mkv"); got != "/mnt/media/series/b.mkv" {
		t.Fatalf("reconnect merge dropped a still-configured key: Remap = %q", got)
	}
}

// TestPathMap_SetRejectedWithoutUsableMediaRoots proves the node fails fast
// (no push, no persistence) when it has no real mediaRoot — the local half of
// the D9 gate.
func TestPathMap_SetRejectedWithoutUsableMediaRoots(t *testing.T) {
	rec := &pushRecorder{}
	srv := rec.server(t)
	configPath := filepath.Join(t.TempDir(), "config.json")

	cases := map[string][]string{
		"empty":   nil,
		"root":    {"/"},
		"shallow": {"/mnt"},
	}
	for name, roots := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &NodeConfig{ServerURL: srv.URL, APIKey: "k", NodeName: "n", MediaRoots: roots}
			pusher := newPathmapPusher(cfg, &nodeSession{}, http.DefaultClient, 20*time.Millisecond)
			fired := make(chan struct{}, 2)
			pusher.pushHook = func(_ string, _ pathmapOp, _ error) { fired <- struct{}{} }
			client, _, _ := startTestSocketWith(t, cfg, configPath, pusher, &nodeSession{})

			status, out := postPathMap(t, client, "http://unix/pathmap/set", pathmapPayload{Key: moviesKey, LocalPath: t.TempDir()})
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s mediaRoots, got %d (%s)", name, status, out.Error)
			}
			if out.Error == "" {
				t.Fatal("expected a non-empty error message")
			}
			// Nothing persisted, no push scheduled.
			if authored := cfg.authoredSnapshot(); len(authored) != 0 {
				t.Fatalf("rejected set must not persist, got %+v", authored)
			}
			select {
			case <-fired:
				t.Fatal("rejected set must not schedule a push")
			case <-time.After(120 * time.Millisecond):
			}
		})
	}
}

// TestPathMap_SetOutsideMediaRootsRejectedNoPush is the Stage 4 socket-level
// proof of the task's emphasized property: a set whose local path resolves
// OUTSIDE a present, valid mediaRoot is rejected at the socket (400) and
// schedules NO push — the node's local containment rejection happens BEFORE any
// push is attempted, not just server-side. Distinct from
// TestPathMap_SetRejectedWithoutUsableMediaRoots, where the mediaRoots list
// itself is the problem; here mediaRoots is valid and the nodePath is the fault.
func TestPathMap_SetOutsideMediaRootsRejectedNoPush(t *testing.T) {
	rec := &pushRecorder{}
	srv := rec.server(t)
	configPath := filepath.Join(t.TempDir(), "config.json")

	mediaRoot := t.TempDir()
	outside := t.TempDir() // a real, non-trivial dir, but NOT under mediaRoot

	cfg := &NodeConfig{ServerURL: srv.URL, APIKey: "k", NodeName: "n", MediaRoots: []string{mediaRoot}}
	pusher := newPathmapPusher(cfg, &nodeSession{}, http.DefaultClient, 20*time.Millisecond)
	fired := make(chan struct{}, 2)
	pusher.pushHook = func(_ string, _ pathmapOp, _ error) { fired <- struct{}{} }
	client, _, _ := startTestSocketWith(t, cfg, configPath, pusher, &nodeSession{})

	status, out := postPathMap(t, client, "http://unix/pathmap/set", pathmapPayload{Key: moviesKey, LocalPath: outside})
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for a nodePath outside every mediaRoot, got %d (%s)", status, out.Error)
	}
	if out.Error == "" {
		t.Fatal("expected a non-empty error message")
	}
	// Nothing persisted, and — the load-bearing assertion — no push scheduled.
	if authored := cfg.authoredSnapshot(); len(authored) != 0 {
		t.Fatalf("a rejected out-of-root set must not persist, got %+v", authored)
	}
	select {
	case <-fired:
		t.Fatal("a set rejected for being outside mediaRoots must NOT schedule a push")
	case <-time.After(120 * time.Millisecond):
	}
	if n := rec.count(); n != 0 {
		t.Fatalf("no push must reach the server for an out-of-root set, got %d", n)
	}
}

// TestPathMap_Server422IsNotSwallowed proves a server mediaRoots-related 422 is
// surfaced (recorded as lastPushError), not silently swallowed, and local state
// is preserved.
func TestPathMap_Server422IsNotSwallowed(t *testing.T) {
	rec := &pushRecorder{status: http.StatusUnprocessableEntity}
	srv := rec.server(t)

	mediaRoot, dir := mediaRootWithSubdir(t, "movies")
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{ServerURL: srv.URL, APIKey: "k", NodeName: "n", MediaRoots: []string{mediaRoot}}
	pusher := newPathmapPusher(cfg, &nodeSession{}, http.DefaultClient, 20*time.Millisecond)
	done := make(chan error, 4)
	pusher.pushHook = func(_ string, _ pathmapOp, err error) { done <- err }
	client, _, _ := startTestSocketWith(t, cfg, configPath, pusher, &nodeSession{})

	if status, out := postPathMap(t, client, "http://unix/pathmap/set", pathmapPayload{Key: moviesKey, LocalPath: dir}); status != http.StatusOK {
		t.Fatalf("set: status %d (%s)", status, out.Error)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("daemon swallowed the server 422 — push reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("push never fired")
	}

	if rec.count() != 1 {
		t.Fatalf("expected the push to actually reach the server, got %d", rec.count())
	}
	if state := getPathMap(t, client); state.LastPushError == "" {
		t.Fatal("server 422 was not surfaced via lastPushError")
	}
	// Local authored state preserved despite the rejection.
	if authored := cfg.authoredSnapshot(); len(authored) != 1 {
		t.Fatalf("a rejected push must preserve local state, got %+v", authored)
	}
}

// TestPathMap_GetExposesStateAndCatalog proves GET /pathmap returns the authored
// mappings and the library-path-key catalog (from the session's ConnectAck).
func TestPathMap_GetExposesStateAndCatalog(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{
		ServerURL:     "http://unused.invalid",
		APIKey:        "k",
		NodeName:      "n",
		MediaRoots:    validMediaRoots,
		AuthoredPaths: []AuthoredPathMapping{{Key: moviesKey, NodePath: "/mnt/media/movies"}},
	}
	sess := &nodeSession{}
	sess.setAck("node-abc", []string{moviesKey, "series_library_root_folder"})
	pusher := newPathmapPusher(cfg, sess, http.DefaultClient, time.Hour)
	client, _, _ := startTestSocketWith(t, cfg, configPath, pusher, sess)

	state := getPathMap(t, client)
	if len(state.AuthoredPaths) != 1 || state.AuthoredPaths[0].Key != moviesKey {
		t.Fatalf("GET /pathmap missing authored mappings: %+v", state.AuthoredPaths)
	}
	if len(state.LibraryPathKeys) != 2 || state.LibraryPathKeys[0] != moviesKey {
		t.Fatalf("GET /pathmap missing catalog: %+v", state.LibraryPathKeys)
	}
}

// readPersistedPathState reads back the on-disk config.json's Remap table and
// authored mappings. Returns slices (not the whole NodeConfig, which carries a
// mutex that go vet forbids copying by value).
func readPersistedPathState(t *testing.T, configPath string) ([]PathMapEntry, []AuthoredPathMapping) {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config.json: %v", err)
	}
	var persisted NodeConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshalling config.json: %v", err)
	}
	return persisted.PathMap, persisted.AuthoredPaths
}
