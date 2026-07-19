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

	"github.com/curtiswtaylorjr/sakms/internal/nodes"
	"github.com/curtiswtaylorjr/sakms/internal/phash"
	"github.com/curtiswtaylorjr/sakms/internal/videophash"
)

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

	// Probe hardware acceleration and log the result. Both phash and videophash
	// run the identical ffmpeg -hwaccels probe; calling once is sufficient.
	hw := phash.ProbeHWAccel(ctx)
	log.Printf("sakms-node: hwaccel detected: %q", hw)

	phashHasher := phash.New()
	videoHasher := videophash.New()

	// postClient is used for heartbeat and result POSTs — a normal timeout is
	// fine for these short-lived requests.
	postClient := &http.Client{Timeout: 30 * time.Second}

	run(ctx, cfg, hw, phashHasher, videoHasher, postClient)
	log.Printf("sakms-node: shut down")
}

// run is the outer reconnect loop. It connects to the SSE stream, reads the
// ConnectAck to obtain the node's server-assigned id, starts the heartbeat
// goroutine bound to this connection's context, then runs the job loop. On
// disconnect it backs off and reconnects until ctx is cancelled.
func run(
	ctx context.Context,
	cfg *NodeConfig,
	hw string,
	phashHasher, videoHasher hasher,
	postClient *http.Client,
) {
	// wg tracks all in-flight job goroutines across reconnects so we wait for
	// them to finish before returning.
	var wg sync.WaitGroup
	defer wg.Wait()

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		err := connect(ctx, cfg, hw, phashHasher, videoHasher, postClient, &wg)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("sakms-node: disconnected: %v — reconnecting in %s", err, backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			// Clean stream end: reset backoff so the next reconnect is fast.
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

// connect opens one SSE stream session: it dials the server, reads ConnectAck,
// starts the heartbeat goroutine for this connection, and runs the job-read
// loop. Returns when the stream ends or ctx is cancelled.
func connect(
	ctx context.Context,
	cfg *NodeConfig,
	hw string,
	phashHasher, videoHasher hasher,
	postClient *http.Client,
	wg *sync.WaitGroup,
) error {
	// Build the stream URL with URL-encoded query parameters.
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

	// The stream client must have Timeout: 0 — a non-zero client timeout
	// would kill the long-lived SSE connection.
	streamClient := &http.Client{Timeout: 0}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL.String(), nil)
	if err != nil {
		return fmt.Errorf("building stream request: %w", err)
	}
	req.Header.Set("X-Api-Key", cfg.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream returned status %d", resp.StatusCode)
	}

	// connCtx is created before readSSE so the SSE-reading goroutine inside
	// readSSE is bound to this connection's cancellation, not the process-level
	// ctx. Without this, the goroutine would leak until process exit on every
	// reconnect (connCancel fires but the goroutine still holds ctx, which is
	// only cancelled on SIGTERM).
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	// Parse SSE frames from the response body.
	nodeID, jobCh, err := readSSE(connCtx, resp)
	if err != nil {
		return err
	}

	log.Printf("sakms-node: connected as %s (id=%s)", cfg.NodeName, nodeID)

	go heartbeat(connCtx, nodeID, cfg, postClient)

	// Job loop: each job runs in its own goroutine so the read loop stays
	// responsive to disconnect detection.
	for {
		select {
		case <-connCtx.Done():
			return nil
		case job, ok := <-jobCh:
			if !ok {
				return nil
			}
			wg.Add(1)
			go func(j nodes.Job) {
				defer wg.Done()
				// Use context.Background() so in-flight hashes survive both
				// stream reconnects and shutdown. Hash already wraps its own
				// internal 2-min timeout, so there is no hang risk. A late
				// result posted after the server fell back is a safe no-op
				// (pending-channel invariant in the registry).
				result := executeJob(context.Background(), cfg, j, phashHasher, videoHasher)
				postResult(postClient, cfg, result)
			}(job)
		}
	}
}

// sseFrame holds the parsed fields of one SSE event.
type sseFrame struct {
	event string
	data  string
}

// readSSE reads from resp until it finds the "ack" frame, extracts the
// nodeID from it, then returns the nodeID and a channel of subsequent Job
// frames. The channel is closed when the stream ends or ctx is cancelled.
func readSSE(ctx context.Context, resp *http.Response) (nodeID string, jobs <-chan nodes.Job, err error) {
	scanner := bufio.NewScanner(resp.Body)
	jobCh := make(chan nodes.Job, 16)

	// Read frames until we get the ack.
	var cur sseFrame
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			// Empty line: end of frame.
			if cur.event == "ack" && cur.data != "" {
				var ack nodes.ConnectAck
				if e := json.Unmarshal([]byte(cur.data), &ack); e == nil && ack.NodeID != "" {
					nodeID = ack.NodeID
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
		// Ignore other SSE fields (id:, retry:, comments).
	}

	if nodeID == "" {
		close(jobCh)
		return "", nil, fmt.Errorf("stream ended before ack")
	}

	// Continue reading job frames in a goroutine.
	go func() {
		defer close(jobCh)
		var cur sseFrame
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()
			if line == "" {
				// End of frame: attempt to decode as a Job.
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

	return nodeID, jobCh, nil
}

// heartbeat POSTs to /api/nodes/heartbeat every 30 seconds until ctx is
// cancelled. Uses the nodeID assigned by the server for this connection.
func heartbeat(ctx context.Context, nodeID string, cfg *NodeConfig, client *http.Client) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := postHeartbeat(ctx, nodeID, cfg, client); err != nil {
				log.Printf("sakms-node: heartbeat error: %v", err)
			}
		}
	}
}

// postHeartbeat sends one heartbeat POST.
func postHeartbeat(ctx context.Context, nodeID string, cfg *NodeConfig, client *http.Client) error {
	body, err := json.Marshal(map[string]string{"id": nodeID})
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
	req.Header.Set("X-Api-Key", cfg.APIKey)
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
	localPath := Remap(cfg.PathMap, job.ServerPath)
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
	// Use a background context so a cancelled per-connection context does not
	// prevent the result from being delivered.
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
	req.Header.Set("X-Api-Key", cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("sakms-node: posting result for job %s: %v", result.JobID, err)
		return
	}
	resp.Body.Close()
}
