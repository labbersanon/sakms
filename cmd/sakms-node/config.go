package main

import (
	"encoding/json"
	"fmt"
	"os"
)

const defaultStatusPort = 7810

// PathMapEntry maps one server-absolute path prefix to a local prefix. The
// node replaces the server prefix with the local prefix before opening a file.
type PathMapEntry struct {
	Server string `json:"server"`
	Local  string `json:"local"`
}

// NodeConfig is loaded from and saved to the JSON config file.
type NodeConfig struct {
	ServerURL  string         `json:"serverUrl"`  // e.g. https://media-admin.zaena.us
	APIKey     string         `json:"apiKey"`     // per-node bearer key; empty = needs pairing
	NodeName   string         `json:"nodeName"`   // e.g. wade-pc-4070
	PathMap    []PathMapEntry `json:"pathMap"`    // applied longest-prefix-first
	StatusPort int            `json:"statusPort"` // port for GET /status; 0 → defaultStatusPort
	MaxJobs    int            `json:"maxJobs"`    // 0 = unlimited
}

// statusPort returns the effective status listener port.
func (cfg *NodeConfig) statusPort() int {
	if cfg.StatusPort > 0 {
		return cfg.StatusPort
	}
	return defaultStatusPort
}

// loadConfig reads the JSON file at path and validates required fields.
// APIKey is intentionally optional: an empty value means the node will enter
// pairing mode on startup and acquire a per-node key from the server.
func loadConfig(path string) (*NodeConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("sakms-node: opening config %s: %w", path, err)
	}
	defer f.Close()

	var cfg NodeConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("sakms-node: decoding config %s: %w", path, err)
	}

	if cfg.ServerURL == "" {
		return nil, fmt.Errorf("sakms-node: config %s: serverUrl is required", path)
	}
	if cfg.NodeName == "" {
		return nil, fmt.Errorf("sakms-node: config %s: nodeName is required", path)
	}
	return &cfg, nil
}

// save atomically writes cfg to path using a write-then-rename pattern so a
// crash mid-write cannot leave a partial or empty config file.
func (cfg *NodeConfig) save(path string) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("sakms-node: marshalling config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("sakms-node: writing config tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("sakms-node: renaming config: %w", err)
	}
	return nil
}
