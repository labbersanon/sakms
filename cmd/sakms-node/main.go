// Command sakms-node is a worker node binary that offloads phash and
// videophash computation to a machine with a better GPU. It connects to a
// running sakms server over SSE, receives Job frames, remaps paths using the
// local path map, computes the requested hash, and POSTs the result back.
// CGo-free: builds with CGO_ENABLED=0 for linux, windows, and darwin.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/labbersanon/sakms/internal/nodes"
	"github.com/labbersanon/sakms/internal/phash"
	"github.com/labbersanon/sakms/internal/videophash"
)

// errUnauthorized is returned by connect when the server responds 401 so the
// caller can distinguish an auth failure (needs re-pairing) from a transient
// network error (needs backoff and reconnect).
var errUnauthorized = errors.New("unauthorized (401)")

// hasher is the interface satisfied by both *phash.Hasher and
// *videophash.Hasher.
type hasher interface {
	Hash(ctx context.Context, path string) (string, error)
}

func main() {
	configPath := flag.String("config", "sakms-node.json", "path to JSON config file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("sakms-node: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Probe hardware acceleration once; both hashers use the same probe.
	hw := phash.ProbeHWAccel(ctx)
	log.Printf("sakms-node: hwaccel detected: %q", hw)

	phashHasher := phash.New()
	videoHasher := videophash.New()

	postClient := &http.Client{Timeout: 30 * time.Second}

	statusSrv := newStatusServer(cfg)
	go statusSrv.ListenAndServe(ctx)

	// Shared per-connection facts (nodeID + library-path-key catalog) the SSE
	// ack populates and both the control socket and the debounced pusher read.
	sess := &nodeSession{}
	// Debounced/coalesced path-mapping pusher (Stage 2). Created once here so it
	// outlives reconnects, exactly like the control socket; used only by the
	// control-socket edit path (the SSE settings handler never schedules a push
	// — D5). No-op until the control socket schedules something.
	pusher := newPathmapPusher(cfg, sess, postClient, pathmapPushDebounce)

	// Local control socket (sakms-node-tray UI): mediaRoots (Stage 1) + node
	// path mappings (Stage 2). Started ONCE here, governed by the top-level
	// process context — NOT inside run()/connect(), which re-enter on every
	// reconnect/re-pair cycle. Because this outlives individual run() cycles,
	// both APIKey write paths (401 handler + pair()) go through cfg.mutateAndSave
	// so this listener's config save can never race them. No-op on non-Linux
	// builds (see control_socket_stub.go).
	go startControlSocket(ctx, cfg, *configPath, pusher, sess)

	// CPU governor (node-resource-governor plan, Stage 3, Option C): set up the
	// delegated leaf cgroup, probe the STATIC enforcement capability, start the
	// async applier worker, and re-apply the persisted cap — all BEFORE the
	// dispatch loop pulls its first job. The startup re-apply is INDEPENDENT of
	// any server re-push (Critic MAJOR-2): a node that restarts while the server
	// is unreachable re-establishes real enforcement from local config.json
	// rather than running uncapped behind a UI that still claims "enforced".
	capState := &capState{enforcement: enforcementUnavailable}
	statusSrv.attachCapState(capState)
	applyCap := setupCPUGovernor(capState)
	capApplier := newCapApplier(applyCap, capState)
	go capApplier.run(ctx)
	// Synchronous, before the state machine below ever accepts a job frame.
	reapplyPersistedCap(cfg.CPUCapPercent, applyCap, capState)

	// Outer state machine: pairing ↔ authenticated.
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		// Pairing mode: connect unauthenticated and wait for operator approval.
		if cfg.APIKey == "" {
			statusSrv.update(statePending, "", "")
			if err := pair(ctx, cfg, *configPath, statusSrv); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("sakms-node: pairing failed: %v — retrying in %s", err, backoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
			backoff = time.Second
		}

		// Authenticated mode: reconnect loop.
		statusSrv.update(stateConnected, "", "")
		err := run(ctx, cfg, *configPath, hw, phashHasher, videoHasher, postClient, statusSrv, sess, capApplier, capState)
		if ctx.Err() != nil {
			return
		}

		if errors.Is(err, errUnauthorized) {
			log.Printf("sakms-node: 401 — clearing API key, entering pairing mode")
			if saveErr := cfg.clearAPIKey(*configPath); saveErr != nil {
				log.Printf("sakms-node: saving config after 401: %v", saveErr)
			}
			statusSrv.update(stateDisconnected, "", "")
			backoff = time.Second
			continue
		}

		if err != nil {
			log.Printf("sakms-node: disconnected: %v — reconnecting in %s", err, backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			log.Printf("sakms-node: stream ended — reconnecting")
			backoff = time.Second
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// run is the authenticated reconnect loop. It calls connect repeatedly until
// ctx is cancelled, a 401 is received (errUnauthorized), or ctx is cancelled.
// Network errors trigger backoff and retry; errUnauthorized propagates immediately.
func run(
	ctx context.Context,
	cfg *NodeConfig,
	configPath string,
	hw string,
	phashHasher, videoHasher hasher,
	postClient *http.Client,
	statusSrv *statusServer,
	sess *nodeSession,
	capApplier *capApplier,
	capState *capState,
) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return nil
		}

		err := connect(ctx, cfg, configPath, hw, phashHasher, videoHasher, postClient, statusSrv, sess, capApplier, capState, &wg)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errUnauthorized) {
			return errUnauthorized
		}
		if err != nil {
			log.Printf("sakms-node: stream error: %v — reconnecting in %s", err, backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			log.Printf("sakms-node: stream ended — reconnecting")
			backoff = time.Second
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
	}
}

// connect opens one authenticated SSE stream session: dials, reads ConnectAck,
// starts the heartbeat goroutine, and runs the job/settings dispatch loop.
func connect(
	ctx context.Context,
	cfg *NodeConfig,
	configPath string,
	hw string,
	phashHasher, videoHasher hasher,
	postClient *http.Client,
	statusSrv *statusServer,
	sess *nodeSession,
	capApplier *capApplier,
	capState *capState,
	wg *sync.WaitGroup,
) error {
	streamURL, err := url.Parse(cfg.ServerURL + "/api/nodes/stream")
	if err != nil {
		return fmt.Errorf("parsing server URL: %w", err)
	}
	q := url.Values{}
	q.Set("name", cfg.NodeName)
	if hw != "" {
		q.Set("capabilities", hw)
	}
	streamURL.RawQuery = q.Encode()

	streamClient := &http.Client{Timeout: 0} // long-lived SSE; no client timeout

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL.String(), nil)
	if err != nil {
		return fmt.Errorf("building stream request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream returned status %d", resp.StatusCode)
	}

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	ack, jobCh, settingsCh, browseCh, err := readSSE(connCtx, resp)
	if err != nil {
		return err
	}
	nodeID := ack.NodeID

	// Record the durable id + library-path-key catalog (D4) for the control
	// socket's GET /pathmap and the pusher's PUT URL id.
	sess.setAck(nodeID, ack.LibraryPathKeys)

	log.Printf("sakms-node: connected as %s (id=%s)", cfg.NodeName, nodeID)
	statusSrv.update(stateConnected, "", nodeID)

	go heartbeat(connCtx, nodeID, cfg, postClient, capState)

	for {
		select {
		case <-connCtx.Done():
			return nil
		case s, ok := <-settingsCh:
			if !ok {
				return nil
			}
			applyServerSettings(cfg, configPath, statusSrv, capApplier, s)
		case br, ok := <-browseCh:
			if !ok {
				return nil
			}
			wg.Add(1)
			go func(req nodes.BrowseRequest) {
				defer wg.Done()
				result := executeBrowse(cfg, req)
				postBrowseResult(postClient, cfg, result)
			}(br)
		case job, ok := <-jobCh:
			if !ok {
				return nil
			}
			wg.Add(1)
			go func(j nodes.Job) {
				defer wg.Done()
				result := executeJob(context.Background(), cfg, j, phashHasher, videoHasher)
				postResult(postClient, cfg, result)
			}(job)
		}
	}
}

// applyServerSettings applies one server-pushed authoritative settings frame to
// local config: it validates the pushed pathMap against the node's mediaRoots
// (grace period when unset), then merges it into the Remap table (mergePathMap,
// add/replace-only) and updates MaxJobs, all under the single config lock.
//
// D5 (break the push-echo loop): this is the ONLY caller of the settings-apply
// path, and it deliberately takes NO pathmapPusher — a server push is applied,
// never re-pushed. Only the control-socket edit path schedules an outbound push,
// so a server echo can never ping-pong back into another push.
func applyServerSettings(cfg *NodeConfig, configPath string, statusSrv *statusServer, capApplier *capApplier, s nodes.NodeSettings) {
	// P8: apply the display-only pause echo in its OWN small write, BEFORE the
	// pathMap-validation early-return below. A pause echo bundled in a frame
	// whose pathMap fails node-side validation must still update the node's
	// cached pause display — pause accuracy must not hinge on pathMap validity.
	// This write touches ONLY DispatchPaused; the later pathMap/MaxJobs write
	// never carries pause, so neither can clobber the other (Principle 3 / P2).
	// Like the pathMap apply, this schedules NO outbound push (P5/D5): a server
	// echo is applied, never re-pushed — only the control-socket toggle pushes.
	if saveErr := cfg.mutateAndSave(configPath, func() {
		cfg.DispatchPaused = s.PauseDispatch
	}); saveErr != nil {
		log.Printf("sakms-node: saving pause display state: %v", saveErr)
	}

	// CPU cap (node-resource-governor plan, Stage 3): persisted + applied in its
	// OWN write ABOVE the pathMap-validation early-return, for the same reason
	// pause is hoisted (P8) — enforcement honesty (Principle 1) must not hinge on
	// pathMap validity. A frame whose pathMap fails validation must still update
	// and re-apply the operator's cap. This write touches ONLY CPUCapPercent so
	// it can never clobber (or be clobbered by) the pause or pathMap/MaxJobs
	// writes. The persist is quick and stays inline; the actual cgroup write is
	// handed to the async applier so this dispatch-loop path never blocks on a
	// slow/hung cpu.max write (the last-apply result is recorded by the applier
	// for GET /status). Like pause, a server echo is applied, never re-pushed.
	if saveErr := cfg.mutateAndSave(configPath, func() {
		cfg.CPUCapPercent = s.CPUCapPercent
	}); saveErr != nil {
		log.Printf("sakms-node: saving cpu cap: %v", saveErr)
	}
	if capApplier != nil {
		capApplier.enqueue(s.CPUCapPercent)
	}

	_, mediaRoots := cfg.snapshot()
	if len(mediaRoots) == 0 {
		warning := "mediaRoots is not configured -- settings pushes are applied unrestricted (grace period). Set mediaRoots in this node's config to enable containment."
		log.Printf("sakms-node: WARNING %s", warning)
		statusSrv.setWarning(warning)
	} else if reason := validateSettingsPush(mediaRoots, s.PathMap); reason != "" {
		warning := fmt.Sprintf("rejected settings push (server attempted to map outside this node's declared media roots): %s", reason)
		log.Printf("sakms-node: %s", warning)
		statusSrv.setWarning(warning)
		return
	} else {
		statusSrv.setWarning("")
	}
	mergedTotal := 0
	if saveErr := cfg.mutateAndSave(configPath, func() {
		cfg.PathMap = mergePathMap(cfg.PathMap, s.PathMap)
		cfg.MaxJobs = s.MaxJobs
		mergedTotal = len(cfg.PathMap)
	}); saveErr != nil {
		log.Printf("sakms-node: saving updated settings: %v", saveErr)
	}
	log.Printf("sakms-node: settings updated (maxJobs=%d, paths=%d, merged total=%d)", s.MaxJobs, len(s.PathMap), mergedTotal)
}

// sseFrame holds the parsed fields of one SSE event.
type sseFrame struct {
	event string
	data  string
}

// readSSE reads from resp until it finds the "ack" frame, parses the
// ConnectAck from it (durable nodeID + library-path-key catalog, D4), then
// returns that ack plus channels for subsequent Job, NodeSettings, and
// BrowseRequest frames. All three channels are closed when the stream ends or
// ctx is cancelled.
func readSSE(ctx context.Context, resp *http.Response) (ack nodes.ConnectAck, jobs <-chan nodes.Job, settings <-chan nodes.NodeSettings, browse <-chan nodes.BrowseRequest, err error) {
	scanner := bufio.NewScanner(resp.Body)
	jobCh := make(chan nodes.Job, 16)
	settingsCh := make(chan nodes.NodeSettings, 4)
	browseCh := make(chan nodes.BrowseRequest, 4)

	// Read frames until we get the ack.
	var cur sseFrame
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if cur.event == "ack" && cur.data != "" {
				var parsed nodes.ConnectAck
				if e := json.Unmarshal([]byte(cur.data), &parsed); e == nil && parsed.NodeID != "" {
					ack = parsed
					break
				}
			}
			cur = sseFrame{}
			continue
		}
		if after, ok := strings.CutPrefix(line, "event:"); ok {
			cur.event = strings.TrimSpace(after)
		} else if after, ok := strings.CutPrefix(line, "data:"); ok {
			cur.data = strings.TrimSpace(after)
		}
	}

	if ack.NodeID == "" {
		close(jobCh)
		close(settingsCh)
		close(browseCh)
		return nodes.ConnectAck{}, nil, nil, nil, fmt.Errorf("stream ended before ack")
	}

	go func() {
		defer close(jobCh)
		defer close(settingsCh)
		defer close(browseCh)
		var cur sseFrame
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()
			if line == "" {
				switch cur.event {
				case "", "job": // unnamed frames are jobs
					if cur.data != "" {
						var job nodes.Job
						if e := json.Unmarshal([]byte(cur.data), &job); e == nil && job.ID != "" {
							select {
							case jobCh <- job:
							case <-ctx.Done():
								return
							}
						}
					}
				case nodes.EventSettings:
					if cur.data != "" {
						var s nodes.NodeSettings
						if e := json.Unmarshal([]byte(cur.data), &s); e == nil {
							select {
							case settingsCh <- s:
							default: // non-blocking: latest settings win
							}
						}
					}
				case nodes.EventBrowseRequest:
					if cur.data != "" {
						var br nodes.BrowseRequest
						if e := json.Unmarshal([]byte(cur.data), &br); e == nil && br.ID != "" {
							select {
							case browseCh <- br:
							case <-ctx.Done():
								return
							}
						}
					}
				}
				cur = sseFrame{}
				continue
			}
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				cur.data = strings.TrimSpace(after)
			} else if after, ok := strings.CutPrefix(line, "event:"); ok {
				cur.event = strings.TrimSpace(after)
			}
		}
	}()

	return ack, jobCh, settingsCh, browseCh, nil
}

