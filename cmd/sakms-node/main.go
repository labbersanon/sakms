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
		err := run(ctx, cfg, *configPath, hw, phashHasher, videoHasher, postClient, statusSrv)
		if ctx.Err() != nil {
			return
		}

		if errors.Is(err, errUnauthorized) {
			log.Printf("sakms-node: 401 — clearing API key, entering pairing mode")
			cfg.APIKey = ""
			if saveErr := cfg.save(*configPath); saveErr != nil {
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
) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return nil
		}

		err := connect(ctx, cfg, configPath, hw, phashHasher, videoHasher, postClient, statusSrv, &wg)
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

	nodeID, jobCh, settingsCh, err := readSSE(connCtx, resp)
	if err != nil {
		return err
	}

	log.Printf("sakms-node: connected as %s (id=%s)", cfg.NodeName, nodeID)
	statusSrv.update(stateConnected, "", nodeID)

	go heartbeat(connCtx, nodeID, cfg, postClient)

	for {
		select {
		case <-connCtx.Done():
			return nil
		case s, ok := <-settingsCh:
			if !ok {
				return nil
			}
			cfg.PathMap = make([]PathMapEntry, len(s.PathMap))
			for i, pm := range s.PathMap {
				cfg.PathMap[i] = PathMapEntry{Server: pm.Server, Local: pm.Local}
			}
			cfg.MaxJobs = s.MaxJobs
			if saveErr := cfg.save(configPath); saveErr != nil {
				log.Printf("sakms-node: saving updated settings: %v", saveErr)
			}
			log.Printf("sakms-node: settings updated (maxJobs=%d, paths=%d)", s.MaxJobs, len(s.PathMap))
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

// sseFrame holds the parsed fields of one SSE event.
type sseFrame struct {
	event string
	data  string
}

// readSSE reads from resp until it finds the "ack" frame, extracts the nodeID
// from it, then returns the nodeID plus channels for subsequent Job and
// NodeSettings frames. Both channels are closed when the stream ends or ctx is
// cancelled.
func readSSE(ctx context.Context, resp *http.Response) (nodeID string, jobs <-chan nodes.Job, settings <-chan nodes.NodeSettings, err error) {
	scanner := bufio.NewScanner(resp.Body)
	jobCh := make(chan nodes.Job, 16)
	settingsCh := make(chan nodes.NodeSettings, 4)

	// Read frames until we get the ack.
	var cur sseFrame
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
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
	}

	if nodeID == "" {
		close(jobCh)
		close(settingsCh)
		return "", nil, nil, fmt.Errorf("stream ended before ack")
	}

	go func() {
		defer close(jobCh)
		defer close(settingsCh)
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

	return nodeID, jobCh, settingsCh, nil
}

// heartbeat POSTs to /api/nodes/heartbeat every 30 seconds until ctx is
// cancelled.
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
