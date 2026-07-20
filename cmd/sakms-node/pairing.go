package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/nodes"
)

// pair opens a pre-auth SSE connection to GET /api/nodes/pair, waits for the
// operator to approve in the server UI, then persists the received API key and
// settings to cfg and saves the updated config to configPath.
//
// It returns nil once the node is successfully paired (cfg.APIKey is set and
// saved). Non-nil errors indicate the stream closed before pairing completed
// (TTL expired, rejected, or network error), and the caller should retry.
func pair(ctx context.Context, cfg *NodeConfig, configPath string, status *statusServer) error {
	pairURL, err := url.Parse(cfg.ServerURL + "/api/nodes/pair")
	if err != nil {
		return fmt.Errorf("parsing pair URL: %w", err)
	}
	q := url.Values{}
	q.Set("name", cfg.NodeName)
	pairURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pairURL.String(), nil)
	if err != nil {
		return fmt.Errorf("building pair request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	client := &http.Client{Timeout: 0} // long-lived SSE connection
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to pair stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pair stream returned status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	var cur struct{ event, data string }

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if line == "" {
			// End of SSE frame.
			switch cur.event {
			case nodes.EventPending:
				var pending struct {
					PairingCode string `json:"pairingCode"`
					DeviceName  string `json:"deviceName"`
				}
				if err := json.Unmarshal([]byte(cur.data), &pending); err == nil {
					log.Printf("sakms-node: pairing code: %s (waiting for approval)", pending.PairingCode)
					status.update(statePending, pending.PairingCode, "")
				}
			case nodes.EventConfig:
				var pairCfg nodes.PairConfig
				if err := json.Unmarshal([]byte(cur.data), &pairCfg); err != nil {
					return fmt.Errorf("parsing config event: %w", err)
				}
				cfg.APIKey = pairCfg.APIKey
				cfg.MaxJobs = pairCfg.Settings.MaxJobs
				cfg.PathMap = make([]PathMapEntry, len(pairCfg.Settings.PathMap))
				for i, pm := range pairCfg.Settings.PathMap {
					cfg.PathMap[i] = PathMapEntry{Server: pm.Server, Local: pm.Local}
				}
				if err := cfg.save(configPath); err != nil {
					return fmt.Errorf("saving config after pairing: %w", err)
				}
				log.Printf("sakms-node: paired successfully — reconnecting authenticated")
				return nil
			}
			cur.event = ""
			cur.data = ""
			continue
		}

		if after, ok := strings.CutPrefix(line, "event:"); ok {
			cur.event = strings.TrimSpace(after)
		} else if after, ok := strings.CutPrefix(line, "data:"); ok {
			cur.data = strings.TrimSpace(after)
		}
		// Ignore id:, retry:, comment lines.
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading pair stream: %w", err)
	}
	return fmt.Errorf("pair stream closed without delivering config")
}