// heartbeat POSTs to /api/nodes/heartbeat every 30 seconds until ctx is
// cancelled. Each beat also carries the node's live CPU governor status
// (capState.snapshot()) back to the server (Stage 3b).
func heartbeat(ctx context.Context, nodeID string, cfg *NodeConfig, client *http.Client, capState *capState) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := postHeartbeat(ctx, nodeID, cfg, client, capState); err != nil {
				log.Printf("sakms-node: heartbeat error: %v", err)
			}
		}
	}
}

func postHeartbeat(ctx context.Context, nodeID string, cfg *NodeConfig, client *http.Client, capState *capState) error {
	// Report the live governor status alongside the id: enforcement is the STATIC
	// capability, and the last-apply result (effectivePercent + error) is reality,
	// not intent — error is "" when the last apply succeeded. The server decodes
	// these as optional fields, so the wire shape stays backward compatible.
	enforcement, apply := capState.snapshot()
	body, err := json.Marshal(map[string]any{
		"id":               nodeID,
		"enforcement":      enforcement,
		"effectivePercent": apply.EffectivePercent,
		"error":            apply.Error,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.ServerURL+"/api/nodes/heartbeat",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// executeJob remaps the server path, runs the requested hash, and returns a
// JobResult. On any hash error the result carries the error string so the
// server can fall back to local execution.
func executeJob(ctx context.Context, cfg *NodeConfig, job nodes.Job, phashH, videoH hasher) nodes.JobResult {
	pathMap, mediaRoots := cfg.snapshot()
	localPath := Remap(pathMap, job.ServerPath)
	if len(mediaRoots) > 0 {
		if err := withinMediaRoots(mediaRoots, localPath); err != nil {
			return nodes.JobResult{JobID: job.ID, Error: err.Error()}
		}
	}
	var hash string
	var err error
	switch job.Type {
	case nodes.JobTypePhash:
		hash, err = phashH.Hash(ctx, localPath)
	case nodes.JobTypeVideoPhash:
		hash, err = videoH.Hash(ctx, localPath)
	default:
		return nodes.JobResult{JobID: job.ID, Error: fmt.Sprintf("unknown job type: %s", job.Type)}
	}
	if err != nil {
		return nodes.JobResult{JobID: job.ID, Error: err.Error()}
	}
	return nodes.JobResult{JobID: job.ID, Hash: hash}
}

// postResult POSTs a JobResult to the server. Logs on error but does not
// retry — the server's Dispatcher will time out and fall back to local.
func postResult(client *http.Client, cfg *NodeConfig, result nodes.JobResult) {
	body, err := json.Marshal(result)
	if err != nil {
		log.Printf("sakms-node: marshalling result for job %s: %v", result.JobID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.ServerURL+"/api/nodes/jobs/"+result.JobID+"/result",
		bytes.NewReader(body),
	)
	if err != nil {
		log.Printf("sakms-node: building result request for job %s: %v", result.JobID, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("sakms-node: posting result for job %s: %v", result.JobID, err)
		return
	}
	resp.Body.Close()
}

// executeBrowse lists the requested directory on this node's own filesystem
// and returns a BrowseResult. On any read error the result carries the error
// string — mirroring executeJob's Hash/Error convention — so the server can
// surface a clear message to the operator instead of hanging.
func executeBrowse(cfg *NodeConfig, req nodes.BrowseRequest) nodes.BrowseResult {
	_, mediaRoots := cfg.snapshot()
	if len(mediaRoots) > 0 {
		if err := withinMediaRoots(mediaRoots, req.Path); err != nil {
			return nodes.BrowseResult{RequestID: req.ID, Error: err.Error()}
		}
	}
	entries, err := browseDirectory(req.Path, req.IncludeFiles)
	if err != nil {
		return nodes.BrowseResult{RequestID: req.ID, Error: err.Error()}
	}
	return nodes.BrowseResult{RequestID: req.ID, Entries: entries}
}

// postBrowseResult POSTs a BrowseResult to the server. Logs on error but does
// not retry — RequestBrowse's own bounded timeout server-side will surface a
// clear error to the operator instead of waiting indefinitely.
func postBrowseResult(client *http.Client, cfg *NodeConfig, result nodes.BrowseResult) {
	body, err := json.Marshal(result)
	if err != nil {
		log.Printf("sakms-node: marshalling browse result for request %s: %v", result.RequestID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.ServerURL+"/api/nodes/browse/"+result.RequestID+"/result",
		bytes.NewReader(body),
	)
	if err != nil {
		log.Printf("sakms-node: building browse result request for %s: %v", result.RequestID, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("sakms-node: posting browse result for %s: %v", result.RequestID, err)
		return
	}
	resp.Body.Close()
}
