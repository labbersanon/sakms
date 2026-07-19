package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// PathMapEntry maps one server-absolute path prefix to a local prefix. The
// node replaces the server prefix with the local prefix before opening a file.
type PathMapEntry struct {
	Server string `json:"server"`
	Local  string `json:"local"`
}

// NodeConfig is loaded from the JSON config file at startup.
type NodeConfig struct {
	ServerURL string         `json:"serverUrl"` // e.g. https://media-admin.zaena.us
	APIKey    string         `json:"apiKey"`    // the single operator X-Api-Key
	NodeName  string         `json:"nodeName"`  // e.g. wade-pc-4070
	PathMap   []PathMapEntry `json:"pathMap"`   // applied longest-prefix-first
}

// loadConfig reads the JSON file at path and validates required fields.
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
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("sakms-node: config %s: apiKey is required", path)
	}
	if cfg.NodeName == "" {
		return nil, fmt.Errorf("sakms-node: config %s: nodeName is required", path)
	}
	return &cfg, nil
}
